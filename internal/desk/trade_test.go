package desk

import (
	"math"
	"strings"
	"testing"
	"time"
)

// day5m builds a session of 5-minute bars from closes, each bar ±0.2% high/low.
func day5m(day string, closes []float64, vol float64) []Bar {
	t := ist(day + " 09:15")
	var out []Bar
	prev := closes[0]
	for i, c := range closes {
		hi, lo := math.Max(prev, c)*1.002, math.Min(prev, c)*0.998
		out = append(out, Bar{T: t.Add(time.Duration(i*5) * time.Minute), O: prev, H: hi, L: lo, C: c, Volume: vol})
		prev = c
	}
	return out
}

func TestTimeEntry(t *testing.T) {
	cal := testCal(t)
	// Range bars 100…101, then a close at 101.5 (above the range high ~101.2).
	bars := day5m("2026-11-23", []float64{100, 100.5, 101, 100.8, 101.5, 102}, 1000)
	bars[4].Volume = 3000
	r := EntryRule{Side: "long", Minutes: 15, BarMin: 5, LastEntry: "14:30"}
	e := TimeEntry("ABC", bars, r, ist("2026-11-23 09:40"), cal)
	if e.State != "triggered" || e.Price != 101.5 || e.At.Format("15:04") != "09:35" || e.VolRatio != 3 {
		t.Fatalf("%+v", e)
	}
	// Before that bar completes: still waiting.
	if e := TimeEntry("ABC", bars, r, ist("2026-11-23 09:39"), cal); e.State != "waiting" {
		t.Errorf("at 09:39 %+v", e)
	}
	// Volume filter: 3× passes, 4× does not.
	r.MinVolX = 4
	if e := TimeEntry("ABC", bars, r, ist("2026-11-23 09:45"), cal); e.State != "waiting" {
		t.Errorf("thin breakout triggered: %+v", e)
	}
	// Holiday and before the open.
	if e := TimeEntry("ABC", bars, r, ist("2026-11-24 10:00"), cal); e.State != "not_open" {
		t.Errorf("holiday: %+v", e)
	}
	// Short side.
	down := day5m("2026-11-23", []float64{100, 99.6, 99.8, 99.9, 99.0}, 1000)
	if e := TimeEntry("ABC", down, EntryRule{Side: "short", Minutes: 15, BarMin: 5, LastEntry: "14:30"}, ist("2026-11-23 09:40"), cal); e.State != "triggered" || e.Price != 99 {
		t.Errorf("short: %+v", e)
	}
}

func TestMakePlan(t *testing.T) {
	lim := DefaultLimits(1_000_000, 0.5, 1, 5)
	e := Entry{Symbol: "ABC", Side: "long", Price: 1012.37, RangeHigh: 1010, RangeLow: 990, State: "triggered"}
	p, err := MakePlan(e, ExitRule{Stop: "mid", TargetR: 2}, lim, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Tick ₹0.10 above ₹1,000; stop 1000.0, risk 12.37, target 1012.37+24.74 → rounded down 1037.1.
	if p.Tick != 0.1 || p.Stop != 1000 || p.Target != 1037.1 || p.RiskPerShr != 12.37 || p.Product != Intraday {
		t.Fatalf("%+v", p)
	}
	// Short: stop rounds up, target rounds up (towards the entry).
	s := Entry{Symbol: "ABC", Side: "short", Price: 990.0, RangeHigh: 1010, RangeLow: 990.5, State: "triggered"}
	p, err = MakePlan(s, ExitRule{Stop: "range", TargetR: 2}, lim, nil, 0)
	if err != nil || p.Stop != 1010 || p.Target != 950 {
		t.Fatalf("short %+v %v", p, err)
	}
	// Refusals: a stop on the wrong side, and a stop inside the spread.
	if _, err := MakePlan(e, ExitRule{Stop: "price", StopVal: 1015}, lim, nil, 0); err == nil || !strings.Contains(err.Error(), "losing side") {
		t.Errorf("wrong-side stop: %v", err)
	}
	q := &Quote{Bid: 1010, Ask: 1020}
	if _, err := MakePlan(e, ExitRule{Stop: "mid"}, lim, q, 0); err == nil || !strings.Contains(err.Error(), "spread") {
		t.Errorf("spread: %v", err)
	}
	// Options: pct stop and target from the limits, MARGIN product for a sale.
	o := Entry{Symbol: "ABC26NOV1100CE", Side: "short", Price: 20, State: "triggered"}
	p, err = MakePlan(o, ExitRule{Stop: "pct"}, lim, nil, 1012)
	if err != nil || p.Stop != 28 || p.Target != 14 || p.Product != Margin || p.Tick != 0.05 {
		t.Fatalf("option %+v %v", p, err)
	}
}

func TestSimulateAndReplay(t *testing.T) {
	// 20 identical rising sessions: every replay and every history run
	// reaches the target.
	var sessions [][]Bar
	for d := 0; d < 20; d++ {
		closes := make([]float64, sessionBars)
		for i := range closes {
			closes[i] = 100 + float64(i)*0.1
		}
		sessions = append(sessions, day5m(ist("2026-09-01").AddDate(0, 0, d).Format("2006-01-02"), closes, 1000))
	}
	p := Plan{Symbol: "ABC", Side: "long", Entry: 100.5, EntryAt: ist("2026-11-23 09:40"), Stop: 100, Target: 101.5, TargetR: 2, StopRule: "mid"}
	a, err := Simulate(p, sessions, 2000, 19, 2, "15:15")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Simulate(p, sessions, 2000, 19, 2, "15:15")
	if a.TargetFirst != 100 || a.AvgR != 2 || a.TargetFirst != b.TargetFirst || a.AvgR != b.AvgR {
		t.Fatalf("%+v", a)
	}
	if !strings.HasPrefix(a.Verdict, "Strongly toward the target") {
		t.Errorf("verdict %q", a.Verdict)
	}
	a.Summarize(Replay(p, sessions, 15, "15:15"))
	if a.HistTrades != 20 || a.HistTargets != 20 || a.HistAvgR != 2 {
		t.Errorf("history %d trades %d targets %v", a.HistTrades, a.HistTargets, a.HistAvgR)
	}
	if _, err := Simulate(p, sessions[:3], 10, 1, 2, "15:15"); err == nil {
		t.Error("3 sessions should be too few")
	}
	// Sessions keeps only full days.
	all := append(append([]Bar{}, sessions[0]...), sessions[1][:40]...)
	if n := len(Sessions(all, ist("2026-11-23"))); n != 1 {
		t.Errorf("full sessions = %d, want 1", n)
	}
}
