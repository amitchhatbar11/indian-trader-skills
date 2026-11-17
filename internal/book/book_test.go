package book

import (
	"testing"
	"time"
)

func pos(ticker string) *Position {
	return &Position{
		Underlying: "RELIANCE", Ticker: ticker, Right: "CE",
		Strike: 3100, Expiry: "28-Jul", Lots: 2, LotSize: 500,
		Entry: 45.50, EntryTime: time.Now(),
	}
}

func TestEnterExitPnL(t *testing.T) {
	b := New(t.TempDir())
	p := pos("RELIANCE26JUL3100CE")
	if err := b.Enter(p); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := b.Enter(pos("RELIANCE26JUL3100CE")); err == nil {
		t.Fatal("duplicate ticker should be rejected")
	}

	tr, err := b.Exit("RELIANCE26JUL3100CE", 30.00, "MANUAL")
	if err != nil {
		t.Fatalf("exit: %v", err)
	}
	// short: (45.50-30.00) * 2 lots * 500 = 15500
	if tr.PnL != 15500 {
		t.Errorf("PnL want 15500, got %.2f", tr.PnL)
	}
	if len(b.List()) != 0 {
		t.Error("position should be closed")
	}
	if len(b.RecentTrades(5)) != 1 {
		t.Error("trade should be recorded")
	}
}

func TestExitValidation(t *testing.T) {
	b := New(t.TempDir())
	if _, err := b.Exit("NOPE", 10, "MANUAL"); err == nil {
		t.Error("exit of unknown ticker should fail")
	}
	b.Enter(pos("X"))
	if _, err := b.Exit("X", 0, "MANUAL"); err == nil {
		t.Error("exit with zero limit should fail")
	}
}

func TestEnterValidation(t *testing.T) {
	b := New(t.TempDir())
	bad := pos("Y")
	bad.Entry = 0
	if err := b.Enter(bad); err == nil {
		t.Error("entry without a limit price should fail")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	b := New(dir)
	b.Enter(pos("RELIANCE26JUL3100CE"))
	b.Enter(pos("TCS26JUL2900PE"))
	b.Exit("TCS26JUL2900PE", 40, "MANUAL")

	b2 := New(dir)
	if len(b2.List()) != 1 || b2.List()[0].Ticker != "RELIANCE26JUL3100CE" {
		t.Errorf("positions not reloaded: %+v", b2.List())
	}
	if len(b2.RecentTrades(5)) != 1 {
		t.Error("trades not reloaded")
	}
}

func TestFind(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(pos("A"))
	p2 := pos("B")
	p2.Right = "PE"
	b.Enter(p2)
	if got := len(b.Find("reliance")); got != 2 {
		t.Errorf("Find should be case-insensitive and return 2, got %d", got)
	}
	if got := len(b.Find("TCS")); got != 0 {
		t.Errorf("Find TCS want 0, got %d", got)
	}
}

// ── Long vs short semantics ─────────────────────────────────────────────────

func TestShortAndLongPnLAreOpposite(t *testing.T) {
	short := &Position{Side: SideSell, Lots: 2, LotSize: 500, Entry: 45.50, LTP: 30}
	long := &Position{Side: SideBuy, Lots: 2, LotSize: 500, Entry: 45.50, LTP: 30}
	// Premium fell 15.50 on 1000 qty.
	if got := short.UnrealPnL(); got != 15500 {
		t.Errorf("short should GAIN on decay: %.0f, want 15500", got)
	}
	if got := long.UnrealPnL(); got != -15500 {
		t.Errorf("long should LOSE on decay: %.0f, want -15500", got)
	}
	// And the mirror case: premium rises.
	short.LTP, long.LTP = 60, 60
	if got := short.UnrealPnL(); got != -14500 {
		t.Errorf("short should lose as premium rises: %.0f", got)
	}
	if got := long.UnrealPnL(); got != 14500 {
		t.Errorf("long should gain as premium rises: %.0f", got)
	}
}

// Books written before buying existed have no side field; those positions
// were all shorts and must keep behaving as shorts.
func TestBlankSideIsTreatedAsShort(t *testing.T) {
	legacy := &Position{Lots: 1, LotSize: 100, Entry: 50, LTP: 30}
	if !legacy.IsShort() || legacy.SideLabel() != SideSell {
		t.Error("blank side must mean SELL")
	}
	if got := legacy.UnrealPnL(); got != 2000 {
		t.Errorf("legacy short P&L = %.0f, want 2000", got)
	}
	var tr TradeRecord
	if tr.SideLabel() != SideSell {
		t.Errorf("blank trade side = %q, want SELL", tr.SideLabel())
	}
}

func TestExitPnLBySide(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{Underlying: "X", Ticker: "LONG", Side: SideBuy, Lots: 1, LotSize: 100, Entry: 20})
	b.Enter(&Position{Underlying: "X", Ticker: "SHORT", Side: SideSell, Lots: 1, LotSize: 100, Entry: 20})

	// Premium doubles: the long profits, the short loses the same amount.
	lt, err := b.Exit("LONG", 40, "MANUAL")
	if err != nil {
		t.Fatal(err)
	}
	st, err := b.Exit("SHORT", 40, "MANUAL")
	if err != nil {
		t.Fatal(err)
	}
	if lt.PnL != 2000 {
		t.Errorf("long exit P&L = %.0f, want +2000", lt.PnL)
	}
	if st.PnL != -2000 {
		t.Errorf("short exit P&L = %.0f, want -2000", st.PnL)
	}
	if lt.Side != SideBuy || st.Side != SideSell {
		t.Errorf("sides not carried into trades: %q / %q", lt.Side, st.Side)
	}
}

func TestEstimateCapital(t *testing.T) {
	// Buying costs the full premium: 45.50 × 1000.
	if got := EstimateCapital(SideBuy, 45.50, 3000, 1000, 20); got != 45500 {
		t.Errorf("buy capital = %.0f, want 45500", got)
	}
	// Selling blocks margin off the underlying notional: 3000 × 1000 × 20%.
	if got := EstimateCapital(SideSell, 45.50, 3000, 1000, 20); got != 600000 {
		t.Errorf("sell margin = %.0f, want 600000", got)
	}
	// A blank side is a short, matching the legacy default.
	if EstimateCapital("", 45.50, 3000, 1000, 20) != 600000 {
		t.Error("blank side should be treated as a short for capital too")
	}
	// Selling almost always ties up far more than buying the same contract.
	buy := EstimateCapital(SideBuy, 45.50, 3000, 1000, 20)
	sell := EstimateCapital(SideSell, 45.50, 3000, 1000, 20)
	if sell <= buy {
		t.Errorf("short margin %.0f should exceed long premium %.0f", sell, buy)
	}
}

func TestTotalCapital(t *testing.T) {
	b := New(t.TempDir())
	b.Enter(&Position{Ticker: "A", Side: SideBuy, Lots: 1, LotSize: 100, Entry: 10, Capital: 1000})
	b.Enter(&Position{Ticker: "B", Side: SideSell, Lots: 1, LotSize: 100, Entry: 10, Capital: 50000})
	if got := b.TotalCapital(); got != 51000 {
		t.Errorf("total capital = %.0f, want 51000", got)
	}
	// Capital is released when the position closes.
	b.Exit("B", 5, "MANUAL")
	if got := b.TotalCapital(); got != 1000 {
		t.Errorf("after exit = %.0f, want 1000", got)
	}
}
