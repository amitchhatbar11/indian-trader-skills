package desk

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"
)

// NSE's 5-minute session: 09:15 to 15:30 is 75 bars (09:15 … 15:25).
const sessionBars = 75

// SimResult is the time machine's answer for a plan.
type SimResult struct {
	Replays     int        `json:"replays"`
	Sessions    int        `json:"pool_sessions"`
	TargetFirst float64    `json:"target_first_pct"`
	StopFirst   float64    `json:"stop_first_pct"`
	Neither     float64    `json:"neither_pct"`
	Green       float64    `json:"green_pct"`
	AvgR        float64    `json:"avg_r"`
	CloseP      [5]float64 `json:"close_p5_25_50_75_95"`
	Breakeven   float64    `json:"breakeven_pct"` // target-first share a TargetR plan needs to break even
	HitRate     float64    `json:"hit_rate_pct"`  // target-first among replays that hit a side
	History     []HistRun  `json:"history"`
	HistAvgR    float64    `json:"history_avg_r"`
	HistTrades  int        `json:"history_trades"`
	HistTargets int        `json:"history_targets"`
	HistStops   int        `json:"history_stops"`
	Verdict     string     `json:"verdict"`
	FlatBy      string     `json:"until"`
}

// HistRun is the plan's rule replayed on one past session.
type HistRun struct {
	Day     string  `json:"day"`
	Entered bool    `json:"entered"`
	Entry   float64 `json:"entry,omitempty"`
	Outcome string  `json:"outcome,omitempty"` // target / stop / close
	R       float64 `json:"r,omitempty"`
}

type poolBar struct{ o, h, l, c float64 } // ratios to the previous bar's close

// Sessions groups 5-minute bars into full sessions before `before` (days
// with all 75 bars; half days and gaps are left out).
func Sessions(bars []Bar, before time.Time) [][]Bar {
	by := map[string][]Bar{}
	var keys []string
	for _, b := range bars {
		if !dayOf(b.T).Before(dayOf(before)) {
			continue
		}
		k := b.T.Format("2006-01-02")
		if _, ok := by[k]; !ok {
			keys = append(keys, k)
		}
		by[k] = append(by[k], b)
	}
	sort.Strings(keys)
	var out [][]Bar
	for _, k := range keys {
		s := by[k]
		if len(s) == sessionBars && s[0].T.Format("15:04") == MarketOpen {
			out = append(out, s)
		}
	}
	return out
}

func slotOf(t time.Time) int {
	t = t.In(IST)
	return (t.Hour()*60 + t.Minute() - (9*60 + 15)) / 5
}

// Simulate replays the plan n times from its entry bar to `until`, drawing
// each bar from the same time of day (± window bars) of a random past
// session, scaled to the running price. A bar touching both the stop and
// the target counts as the stop.
func Simulate(p Plan, sessions [][]Bar, n int, seed int64, window int, until string) (SimResult, error) {
	res := SimResult{Replays: n, Sessions: len(sessions), FlatBy: until}
	if len(sessions) < 5 {
		return res, fmt.Errorf("only %d full sessions of 5-minute bars — need at least 5", len(sessions))
	}
	pool := make([][]poolBar, len(sessions))
	for i, s := range sessions {
		pool[i] = make([]poolBar, len(s))
		for j, b := range s {
			pc := b.O
			if j > 0 {
				pc = s[j-1].C
			}
			pool[i][j] = poolBar{b.O / pc, b.H / pc, b.L / pc, b.C / pc}
		}
	}
	start := 0
	if !p.EntryAt.IsZero() {
		start = slotOf(p.EntryAt) + 1 // the bar after the trigger bar
	}
	end := slotOf(Clock(time.Now(), until)) - 1 // the last bar completed by `until`
	if end >= sessionBars {
		end = sessionBars - 1
	}
	if start > end {
		return res, fmt.Errorf("entry at %s leaves no bars before %s", p.EntryAt.Format("15:04"), until)
	}
	long := p.Side != "short"
	risk := math.Abs(p.Entry - p.Stop)
	rng := rand.New(rand.NewSource(seed))
	closes := make([]float64, 0, n)
	var sumR float64
	var tgt, stp, nei, green int
	for r := 0; r < n; r++ {
		px := p.Entry
		out := ""
		for i := start; i <= end; i++ {
			j := i + rng.Intn(2*window+1) - window
			j = max(0, min(sessionBars-1, j))
			b := pool[rng.Intn(len(pool))][j]
			hi, lo, c := px*b.h, px*b.l, px*b.c
			if long {
				if lo <= p.Stop {
					out = "stop"
				} else if hi >= p.Target {
					out = "target"
				}
			} else {
				if hi >= p.Stop {
					out = "stop"
				} else if lo <= p.Target {
					out = "target"
				}
			}
			if out != "" {
				break
			}
			px = c
		}
		var rr float64
		switch out {
		case "target":
			tgt++
			rr = p.TargetR
			green++
		case "stop":
			stp++
			rr = -1
		default:
			nei++
			rr = (px - p.Entry) / risk
			if !long {
				rr = -rr
			}
			if rr > 0 {
				green++
			}
			closes = append(closes, px)
		}
		sumR += rr
	}
	f := func(k int) float64 { return round2(float64(k) / float64(n) * 100) }
	res.TargetFirst, res.StopFirst, res.Neither, res.Green = f(tgt), f(stp), f(nei), f(green)
	res.AvgR = round2(sumR / float64(n))
	sort.Float64s(closes)
	for i, q := range []float64{0.05, 0.25, 0.5, 0.75, 0.95} {
		if len(closes) > 0 {
			res.CloseP[i] = round2(closes[int(q*float64(len(closes)-1))])
		}
	}
	res.Breakeven = round2(100 / (1 + p.TargetR))
	if tgt+stp > 0 {
		res.HitRate = round2(float64(tgt) / float64(tgt+stp) * 100)
	}
	res.Verdict = verdict(res.HitRate, res.Breakeven, p.TargetR, tgt+stp)
	return res, nil
}

func verdict(hit, be, r float64, n int) string {
	if n == 0 {
		return "Neither side is reached in the replays — the plan's levels are too far for the time left."
	}
	d := hit - be
	name := "break-even"
	if math.Abs(r-1) < 0.01 {
		name = "a coin flip"
	}
	switch {
	case math.Abs(d) < 5:
		return fmt.Sprintf("Close to %s: the target comes first %.0f%% of the time; a %.1fR plan needs %.0f%%.", name, hit, r, be)
	case d >= 15:
		return fmt.Sprintf("Strongly toward the target: %.0f%% target-first against %.0f%% needed.", hit, be)
	case d > 0:
		return fmt.Sprintf("Leans toward the target: %.0f%% target-first against %.0f%% needed.", hit, be)
	case d <= -15:
		return fmt.Sprintf("Strongly toward the stop: only %.0f%% target-first against %.0f%% needed.", hit, be)
	}
	return fmt.Sprintf("Leans toward the stop: %.0f%% target-first against %.0f%% needed.", hit, be)
}

// Replay runs the plan's rule on every past session: the opening range of
// that day, entry on the first 5-minute close beyond it, the stop by the
// same rule (or the same distance in % for pct/atr/price stops), the target
// at the same R, flat at `until`.
func Replay(p Plan, sessions [][]Bar, rangeMin int, until string) []HistRun {
	var out []HistRun
	long := p.Side != "short"
	riskPct := math.Abs(p.Entry-p.Stop) / p.Entry
	for _, s := range sessions {
		day := s[0].T
		h := HistRun{Day: day.Format("2006-01-02")}
		nr := rangeMin / 5
		if nr < 1 || nr >= len(s) {
			continue
		}
		hi, lo := math.Inf(-1), math.Inf(1)
		for _, b := range s[:nr] {
			hi, lo = math.Max(hi, b.H), math.Min(lo, b.L)
		}
		last := slotOf(Clock(day, until)) - 1
		entryIdx := -1
		for i := nr; i <= last && i < len(s); i++ {
			if long && s[i].C > hi || !long && s[i].C < lo {
				entryIdx = i
				break
			}
		}
		if entryIdx < 0 {
			out = append(out, h)
			continue
		}
		e := s[entryIdx].C
		var stop float64
		switch p.StopRule {
		case "mid":
			stop = (hi + lo) / 2
		case "range":
			stop = lo
			if !long {
				stop = hi
			}
		default:
			stop = e * (1 - riskPct)
			if !long {
				stop = e * (1 + riskPct)
			}
		}
		risk := math.Abs(e - stop)
		if risk <= 0 || long && stop >= e || !long && stop <= e {
			out = append(out, h)
			continue
		}
		tgt := e + p.TargetR*risk
		if !long {
			tgt = e - p.TargetR*risk
		}
		h.Entered, h.Entry = true, round2(e)
		px := e
		for i := entryIdx + 1; i <= last && i < len(s); i++ {
			b := s[i]
			if long && b.L <= stop || !long && b.H >= stop {
				h.Outcome, h.R = "stop", -1
				break
			}
			if long && b.H >= tgt || !long && b.L <= tgt {
				h.Outcome, h.R = "target", p.TargetR
				break
			}
			px = b.C
		}
		if h.Outcome == "" {
			h.Outcome = "close"
			h.R = (px - e) / risk
			if !long {
				h.R = -h.R
			}
			h.R = round2(h.R)
		}
		out = append(out, h)
	}
	return out
}

// Summarize fills the history fields of res.
func (res *SimResult) Summarize(h []HistRun) {
	res.History = h
	var sum float64
	for _, x := range h {
		if !x.Entered {
			continue
		}
		res.HistTrades++
		sum += x.R
		switch x.Outcome {
		case "target":
			res.HistTargets++
		case "stop":
			res.HistStops++
		}
	}
	if res.HistTrades > 0 {
		res.HistAvgR = round2(sum / float64(res.HistTrades))
	}
}
