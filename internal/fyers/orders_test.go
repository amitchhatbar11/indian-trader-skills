package fyers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) *FyersClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("APP", "secret", "tok")
	c.baseURL = srv.URL
	return c
}

// v3 places orders at POST /orders/sync with the required stopPrice field,
// and a 5xx must NOT be retried — the server may already have the order.
func TestPlaceOrderSyncEndpointAndNoRetry(t *testing.T) {
	var calls int32
	var path, method string
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		path, method = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.WriteHeader(502)
		w.Write([]byte(`bad gateway`))
	})
	_, err := c.PlaceOrder(OrderRequest{Symbol: "NSE:X26OCT100PE", Qty: 50, Type: 1, Side: -1,
		ProductType: "MARGIN", LimitPrice: 10, Validity: "DAY", OrderTag: "ga123"})
	if err == nil {
		t.Fatal("502 should surface as an error")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("order placement was sent %d times — must be exactly once", n)
	}
	if method != "POST" || path != "/orders/sync" {
		t.Errorf("placed via %s %s, want POST /orders/sync", method, path)
	}
	if _, ok := body["stopPrice"]; !ok {
		t.Error("stopPrice is required by the v3 API and must always be sent")
	}
	if body["limitPrice"] != 10.0 || body["orderTag"] != "ga123" || body["type"] != 1.0 {
		t.Errorf("body = %v", body)
	}
}

func TestGetOrderParsesFill(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders" || r.URL.Query().Get("id") != "123" {
			t.Errorf("got %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Write([]byte(`{"s":"ok","code":200,"orderBook":[{"id":"123","symbol":"NSE:X","qty":100,
			"filledQty":50,"remainingQuantity":50,"tradedPrice":10.15,"limitPrice":10,"status":6,
			"side":-1,"message":"","orderTag":"ga1"}]}`))
	})
	o, err := c.GetOrder("123")
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != OrderPending || o.FilledQty != 50 || o.Remaining != 50 || o.TradedPrice != 10.15 || o.OrderTag != "ga1" {
		t.Errorf("order = %+v", o)
	}
}

func TestCancelOrderOnceViaDelete(t *testing.T) {
	var calls int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		b, _ := io.ReadAll(r.Body)
		if r.Method != "DELETE" || r.URL.Path != "/orders/sync" || !strings.Contains(string(b), `"id":"123"`) {
			t.Errorf("cancel sent as %s %s %s", r.Method, r.URL.Path, b)
		}
		w.Write([]byte(`{"s":"ok","code":1103,"message":"Successfully cancelled order","id":"123"}`))
	})
	if err := c.CancelOrder("123"); err != nil || calls != 1 {
		t.Fatalf("cancel err=%v calls=%d", err, calls)
	}
}

func TestGetPositions(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"s":"ok","code":200,"netPositions":[{"symbol":"NSE:X26OCT100PE","netQty":-500,
			"buyQty":0,"sellQty":500,"netAvg":10.1,"productType":"MARGIN"}],"overall":{}}`))
	})
	ps, err := c.GetPositions()
	if err != nil || len(ps) != 1 || ps[0].NetQty != -500 || ps[0].SellQty != 500 {
		t.Fatalf("positions = %+v err=%v", ps, err)
	}
}
