package desk

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseBandsAndDeals(t *testing.T) {
	bands, err := ParseBands([]byte("Symbol,Series,Security Name,Band,Remarks\nABC,EQ,ABC Ltd,5,-\nRELIANCE,EQ,Reliance,No Band,-\n"))
	if err != nil || bands["ABC"] != 5 || bands["RELIANCE"] != 0 {
		t.Fatalf("%v %v", bands, err)
	}
	ds, err := ParseDeals([]byte("Date,Symbol,Security Name,Client Name,Buy/Sell,Quantity Traded,Trade Price / Wght. Avg. Price,Remarks\n"+
		"16-NOV-2026,AAATECH,AAA Technologies Limited,INDRAPRASTHA AGENCIES,BUY,435000,92.2,-\n"), "bulk")
	if err != nil || len(ds) != 1 {
		t.Fatal(ds, err)
	}
	d := ds[0]
	if d.Date.Format("2006-01-02") != "2026-11-16" || d.Qty != 435000 || d.Value != 435000*92.2 || d.Side != "BUY" || d.Key == "" {
		t.Errorf("%+v", d)
	}
	// A high-frequency firm buying and selling the same stock nets to its difference.
	churn, _ := ParseDeals([]byte("Date,Symbol,Security Name,Client Name,Buy/Sell,Quantity Traded,Trade Price / Wght. Avg. Price,Remarks\n"+
		"16-NOV-2026,KAYNES,Kaynes,GRAVITON RESEARCH,BUY,849421,2928.85,-\n16-NOV-2026,KAYNES,Kaynes,GRAVITON RESEARCH,SELL,845348,2925.01,-\n"+
		"16-NOV-2026,KAYNES,Kaynes,SOME FUND,BUY,100000,2930,-\n"), "bulk")
	net := NetDeals(churn)
	if len(net) != 2 || net[0].Qty != 4073 || net[0].Side != "BUY" || !net[0].Churn || net[1].Churn || net[1].Note != "" || net[1].Qty != 100000 {
		t.Errorf("netted %+v", net)
	}
	in, err := ParseInsider([]byte(`{"data":[{"symbol":"ABC","company":"ABC Ltd","acqName":"Promoter X","tdpTransactionType":"Buy",
		"secAcq":"1000","secVal":"500000","intimDt":"12-Nov-2026","personCategory":"Promoters","acqMode":"Market Purchase"}]}`))
	if err != nil || len(in) != 1 || in[0].Price != 500 || in[0].Side != "BUY" || in[0].Kind != "insider" {
		t.Errorf("%+v %v", in, err)
	}
}

func TestNewsParsing(t *testing.T) {
	ann, err := ParseAnnouncements([]byte(`[{"symbol":"TCS","desc":"Outcome of Board Meeting","attchmntText":"Financial results for the quarter","attchmntFile":"https://x/a.pdf","an_dt":"16-Nov-2026 19:26:15","seq_id":"1"}]`))
	if err != nil || len(ann) != 1 || ann[0].Time.Format("15:04") != "19:26" || ann[0].Tags[0] != "results" {
		t.Fatalf("%+v %v", ann, err)
	}
	rss := `<rss><channel><item><title>Company bags order worth Rs 500 crore</title><link>https://n/1</link>
<pubDate>Mon, 16 Nov 2026 10:00:00 GMT</pubDate><guid>g1</guid><source>Economic Times</source></item></channel></rss>`
	items, err := ParseRSS([]byte(rss), "ABC")
	if err != nil || len(items) != 1 || items[0].Source != "Economic Times" || items[0].Tags[0] != "order_win" || items[0].Time.Format("15:04") != "15:30" {
		t.Fatalf("%+v %v", items, err)
	}
	if got := Tag("Intimation of Board Meeting to consider dividend"); strings.Join(got, ",") != "board_meeting,dividend" {
		t.Errorf("tags %v", got)
	}
	if got := Tag("Mutual Funds Buy Jio Platforms In October - NDTV Profit"); got[0] == "results" {
		t.Errorf("a publication's name is not results news: %v", got)
	}
	if got := Tag("TCS Q2 net profit rises 15%"); got[0] != "results" {
		t.Errorf("results headline: %v", got)
	}
}

func TestParseXBRL(t *testing.T) {
	x := `<xbrli:xbrl xmlns:xbrli="http://www.xbrl.org/2003/instance" xmlns:in-bse-fin="x">
<xbrli:context id="OneD"><xbrli:entity><xbrli:identifier scheme="s">1</xbrli:identifier></xbrli:entity>
<xbrli:period><xbrli:startDate>2024-10-01</xbrli:startDate><xbrli:endDate>2024-12-31</xbrli:endDate></xbrli:period></xbrli:context>
<xbrli:context id="FourD"><xbrli:entity><xbrli:identifier scheme="s">1</xbrli:identifier></xbrli:entity>
<xbrli:period><xbrli:startDate>2024-04-01</xbrli:startDate><xbrli:endDate>2024-12-31</xbrli:endDate></xbrli:period></xbrli:context>
<xbrli:context id="OneSeg"><xbrli:entity><xbrli:identifier scheme="s">1</xbrli:identifier><xbrli:segment>s</xbrli:segment></xbrli:entity>
<xbrli:period><xbrli:startDate>2024-10-01</xbrli:startDate><xbrli:endDate>2024-12-31</xbrli:endDate></xbrli:period></xbrli:context>
<in-bse-fin:RevenueFromOperations contextRef="OneSeg">1</in-bse-fin:RevenueFromOperations>
<in-bse-fin:RevenueFromOperations contextRef="OneD">639730000000.00</in-bse-fin:RevenueFromOperations>
<in-bse-fin:RevenueFromOperations contextRef="FourD">1908450000000.00</in-bse-fin:RevenueFromOperations>
<in-bse-fin:ProfitBeforeTax contextRef="OneD">166660000000.00</in-bse-fin:ProfitBeforeTax>
<in-bse-fin:FinanceCosts contextRef="OneD">2000000000</in-bse-fin:FinanceCosts>
<in-bse-fin:OtherIncome contextRef="OneD">10000000000</in-bse-fin:OtherIncome>
<in-bse-fin:ProfitLossForPeriod contextRef="OneD">124440000000.00</in-bse-fin:ProfitLossForPeriod>
<in-bse-fin:BasicEarningsLossPerShareFromContinuingAndDiscontinuedOperations contextRef="OneD">34.21</in-bse-fin:BasicEarningsLossPerShareFromContinuingAndDiscontinuedOperations>
</xbrli:xbrl>`
	q, err := ParseXBRL([]byte(x), ist("2024-10-01"), ist("2024-12-31"))
	if err != nil {
		t.Fatal(err)
	}
	if q.Revenue != 63973 || q.PAT != 12444 || q.EPS != 34.21 || q.PBT != 16666 {
		t.Fatalf("%+v", q)
	}
	if m := q.OperatingMargin(); m < 24.75 || m > 24.85 { // (16666+200−1000)/63973 = 24.80%
		t.Errorf("operating margin %.2f", m)
	}
	if _, err := ParseXBRL([]byte(x), ist("2023-10-01"), ist("2023-12-31")); err == nil {
		t.Error("a quarter not in the filing should be an error")
	}
}

func TestAlerts(t *testing.T) {
	cal := testCal(t)
	made := ist("2026-11-23 09:30")
	p := Plan{Symbol: "ABC", Side: "long", Stop: 495, Target: 510, FlatBy: "15:15", Product: Intraday}
	as := FromPlan(p, made)
	as = append(as, Alert{ID: "pct", Symbol: "ABC", Kind: "pct", Pct: -2, Created: made}, Alert{ID: "bell", Kind: "bell", Created: made})
	if len(as) != 5 {
		t.Fatal(as)
	}
	rung := CheckAlerts(as, map[string]Quote{"ABC": {LTP: 494, PrevClose: 505}}, ist("2026-11-23 10:00"), cal)
	ids := func(r []Alert) string {
		var s []string
		for _, a := range r {
			s = append(s, a.Label+a.ID[len(a.ID)-3:])
		}
		return strings.Join(s, ",")
	}
	// The stop (494 ≤ 495) and −2.2% ring; the bell was set after 09:15, so it waits for tomorrow.
	if len(rung) != 2 || !as[0].Rung() || !as[3].Rung() || as[4].Rung() {
		t.Fatalf("rang %s", ids(rung))
	}
	// Each rings once.
	if r := CheckAlerts(as, map[string]Quote{"ABC": {LTP: 490, PrevClose: 505}}, ist("2026-11-23 10:05"), cal); len(r) != 0 {
		t.Errorf("rang again: %s", ids(r))
	}
	// Flat-by at 15:15; the bell the next trading day (24th is a holiday → 25th).
	if r := CheckAlerts(as, nil, ist("2026-11-23 15:16"), cal); len(r) != 1 || r[0].Kind != "time" {
		t.Errorf("flat-by: %s", ids(r))
	}
	if r := CheckAlerts(as, nil, ist("2026-11-24 09:20"), cal); len(r) != 0 {
		t.Error("bell rang on a holiday")
	}
	if r := CheckAlerts(as, nil, ist("2026-11-25 09:15"), cal); len(r) != 1 || r[0].Kind != "bell" {
		t.Errorf("bell: %s", ids(r))
	}
}

func TestUpstoxFeed(t *testing.T) {
	inst := []map[string]any{
		{"segment": "NSE_EQ", "trading_symbol": "RELIANCE", "instrument_key": "NSE_EQ|INE002A01018", "instrument_type": "EQ"},
		{"segment": "NSE_FO", "trading_symbol": "RELIANCE 1440 CE 24 NOV 26", "instrument_key": "NSE_FO|123", "instrument_type": "CE",
			"underlying_symbol": "RELIANCE", "strike_price": 1440.0, "expiry": ist("2026-11-24 14:30").UnixMilli()},
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	json.NewEncoder(w).Encode(inst)
	w.Close()
	var auth string
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "NSE.json.gz"):
			rw.Write(gz.Bytes())
		case strings.HasPrefix(r.URL.Path, "/v2/market-quote/quotes"):
			auth = r.Header.Get("Authorization")
			rw.Write([]byte(`{"status":"success","data":{"NSE_EQ:RELIANCE":{"last_price":1450,"net_change":10,"volume":5,"instrument_token":"NSE_EQ|INE002A01018",
			"ohlc":{"open":1440,"high":1455,"low":1435},"depth":{"buy":[{"price":1449.9}],"sell":[{"price":1450.1}]}}}}`))
		case strings.Contains(r.URL.Path, "/v3/historical-candle/"):
			paths = append(paths, r.URL.EscapedPath())
			rw.Write([]byte(`{"data":{"candles":[["2026-11-16T09:20:00+05:30",2,3,1,2.5,100,0],["2026-11-16T09:15:00+05:30",1,2,1,2,50,0]]}}`))
		}
	}))
	defer srv.Close()
	u := &UpstoxFeed{Token: "tok", API: srv.URL, Assets: srv.URL, CacheDir: t.TempDir()}
	qs, err := u.Quotes([]string{"RELIANCE"})
	if err != nil || qs["RELIANCE"].LTP != 1450 || qs["RELIANCE"].PrevClose != 1440 || qs["RELIANCE"].Bid != 1449.9 || auth != "Bearer tok" {
		t.Fatalf("%+v %v %s", qs, err, auth)
	}
	bs, err := u.Bars("RELIANCE26NOV1440CE", "5", ist("2026-11-16"), ist("2026-11-16"))
	if err != nil || len(bs) != 2 || bs[0].T.Format("15:04") != "09:15" || len(paths) == 0 ||
		!strings.Contains(paths[0], "NSE_FO%7C123/minutes/5/2026-11-16/2026-11-16") {
		t.Fatalf("%+v %v %v", bs, err, paths)
	}
}

func TestKiteFeedAndMulti(t *testing.T) {
	dump := "instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange\n" +
		"738561,2885,RELIANCE,RELIANCE,0,,0,0.1,1,EQ,NSE,NSE\n12345,48,RELIANCE26NOV1440CE,RELIANCE,0,2026-11-24,1440,0.05,500,CE,NFO-OPT,NFO\n"
	var hdr string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Get("Authorization")
		switch {
		case strings.HasPrefix(r.URL.Path, "/instruments/historical/12345/5minute"):
			rw.Write([]byte(`{"data":{"candles":[["2026-11-16T09:15:00+0530",10,11,9,10.5,1000]]}}`))
		case strings.HasPrefix(r.URL.Path, "/instruments/"):
			rw.Write([]byte(dump))
		case r.URL.Path == "/quote":
			if r.URL.Query()["i"][0] != "NFO:RELIANCE26NOV1440CE" {
				t.Errorf("quote key %v", r.URL.Query())
			}
			rw.Write([]byte(`{"data":{"NFO:RELIANCE26NOV1440CE":{"last_price":10.5,"volume":900,"ohlc":{"open":9,"high":11,"low":8,"close":9.5},
			"depth":{"buy":[{"price":10.45}],"sell":[{"price":10.55}]}}}}`))
		}
	}))
	defer srv.Close()
	k := &KiteFeed{APIKey: "key", Token: "tok", API: srv.URL, CacheDir: t.TempDir()}
	bs, err := k.Bars("RELIANCE26NOV1440CE", "5", ist("2026-11-16"), ist("2026-11-16"))
	if err != nil || len(bs) != 1 || bs[0].C != 10.5 || hdr != "token key:tok" {
		t.Fatalf("%+v %v %s", bs, err, hdr)
	}
	// Multi: the first feed fails, the second answers.
	m := &Multi{Feeds: []Feed{failing{}, k}}
	qs, err := m.Quotes([]string{"RELIANCE26NOV1440CE"})
	if err != nil || qs["RELIANCE26NOV1440CE"].PrevClose != 9.5 || qs["RELIANCE26NOV1440CE"].Source != "zerodha" {
		t.Fatalf("%+v %v", qs, err)
	}
	if _, err := (&Multi{Feeds: []Feed{failing{}}}).Quotes([]string{"X"}); err == nil {
		t.Error("all feeds failing should be an error")
	}
}

type failing struct{}

func (failing) Name() string                                             { return "down" }
func (failing) Quotes([]string) (map[string]Quote, error)                { return nil, errDown }
func (failing) Bars(string, string, time.Time, time.Time) ([]Bar, error) { return nil, errDown }

var errDown = &net{}

type net struct{}

func (*net) Error() string { return "connection refused" }
