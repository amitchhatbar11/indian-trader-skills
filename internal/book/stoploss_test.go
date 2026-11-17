package book

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// fakeMon records the orders CheckStops would place.
type fakeMon struct {
	calls []struct {
		ticker string
		qty    int
		limit  float64
		sell   bool
	}
	err error
}

func (f *fakeMon) PlaceLimit(ticker string, qty int, limit float64, sell bool) (string, error) {
	f.calls = append(f.calls, struct {
		ticker string
		qty    int
		limit  float64
		sell   bool
	}{ticker, qty, limit, sell})
	if f.err != nil {
		return "", f.err
	}
	return "ORD1", nil
}

// The stop must sit exactly where the loss equals the risk budget, and on the
// correct side of entry for each direction.
func TestStopLossPrice(t *testing.T) {
	// Long: 550 qty, ₹10,000 risk → 18.18/unit below entry.
	got := StopLossPrice(SideBuy, 35.60, 550, 10000)
	if math.Abs(got-17.4181818) > 1e-6 {
		t.Errorf("long SL = %.4f, want 17.4182", got)
	}
	// Short: same 18.18 distance, but ABOVE entry (36.55 + 18.1818).
	got = StopLossPrice(SideSell, 36.55, 550, 10000)
	if math.Abs(got-54.7318181) > 1e-6 {
		t.Errorf("short SL = %.4f, want 54.7318", got)
	}
	// Both sides put the stop the same distance from entry.
	longDist := 35.60 - StopLossPrice(SideBuy, 35.60, 550, 10000)
	shortDist := StopLossPrice(SideSell, 35.60, 550, 10000) - 35.60
	if math.Abs(longDist-shortDist) > 1e-9 {
		t.Errorf("stop distance differs by side: long %.4f, short %.4f", longDist, shortDist)
	}
	// A blank side is a short.
	if StopLossPrice("", 36.55, 550, 10000) != StopLossPrice(SideSell, 36.55, 550, 10000) {
		t.Error("blank side should size like a short")
	}
	// Losing exactly the risk budget lands on the stop.
	entry, qty, risk := 35.60, 550, 10000.0
	sl := StopLossPrice(SideBuy, entry, qty, risk)
	if loss := (entry - sl) * float64(qty); math.Abs(loss-risk) > 0.01 {
		t.Errorf("loss at SL = %.2f, want %.2f", loss, risk)
	}
}

// A long cannot lose more than the premium, so when the risk budget exceeds
// the whole position there is nothing to stop out of.
func TestLongStopClampsWhenRiskExceedsPremium(t *testing.T) {
	// 1.50 × 550 = ₹825 max loss, well under a ₹10,000 budget.
	if got := StopLossPrice(SideBuy, 1.50, 550, 10000); got != 0 {
		t.Errorf("SL = %.2f, want 0 (no stop applies)", got)
	}
	p := &Position{Side: SideBuy, Entry: 1.50, Lots: 1, LotSize: 550, LTP: 0.05,
		SLPrice: StopLossPrice(SideBuy, 1.50, 550, 10000)}
	if p.StopHit() {
		t.Error("a long with no applicable stop must never trigger one")
	}
	if got := p.MaxLoss(); math.Abs(got-825) > 1e-9 {
		t.Errorf("max loss = %.2f, want 825", got)
	}
	// Shorts have no ceiling, so their stop is always live.
	if StopLossPrice(SideSell, 1.50, 550, 10000) <= 0 {
		t.Error("a short should always get a stop")
	}
	if (&Position{Side: SideSell}).MaxLoss() != 0 {
		t.Error("short max loss is unbounded, report 0")
	}
}

func TestStopLossGuards(t *testing.T) {
	for _, c := range []struct {
		side  string
		entry float64
		qty   int
		risk  float64
	}{
		{SideBuy, 35.60, 0, 10000},  // no qty
		{SideBuy, 35.60, 550, 0},    // no risk budget (CAPITAL unset)
		{SideBuy, 0, 550, 10000},    // no entry
		{SideSell, 35.60, -1, 1000}, // negative qty
	} {
		if got := StopLossPrice(c.side, c.entry, c.qty, c.risk); got != 0 {
			t.Errorf("%+v should give no stop, got %.2f", c, got)
		}
	}
	// A zero SL or missing LTP never triggers.
	if (&Position{SLPrice: 0, LTP: 10}).StopHit() {
		t.Error("zero SL must not trigger")
	}
	if (&Position{SLPrice: 20, LTP: 0, Side: SideSell}).StopHit() {
		t.Error("missing LTP must not trigger")
	}
}

func TestStopHitDirection(t *testing.T) {
	long := &Position{Side: SideBuy, Entry: 35.60, SLPrice: 17.42}
	short := &Position{Side: SideSell, Entry: 36.55, SLPrice: 54.77}

	long.LTP = 17.50
	if long.StopHit() {
		t.Error("long above its stop should not trigger")
	}
	long.LTP = 17.42
	if !long.StopHit() {
		t.Error("long AT its stop should trigger")
	}
	long.LTP = 10
	if !long.StopHit() {
		t.Error("long below its stop should trigger")
	}

	short.LTP = 54.00
	if short.StopHit() {
		t.Error("short below its stop should not trigger")
	}
	short.LTP = 54.77
	if !short.StopHit() {
		t.Error("short AT its stop should trigger")
	}
	short.LTP = 80
	if !short.StopHit() {
		t.Error("short above its stop should trigger")
	}
}

// The closing order must go the opposite way to the position, and be priced
// through the market so a limit order actually fills.
func TestExitLimitIsMarketable(t *testing.T) {
	// Selling to close a long: price below the LTP.
	if got := exitLimit(20, 1, true); math.Abs(got-19.8) > 1e-9 {
		t.Errorf("long exit limit = %.4f, want 19.8", got)
	}
	// Buying to close a short: price above the LTP.
	if got := exitLimit(20, 1, false); math.Abs(got-20.2) > 1e-9 {
		t.Errorf("short exit limit = %.4f, want 20.2", got)
	}
	// Never below the exchange tick floor.
	if got := exitLimit(0.05, 50, true); got < 0.05 {
		t.Errorf("exit limit %.4f below tick floor", got)
	}
	if got := exitLimit(20, 0, true); got != 20 {
		t.Errorf("zero buffer should use the LTP exactly, got %.2f", got)
	}
}

func TestCheckStopsClosesAndReports(t *testing.T) {
	b := New(t.TempDir())
	// Long, stopped out.
	b.Enter(&Position{LiveOrder: true, Underlying: "A", Ticker: "ALONG", Side: SideBuy, Lots: 1, LotSize: 550,
		Entry: 35.60, RiskAmount: 10000, SLPrice: 17.42})
	// Short, comfortably inside its stop.
	b.Enter(&Position{LiveOrder: true, Underlying: "B", Ticker: "BSHORT", Side: SideSell, Lots: 1, LotSize: 550,
		Entry: 36.55, RiskAmount: 10000, SLPrice: 54.77})

	b.Positions["ALONG"].LTP = 17.00 // through the stop
	b.Positions["BSHORT"].LTP = 40.0 // fine

	m := &fakeMon{}
	events := b.CheckStops(m, true, 1)
	// Live: the exit order is placed, but the position stays open until the
	// fill is confirmed (see live_test.go for the fill lifecycle).
	if len(events) != 1 || !events[0].Pending || events[0].Exited {
		t.Fatalf("want 1 pending exit, got %+v", events)
	}
	if events[0].Position.Ticker != "ALONG" {
		t.Errorf("wrong position stopped: %s", events[0].Position.Ticker)
	}
	if len(m.calls) != 1 || !m.calls[0].sell {
		t.Errorf("closing a LONG must SELL: %+v", m.calls)
	}
	if m.calls[0].qty != 550 {
		t.Errorf("qty = %d, want 550", m.calls[0].qty)
	}
	if p := b.Positions["ALONG"]; p == nil || p.State != StatePendingExit || p.ExitReason != "SL" {
		t.Errorf("stopped live position should be PENDING_EXIT until filled: %+v", p)
	}
	// The monitor must not place a second exit while one is working.
	if again := b.CheckStops(m, true, 1); len(again) != 0 || len(m.calls) != 1 {
		t.Errorf("second exit placed while one was working: %+v", m.calls)
	}
	if _, ok := b.Positions["BSHORT"]; !ok {
		t.Error("untriggered position must stay open")
	}
}

func TestCheckStopsShortBuysBack(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{LiveOrder: true, Underlying: "B", Ticker: "S", Side: SideSell, Lots: 1, LotSize: 100,
		Entry: 20, RiskAmount: 1000, SLPrice: 30})
	b.Positions["S"].LTP = 31
	m := &fakeMon{}
	ev := b.CheckStops(m, true, 1)
	if len(ev) != 1 || !ev[0].Pending {
		t.Fatalf("short should have an exit order working: %+v", ev)
	}
	if m.calls[0].sell {
		t.Error("closing a SHORT must BUY, not sell")
	}
	if m.calls[0].limit <= 31 {
		t.Errorf("buy-back limit %.2f should be above the LTP to fill", m.calls[0].limit)
	}
}

// A rejected order must leave the position open — the book must never claim
// something is closed when the broker refused it.
func TestCheckStopsKeepsPositionWhenOrderFails(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{LiveOrder: true, Ticker: "X", Side: SideBuy, Lots: 1, LotSize: 100, Entry: 20,
		RiskAmount: 1000, SLPrice: 10})
	b.Positions["X"].LTP = 9

	m := &fakeMon{err: errors.New("insufficient funds")}
	ev := b.CheckStops(m, true, 1)
	if len(ev) != 1 || ev[0].Exited {
		t.Fatalf("failed order must report a non-exit: %+v", ev)
	}
	if _, still := b.Positions["X"]; !still {
		t.Fatal("position must stay open when the exit order is rejected")
	}
	// And it must back off rather than re-firing on the next tick.
	if ev2 := b.CheckStops(m, true, 1); len(ev2) != 0 {
		t.Errorf("should back off after a failure, got %+v", ev2)
	}
	if len(m.calls) != 1 {
		t.Errorf("only one order attempt expected, got %d", len(m.calls))
	}
	// After the backoff window it retries.
	b.Positions["X"].SLHitAt = time.Now().Add(-slRetryAfter - time.Second)
	if ev3 := b.CheckStops(m, true, 1); len(ev3) != 1 {
		t.Errorf("should retry after the backoff, got %+v", ev3)
	}
}

func TestCheckStopsAlertOnlyMode(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{LiveOrder: true, Ticker: "X", Side: SideBuy, Lots: 1, LotSize: 100, Entry: 20,
		RiskAmount: 1000, SLPrice: 10})
	b.Positions["X"].LTP = 9

	m := &fakeMon{}
	ev := b.CheckStops(m, false, 1) // autoExit off
	if len(ev) != 1 || ev[0].Exited {
		t.Fatalf("alert-only should report without exiting: %+v", ev)
	}
	if len(m.calls) != 0 {
		t.Error("alert-only must not place any order")
	}
	if _, still := b.Positions["X"]; !still {
		t.Error("alert-only must leave the position open")
	}
}

// A live position must get a real closing order even if nothing else says
// "live" — and a paper position never does, whatever the global setting was.
func TestCheckStopsFollowsPositionMode(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{Ticker: "LIVE", LiveOrder: true, Side: SideSell, Lots: 1, LotSize: 100, Entry: 20, SLPrice: 30})
	b.Enter(&Position{Ticker: "PAPER", Side: SideSell, Lots: 1, LotSize: 100, Entry: 20, SLPrice: 30})
	b.Positions["LIVE"].LTP, b.Positions["PAPER"].LTP = 31, 31
	m := &fakeMon{}
	b.CheckStops(m, true, 1)
	if len(m.calls) != 1 || m.calls[0].ticker != "LIVE" {
		t.Fatalf("orders = %+v, want exactly one for LIVE", m.calls)
	}
	ps := b.List()
	if len(ps) != 1 || ps[0].Ticker != "LIVE" || ps[0].State != StatePendingExit {
		t.Errorf("PAPER should be closed, LIVE pending its fill: %+v", ps)
	}
}

// Without a broker connection a live position must stay open.
func TestCheckStopsLiveWithoutBrokerStaysOpen(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{Ticker: "LIVE", LiveOrder: true, Side: SideSell, Lots: 1, LotSize: 100, Entry: 20, SLPrice: 30})
	b.Positions["LIVE"].LTP = 31
	if ev := b.CheckStops(nil, true, 1); len(ev) != 1 || ev[0].Exited || len(b.List()) != 1 {
		t.Fatalf("live position closed without an order: %+v", ev)
	}
}

func TestCheckStopsPaperMode(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{Ticker: "X", Side: SideBuy, Lots: 1, LotSize: 100, Entry: 20,
		RiskAmount: 1000, SLPrice: 10})
	b.Positions["X"].LTP = 9

	m := &fakeMon{}
	ev := b.CheckStops(m, true, 1) // paper position
	if len(ev) != 1 || !ev[0].Exited {
		t.Fatalf("paper mode should still close the book entry: %+v", ev)
	}
	if len(m.calls) != 0 {
		t.Error("paper mode must not place a real order")
	}
	if len(b.RecentTrades(5)) != 1 || b.RecentTrades(1)[0].Reason != "SL" {
		t.Error("trade should be recorded with reason SL")
	}
}

// Alert-only mode (AUTO_SL_EXIT=false) applies to manual positions; an
// auto-trader position is unattended and must still be closed.
func TestAutoPositionsAlwaysExitOnStop(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{Ticker: "AUTO", Auto: true, Side: SideSell, Lots: 1, LotSize: 100, Entry: 20, SLPrice: 30})
	b.Enter(&Position{Ticker: "MAN", Side: SideSell, Lots: 1, LotSize: 100, Entry: 20, SLPrice: 30})
	b.Positions["AUTO"].LTP, b.Positions["MAN"].LTP = 31, 31
	b.CheckStops(nil, false, 1)
	if ps := b.List(); len(ps) != 1 || ps[0].Ticker != "MAN" {
		t.Fatalf("want only the manual position left open, got %+v", ps)
	}
	if b.Trades[0].Reason != "SL" || !b.Trades[0].Auto {
		t.Errorf("auto stop trade = %+v", b.Trades[0])
	}
}

func TestTargetPrice(t *testing.T) {
	cases := []struct {
		side   string
		entry  float64
		qty    int
		amount float64
		want   float64
	}{
		{SideSell, 20, 1000, 10000, 10}, // ₹10k on 1000 qty = ₹10 of decay
		{SideBuy, 20, 1000, 10000, 30},  // long: premium must rise ₹10
		{SideSell, 50, 200, 10000, 0},   // needs the whole ₹50 premium — unreachable
		{SideSell, 7.05, 2500, 10000, 3.05},
		{SideSell, 20, 1000, 0, 0}, // off
		{SideSell, 20, 0, 10000, 0},
	}
	for _, c := range cases {
		if got := TargetPrice(c.side, c.entry, c.qty, c.amount); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("TargetPrice(%s, %.2f, %d, ₹%.0f) = %.2f, want %.2f", c.side, c.entry, c.qty, c.amount, got, c.want)
		}
	}
}

func TestTargetHitBooksProfit(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{Ticker: "S", Side: SideSell, Lots: 1, LotSize: 100, Entry: 20, SLPrice: 30, TargetPrice: 12})
	b.Positions["S"].LTP = 12.5
	if ev := b.CheckStops(nil, true, 1); len(ev) != 0 {
		t.Fatal("exited before the target")
	}
	b.Positions["S"].LTP = 11.9
	ev := b.CheckStops(nil, true, 1)
	if len(ev) != 1 || ev[0].Reason != "TARGET" || !ev[0].Exited {
		t.Fatalf("target exit = %+v", ev)
	}
	if tr := b.Trades[0]; tr.Reason != "TARGET" || tr.PnL <= 0 {
		t.Errorf("trade = %+v, want a TARGET exit in profit", tr)
	}
	if !strings.Contains(ev[0].Describe(), "TARGET HIT") {
		t.Errorf("alert = %q", ev[0].Describe())
	}
}

// A long's target is above entry.
func TestTargetHitLong(t *testing.T) {
	p := &Position{Side: SideBuy, Entry: 20, TargetPrice: 28, LTP: 27}
	if p.TargetHit() {
		t.Error("long hit target below it")
	}
	p.LTP = 28
	if !p.TargetHit() {
		t.Error("long at target not hit")
	}
}
