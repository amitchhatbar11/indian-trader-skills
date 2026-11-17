package fyers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSpanMargin(t *testing.T) {
	var body struct {
		Data []map[string]any `json:"data"`
	}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Write([]byte(`{"s":"ok","code":200,"data":{"span":150000,"expo":40000,"total":175000,"benefit":15000}}`))
	}))
	defer srv.Close()
	c := New("APP", "secret", "tok")
	c.spanURL = srv.URL

	m, err := c.SpanMargin([]MarginLeg{{Symbol: "X26NOV110CE", Qty: 500, Sell: true}, {Symbol: "X26NOV90PE", Qty: 500, Sell: true}}, "MARGIN")
	if err != nil {
		t.Fatal(err)
	}
	if m.Total != 175000 || m.Benefit != 15000 {
		t.Errorf("got %+v", m)
	}
	if path != "/span_margin" || len(body.Data) != 2 {
		t.Fatalf("path %s body %+v", path, body)
	}
	leg := body.Data[0]
	if leg["symbol"] != "NSE:X26NOV110CE" || leg["side"] != -1.0 || leg["qty"] != 500.0 || leg["type"] != 1.0 || leg["productType"] != "MARGIN" {
		t.Errorf("leg sent as %+v", leg)
	}
}

func TestSpanMarginError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"s":"error","code":-50,"message":"invalid symbol"}`))
	}))
	defer srv.Close()
	c := New("APP", "secret", "tok")
	c.spanURL = srv.URL
	if _, err := c.SpanMargin([]MarginLeg{{Symbol: "BAD", Qty: 1, Sell: true}}, "MARGIN"); err == nil {
		t.Fatal("want an error")
	}
	if _, err := c.SpanMargin(nil, "MARGIN"); err == nil {
		t.Fatal("empty basket should be refused")
	}
}
