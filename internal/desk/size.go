package desk

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Order is a proposed order (~/.nse-desk/orders.json). Every order is a LIMIT
// order: NSE stock-option strikes reject market orders, and the desk never
// sends one for stocks either.
type Order struct {
	ID        string    `json:"id"`
	Symbol    string    `json:"symbol"`
	Side      string    `json:"side"` // buy / sell
	Qty       int       `json:"qty"`
	LotSize   int       `json:"lot_size,omitempty"` // options
	Type      string    `json:"type"`               // LIMIT
	Limit     float64   `json:"limit"`
	Product   string    `json:"product"` // INTRADAY / CNC / MARGIN
	Stop      float64   `json:"stop,omitempty"`
	Target    float64   `json:"target,omitempty"`
	FlatBy    string    `json:"flat_by,omitempty"`
	MaxLoss   float64   `json:"max_loss"` // ₹ at the stop, before charges
	Margin    float64   `json:"margin"`   // ₹ blocked (estimate unless MarginSource says SPAN)
	MarginSrc string    `json:"margin_source,omitempty"`
	Exit      bool      `json:"exit,omitempty"` // closes (part of) an open paper position
	Tick      float64   `json:"tick"`
	Why       string    `json:"why,omitempty"` // which limit decided the size
	Created   time.Time `json:"created"`
}

// Value is the order's notional (premium for options).
func (o Order) Value() float64 { return o.Limit * float64(o.Qty) }

// SizeInput is what the sizer needs beyond the plan and the limits.
type SizeInput struct {
	Lot        int     // options: lot size
	Spot       float64 // options: underlying price (margin estimate, tick)
	SpanMargin func(lots int) (float64, error)
	MarginUsed float64 // ₹ already blocked by open paper positions
	Limit      float64 // a limit other than the plan's entry (0 = entry)
}

// Size turns a plan into ONE limit order sized to the limits. A stop too
// far for the max loss is a refusal, never a smaller-than-1 order.
func Size(p Plan, lim Limits, in SizeInput, now time.Time) (Order, error) {
	ins := ParseInstrument(p.Symbol)
	o := Order{Symbol: ins.Symbol, Type: "LIMIT", Product: p.Product, Stop: p.Stop, Target: p.Target,
		FlatBy: p.FlatBy, Tick: p.Tick, Created: now}
	o.Side = "buy"
	if p.Side == "short" {
		o.Side = "sell"
	}
	o.Limit = p.Entry
	if in.Limit > 0 {
		o.Limit = in.Limit
	}
	if !OnTick(o.Limit, p.Tick) {
		o.Limit = RoundTick(o.Limit, p.Tick, o.Side == "sell")
	}
	risk := math.Abs(o.Limit - p.Stop)
	if risk <= 0 {
		return o, fmt.Errorf("no distance between the limit and the stop")
	}
	avail := lim.Capital*(1-lim.CashBufferPct/100) - in.MarginUsed
	posCap := lim.Capital * lim.MaxPositionPct / 100

	type cap struct {
		name string
		n    int
	}
	var caps []cap
	unit := 1 // shares per step
	if ins.Option {
		if in.Lot <= 0 {
			return o, fmt.Errorf("no lot size for %s", ins.Symbol)
		}
		unit = in.Lot
		o.LotSize = in.Lot
	}
	perUnitRisk := risk * float64(unit)
	caps = append(caps, cap{"max loss per trade " + Rupees(lim.MaxLossPerTrade), int(lim.MaxLossPerTrade / perUnitRisk)})
	if lim.MaxOrderValue > 0 {
		caps = append(caps, cap{"max order value " + Rupees(lim.MaxOrderValue), int(lim.MaxOrderValue / (o.Limit * float64(unit)))})
	}

	// Margin per step.
	var mPer float64
	switch {
	case ins.Option && o.Side == "sell":
		mPer = in.Spot * float64(in.Lot) * lim.OptionMarginPct / 100
		o.MarginSrc = fmt.Sprintf("estimate %.0f%% of notional", lim.OptionMarginPct)
		if in.SpanMargin != nil {
			if m, err := in.SpanMargin(1); err == nil && m > 0 {
				mPer, o.MarginSrc = m, "Fyers SPAN"
			}
		}
		if mPer <= 0 {
			return o, fmt.Errorf("no margin estimate for a short %s (no underlying price)", ins.Symbol)
		}
	case ins.Option:
		mPer = o.Limit * float64(in.Lot) // a bought option costs its premium
		o.MarginSrc = "premium"
	case p.Product == Intraday:
		lev := math.Max(lim.IntradayLeverage, 1)
		mPer = o.Limit / lev
		o.MarginSrc = fmt.Sprintf("intraday, value ÷ %.0f", lev)
	default:
		mPer = o.Limit
		o.MarginSrc = "full value (delivery)"
	}
	caps = append(caps, cap{"cash after the " + fmt.Sprintf("%.0f%%", lim.CashBufferPct) + " buffer and open margin", int(math.Max(avail, 0) / mPer)})
	if ins.Option && o.Side == "sell" {
		caps = append(caps, cap{fmt.Sprintf("position cap %.0f%% of capital (margin)", lim.MaxPositionPct), int(posCap / mPer)})
	} else {
		caps = append(caps, cap{fmt.Sprintf("position cap %.0f%% of capital (value)", lim.MaxPositionPct), int(posCap / (o.Limit * float64(unit)))})
	}

	best := caps[0]
	for _, c := range caps[1:] {
		if c.n < best.n {
			best = c
		}
	}
	if caps[0].n < 1 {
		return o, fmt.Errorf("one %s loses %s at the stop — over the %s max loss per trade; the stop is too far, so this is a refusal, not a smaller order",
			map[bool]string{true: "lot", false: "share"}[ins.Option], Rupees(perUnitRisk), Rupees(lim.MaxLossPerTrade))
	}
	if best.n < 1 {
		return o, fmt.Errorf("not even one %s fits: %s", map[bool]string{true: "lot", false: "share"}[ins.Option], best.name)
	}
	o.Qty = best.n * unit
	o.MaxLoss = round2(risk * float64(o.Qty))
	o.Margin = round2(mPer * float64(best.n))
	o.Why = "sized by " + best.name
	o.ID = strings.ToLower(fmt.Sprintf("%s-%s-%s", ins.Symbol, o.Side, now.In(IST).Format("150405")))
	return o, nil
}
