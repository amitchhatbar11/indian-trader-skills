package desk

import (
	"strings"
	"testing"
	"time"
)

func testLimits() Limits {
	l := DefaultLimits(1_000_000, 0.5, 1, 5) // ₹5k a trade, ₹10k a day
	l.MinRewardRisk = 1.5
	return l
}

func TestSizeStock(t *testing.T) {
	lim := testLimits()
	p := Plan{Symbol: "ABC", Side: "long", Entry: 500, Stop: 495, Target: 510, Tick: 0.05, Product: Intraday, FlatBy: "15:15"}
	o, err := Size(p, lim, SizeInput{}, ist("2026-11-23 10:00"))
	if err != nil {
		t.Fatal(err)
	}
	// ₹5,000 ÷ ₹5 = 1,000 shares; position cap 25% of ₹10L = ₹2.5L → 500 shares decides.
	if o.Qty != 500 || o.Type != "LIMIT" || o.Side != "buy" || o.MaxLoss != 2500 || !strings.Contains(o.Why, "position cap") {
		t.Fatalf("%+v", o)
	}
	if o.Margin != 50000 { // intraday: ₹2.5L ÷ 5
		t.Errorf("margin %v", o.Margin)
	}
	// A stop too far for one share is a refusal.
	far := p
	far.Stop = 1
	far.Entry = 6000
	if _, err := Size(far, lim, SizeInput{}, ist("2026-11-23 10:00")); err == nil || !strings.Contains(err.Error(), "refusal") {
		t.Errorf("far stop: %v", err)
	}
}

func TestSizeOptionSell(t *testing.T) {
	lim := testLimits()
	p := Plan{Symbol: "ABC26NOV1100CE", Side: "short", Option: true, Entry: 20, Stop: 28, Target: 14, Tick: 0.05, Product: Margin}
	o, err := Size(p, lim, SizeInput{Lot: 250, Spot: 1012, SpanMargin: func(int) (float64, error) { return 45000, nil }}, ist("2026-11-23 10:00"))
	if err != nil {
		t.Fatal(err)
	}
	// Loss a lot at the stop = 8 × 250 = ₹2,000 → 2 lots by loss; margin ₹45k a lot.
	if o.Qty != 500 || o.LotSize != 250 || o.Side != "sell" || o.MarginSrc != "Fyers SPAN" || o.Margin != 90000 || o.MaxLoss != 4000 {
		t.Fatalf("%+v", o)
	}
}

func market(t *testing.T, now string, qs map[string]Quote) Market {
	return Market{Now: ist(now), Cal: testCal(t), Quotes: qs, Banned: map[string]bool{"BANNED": true},
		Bands: map[string]float64{"ABC": 0, "SMALL": 5}, Ledger: &Ledger{StartCapital: 1_000_000}}
}

func goodOrder() Order {
	return Order{ID: "abc-buy-1", Symbol: "ABC", Side: "buy", Qty: 500, Type: "LIMIT", Limit: 500, Product: Intraday,
		Stop: 495, Target: 510, MaxLoss: 2500, Margin: 50000, Tick: 0.05, FlatBy: "15:15"}
}

func TestRiskPassAndBlocks(t *testing.T) {
	lim := testLimits()
	q := map[string]Quote{"ABC": {LTP: 500, Bid: 499.95, Ask: 500.05, PrevClose: 495}}
	vs := Check([]Order{goodOrder()}, lim, market(t, "2026-11-23 10:00", q))
	if !vs[0].Pass {
		t.Fatalf("good order blocked: %v", vs[0].Blocks)
	}
	cases := map[string]func(o *Order){
		"only LIMIT":       func(o *Order) { o.Type = "MARKET" },
		"tick":             func(o *Order) { o.Limit = 500.03 },
		"no stop":          func(o *Order) { o.Stop = 0 },
		"max loss":         func(o *Order) { o.Stop = 480 },
		"reward:risk":      func(o *Order) { o.Target = 505 },
		"position cap":     func(o *Order) { o.Qty = 600; o.Stop = 496 },
		"shorted intraday": func(o *Order) { o.Side = "sell"; o.Stop = 505; o.Target = 490; o.Product = Delivery },
	}
	for want, mod := range cases {
		o := goodOrder()
		mod(&o)
		v := Check([]Order{o}, lim, market(t, "2026-11-23 10:00", q))[0]
		if v.Pass || !strings.Contains(strings.Join(v.Blocks, "|"), want) {
			t.Errorf("%s: pass=%v blocks=%v", want, v.Pass, v.Blocks)
		}
	}
	// Market closed (holiday) and outside the entry window.
	for _, now := range []string{"2026-11-24 10:00", "2026-11-23 09:20", "2026-11-23 14:45"} {
		if v := Check([]Order{goodOrder()}, lim, market(t, now, q))[0]; v.Pass {
			t.Errorf("%s: passed", now)
		}
	}
	// A batch: two good orders in the same stock — the second is a duplicate.
	o2 := goodOrder()
	o2.ID = "abc-buy-2"
	if vs := Check([]Order{goodOrder(), o2}, lim, market(t, "2026-11-23 10:00", q)); !vs[0].Pass || vs[1].Pass {
		t.Errorf("batch: %+v", vs)
	}
}

func TestRiskOptionsAndExits(t *testing.T) {
	lim := testLimits()
	opt := Order{ID: "o1", Symbol: "BANNED26NOV100CE", Side: "sell", Qty: 500, LotSize: 250, Type: "LIMIT", Limit: 5, Product: Margin,
		Stop: 7, Target: 3.5, MaxLoss: 1000, Margin: 40000}
	q := map[string]Quote{"BANNED26NOV100CE": {LTP: 5, Bid: 4.95, Ask: 5.05, Volume: 2000}, "BANNED": {LTP: 95}}
	v := Check([]Order{opt}, lim, market(t, "2026-11-23 10:00", q))[0]
	joined := strings.Join(v.Blocks, "|")
	if v.Pass || !strings.Contains(joined, "ban period") || !strings.Contains(joined, "under 25 lots") {
		t.Errorf("option: %v", v.Blocks)
	}
	// A sold option's 0.75 reward:risk passes its own floor (0.5), not the stock one (1.5).
	if strings.Contains(joined, "reward:risk") {
		t.Errorf("option sale held to the stock reward:risk floor: %v", v.Blocks)
	}
	ok := opt
	ok.Symbol = "OKAY26NOV100CE"
	q2 := map[string]Quote{"OKAY26NOV100CE": {LTP: 5, Bid: 4.95, Ask: 5.05, Volume: 10000}, "OKAY": {LTP: 95}}
	if v := Check([]Order{ok}, lim, market(t, "2026-11-23 10:00", q2))[0]; !v.Pass {
		t.Errorf("a liquid, unbanned option sale was blocked: %v", v.Blocks)
	}
	// An exit is never blocked by the day's limits — only by its format.
	m := market(t, "2026-11-23 15:00", map[string]Quote{"ABC": {LTP: 480}})
	m.Ledger.Positions = []*PaperPos{{Symbol: "ABC", Side: "long", Qty: 500, Entry: 500}}
	ex := Order{ID: "x", Symbol: "ABC", Side: "sell", Qty: 500, Type: "LIMIT", Limit: 480, Product: Intraday, Exit: true}
	if v := Check([]Order{ex}, lim, m)[0]; !v.Pass || !v.Exit {
		t.Errorf("exit blocked: %+v", v)
	}
	ex.Type = "MARKET"
	if v := Check([]Order{ex}, lim, m)[0]; v.Pass {
		t.Error("a MARKET exit must still be refused")
	}
}

func TestPaperBroker(t *testing.T) {
	now := ist("2026-11-23 10:00")
	l := &Ledger{StartCapital: 1_000_000}
	o := goodOrder()
	if _, err := l.Approve(o, "ok?", now); err == nil {
		t.Fatal(`"ok?" is not a yes`)
	}
	if _, err := l.Place(o.ID, Quote{LTP: 500}, now); err == nil {
		t.Fatal("placed without approval")
	}
	if _, err := l.Approve(o, "Yes", now); err != nil {
		t.Fatal(err)
	}
	// Not marketable: rests.
	po, err := l.Place(o.ID, Quote{LTP: 501, Bid: 500.95, Ask: 501.05}, now)
	if err != nil || po.Status != "pending" || len(l.Positions) != 0 {
		t.Fatalf("%+v %v", po, err)
	}
	// The price comes to it: filled at the ask, under the limit.
	l.Sync(map[string]Quote{"ABC": {LTP: 499.9, Bid: 499.85, Ask: 499.95}}, now.Add(time.Minute))
	if po.Status != "filled" || po.FillPx != 499.95 || len(l.Positions) != 1 {
		t.Fatalf("%+v", po)
	}
	// Target: a resting limit fills at the target.
	evs := l.Sync(map[string]Quote{"ABC": {LTP: 510.4}}, now.Add(time.Hour))
	if len(evs) != 1 || len(l.Trades) != 1 || l.Trades[0].Reason != "TARGET" || l.Trades[0].Exit != 510 {
		t.Fatalf("%+v %+v", evs, l.Trades)
	}
	tr := l.Trades[0]
	if tr.Gross != round2((510-499.95)*500) || tr.Charges <= 0 || tr.Net >= tr.Gross {
		t.Errorf("trade %+v", tr)
	}
	// Intraday positions are flattened at the flat-by time; a stale quote never triggers anything.
	l.Positions = []*PaperPos{{Symbol: "XYZ", Side: "short", Qty: 100, Entry: 200, Stop: 210, Target: 180, Product: Intraday, FlatBy: "15:15"}}
	if evs := l.Sync(map[string]Quote{}, ist("2026-11-23 15:20")); len(evs) != 0 {
		t.Error("closed without a quote")
	}
	if evs := l.Sync(map[string]Quote{"XYZ": {LTP: 199}}, ist("2026-11-23 15:15")); len(evs) != 1 || l.Trades[1].Reason != "FLAT_BY" {
		t.Errorf("flat-by: %+v", evs)
	}
}
