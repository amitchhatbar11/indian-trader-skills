package desk

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func ist(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, IST)
	if err != nil {
		t, err = time.ParseInLocation("2006-01-02", s, IST)
		if err != nil {
			panic(err)
		}
	}
	return t
}

// The desk must never reach a real order path: no order placement, cancel
// or book entry anywhere in the package or its command.
func TestNoOrderAPI(t *testing.T) {
	bad := regexp.MustCompile(`\b(PlaceOrder|PlaceLimit|PlaceLimitOrder|CancelOrder|SubmitEntry|SubmitExit|doRequestOnce|place_order)\b|"/orders(/sync)?["?]|/v2/order/|/orders/regular`)
	for _, dir := range []string{".", "../../cmd/desk", "../fyers", "../breeze"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		if len(files) == 0 {
			t.Fatalf("no sources in %s", dir)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if m := bad.Find(data); m != nil {
				t.Errorf("%s references %q — the desk is paper-only and must never call an order API", f, m)
			}
		}
	}
}

func TestParseInstrument(t *testing.T) {
	for _, c := range []struct {
		in     string
		opt    bool
		under  string
		strike float64
		right  string
		month  time.Month
	}{
		{"reliance", false, "RELIANCE", 0, "", 0},
		{"NSE:RELIANCE-EQ", false, "RELIANCE", 0, "", 0},
		{"RELIANCE26NOV1440CE", true, "RELIANCE", 1440, "CE", time.November},
		{"M&M26DEC3250.5PE", true, "M&M", 3250.5, "PE", time.December},
		{"BAJAJ-AUTO27JAN9000CE", true, "BAJAJ-AUTO", 9000, "CE", time.January},
	} {
		got := ParseInstrument(c.in)
		if got.Option != c.opt || got.Underlying != c.under || got.Strike != c.strike || got.Right != c.right || got.Month != c.month {
			t.Errorf("%s → %+v", c.in, got)
		}
	}
	if s := OptionTicker("RELIANCE", ist("2026-11-24"), 1440, "ce"); s != "RELIANCE26NOV1440CE" {
		t.Errorf("OptionTicker = %s", s)
	}
}

func TestTicks(t *testing.T) {
	stock := Instrument{Symbol: "X"}
	for _, c := range []struct{ px, tick float64 }{{100, 0.01}, {250, 0.05}, {999, 0.05}, {1500, 0.10}, {7000, 0.50}, {15000, 1}, {30000, 5}} {
		if got := TickSize(stock, c.px); got != c.tick {
			t.Errorf("tick at ₹%v = %v, want %v", c.px, got, c.tick)
		}
	}
	opt := Instrument{Option: true}
	if TickSize(opt, 200) != 0.01 || TickSize(opt, 2000) != 0.05 {
		t.Error("option ticks follow the underlying: 0.01 below ₹250, else 0.05")
	}
	if RoundTick(1012.37, 0.10, false) != 1012.3 || RoundTick(1012.31, 0.10, true) != 1012.4 || RoundTick(1012.30, 0.10, true) != 1012.3 {
		t.Error("RoundTick")
	}
	if !OnTick(1012.3, 0.1) || OnTick(1012.35, 0.1) {
		t.Error("OnTick")
	}
	if Rupees(1234567) != "₹12,34,567" || Rupees(-950) != "−₹950" || Rupees(100000) != "₹1,00,000" {
		t.Errorf("Rupees: %s %s %s", Rupees(1234567), Rupees(-950), Rupees(100000))
	}
}

const holidayJSON = `{"CM":[{"tradingDate":"24-Nov-2026","weekDay":"Tuesday","description":"Guru Nanak Jayanti"},
{"tradingDate":"25-Dec-2026","weekDay":"Friday","description":"Christmas"}],
"FO":[{"tradingDate":"24-Nov-2026","weekDay":"Tuesday","description":"Guru Nanak Jayanti"}]}`

func testCal(t *testing.T) *Calendar {
	c, err := ParseHolidays([]byte(holidayJSON))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCalendar(t *testing.T) {
	c := testCal(t)
	for _, x := range []struct {
		at, state string
		open      bool
	}{
		{"2026-11-23 10:00", "open", true},
		{"2026-11-23 09:05", "pre-open", false},
		{"2026-11-23 15:30", "closed — after the close", false},
		{"2026-11-24 10:00", "closed — holiday: Guru Nanak Jayanti", false},
		{"2026-11-21 10:00", "closed — weekend", false},
	} {
		st := c.At(ist(x.at))
		if st.Open != x.open || !strings.HasPrefix(st.State, x.state) {
			t.Errorf("%s: %+v", x.at, st)
		}
	}
	if n := c.At(ist("2026-11-23 16:00")).NextOpen; n.Format("2006-01-02 15:04") != "2026-11-25 09:15" {
		t.Errorf("next open after Monday's close (Tuesday a holiday) = %s", n)
	}
	// 22 Dec → 29 Dec: 23, 24, (25 holiday), 28, 29.
	if n := c.SessionsBetween(ist("2026-12-22"), ist("2026-12-29")); n != 4 {
		t.Errorf("sessions = %d, want 4", n)
	}
}
