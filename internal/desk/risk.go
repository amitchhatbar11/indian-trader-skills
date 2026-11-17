package desk

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Verdict is the risk check's answer for one order (~/.nse-desk/risk.json).
// The paper broker accepts only an order whose fingerprint matches a PASS.
type Verdict struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	Pass        bool      `json:"pass"`
	Exit        bool      `json:"exit"`
	Blocks      []string  `json:"blocks,omitempty"`
	Warnings    []string  `json:"warnings,omitempty"`
	Checked     time.Time `json:"checked"`
}

// Fingerprint identifies an order's exact content; any edit after the check
// changes it, so an edited order must be checked again.
func Fingerprint(o Order) string {
	o.Created = time.Time{}
	b, _ := json.Marshal(o)
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:8])
}

// Market is what the risk check knows about the market right now.
type Market struct {
	Now    time.Time
	Cal    *Calendar
	Quotes map[string]Quote   // fresh quotes for the orders' symbols
	Banned map[string]bool    // NSE F&O ban list (nil = unknown)
	Bands  map[string]float64 // price band %, 0 = no band (F&O stocks)
	Ledger *Ledger
}

// Check runs every rule over the orders in file order, as if each passed
// order had filled — so a batch that is fine one by one but breaks a limit
// together is caught.
func Check(orders []Order, lim Limits, m Market) []Verdict {
	today := m.Ledger.Today(m.Now)
	batch := map[string]bool{}
	marginUsed := m.Ledger.MarginUsed()
	var out []Verdict
	for _, o := range orders {
		v := Verdict{ID: o.ID, Fingerprint: Fingerprint(o), Checked: m.Now}
		block := func(f string, a ...any) { v.Blocks = append(v.Blocks, fmt.Sprintf(f, a...)) }
		warn := func(f string, a ...any) { v.Warnings = append(v.Warnings, fmt.Sprintf(f, a...)) }
		in := ParseInstrument(o.Symbol)
		q, hasQ := m.Quotes[in.Symbol]

		// Is it an exit? It only reduces what is held.
		if h := m.Ledger.Holding(in.Symbol); h != nil {
			opp := h.Side == "long" && o.Side == "sell" || h.Side == "short" && o.Side == "buy"
			if opp && o.Qty <= h.Qty {
				v.Exit = true
			}
		}
		if o.Exit && !v.Exit {
			block("marked as an exit, but there is no open position it reduces")
		}

		// Format — checked for exits too.
		switch {
		case in.Symbol == "":
			block("no symbol")
		case o.Side != "buy" && o.Side != "sell":
			block("side %q is not buy or sell", o.Side)
		case o.Qty <= 0:
			block("quantity %d", o.Qty)
		}
		if o.Type != "LIMIT" {
			block("order type %q — only LIMIT orders are sent (NSE stock options reject market orders; the desk never sends one)", o.Type)
		}
		if o.Limit <= 0 {
			block("no limit price")
		}
		tick := TickSize(in, o.Limit)
		if in.Option {
			if ref, ok := m.Quotes[in.Underlying]; ok {
				tick = TickSize(in, ref.LTP)
			} else if o.Tick > 0 {
				tick = o.Tick
			}
		}
		if o.Limit > 0 && !OnTick(o.Limit, tick) {
			block("limit ₹%.2f is not a multiple of the ₹%.2f tick", o.Limit, tick)
		}
		if in.Option && o.LotSize > 0 && o.Qty%o.LotSize != 0 {
			block("quantity %d is not a whole number of %d-unit lots", o.Qty, o.LotSize)
		}
		if in.Option && o.LotSize <= 0 {
			block("no lot size on an option order")
		}

		if v.Exit {
			// Getting out is never blocked by a day limit or a market rule.
			if st := m.Cal.At(m.Now); !st.Open {
				warn("market %s — the exit will only fill when it opens", st.State)
			}
			v.Pass = len(v.Blocks) == 0
			out = append(out, v)
			continue
		}

		// Asset rules.
		short := o.Side == "sell"
		if in.Option && !lim.AllowOptions {
			block("options are not allowed (allow_options false)")
		}
		if in.Option && short && !lim.AllowOptionSell {
			block("selling options is not allowed (allow_option_sell false)")
		}
		if !in.Option && short {
			if !lim.AllowShort {
				block("short selling is not allowed (allow_short false)")
			}
			if o.Product != Intraday {
				block("a stock can only be shorted intraday (product %s)", o.Product)
			}
		}
		for _, b := range lim.Blocked {
			if strings.EqualFold(b, in.Symbol) || strings.EqualFold(b, in.Underlying) {
				block("%s is on your blocked list", b)
			}
		}
		if in.Option {
			switch {
			case m.Banned == nil:
				warn("the F&O ban list could not be read — check it before trading %s", in.Underlying)
			case m.Banned[in.Underlying]:
				block("%s is in the NSE F&O ban period — no fresh positions", in.Underlying)
			}
		}
		if !in.Option && o.Limit > 0 && o.Limit < lim.MinPrice {
			block("price ₹%.2f is under the ₹%.0f floor", o.Limit, lim.MinPrice)
		}

		// Market state, prices and liquidity.
		if st := m.Cal.At(m.Now); !st.Open {
			block("market %s", st.State)
		}
		hm := m.Now.In(IST).Format("15:04")
		if lim.NoEntriesBefore != "" && hm < lim.NoEntriesBefore {
			block("no entries before %s (now %s)", lim.NoEntriesBefore, hm)
		}
		if lim.NoEntriesAfter != "" && hm >= lim.NoEntriesAfter {
			block("no entries at or after %s (now %s)", lim.NoEntriesAfter, hm)
		}
		if !hasQ || q.LTP <= 0 {
			block("no fresh quote for %s — the checks against the market cannot run", in.Symbol)
		} else {
			if band, ok := m.Bands[in.Symbol]; ok && band > 0 && q.PrevClose > 0 && !in.Option {
				lo, hi := q.PrevClose*(1-band/100), q.PrevClose*(1+band/100)
				if o.Limit < lo || o.Limit > hi {
					block("limit ₹%.2f is outside the %.0f%% price band (₹%.2f–₹%.2f)", o.Limit, band, lo, hi)
				}
			}
			sp := -1.0
			if q.Bid > 0 && q.Ask >= q.Bid {
				sp = (q.Ask - q.Bid) / ((q.Ask + q.Bid) / 2) * 100
			}
			if in.Option {
				if sp < 0 {
					block("no two-sided market (bid or ask missing)")
				} else if lim.MaxSpreadPct > 0 && sp > lim.MaxSpreadPct {
					block("bid-ask spread %.1f%% is over %.1f%%", sp, lim.MaxSpreadPct)
				}
				if need := float64(lim.MinOptVolumeLots * o.LotSize); need > 0 && q.Volume < need {
					block("strike traded %.0f today — under %d lots (%.0f)", q.Volume, lim.MinOptVolumeLots, need)
				}
			} else if sp > lim.MaxSpreadPct && lim.MaxSpreadPct > 0 {
				warn("bid-ask spread %.1f%% is wide", sp)
			}
			if band := math.Abs(o.Limit-q.LTP) / q.LTP * 100; band > 10 {
				block("limit ₹%.2f is %.0f%% from the last price ₹%.2f — check the price", o.Limit, band, q.LTP)
			}
		}

		// Stop, loss and reward.
		if o.Stop <= 0 {
			if lim.RequireStop {
				block("no stop (require_stop)")
			}
		} else if !short && o.Stop >= o.Limit || short && o.Stop <= o.Limit {
			block("stop ₹%.2f is on the wrong side of the limit ₹%.2f", o.Stop, o.Limit)
		} else {
			from := o.Limit
			if hasQ && q.LTP > 0 {
				if !short {
					from = math.Max(o.Limit, q.LTP)
				} else {
					from = math.Min(o.Limit, q.LTP)
				}
			}
			loss := math.Abs(from-o.Stop) * float64(o.Qty)
			if loss > lim.MaxLossPerTrade*1.005 {
				block("loss at the stop %s (from ₹%.2f) is over the %s max loss per trade", Rupees(loss), from, Rupees(lim.MaxLossPerTrade))
			}
			if o.Target > 0 {
				rr := math.Abs(o.Target-o.Limit) / math.Abs(o.Limit-o.Stop)
				min, what := lim.MinRewardRisk, "min_reward_risk"
				if in.Option && short {
					min, what = lim.MinRewardRiskOptionSell, "min_reward_risk_option_sell"
				}
				if rr+1e-9 < min {
					block("reward:risk %.2f is under %.2f (%s)", rr, min, what)
				}
			}
			lossToday := math.Max(-today.Realized, 0)
			if lossToday+today.OpenRisk+loss > lim.MaxDailyLoss*1.005 {
				block("today's loss %s + open risk %s + this %s is over the %s daily loss limit",
					Rupees(lossToday), Rupees(today.OpenRisk), Rupees(loss), Rupees(lim.MaxDailyLoss))
			}
			today.OpenRisk += loss
		}

		// Size and cash.
		if lim.MaxOrderValue > 0 && o.Value() > lim.MaxOrderValue {
			block("order value %s is over %s", Rupees(o.Value()), Rupees(lim.MaxOrderValue))
		}
		capAmt := lim.Capital * lim.MaxPositionPct / 100
		if in.Option && short {
			if o.Margin > capAmt {
				block("margin %s is over the %.0f%% position cap (%s)", Rupees(o.Margin), lim.MaxPositionPct, Rupees(capAmt))
			}
		} else if o.Value() > capAmt {
			block("position value %s is over the %.0f%% position cap (%s)", Rupees(o.Value()), lim.MaxPositionPct, Rupees(capAmt))
		}
		if avail := lim.Capital*(1-lim.CashBufferPct/100) - marginUsed; o.Margin > avail {
			block("needs %s of margin; %s is free after the buffer", Rupees(o.Margin), Rupees(math.Max(avail, 0)))
		}

		// Counts and duplicates.
		if today.Entries >= lim.MaxTradesPerDay {
			block("%d entries already today (max %d)", today.Entries, lim.MaxTradesPerDay)
		}
		if lim.MaxOrdersPerDay > 0 && today.Orders >= lim.MaxOrdersPerDay {
			block("%d orders already today (max %d)", today.Orders, lim.MaxOrdersPerDay)
		}
		key := in.Symbol + "|" + o.Side
		if today.Symbols[key] {
			block("a %s of %s was already placed today", o.Side, in.Symbol)
		}
		if batch[key] {
			block("%s %s twice in one batch", o.Side, in.Symbol)
		}

		v.Pass = len(v.Blocks) == 0
		if v.Pass {
			batch[key] = true
			today.Entries++
			today.Orders++
			marginUsed += o.Margin
		}
		out = append(out, v)
	}
	return out
}

// PassFor finds a PASS verdict matching the order's current content.
func PassFor(vs []Verdict, o Order) (Verdict, bool) {
	fp := Fingerprint(o)
	for _, v := range vs {
		if v.ID == o.ID && v.Fingerprint == fp && v.Pass {
			return v, true
		}
	}
	return Verdict{}, false
}
