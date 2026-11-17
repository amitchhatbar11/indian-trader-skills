package fyers

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StockSpotSymbol returns the quotes symbol for a stock's cash leg
// (GetQuotes prepends "NSE:").
func StockSpotSymbol(sym string) string {
	return sym + "-EQ"
}

// BuildStockMonthlySymbol builds a stock monthly option symbol in Fyers v3
// format, e.g. RELIANCE26AUG1450CE (SYM+YY+MMM+STRIKE+CE|PE).
// Stock monthlies use a 3-letter month abbreviation with NO day, unlike
// NIFTY weeklies which use numeric month + day.
func BuildStockMonthlySymbol(sym string, strike float64, right string, expiry time.Time) string {
	yearStr := fmt.Sprintf("%02d", expiry.Year()%100)
	monthStr := strings.ToUpper(expiry.Format("Jan"))
	strikeStr := strconv.FormatFloat(strike, 'f', -1, 64)
	return fmt.Sprintf("%s%s%s%s%s", sym, yearStr, monthStr, strikeStr, strings.ToUpper(right))
}

// StockMonthlyExpiry returns the nominal monthly stock-derivative expiry:
// the last Tuesday of the month (NSE moved stock monthlies from Thursday to
// Tuesday in Sep 2025). Holiday shifts move the actual day earlier, which
// slightly affects DTE math but not symbols.
func StockMonthlyExpiry(year int, month time.Month) time.Time {
	d := time.Date(year, month+1, 1, 0, 0, 0, 0, time.Local).AddDate(0, 0, -1)
	for d.Weekday() != time.Tuesday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// NextStockMonthlyInWindow returns the first monthly expiry whose DTE from
// `from` falls inside [dteLo, dteHi], scanning this month and the next two.
// Returns nil if no expiry is inside the window today.
func NextStockMonthlyInWindow(from time.Time, dteLo, dteHi int) *time.Time {
	day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
	for i := 0; i < 3; i++ {
		m := day.AddDate(0, i, 0)
		exp := StockMonthlyExpiry(m.Year(), m.Month())
		dte := int(exp.Sub(day).Hours() / 24)
		if dte >= dteLo && dte <= dteHi {
			return &exp
		}
	}
	return nil
}

// StrikeCandidates returns plausible listed strikes near the target price,
// sorted nearest-first. Stock strike steps vary by price band (2.5 to 100),
// so candidates from all common steps are probed and the quote decides.
func StrikeCandidates(target float64) []float64 {
	steps := []float64{2.5, 5, 10, 20, 25, 50, 100}
	seen := map[float64]bool{}
	var out []float64
	for _, step := range steps {
		for _, k := range []float64{
			math.Round(target/step) * step,
			math.Floor(target/step) * step,
			math.Ceil(target/step) * step,
		} {
			if k > 0 && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return math.Abs(out[i]-target) < math.Abs(out[j]-target)
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}
