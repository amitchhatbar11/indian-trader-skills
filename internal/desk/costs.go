package desk

import "math"

// Product is how a trade is held.
const (
	Intraday = "INTRADAY" // equity intraday (MIS): squared off the same day
	Delivery = "CNC"      // equity delivery
	Margin   = "MARGIN"   // F&O carried overnight (NRML)
)

// Charges are NSE's statutory charges plus brokerage, as % of turnover
// unless named otherwise. Rates as of 2025–26 (STT on options 0.1% of the
// sold premium since Oct 2024; NSE transaction charges 0.00297% cash,
// 0.03503% options of premium; stamp duty on the buy side; GST 18% on
// brokerage + exchange + SEBI fees). DP charges on delivery sells are not
// included.
type Charges struct {
	BrokerageFlat float64 // ₹ per executed order (cap)
	BrokeragePct  float64 // % of the order's value; the lower of the two applies (0 = flat only)
	STTBuyPct     float64
	STTSellPct    float64
	ExchangePct   float64
	SEBIPct       float64
	StampBuyPct   float64
	GSTPct        float64
}

// ChargesFor returns the schedule for an instrument and product.
func ChargesFor(in Instrument, product string) Charges {
	switch {
	case in.Option:
		return Charges{BrokerageFlat: 20, STTSellPct: 0.1, ExchangePct: 0.03503, SEBIPct: 0.0001, StampBuyPct: 0.003, GSTPct: 18}
	case product == Delivery:
		return Charges{BrokerageFlat: 20, BrokeragePct: 0.03, STTBuyPct: 0.1, STTSellPct: 0.1, ExchangePct: 0.00297, SEBIPct: 0.0001, StampBuyPct: 0.015, GSTPct: 18}
	}
	return Charges{BrokerageFlat: 20, BrokeragePct: 0.03, STTSellPct: 0.025, ExchangePct: 0.00297, SEBIPct: 0.0001, StampBuyPct: 0.003, GSTPct: 18}
}

// Order is the charges on one executed order of value v (₹), buy or sell.
func (c Charges) Order(v float64, buy bool) float64 {
	brk := c.BrokerageFlat
	if c.BrokeragePct > 0 {
		brk = math.Min(brk, v*c.BrokeragePct/100)
	}
	exch := v * c.ExchangePct / 100
	sebi := v * c.SEBIPct / 100
	t := brk + exch + sebi + (brk+exch+sebi)*c.GSTPct/100
	if buy {
		t += v * (c.STTBuyPct + c.StampBuyPct) / 100
	} else {
		t += v * c.STTSellPct / 100
	}
	return math.Round(t*100) / 100
}

// RoundTrip is the charges for buying and selling qty at the two prices
// (either order: a short sells first).
func (c Charges) RoundTrip(buyPx, sellPx float64, qty int) float64 {
	q := float64(qty)
	return c.Order(buyPx*q, true) + c.Order(sellPx*q, false)
}
