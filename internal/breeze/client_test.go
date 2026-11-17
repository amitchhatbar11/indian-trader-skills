package breeze

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionAndHistory(t *testing.T) {
	var histCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/customerdetails"):
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"SessionToken":"S123"`) || !strings.Contains(string(body), `"AppKey":"K"`) {
				t.Errorf("login body = %s", body)
			}
			io.WriteString(w, `{"Success":{"session_token":"QjY0VE9LRU4="},"Status":200,"Error":null}`)
		case strings.HasSuffix(r.URL.Path, "/historicalcharts"):
			histCalls.Add(1)
			if r.Header.Get("X-SessionToken") != "QjY0VE9LRU4=" || r.Header.Get("apikey") != "K" {
				t.Errorf("headers = %v", r.Header)
			}
			q := r.URL.Query()
			if q.Get("stock_code") != "RELIND" || q.Get("exch_code") != "NFO" || q.Get("right") != "put" ||
				q.Get("strike_price") != "1440" || q.Get("expiry_date") != "2026-10-27T07:00:00.000Z" ||
				q.Get("from_date") != "2026-10-05T00:00:00.000Z" || q.Get("to_date") != "2026-10-05T23:59:59.000Z" {
				t.Errorf("query = %v", q)
			}
			// numbers arrive as numbers or strings; order not guaranteed;
			// a bar from another day must be dropped
			io.WriteString(w, `{"Success":[
				{"datetime":"2026-10-05 09:16:00","open":"12.5","high":13,"low":"12","close":"12.8","volume":"1500","open_interest":""},
				{"datetime":"2026-10-05 09:15:00","open":12,"high":12.6,"low":11.9,"close":12.5,"volume":900,"open_interest":100},
				{"datetime":"2026-10-06 09:15:00","open":1,"high":1,"low":1,"close":1,"volume":1}
			],"Status":200,"Error":null}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New("K", "", t.TempDir())
	c.APIURL, c.HistURL, c.MinGap = srv.URL+"/", srv.URL+"/", 0
	c.Now = func() time.Time { return time.Date(2026, 11, 9, 10, 0, 0, 0, IST) }
	if err := c.login("S123"); err != nil {
		t.Fatal(err)
	}
	req := Request{StockCode: "RELIND", Exchange: "NFO", Product: "options", Interval: "1minute",
		Expiry: time.Date(2026, 10, 27, 0, 0, 0, 0, IST), Right: "put", Strike: 1440}
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, IST)
	for i := 0; i < 2; i++ { // the second read comes from the disk cache
		cs, err := c.History(req, day, day)
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) != 2 || cs[0].Close != 12.5 || cs[1].Close != 12.8 || cs[1].Volume != 1500 ||
			cs[0].Time.Format("15:04") != "09:15" {
			t.Fatalf("pass %d: candles = %+v", i, cs)
		}
	}
	if n := histCalls.Load(); n != 1 {
		t.Errorf("history calls = %d, want 1 (cached)", n)
	}
}

func TestHistoryChunksAndBudget(t *testing.T) {
	var froms []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		froms = append(froms, r.URL.Query().Get("from_date")[:10]+"→"+r.URL.Query().Get("to_date")[:10])
		io.WriteString(w, `{"Success":[],"Status":200,"Error":null}`)
	}))
	defer srv.Close()
	c := New("K", "T", "")
	c.HistURL, c.MinGap, c.MaxCalls = srv.URL+"/", 0, 3
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, IST)
	if _, err := c.History(Request{StockCode: "X", Exchange: "NSE", Interval: "5minute"}, from, from.AddDate(0, 0, 24)); err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-10-01→2026-10-10", "2026-10-11→2026-10-20", "2026-10-21→2026-10-25"}
	if strings.Join(froms, " ") != strings.Join(want, " ") {
		t.Errorf("chunks = %v, want %v", froms, want)
	}
	if _, err := c.History(Request{StockCode: "X", Exchange: "NSE", Interval: "1day"}, from, from); !errors.Is(err, ErrDailyLimit) {
		t.Errorf("over budget: err = %v, want ErrDailyLimit", err)
	}
}

// Two clients (two commands) on one day share the ledger and its limit.
func TestDailyLedgerSharedAcrossClients(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Success":[],"Status":200,"Error":null}`)
	}))
	defer srv.Close()
	ledger := t.TempDir() + "/usage.json"
	clock := func() time.Time { return time.Date(2026, 11, 10, 10, 0, 0, 0, IST) }
	mk := func() *Client {
		c := New("K", "T", "")
		c.HistURL, c.MinGap, c.MaxCalls, c.Now = srv.URL+"/", 0, 0, clock
		c.UsagePath, c.DailyLimit = ledger, 3
		return c
	}
	req := Request{StockCode: "X", Exchange: "NSE", Interval: "1day"}
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, IST)
	a, b := mk(), mk()
	for i := 0; i < 2; i++ {
		if _, err := a.History(req, day.AddDate(0, 0, i), day.AddDate(0, 0, i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.History(req, day, day); err != nil {
		t.Fatal(err)
	}
	if _, err := b.History(req, day, day); !errors.Is(err, ErrDailyLimit) {
		t.Errorf("4th call of the day: err = %v, want ErrDailyLimit", err)
	}
	if a.UsedToday() != 3 {
		t.Errorf("ledger = %d, want 3", a.UsedToday())
	}
	// cache-only client: no calls at all
	c := mk()
	c.DailyLimit, c.MaxCalls = 0, -1
	if _, err := c.History(req, day, day); !errors.Is(err, ErrDailyLimit) {
		t.Errorf("cache-only client made a call: %v", err)
	}
}

func TestParseHistoryErrors(t *testing.T) {
	if cs, err := parseHistory([]byte(`{"Success":null,"Status":500,"Error":"No Data Found"}`)); err != nil || len(cs) != 0 {
		t.Errorf("no data: %v %v", cs, err)
	}
	if _, err := parseHistory([]byte(`{"Success":null,"Status":401,"Error":"Session key is expired."}`)); err == nil {
		t.Error("expired session should be an error")
	}
}

func TestParseStockCodes(t *testing.T) {
	src := `"Token", "ShortName", "Series", "CompanyName", "ExchangeCode"
"2885","RELIND","EQ","RELIANCE INDUSTRIES","RELIANCE"
"2031","MAHMAH","EQ","MAHINDRA & MAHINDRA LIMITED","M&M"
"9999","RELBE","BE","RELIANCE BE","RELIANCE"
`
	got, err := ParseStockCodes(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if got["RELIANCE"] != "RELIND" || got["M&M"] != "MAHMAH" {
		t.Errorf("codes = %v", got)
	}
}
