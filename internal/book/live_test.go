package book

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// fakeBroker is a scripted broker: orders get IDs O1, O2, …; tests set each
// order's state and the net positions directly.
type fakeBroker struct {
	placed    []string // "TICKER qty@limit sell=bool"
	orders    map[string]OrderState
	cancelled []string
	net       map[string]int
	placeErr  error
}

func newFakeBroker() *fakeBroker {
	return &fakeBroker{orders: map[string]OrderState{}, net: map[string]int{}}
}

func (f *fakeBroker) PlaceLimit(ticker string, qty int, limit float64, sell bool) (string, error) {
	if f.placeErr != nil {
		return "", f.placeErr
	}
	id := fmt.Sprintf("O%d", len(f.placed)+1)
	f.placed = append(f.placed, fmt.Sprintf("%s %d@%.2f sell=%v", ticker, qty, limit, sell))
	f.orders[id] = OrderState{Status: fyers.OrderPending, Remaining: qty}
	return id, nil
}
func (f *fakeBroker) Order(id string) (OrderState, error) {
	st, ok := f.orders[id]
	if !ok {
		return OrderState{}, errors.New("unknown order")
	}
	return st, nil
}
func (f *fakeBroker) Cancel(id string) error {
	f.cancelled = append(f.cancelled, id)
	return nil
}
func (f *fakeBroker) NetPositions() (map[string]int, error) { return f.net, nil }

var t0 = time.Date(2026, 10, 23, 10, 30, 0, 0, time.Local)

const entryTO, exitTO = 2 * time.Minute, time.Minute

// liveShort is a 2-lot short submitted live at ₹20 with a ₹28 stop / ₹14 target.
func liveShort(t *testing.T) (*Book, *fakeBroker) {
	t.Helper()
	b, br := New(t.TempDir()), newFakeBroker()
	p := &Position{Underlying: "X", Ticker: "X26OCT100PE", Right: "PE", Side: SideSell,
		Lots: 2, LotSize: 500, Entry: 20, LiveOrder: true, SLPrice: 28, TargetPrice: 14, Capital: 200000}
	if err := b.SubmitEntry(p, br, t0); err != nil {
		t.Fatal(err)
	}
	return b, br
}

func TestLiveEntryIsPendingUntilFilled(t *testing.T) {
	b, br := liveShort(t)
	p := b.Positions["X26OCT100PE"]
	if p.State != StatePendingEntry || p.OrderID != "O1" || br.placed[0] != "X26OCT100PE 1000@20.00 sell=true" {
		t.Fatalf("pending entry = %+v, orders %v", p, br.placed)
	}
	// Pending entries are not managed: no stop can fire on an unfilled order.
	p.LTP = 50
	if ev := b.CheckStops(br, true, 1); len(ev) != 0 {
		t.Fatal("stop fired on an unfilled entry")
	}
	p.LTP = 20

	// Still pending → nothing changes.
	if ev := b.ReconcileOrders(br, t0.Add(30*time.Second), entryTO, exitTO); len(ev) != 0 {
		t.Fatalf("events while pending: %v", ev)
	}
	// Filled at 20.30 (a seller can fill above the limit).
	br.orders["O1"] = OrderState{Status: fyers.OrderFilled, Filled: 1000, AvgPrice: 20.30}
	ev := b.ReconcileOrders(br, t0.Add(time.Minute), entryTO, exitTO)
	if len(ev) != 1 || ev[0].Kind != "FILLED" {
		t.Fatalf("events = %v", ev)
	}
	if p.State != StateOpen || p.Entry != 20.30 || math.Abs(p.SLPrice-28.30) > 1e-9 || math.Abs(p.TargetPrice-14.30) > 1e-9 {
		t.Errorf("filled position = %+v (stop/target should keep their distances from the real fill)", p)
	}
}

func TestLivePartialEntryKeepsFilledLotsAfterTimeout(t *testing.T) {
	b, br := liveShort(t)
	br.orders["O1"] = OrderState{Status: fyers.OrderPending, Filled: 500, Remaining: 500, AvgPrice: 20}
	// Past the 2-minute timeout: cancel the remainder (once).
	ev := b.ReconcileOrders(br, t0.Add(3*time.Minute), entryTO, exitTO)
	if len(ev) != 1 || ev[0].Kind != "CANCELLING" || len(br.cancelled) != 1 {
		t.Fatalf("events %v, cancels %v", ev, br.cancelled)
	}
	if b.ReconcileOrders(br, t0.Add(3*time.Minute+10*time.Second), entryTO, exitTO); len(br.cancelled) != 1 {
		t.Error("cancel sent twice")
	}
	// Broker confirms: cancelled with 500 filled.
	br.orders["O1"] = OrderState{Status: fyers.OrderCancelled, Filled: 500, AvgPrice: 20}
	ev = b.ReconcileOrders(br, t0.Add(4*time.Minute), entryTO, exitTO)
	p := b.Positions["X26OCT100PE"]
	if len(ev) != 1 || ev[0].Kind != "PARTIAL" || p.State != StateOpen || p.Lots != 1 || p.Capital != 100000 {
		t.Fatalf("partial: events %v, position %+v", ev, p)
	}
}

func TestLiveRejectedEntryRemovesPosition(t *testing.T) {
	b, br := liveShort(t)
	br.orders["O1"] = OrderState{Status: fyers.OrderRejected, Message: "insufficient margin"}
	ev := b.ReconcileOrders(br, t0.Add(time.Minute), entryTO, exitTO)
	if len(ev) != 1 || ev[0].Kind != "REJECTED" || !strings.Contains(ev[0].Msg, "insufficient margin") {
		t.Fatalf("events = %v", ev)
	}
	if len(b.List()) != 0 || len(b.Trades) != 0 {
		t.Error("a rejected entry must leave no position and no trade")
	}
}

func TestLiveEntryOrderFailureRecordsNothing(t *testing.T) {
	b, br := New(t.TempDir()), newFakeBroker()
	br.placeErr = errors.New("order rejected: RMS")
	err := b.SubmitEntry(&Position{Ticker: "X", Lots: 1, LotSize: 100, Entry: 10, LiveOrder: true}, br, t0)
	if err == nil || len(b.List()) != 0 {
		t.Fatalf("err=%v, positions %d", err, len(b.List()))
	}
	if err := b.SubmitEntry(&Position{Ticker: "Y", Lots: 1, LotSize: 100, Entry: 10, LiveOrder: true}, nil, t0); err == nil {
		t.Error("live entry without a broker must fail")
	}
}

// filledShort is liveShort with its entry filled at ₹20.
func filledShort(t *testing.T) (*Book, *fakeBroker, *Position) {
	b, br := liveShort(t)
	br.orders["O1"] = OrderState{Status: fyers.OrderFilled, Filled: 1000, AvgPrice: 20}
	b.ReconcileOrders(br, t0.Add(time.Minute), entryTO, exitTO)
	return b, br, b.Positions["X26OCT100PE"]
}

func TestLiveStopExitStaysOpenUntilFilled(t *testing.T) {
	b, br, p := filledShort(t)
	p.LTP = 28.5 // through the 28 stop
	ev := b.CheckStops(br, true, 1)
	if len(ev) != 1 || !ev[0].Pending || p.State != StatePendingExit || br.placed[1] != "X26OCT100PE 1000@28.79 sell=false" {
		t.Fatalf("stop exit: events %+v, pos %+v, orders %v", ev, p, br.placed)
	}
	if len(b.Trades) != 0 {
		t.Fatal("trade booked before the exit filled")
	}
	br.orders["O2"] = OrderState{Status: fyers.OrderFilled, Filled: 1000, AvgPrice: 28.60}
	rev := b.ReconcileOrders(br, t0.Add(5*time.Minute), entryTO, exitTO)
	if len(rev) != 1 || rev[0].Kind != "EXIT_FILLED" || len(b.List()) != 0 {
		t.Fatalf("exit fill: %v, open %d", rev, len(b.List()))
	}
	tr := b.Trades[0]
	if tr.Exit != 28.60 || tr.Reason != "SL" || math.Abs(tr.PnL-(-8600)) > 1e-6 {
		t.Errorf("trade = %+v (booked at the real fill, ₹-8,600)", tr)
	}
}

func TestLiveExitRejectedReturnsToOpen(t *testing.T) {
	b, br, p := filledShort(t)
	if _, pending, err := b.SubmitExit(p.Ticker, 18, "MANUAL", br, t0.Add(2*time.Minute)); err != nil || !pending {
		t.Fatalf("submit exit: pending=%v err=%v", pending, err)
	}
	if _, _, err := b.SubmitExit(p.Ticker, 18, "MANUAL", br, t0.Add(2*time.Minute)); err == nil {
		t.Error("a second exit while one is working must be refused")
	}
	br.orders["O2"] = OrderState{Status: fyers.OrderRejected, Message: "price out of range"}
	ev := b.ReconcileOrders(br, t0.Add(3*time.Minute), entryTO, exitTO)
	if len(ev) != 1 || ev[0].Kind != "EXIT_FAILED" || p.State != StateOpen || len(b.Trades) != 0 {
		t.Fatalf("rejected exit: %v, pos %+v", ev, p)
	}
	if !p.Managed() {
		t.Error("a position back to open must be managed by the stop monitor again")
	}
}

func TestLiveExitTimeoutCancelsAndPartialFillIsBooked(t *testing.T) {
	b, br, p := filledShort(t)
	b.SubmitExit(p.Ticker, 18, "TARGET", br, t0.Add(2*time.Minute))
	br.orders["O2"] = OrderState{Status: fyers.OrderPending, Filled: 500, Remaining: 500, AvgPrice: 18}
	ev := b.ReconcileOrders(br, t0.Add(3*time.Minute+time.Second), entryTO, exitTO)
	if len(ev) != 1 || ev[0].Kind != "CANCELLING" || br.cancelled[0] != "O2" {
		t.Fatalf("exit timeout: %v, cancels %v", ev, br.cancelled)
	}
	br.orders["O2"] = OrderState{Status: fyers.OrderCancelled, Filled: 500, AvgPrice: 18}
	ev = b.ReconcileOrders(br, t0.Add(4*time.Minute), entryTO, exitTO)
	if len(ev) != 1 || ev[0].Kind != "EXIT_PARTIAL" {
		t.Fatalf("partial exit: %v", ev)
	}
	if p.Lots != 1 || p.State != StateOpen || len(b.Trades) != 1 || b.Trades[0].Lots != 1 || b.Trades[0].PnL != 1000 {
		t.Errorf("after partial exit: pos %+v, trades %+v", p, b.Trades)
	}
}

// Exiting a live entry that hasn't filled cancels the entry order instead.
func TestExitOfPendingEntryCancelsIt(t *testing.T) {
	b, br := liveShort(t)
	_, pending, err := b.SubmitExit("X26OCT100PE", 19, "MANUAL", br, t0.Add(10*time.Second))
	if err != nil || !pending || len(br.cancelled) != 1 || br.cancelled[0] != "O1" || len(br.placed) != 1 {
		t.Fatalf("pending=%v err=%v cancels=%v placed=%v", pending, err, br.cancelled, br.placed)
	}
}

func TestPaperUnaffected(t *testing.T) {
	b, br := New(t.TempDir()), newFakeBroker()
	b.SubmitEntry(&Position{Ticker: "P", Side: SideSell, Lots: 1, LotSize: 100, Entry: 10}, br, t0)
	tr, pending, err := b.SubmitExit("P", 8, "MANUAL", br, t0)
	if err != nil || pending || tr == nil || tr.PnL != 200 || len(br.placed) != 0 {
		t.Fatalf("paper must fill instantly with no broker orders: tr=%+v pending=%v err=%v placed=%v", tr, pending, err, br.placed)
	}
}

func TestReconcilePositionsFlagsAndResolvesMismatch(t *testing.T) {
	b, br, p := filledShort(t)
	br.net["X26OCT100PE"] = -1000 // matches the book (short 1000)
	if ev, _ := b.ReconcilePositions(br); len(ev) != 0 || p.Mismatch != "" {
		t.Fatalf("matching position flagged: %v", ev)
	}
	// Someone closed it in the Fyers app.
	br.net["X26OCT100PE"] = 0
	ev, _ := b.ReconcilePositions(br)
	if len(ev) != 1 || ev[0].Kind != "MISMATCH" || p.Managed() {
		t.Fatalf("mismatch: %v, managed=%v", ev, p.Managed())
	}
	// Suspended: the stop must NOT send a buy-back that would open a long.
	p.LTP = 50
	if sev := b.CheckStops(br, true, 1); len(sev) != 0 || len(br.placed) != 1 {
		t.Fatalf("stop fired on a mismatched position: %+v", br.placed)
	}
	if _, _, err := b.SubmitExit(p.Ticker, 50, "MANUAL", br, t0); err == nil {
		t.Error("manual exit of a mismatched position must be refused")
	}
	// Not re-alerted every minute.
	if ev, _ := b.ReconcilePositions(br); len(ev) != 0 {
		t.Errorf("mismatch re-alerted: %v", ev)
	}
	// Restored → resolved.
	br.net["X26OCT100PE"] = -1000
	if ev, _ := b.ReconcilePositions(br); len(ev) != 1 || ev[0].Kind != "RESOLVED" || !p.Managed() {
		t.Fatalf("resolve: %v", ev)
	}
}

func TestReconcilePositionsReportsUntrackedOnce(t *testing.T) {
	b, br := New(t.TempDir()), newFakeBroker()
	br.net["SBIN26OCT800CE"] = -750
	br.net["SBIN-EQ"] = 100 // not an option: ignored
	ev, _ := b.ReconcilePositions(br)
	if len(ev) != 1 || ev[0].Kind != "UNTRACKED" || ev[0].Ticker != "SBIN26OCT800CE" {
		t.Fatalf("untracked: %v", ev)
	}
	if ev, _ := b.ReconcilePositions(br); len(ev) != 0 {
		t.Error("untracked position reported twice")
	}
}

// Paper positions are never compared with the broker.
func TestReconcileIgnoresPaper(t *testing.T) {
	b, br := New(t.TempDir()), newFakeBroker()
	b.Enter(&Position{Ticker: "P26OCT100PE", Side: SideSell, Lots: 1, LotSize: 100, Entry: 10})
	if ev, _ := b.ReconcilePositions(br); len(ev) != 0 {
		t.Errorf("paper position compared with the broker: %v", ev)
	}
}

func TestDropOnlyMismatched(t *testing.T) {
	b, br, p := filledShort(t)
	if err := b.Drop(p.Ticker); err == nil {
		t.Fatal("dropped a healthy position")
	}
	br.net[p.Ticker] = 0
	b.ReconcilePositions(br)
	if err := b.Drop(p.Ticker); err != nil || len(b.List()) != 0 || len(b.Trades) != 0 {
		t.Fatalf("drop: err=%v open=%d trades=%d", err, len(b.List()), len(b.Trades))
	}
}

// Pending state survives a restart: the reconciler picks it up again.
func TestPendingStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	b, br := New(dir), newFakeBroker()
	b.SubmitEntry(&Position{Ticker: "X26OCT100PE", Side: SideSell, Lots: 1, LotSize: 500, Entry: 20, LiveOrder: true}, br, t0)
	again := New(dir)
	if p := again.Positions["X26OCT100PE"]; p == nil || p.State != StatePendingEntry || p.OrderID != "O1" {
		t.Fatalf("after restart: %+v", p)
	}
	br.orders["O1"] = OrderState{Status: fyers.OrderFilled, Filled: 500, AvgPrice: 20.1}
	if ev := again.ReconcileOrders(br, t0.Add(time.Minute), entryTO, exitTO); len(ev) != 1 || ev[0].Kind != "FILLED" {
		t.Fatalf("reconcile after restart: %v", ev)
	}
}

func TestOrderTagsAreUniqueAndAlphanumeric(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		tag := newOrderTag()
		if seen[tag] {
			t.Fatalf("duplicate tag %s", tag)
		}
		seen[tag] = true
		for _, r := range tag {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				t.Fatalf("tag %q has a non-alphanumeric character", tag)
			}
		}
	}
}

// An unfilled entry has no P&L, so it can't count toward the daily loss limit.
func TestPendingEntryHasNoPnL(t *testing.T) {
	b, _ := liveShort(t)
	b.Positions["X26OCT100PE"].LTP = 40 // price moved while the order is unfilled
	if got := b.DayPnL(t0); got != 0 {
		t.Errorf("DayPnL = %.0f with only an unfilled entry, want 0", got)
	}
}
