package bhavcopy

import (
	"strings"
	"testing"
)

// NSE's real file pads header names and values with leading spaces and mixes
// series (EQ, BE, …). The fixture reproduces that so the parser is tested
// against the format it will actually meet.
const sampleCSV = `SYMBOL, SERIES, DATE1, PREV_CLOSE, OPEN_PRICE, HIGH_PRICE, LOW_PRICE, LAST_PRICE, CLOSE_PRICE, AVG_PRICE, TTL_TRD_QNTY, TURNOVER_LACS, NO_OF_TRADES, DELIV_QTY, DELIV_PER
RELIANCE, EQ, 05-Aug-2026, 1280.00, 1285.00, 1325.20, 1281.20, 1324.00, 1325.00, 1305.10, 20342297, 265470.11, 412034, 9123456, 44.85
M&M, EQ, 05-Aug-2026, 3200.00, 3210.00, 3250.00, 3195.00, 3245.00, 3244.00, 3222.10, 2084221, 67150.20, 91234, 1102233, 52.88
TATASTEEL, BE, 05-Aug-2026, 160.00, 161.00, 162.00, 159.50, 161.50, 161.40, 160.90, 5000000, 8045.00, 21000, 2500000, 50.00
SUSPENDCO, EQ, 05-Aug-2026, -, -, -, -, -, -, -, -, -, -, -, -
`

func TestParseTrimsAndFiltersToEQ(t *testing.T) {
	day, err := Parse(strings.NewReader(sampleCSV), nil)
	if err != nil {
		t.Fatal(err)
	}
	// EQ rows only: TATASTEEL is BE, SUSPENDCO has dashes for every number.
	if len(day.Rows) != 2 {
		t.Fatalf("got %d rows, want 2 (RELIANCE, M&M); rows=%v", len(day.Rows), day.Rows)
	}
	r, ok := day.Rows["RELIANCE"]
	if !ok {
		t.Fatal("RELIANCE missing — header/value trimming likely broke")
	}
	if r.PrevClose != 1280 || r.Open != 1285 || r.Close != 1325 || r.Volume != 20342297 {
		t.Errorf("RELIANCE parsed wrong: %+v", r)
	}
	// An ampersand symbol must survive verbatim so it matches the Nifty list.
	if _, ok := day.Rows["M&M"]; !ok {
		t.Error("M&M missing from parsed rows")
	}
	if _, ok := day.Rows["TATASTEEL"]; ok {
		t.Error("TATASTEEL is series BE and must be excluded")
	}
	if _, ok := day.Rows["SUSPENDCO"]; ok {
		t.Error("all-dashes row must be skipped, not zero-filled")
	}
}

func TestParseHonoursWantFilter(t *testing.T) {
	day, err := Parse(strings.NewReader(sampleCSV), map[string]bool{"RELIANCE": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(day.Rows) != 1 || day.Rows["RELIANCE"].Close != 1325 {
		t.Fatalf("want filter not applied: %v", day.Rows)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	if _, err := Parse(strings.NewReader("not,a,bhavcopy\n1,2,3\n"), nil); err == nil {
		t.Error("expected an error for a CSV missing the required columns")
	}
	if _, err := Parse(strings.NewReader(""), nil); err == nil {
		t.Error("expected an error for empty input")
	}
}
