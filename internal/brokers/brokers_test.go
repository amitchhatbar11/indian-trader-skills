package brokers

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/book"
	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

const ticker = "RELIANCE26NOV1400CE"

// Every adapter is a book.Broker.
var (
	_ book.Broker = (*Upstox)(nil)
	_ book.Broker = (*Kite)(nil)
	_ book.Broker = (*Dhan)(nil)
	_ book.Broker = (*Angel)(nil)
)

type rec struct {
	places int32
	body   string
	form   url.Values
}

func TestStatusMap(t *testing.T) {
	for w, want := range map[string]int{"complete": fyers.OrderFilled, "TRADED": fyers.OrderFilled, "rejected": fyers.OrderRejected,
		"CANCELLED": fyers.OrderCancelled, "EXPIRED": fyers.OrderExpired, "open": fyers.OrderPending, "PART_TRADED": fyers.OrderPending,
		"validation pending": fyers.OrderTransit} {
		if got := status(w); got != want {
			t.Errorf("%s → %d, want %d", w, got, want)
		}
	}
	if len(newTag()) > 20 {
		t.Error("tag over 20 characters (Zerodha / Angel One limit)")
	}
}

// placeOnce: a definite refusal is final; an unclear answer is looked up by
// tag and never resent.
func TestPlaceOnce(t *testing.T) {
	var looked int
	if _, err := placeOnce(func() (string, error) { return "", io.EOF }, func() (string, error) { looked++; return "x", nil }); err == nil || looked != 0 {
		t.Errorf("refusal: %v looked=%d", err, looked)
	}
	id, err := placeOnce(func() (string, error) { return "", errUnclear }, func() (string, error) { looked++; return "found1", nil })
	if err != nil || id != "found1" || looked != 1 {
		t.Errorf("unclear+found: %s %v", id, err)
	}
	if _, err := placeOnce(func() (string, error) { return "", errUnclear }, func() (string, error) { return "", nil }); err == nil || !strings.Contains(err.Error(), "not acknowledged") {
		t.Errorf("unclear+missing: %v", err)
	}
}

func upstoxServer(t *testing.T, r *rec, placeCode int) (*Upstox, func()) {
	inst := []map[string]any{{"segment": "NSE_FO", "trading_symbol": "x", "instrument_key": "NSE_FO|123", "instrument_type": "CE",
		"underlying_symbol": "RELIANCE", "strike_price": 1400.0, "expiry": time.Date(2026, 11, 23, 14, 30, 0, 0, desk.IST).UnixMilli()}}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	json.NewEncoder(w).Encode(inst)
	w.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, q *http.Request) {
		if q.Header.Get("Authorization") != "Bearer tok" && !strings.HasSuffix(q.URL.Path, ".gz") {
			t.Errorf("auth %q", q.Header.Get("Authorization"))
		}
		switch {
		case strings.HasSuffix(q.URL.Path, "NSE.json.gz"):
			rw.Write(gz.Bytes())
		case q.URL.Path == "/v2/order/place":
			atomic.AddInt32(&r.places, 1)
			b, _ := io.ReadAll(q.Body)
			r.body = string(b)
			rw.WriteHeader(placeCode)
			if placeCode == 200 {
				rw.Write([]byte(`{"status":"success","data":{"order_id":"2611170001"}}`))
			} else {
				rw.Write([]byte(`{"status":"error","errors":[{"message":"Insufficient funds"}]}`))
			}
		case q.URL.Path == "/v2/order/retrieve-all":
			var m map[string]any
			json.Unmarshal([]byte(r.body), &m)
			rw.Write([]byte(`{"status":"success","data":[{"order_id":"2611170009","tag":"` + m["tag"].(string) + `"}]}`))
		case q.URL.Path == "/v2/order/details":
			rw.Write([]byte(`{"status":"success","data":{"order_id":"2611170001","status":"complete","filled_quantity":500,"pending_quantity":0,"average_price":2.45}}`))
		case q.URL.Path == "/v2/portfolio/short-term-positions":
			rw.Write([]byte(`{"status":"success","data":[{"instrument_token":"NSE_FO|123","quantity":-500}]}`))
		}
	}))
	u := NewUpstox("tok", t.TempDir())
	u.API, u.HFT = srv.URL, srv.URL
	u.keys.Assets, u.keys.API = srv.URL, srv.URL
	return u, srv.Close
}

func TestUpstox(t *testing.T) {
	r := &rec{}
	u, done := upstoxServer(t, r, 200)
	defer done()
	id, err := u.PlaceLimit(ticker, 500, 2.4, true)
	if err != nil || id != "2611170001" || r.places != 1 {
		t.Fatalf("%s %v places=%d", id, err, r.places)
	}
	var m map[string]any
	json.Unmarshal([]byte(r.body), &m)
	if m["order_type"] != "LIMIT" || m["product"] != "D" || m["transaction_type"] != "SELL" || m["instrument_token"] != "NSE_FO|123" || m["quantity"] != 500.0 || m["price"] != 2.4 {
		t.Errorf("order sent as %v", m)
	}
	st, err := u.Order(id)
	if err != nil || st.Status != fyers.OrderFilled || st.Filled != 500 || st.AvgPrice != 2.45 {
		t.Errorf("order %+v %v", st, err)
	}
	pos, err := u.NetPositions()
	if err != nil || pos[ticker] != -500 {
		t.Errorf("positions %v %v", pos, err)
	}
	if _, err := u.PlaceLimit("RELIANCE", 1, 1400, false); err == nil {
		t.Error("a stock order was accepted")
	}

	// Refused (4xx): reported, not looked up, not resent.
	r2 := &rec{}
	u2, done2 := upstoxServer(t, r2, 400)
	defer done2()
	if _, err := u2.PlaceLimit(ticker, 500, 2.4, true); err == nil || !strings.Contains(err.Error(), "Insufficient funds") || r2.places != 1 {
		t.Errorf("refusal: %v places=%d", err, r2.places)
	}
	// Unclear (502): found by its tag in the order book, sent once.
	r3 := &rec{}
	u3, done3 := upstoxServer(t, r3, 502)
	defer done3()
	if id, err := u3.PlaceLimit(ticker, 500, 2.4, true); err != nil || id != "2611170009" || r3.places != 1 {
		t.Errorf("unclear: %s %v places=%d", id, err, r3.places)
	}
}

func TestKite(t *testing.T) {
	r := &rec{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, q *http.Request) {
		if q.Header.Get("Authorization") != "token key:tok" || q.Header.Get("X-Kite-Version") != "3" {
			t.Errorf("headers %v", q.Header)
		}
		switch {
		case q.Method == "POST" && q.URL.Path == "/orders/regular":
			atomic.AddInt32(&r.places, 1)
			q.ParseForm()
			r.form = q.PostForm
			rw.Write([]byte(`{"status":"success","data":{"order_id":"151220000000000"}}`))
		case q.Method == "GET" && q.URL.Path == "/orders/151220000000000":
			rw.Write([]byte(`{"status":"success","data":[{"status":"OPEN PENDING","filled_quantity":0},{"status":"OPEN","filled_quantity":250,"pending_quantity":250,"average_price":2.4}]}`))
		case q.Method == "DELETE" && q.URL.Path == "/orders/regular/151220000000000":
			rw.Write([]byte(`{"status":"success","data":{"order_id":"151220000000000"}}`))
		case q.URL.Path == "/portfolio/positions":
			rw.Write([]byte(`{"status":"success","data":{"net":[{"tradingsymbol":"RELIANCE26NOV1400CE","exchange":"NFO","quantity":-250},{"tradingsymbol":"INFY","exchange":"NSE","quantity":10}]}}`))
		}
	}))
	defer srv.Close()
	k := NewKite("key", "tok")
	k.API = srv.URL
	id, err := k.PlaceLimit(ticker, 500, 2.4, true)
	f := r.form
	if err != nil || id != "151220000000000" || f.Get("order_type") != "LIMIT" || f.Get("product") != "NRML" || f.Get("exchange") != "NFO" ||
		f.Get("tradingsymbol") != ticker || f.Get("transaction_type") != "SELL" || f.Get("price") != "2.40" || f.Get("quantity") != "500" || f.Get("tag") == "" {
		t.Fatalf("%s %v form=%v", id, err, f)
	}
	st, err := k.Order(id)
	if err != nil || st.Status != fyers.OrderPending || st.Filled != 250 || st.Remaining != 250 {
		t.Errorf("partial %+v %v", st, err)
	}
	if err := k.Cancel(id); err != nil {
		t.Error(err)
	}
	if pos, err := k.NetPositions(); err != nil || len(pos) != 1 || pos[ticker] != -250 {
		t.Errorf("positions %v %v", pos, err)
	}
}

const dhanCSV = "SEM_EXM_EXCH_ID,SEM_SEGMENT,SEM_SMST_SECURITY_ID,SEM_INSTRUMENT_NAME,SEM_EXPIRY_CODE,SEM_TRADING_SYMBOL,SEM_LOT_UNITS,SEM_CUSTOM_SYMBOL,SEM_EXPIRY_DATE,SEM_STRIKE_PRICE,SEM_OPTION_TYPE,SEM_TICK_SIZE,SEM_EXPIRY_FLAG,SEM_EXCH_INSTRUMENT_TYPE,SEM_SERIES,SM_SYMBOL_NAME\n" +
	"NSE,D,52175,OPTSTK,0,RELIANCE-Nov2026-1400-CE,500.0,RELIANCE 23 NOV 1400 CALL,2026-11-23 14:30:00,1400.00000,CE,5.0000,M,OP,,RELIANCE\n" +
	"BSE,D,1101327,OPTSTK,0,RELIANCE-Nov2026-1400-CE,500.0,x,2026-11-26 15:30:00,1400.00000,CE,5.0000,M,OPTSTK,,RELIOPT\n"

func TestDhan(t *testing.T) {
	r := &rec{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, q *http.Request) {
		if q.URL.Path != "/master.csv" && q.Header.Get("access-token") != "jwt" {
			t.Errorf("token header %q", q.Header.Get("access-token"))
		}
		switch {
		case q.URL.Path == "/master.csv":
			rw.Write([]byte(dhanCSV))
		case q.Method == "POST" && q.URL.Path == "/orders":
			atomic.AddInt32(&r.places, 1)
			b, _ := io.ReadAll(q.Body)
			r.body = string(b)
			rw.Write([]byte(`{"orderId":"112111182198","orderStatus":"TRANSIT"}`))
		case q.URL.Path == "/orders/112111182198":
			rw.Write([]byte(`{"orderId":"112111182198","orderStatus":"TRADED","filledQty":500,"remainingQuantity":0,"averageTradedPrice":2.4}`))
		case q.URL.Path == "/positions":
			rw.Write([]byte(`[{"securityId":"52175","exchangeSegment":"NSE_FNO","netQty":-500},{"securityId":"11536","exchangeSegment":"NSE_EQ","netQty":40}]`))
		}
	}))
	defer srv.Close()
	d := NewDhan("1000000003", "jwt", "")
	d.API, d.MasterURL = srv.URL, srv.URL+"/master.csv"
	id, err := d.PlaceLimit(ticker, 500, 2.4, true)
	var m map[string]any
	json.Unmarshal([]byte(r.body), &m)
	if err != nil || id != "112111182198" || m["securityId"] != "52175" || m["orderType"] != "LIMIT" || m["productType"] != "MARGIN" ||
		m["exchangeSegment"] != "NSE_FNO" || m["transactionType"] != "SELL" || m["dhanClientId"] != "1000000003" || m["correlationId"] == "" {
		t.Fatalf("%s %v body=%v", id, err, m)
	}
	if st, err := d.Order(id); err != nil || st.Status != fyers.OrderFilled || st.Filled != 500 {
		t.Errorf("order %+v %v", st, err)
	}
	if pos, err := d.NetPositions(); err != nil || len(pos) != 1 || pos[ticker] != -500 {
		t.Errorf("positions %v %v", pos, err)
	}
}

func TestAngel(t *testing.T) {
	r := &rec{}
	master := `[{"token":"125965","symbol":"RELIANCE23NOV261400CE","name":"RELIANCE","expiry":"23NOV2026","strike":"140000.000000","lotsize":"500","instrumenttype":"OPTSTK","exch_seg":"NFO"},
	{"token":"2885","symbol":"RELIANCE-EQ","name":"RELIANCE","expiry":"","strike":"-1.000000","instrumenttype":"","exch_seg":"NSE"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, q *http.Request) {
		if q.URL.Path != "/master.json" && (q.Header.Get("Authorization") != "Bearer jwt" || q.Header.Get("X-PrivateKey") != "key") {
			t.Errorf("headers %v", q.Header)
		}
		switch q.URL.Path {
		case "/master.json":
			rw.Write([]byte(master))
		case "/rest/secure/angelbroking/order/v1/placeOrder":
			atomic.AddInt32(&r.places, 1)
			b, _ := io.ReadAll(q.Body)
			r.body = string(b)
			rw.Write([]byte(`{"status":true,"message":"SUCCESS","errorcode":"","data":{"orderid":"201020000000080","uniqueorderid":"u1"}}`))
		case "/rest/secure/angelbroking/order/v1/getOrderBook":
			rw.Write([]byte(`{"status":true,"message":"SUCCESS","data":[{"orderid":"201020000000080","status":"complete","filledshares":"500","unfilledshares":"0","averageprice":2.4}]}`))
		case "/rest/secure/angelbroking/order/v1/getPosition":
			rw.Write([]byte(`{"status":true,"message":"SUCCESS","data":[{"symboltoken":"125965","exchange":"NFO","netqty":"-500"}]}`))
		}
	}))
	defer srv.Close()
	a := NewAngel("key", "jwt", "")
	a.API, a.MasterURL = srv.URL, srv.URL+"/master.json"
	id, err := a.PlaceLimit(ticker, 500, 2.4, true)
	var m map[string]any
	json.Unmarshal([]byte(r.body), &m)
	if err != nil || id != "201020000000080" || m["tradingsymbol"] != "RELIANCE23NOV261400CE" || m["symboltoken"] != "125965" ||
		m["ordertype"] != "LIMIT" || m["producttype"] != "CARRYFORWARD" || m["exchange"] != "NFO" || m["quantity"] != "500" || m["price"] != "2.40" {
		t.Fatalf("%s %v body=%v", id, err, m)
	}
	if st, err := a.Order(id); err != nil || st.Status != fyers.OrderFilled || st.Filled != 500 || st.AvgPrice != 2.4 {
		t.Errorf("order %+v %v", st, err)
	}
	if pos, err := a.NetPositions(); err != nil || pos[ticker] != -500 {
		t.Errorf("positions %v %v", pos, err)
	}
}

func TestOpenNeedsCredentials(t *testing.T) {
	for _, n := range []string{"upstox", "zerodha", "dhan", "angelone"} {
		if _, err := Open(n, Options{}); err == nil {
			t.Errorf("%s opened without credentials", n)
		}
	}
	if _, err := Open("robinhood", Options{}); err == nil {
		t.Error("unknown broker accepted")
	}
}
