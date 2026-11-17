package fyers

import (
	"testing"
	"time"
)

func TestStockMonthlyExpiry(t *testing.T) {
	// Last Tuesdays: Jul 2026 -> 28th, Aug 2026 -> 25th
	if got := StockMonthlyExpiry(2026, time.July); got.Format("2006-01-02") != "2026-07-28" {
		t.Errorf("Jul 2026 expiry = %s, want 2026-07-28", got.Format("2006-01-02"))
	}
	if got := StockMonthlyExpiry(2026, time.August); got.Format("2006-01-02") != "2026-08-25" {
		t.Errorf("Aug 2026 expiry = %s, want 2026-08-25", got.Format("2006-01-02"))
	}
}

func TestNextStockMonthlyInWindow(t *testing.T) {
	// On 2026-07-24: Jul expiry is 4 DTE (too near), Aug 25 is 32 DTE (in window)
	from := time.Date(2026, 7, 24, 10, 0, 0, 0, time.Local)
	got := NextStockMonthlyInWindow(from, 28, 34)
	if got == nil || got.Format("2006-01-02") != "2026-08-25" {
		t.Fatalf("window expiry = %v, want 2026-08-25", got)
	}
	// On 2026-07-14: Jul is 14 DTE (below), Aug is 42 DTE (above) -> none
	from = time.Date(2026, 7, 14, 10, 0, 0, 0, time.Local)
	if got := NextStockMonthlyInWindow(from, 28, 34); got != nil {
		t.Errorf("expected no expiry in window, got %v", got)
	}
}

func TestBuildStockMonthlySymbol(t *testing.T) {
	exp := time.Date(2026, 8, 25, 0, 0, 0, 0, time.Local)
	if got := BuildStockMonthlySymbol("SBIN", 800, "CE", exp); got != "SBIN26AUG800CE" {
		t.Errorf("symbol = %s, want SBIN26AUG800CE", got)
	}
	if got := BuildStockMonthlySymbol("SBIN", 802.5, "PE", exp); got != "SBIN26AUG802.5PE" {
		t.Errorf("symbol = %s, want SBIN26AUG802.5PE", got)
	}
}

func TestStrikeCandidates(t *testing.T) {
	cands := StrikeCandidates(748.6)
	if len(cands) == 0 {
		t.Fatal("no candidates")
	}
	// sorted nearest-first
	for i := 1; i < len(cands); i++ {
		di := abs(cands[i] - 748.6)
		dp := abs(cands[i-1] - 748.6)
		if dp > di {
			t.Errorf("candidates not sorted by distance: %v", cands)
			break
		}
	}
	found := false
	for _, k := range cands {
		if k == 750 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 750 among candidates, got %v", cands)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
