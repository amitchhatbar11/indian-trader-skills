package desk

import (
	"fmt"
	"math"
	"time"
)

// Entry is the entry timer's call (~/.nse-desk/entry.json).
type Entry struct {
	Symbol    string    `json:"symbol"`
	Side      string    `json:"side"`            // long / short
	State     string    `json:"state"`           // waiting / triggered / expired / not_open
	Price     float64   `json:"price,omitempty"` // the trigger bar's close
	At        time.Time `json:"at,omitempty"`    // the trigger bar's start
	Level     float64   `json:"level"`           // the line it had to close beyond
	RangeHigh float64   `json:"range_high"`
	RangeLow  float64   `json:"range_low"`
	Minutes   int       `json:"range_minutes"`
	BarMin    int       `json:"bar_minutes"`
	VolRatio  float64   `json:"vol_ratio,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// EntryRule configures the entry timer.
type EntryRule struct {
	Side      string  // long / short
	Minutes   int     // opening range length (15)
	BarMin    int     // 1 or 5
	Level     float64 // a custom line instead of the range edge (0 = range)
	MinVolX   float64 // trigger bar volume ≥ this × the range bars' average (0 = off)
	LastEntry string  // no trigger after this (the entry window's end)
}

// TimeEntry applies the rule to today's bars completed by now: the first
// bar to CLOSE beyond the line (above for long, below for short) after the
// opening range.
func TimeEntry(sym string, bars []Bar, r EntryRule, now time.Time, cal *Calendar) Entry {
	e := Entry{Symbol: sym, Side: r.Side, Minutes: r.Minutes, BarMin: r.BarMin, State: "waiting"}
	if !cal.TradingDay(now) || now.In(IST).Format("15:04") < MarketOpen {
		e.State, e.Note = "not_open", cal.At(now).State
		return e
	}
	o := OpeningRange(sym, bars, r.Minutes, r.BarMin, now)
	e.RangeHigh, e.RangeLow = o.High, o.Low
	e.Level = o.High
	if r.Side == "short" {
		e.Level = o.Low
	}
	if r.Level > 0 {
		e.Level = r.Level
	}
	open := Clock(now, MarketOpen)
	rangeEnd := open.Add(time.Duration(r.Minutes) * time.Minute)
	last := Clock(now, r.LastEntry)
	var rng []Bar
	for _, b := range bars {
		if !dayOf(b.T).Equal(dayOf(now)) || b.T.Before(open) {
			continue
		}
		if b.T.Add(time.Duration(r.BarMin) * time.Minute).After(now) {
			break
		}
		if b.T.Before(rangeEnd) {
			rng = append(rng, b)
			continue
		}
		if !b.T.Before(last) {
			e.State, e.Note = "expired", "no close beyond the line before "+r.LastEntry
			return e
		}
		beyond := b.C > e.Level
		if r.Side == "short" {
			beyond = b.C < e.Level
		}
		if !beyond || e.Level <= 0 {
			continue
		}
		var avg float64
		for _, x := range rng {
			avg += x.Volume
		}
		if len(rng) > 0 {
			avg /= float64(len(rng))
		}
		vr := 0.0
		if avg > 0 {
			vr = b.Volume / avg
		}
		if r.MinVolX > 0 && vr < r.MinVolX {
			continue // closed beyond, but on thin volume: keep waiting
		}
		e.State, e.Price, e.At, e.VolRatio = "triggered", b.C, b.T, round2(vr)
		return e
	}
	if now.After(last) {
		e.State, e.Note = "expired", "no close beyond the line before "+r.LastEntry
	} else if len(rng) == 0 || o.State == "forming" {
		e.Note = "opening range still forming"
	}
	return e
}

// Plan is the exit plan (~/.nse-desk/plan.json): entry, stop and target set
// before the trade.
type Plan struct {
	Symbol     string    `json:"symbol"`
	Side       string    `json:"side"` // long / short (for options: long = bought, short = sold)
	Option     bool      `json:"option"`
	Entry      float64   `json:"entry"`
	EntryAt    time.Time `json:"entry_at,omitempty"`
	Stop       float64   `json:"stop"`
	StopRule   string    `json:"stop_rule"`
	Target     float64   `json:"target"`
	TargetR    float64   `json:"target_r"`
	Tick       float64   `json:"tick"`
	RiskPerShr float64   `json:"risk_per_unit"`
	RangeHigh  float64   `json:"range_high,omitempty"`
	RangeLow   float64   `json:"range_low,omitempty"`
	FlatBy     string    `json:"flat_by"`
	Product    string    `json:"product"`
	Spread     float64   `json:"spread,omitempty"`
	Notes      []string  `json:"notes,omitempty"`
}

// ExitRule configures the planner.
type ExitRule struct {
	Stop     string  // mid / range / pct / atr / price (options: pct)
	StopVal  float64 // pct %, atr multiple, or the price
	ATR      float64 // the ATR to use with "atr"
	TargetR  float64 // target at this many R (0 = use TargetPx)
	TargetPx float64
}

// MakePlan sets the stop and the target for an entry, rounded to NSE's
// tick in the safe direction, and refuses plans that cannot work.
func MakePlan(e Entry, r ExitRule, lim Limits, q *Quote, refPrice float64) (Plan, error) {
	in := ParseInstrument(e.Symbol)
	p := Plan{Symbol: in.Symbol, Side: e.Side, Option: in.Option, Entry: e.Price, EntryAt: e.At, StopRule: r.Stop,
		RangeHigh: e.RangeHigh, RangeLow: e.RangeLow, FlatBy: lim.FlatBy, Product: lim.DefaultProduct}
	if in.Option {
		p.Product = Margin
		if p.Side == "long" {
			p.Product = Intraday
		}
	}
	if p.Entry <= 0 {
		return p, fmt.Errorf("no entry price — run the entry timer first, or give --entry")
	}
	if refPrice <= 0 {
		refPrice = p.Entry
	}
	p.Tick = TickSize(in, refPrice)
	long := p.Side != "short"
	sign := 1.0
	if !long {
		sign = -1
	}

	var stop float64
	switch r.Stop {
	case "mid", "":
		if e.RangeHigh <= 0 {
			return p, fmt.Errorf("no opening range to put the stop at — use --stop pct|atr|price")
		}
		stop = (e.RangeHigh + e.RangeLow) / 2
		p.StopRule = "mid"
	case "range":
		stop = e.RangeLow
		if !long {
			stop = e.RangeHigh
		}
	case "pct":
		v := r.StopVal
		if v <= 0 {
			v = lim.OptionStopPct
		}
		stop = p.Entry * (1 - sign*v/100)
	case "atr":
		if r.ATR <= 0 {
			return p, fmt.Errorf("no ATR available for the atr stop")
		}
		m := r.StopVal
		if m <= 0 {
			m = 1
		}
		stop = p.Entry - sign*m*r.ATR
	case "price":
		stop = r.StopVal
	default:
		return p, fmt.Errorf("unknown stop rule %q (mid, range, pct, atr, price)", r.Stop)
	}
	// A long's stop rounds down, a short's up: never tighter than asked.
	p.Stop = RoundTick(stop, p.Tick, !long)
	if long && p.Stop >= p.Entry || !long && p.Stop <= p.Entry {
		return p, fmt.Errorf("stop ₹%.2f is not on the losing side of the entry ₹%.2f", p.Stop, p.Entry)
	}
	if p.Stop <= 0 {
		return p, fmt.Errorf("stop ₹%.2f is at or below zero", p.Stop)
	}
	p.RiskPerShr = round2(math.Abs(p.Entry - p.Stop))

	switch {
	case r.TargetPx > 0:
		p.Target = r.TargetPx
	case in.Option && r.TargetR <= 0:
		p.Target = p.Entry * (1 + sign*lim.OptionTargetPct/100)
	default:
		rr := r.TargetR
		if rr <= 0 {
			rr = 2
		}
		p.Target = p.Entry + sign*rr*p.RiskPerShr
	}
	// The target rounds towards the entry: never further than asked.
	p.Target = RoundTick(p.Target, p.Tick, !long)
	if long && p.Target <= p.Entry || !long && p.Target >= p.Entry {
		return p, fmt.Errorf("target ₹%.2f is not on the winning side of the entry ₹%.2f", p.Target, p.Entry)
	}
	if p.Target <= 0 {
		return p, fmt.Errorf("target ₹%.2f is at or below zero", p.Target)
	}
	p.TargetR = round2(math.Abs(p.Target-p.Entry) / p.RiskPerShr)

	// A stop inside the noise will be hit by the spread alone.
	if q != nil && q.Bid > 0 && q.Ask > q.Bid {
		p.Spread = round2(q.Ask - q.Bid)
		if p.RiskPerShr < 2*p.Spread {
			return p, fmt.Errorf("stop distance ₹%.2f is under twice the bid-ask spread (₹%.2f) — the spread alone would stop you out", p.RiskPerShr, p.Spread)
		}
	}
	if p.RiskPerShr < 3*p.Tick {
		return p, fmt.Errorf("stop distance ₹%.2f is under 3 ticks (₹%.2f)", p.RiskPerShr, p.Tick)
	}
	if p.Product == Intraday {
		p.Notes = append(p.Notes, "intraday: flat by "+lim.FlatBy+" (brokers square off MIS positions around 15:20)")
	}
	return p, nil
}
