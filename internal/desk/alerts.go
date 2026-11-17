package desk

import (
	"fmt"
	"strings"
	"time"
)

// Alert is one price or time alert (~/.nse-desk/alerts.json). Each rings once.
type Alert struct {
	ID      string    `json:"id"`
	Symbol  string    `json:"symbol,omitempty"`
	Kind    string    `json:"kind"` // above / below / pct / time / bell
	Level   float64   `json:"level,omitempty"`
	Pct     float64   `json:"pct,omitempty"` // pct: the move from the previous close, ± %
	At      string    `json:"at,omitempty"`  // time: "HH:MM"
	Label   string    `json:"label,omitempty"`
	Created time.Time `json:"created"`
	RungAt  time.Time `json:"rung_at,omitempty"`
	RungPx  float64   `json:"rung_price,omitempty"`
}

// Rung reports an alert that has fired.
func (a Alert) Rung() bool { return !a.RungAt.IsZero() }

// Describe is the alert in words.
func (a Alert) Describe() string {
	l := a.Label
	if l != "" {
		l = " (" + l + ")"
	}
	switch a.Kind {
	case "above":
		return fmt.Sprintf("%s at or above ₹%.2f%s", a.Symbol, a.Level, l)
	case "below":
		return fmt.Sprintf("%s at or below ₹%.2f%s", a.Symbol, a.Level, l)
	case "pct":
		return fmt.Sprintf("%s moves %+.1f%% from the previous close%s", a.Symbol, a.Pct, l)
	case "time":
		return fmt.Sprintf("%s IST on a trading day%s", a.At, l)
	case "bell":
		return "NSE opening bell (09:15)" + l
	}
	return a.Kind
}

// FromPlan makes the plan's three alerts: the stop, the target and "flat
// by" at the plan's flat-by time.
func FromPlan(p Plan, now time.Time) []Alert {
	stopKind, tgtKind := "below", "above"
	if p.Side == "short" {
		stopKind, tgtKind = "above", "below"
	}
	base := strings.ToLower(p.Symbol)
	out := []Alert{
		{ID: base + "-plan-stop", Symbol: p.Symbol, Kind: stopKind, Level: p.Stop, Label: "stop", Created: now},
		{ID: base + "-plan-target", Symbol: p.Symbol, Kind: tgtKind, Level: p.Target, Label: "target", Created: now},
	}
	if p.FlatBy != "" && p.Product == Intraday {
		out = append(out, Alert{ID: base + "-plan-flat", Kind: "time", At: p.FlatBy, Label: "flat by close: " + p.Symbol, Created: now})
	}
	return out
}

// CheckAlerts rings every alert whose condition holds now. Price alerts
// need a fresh quote; time and bell alerts need a trading day.
func CheckAlerts(as []Alert, quotes map[string]Quote, now time.Time, cal *Calendar) []Alert {
	var rung []Alert
	hm := now.In(IST).Format("15:04")
	for i := range as {
		a := &as[i]
		if a.Rung() {
			continue
		}
		hit := false
		var px float64
		switch a.Kind {
		case "above", "below", "pct":
			q, ok := quotes[a.Symbol]
			if !ok || q.LTP <= 0 {
				continue
			}
			px = q.LTP
			switch a.Kind {
			case "above":
				hit = q.LTP >= a.Level
			case "below":
				hit = q.LTP <= a.Level
			case "pct":
				c := q.ChangePct()
				hit = q.PrevClose > 0 && (a.Pct >= 0 && c >= a.Pct || a.Pct < 0 && c <= a.Pct)
			}
		case "time":
			// Rings at (or after) the time on a trading day that is the
			// creation day — or a later one, for an alert set after the time.
			hit = cal.TradingDay(now) && hm >= a.At &&
				(dayOf(now).After(dayOf(a.Created)) || a.Created.In(IST).Format("15:04") < a.At)
		case "bell":
			hit = cal.At(now).Open && (dayOf(now).After(dayOf(a.Created)) || a.Created.In(IST).Format("15:04") < MarketOpen)
		}
		if hit {
			a.RungAt, a.RungPx = now, px
			rung = append(rung, *a)
		}
	}
	return rung
}
