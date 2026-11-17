package book

import (
	"math"
	"strings"
	"testing"
	"time"
)

var ist = time.FixedZone("IST", 5*3600+1800)

func at(hhmm string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-08-13 "+hhmm, ist)
	return t
}

// liquid is a quote every liquidity/band rule accepts at a ₹40 limit.
var liquid = OptQuote{LTP: 40, Bid: 39.9, Ask: 40.1, Volume: 500000}

var allRules = Rules{
	Capital: 1000000, DailyLossPct: 0.5,
	LimitBandPct: 10, MinOptVolume: 100000, MaxSpreadPct: 5,
}

func sellPE() EntryContext {
	return EntryContext{Side: SideSell, Right: "PE", Direction: "UP"}
}

func TestCheckEntryPassesCleanTrade(t *testing.T) {
	b := New(t.TempDir())
	warns, err := b.CheckEntry(at("10:00"), allRules, sellPE(), liquid, 40)
	if err != nil || len(warns) != 0 {
		t.Fatalf("clean trade refused: warns=%v err=%v", warns, err)
	}
}

func TestCheckEntryRefusals(t *testing.T) {
	cases := []struct {
		name  string
		ec    EntryContext
		q     OptQuote
		limit float64
		want  string
	}{
		{"banned", EntryContext{Side: SideSell, Right: "PE", Banned: true}, liquid, 40, "ban period"},
		{"thin strike", sellPE(), OptQuote{LTP: 40, Bid: 39.9, Ask: 40.1, Volume: 5000}, 40, "below the 100000 minimum"},
		{"wide spread", sellPE(), OptQuote{LTP: 40, Bid: 36, Ask: 44, Volume: 500000}, 40, "spread 20.0%"},
		{"one-sided", sellPE(), OptQuote{LTP: 40, Ask: 41, Volume: 500000}, 40, "no two-sided market"},
		{"fat finger low", sellPE(), liquid, 1, "outside ±10%"},
		{"fat finger high", sellPE(), liquid, 60, "outside ±10%"},
	}
	for _, c := range cases {
		b := New(t.TempDir())
		_, err := b.CheckEntry(at("10:00"), allRules, c.ec, c.q, c.limit)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

// Every failing rule is reported at once, not just the first.
func TestCheckEntryReportsAllFailures(t *testing.T) {
	b := New(t.TempDir())
	ec := EntryContext{Side: SideSell, Right: "PE", Banned: true}
	_, err := b.CheckEntry(at("10:00"), allRules, ec, OptQuote{LTP: 40, Bid: 30, Ask: 50, Volume: 10}, 1)
	for _, want := range []string{"ban period", "minimum", "spread", "outside"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("joined error missing %q: %v", want, err)
		}
	}
}

func TestZeroRulesDisableChecks(t *testing.T) {
	b := New(t.TempDir())
	_, err := b.CheckEntry(at("10:00"), Rules{}, sellPE(), OptQuote{LTP: 40, Volume: 0}, 1)
	if err != nil {
		t.Errorf("zero Rules should not refuse anything but a ban/halt: %v", err)
	}
}

// Cheap options get at least ₹0.50 of slack, so a ₹0.80 option can be
// priced at ₹1.25 without tripping a 10% band.
func TestLimitBandFloorForCheapOptions(t *testing.T) {
	q := OptQuote{LTP: 0.80, Bid: 0.75, Ask: 0.85}
	if err := limitBandErr(q, 1.25, 10); err != nil {
		t.Errorf("₹0.50 slack not applied: %v", err)
	}
	if err := limitBandErr(q, 1.50, 10); err == nil {
		t.Error("₹1.50 on a ₹0.80 option should be refused")
	}
}

// Exits are only price-checked: no ban, halt or liquidity can block them.
func TestCheckExitOnlyChecksPrice(t *testing.T) {
	thin := OptQuote{LTP: 40, Bid: 30, Ask: 50, Volume: 1}
	if err := CheckExit(allRules, thin, 40); err != nil {
		t.Errorf("exit in a thin market refused: %v", err)
	}
	if err := CheckExit(allRules, liquid, 1); err == nil {
		t.Error("exit at ₹1 on a ₹40 option should be refused as a fat finger")
	}
}

func TestFadeRisk(t *testing.T) {
	cases := []struct {
		side, right, dir, w52 string
		want                  bool
	}{
		{SideSell, "CE", "UP", "AT 52W HIGH", true},
		{SideSell, "CE", "UP", "NEAR 52W HIGH", true},
		{SideSell, "PE", "DOWN", "AT 52W LOW", true},
		{SideSell, "PE", "UP", "AT 52W HIGH", false}, // selling with the move is fine
		{SideSell, "CE", "UP", "", false},
		{SideBuy, "CE", "UP", "AT 52W HIGH", false}, // only sellers fade
		{SideSell, "CE", "DOWN", "AT 52W HIGH", false},
	}
	for _, c := range cases {
		if got := FadeRisk(c.side, c.right, c.dir, c.w52); got != c.want {
			t.Errorf("FadeRisk(%s %s, %s, %q) = %v, want %v", c.side, c.right, c.dir, c.w52, got, c.want)
		}
	}
}

func TestFadeWarnsByDefaultAndBlocksWhenConfigured(t *testing.T) {
	ec := EntryContext{Side: SideSell, Right: "CE", Direction: "UP", W52Status: "AT 52W HIGH"}
	b := New(t.TempDir())
	warns, err := b.CheckEntry(at("10:00"), allRules, ec, liquid, 40)
	if err != nil || len(warns) != 1 {
		t.Fatalf("default should warn, not block: warns=%v err=%v", warns, err)
	}
	strict := allRules
	strict.Block52wFade = true
	if _, err := b.CheckEntry(at("10:00"), strict, ec, liquid, 40); err == nil {
		t.Error("Block52wFade should refuse the entry")
	}
}

// ── Daily loss halt ──

func TestDailyLossHaltLatches(t *testing.T) {
	dir := t.TempDir()
	b := New(dir)
	r := allRules // limit ₹5,000

	// A ₹6,000 realised loss today trips it.
	b.Trades = append(b.Trades, TradeRecord{PnL: -6000, ExitTime: at("10:30")})
	if tripped, pnl := b.CheckDailyLoss(at("10:31"), r); !tripped || pnl != -6000 {
		t.Fatalf("expected trip at -6000, got tripped=%v pnl=%.0f", tripped, pnl)
	}
	// Second call does not re-trip (alert fires once).
	if tripped, _ := b.CheckDailyLoss(at("10:32"), r); tripped {
		t.Error("halt tripped twice")
	}
	// A later profit does not re-open trading the same day.
	b.Trades = append(b.Trades, TradeRecord{PnL: 9000, ExitTime: at("11:00")})
	if halted, _ := b.Halted(at("11:01")); !halted {
		t.Error("halt should latch for the day despite recovery")
	}
	if _, err := b.CheckEntry(at("11:01"), r, sellPE(), liquid, 40); err == nil || !strings.Contains(err.Error(), "halted") {
		t.Errorf("entry during halt: %v", err)
	}
	// A restart keeps the halt.
	if halted, _ := New(dir).Halted(at("12:00")); !halted {
		t.Error("halt lost across restart")
	}
	// Tomorrow trading reopens.
	if halted, _ := b.Halted(at("10:00").AddDate(0, 0, 1)); halted {
		t.Error("halt carried into the next day")
	}
}

func TestDayPnLCountsOpenLossAndIgnoresYesterday(t *testing.T) {
	b := New(t.TempDir())
	b.Trades = append(b.Trades, TradeRecord{PnL: -20000, ExitTime: at("10:00").AddDate(0, 0, -1)})
	b.Positions["X"] = &Position{Ticker: "X", Side: SideSell, Entry: 40, LTP: 50, Lots: 1, LotSize: 600}
	if got := b.DayPnL(at("10:00")); got != -6000 {
		t.Errorf("DayPnL = %.0f, want -6000 (open loss only)", got)
	}
	if tripped, _ := b.CheckDailyLoss(at("10:00"), allRules); !tripped {
		t.Error("open MTM loss past the limit should trip the halt")
	}
}

func TestDailyLossOffWithoutCapital(t *testing.T) {
	b := New(t.TempDir())
	b.Trades = append(b.Trades, TradeRecord{PnL: -1e6, ExitTime: at("10:00")})
	if tripped, _ := b.CheckDailyLoss(at("10:01"), Rules{DailyLossPct: 0.5}); tripped {
		t.Error("halt must not trip with no capital configured")
	}
}

func TestTakeHaltNoticeOnce(t *testing.T) {
	b := New(t.TempDir())
	b.Trades = append(b.Trades, TradeRecord{PnL: -6000, ExitTime: at("10:00")})
	b.CheckDailyLoss(at("10:01"), allRules)
	if _, ok := b.TakeHaltNotice(at("10:01")); !ok {
		t.Fatal("first notice not delivered")
	}
	if _, ok := b.TakeHaltNotice(at("10:02")); ok {
		t.Error("notice delivered twice")
	}
}

func TestFlattenAllClosesEverything(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{Ticker: "A", Side: SideSell, Entry: 40, Lots: 1, LotSize: 100})
	b.Enter(&Position{Ticker: "B", Side: SideBuy, Entry: 20, Lots: 1, LotSize: 100})
	m := &fakeMon{}
	events := b.FlattenAll(m, 1, "HALT")
	if len(events) != 2 || len(b.List()) != 0 || len(m.calls) != 0 {
		t.Fatalf("flatten: %d events, %d left open, %d orders", len(events), len(b.List()), len(m.calls))
	}
	for _, tr := range b.Trades {
		if tr.Reason != "HALT" {
			t.Errorf("reason = %q, want HALT", tr.Reason)
		}
	}
}

// ── Trailing stop ──

func TestTrailShortTightensOnly(t *testing.T) {
	p := &Position{Side: SideSell, Entry: 40, SLPrice: 58, LTP: 40}
	// 20% in profit: below the 30% trigger, nothing changes.
	p.LTP = 32
	if p.trail(30, 20); p.SLPrice != 58 || p.Trailed {
		t.Fatalf("trailed before trigger: SL %.2f", p.SLPrice)
	}
	// 30% in profit (₹28): SL to 28 × 1.2 = 33.60.
	p.LTP = 28
	if armed := p.trail(30, 20); !armed || math.Abs(p.SLPrice-33.6) > 1e-9 {
		t.Fatalf("SL = %.2f armed=%v, want 33.60 armed", p.SLPrice, armed)
	}
	// Premium bounces to 31: best stays 28, SL must not loosen.
	p.LTP = 31
	if p.trail(30, 20); math.Abs(p.SLPrice-33.6) > 1e-9 {
		t.Errorf("SL loosened to %.2f on a bounce", p.SLPrice)
	}
	// New low 20: SL follows down to 24.
	p.LTP = 20
	if armed := p.trail(30, 20); armed || math.Abs(p.SLPrice-24) > 1e-9 {
		t.Errorf("SL = %.2f armed=%v, want 24 and no re-arm alert", p.SLPrice, armed)
	}
}

func TestTrailLongCreatesStopWhenNoneExisted(t *testing.T) {
	p := &Position{Side: SideBuy, Entry: 10, LTP: 14} // no SL (risk > premium)
	if armed := p.trail(30, 20); !armed || math.Abs(p.SLPrice-11.2) > 1e-9 {
		t.Fatalf("long SL = %.2f, want 11.20 (14 × 0.8)", p.SLPrice)
	}
	p.LTP = 12
	if p.trail(30, 20); math.Abs(p.SLPrice-11.2) > 1e-9 {
		t.Errorf("long SL moved down to %.2f", p.SLPrice)
	}
}

func TestTrailedStopExitsAsTSL(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{Ticker: "A", Side: SideSell, Entry: 40, Lots: 1, LotSize: 100, SLPrice: 58})
	b.Positions["A"].LTP = 28
	if armed := b.UpdateTrails(30, 20); len(armed) != 1 {
		t.Fatalf("expected 1 armed, got %d", len(armed))
	}
	b.Positions["A"].LTP = 34 // through the 33.60 trail
	events := b.CheckStops(nil, true, 0)
	if len(events) != 1 || events[0].Reason != "TSL" || b.Trades[0].Reason != "TSL" {
		t.Fatalf("trailed stop exit: %+v trades=%+v", events, b.Trades)
	}
	if b.Trades[0].PnL <= 0 {
		t.Errorf("trailed short should exit in profit, got ₹%.0f", b.Trades[0].PnL)
	}
}

func TestUpdateTrailsOff(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{Ticker: "A", Side: SideSell, Entry: 40, Lots: 1, LotSize: 100, SLPrice: 58})
	b.Positions["A"].LTP = 5
	if b.UpdateTrails(0, 20); b.Positions["A"].SLPrice != 58 {
		t.Error("trailing ran with trigger 0")
	}
}
