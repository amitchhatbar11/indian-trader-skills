package book

import (
	"fmt"
	"log"
	"math"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// slRetryAfter is how long to wait before re-attempting an auto-exit whose
// order was rejected, so a broker error does not spam orders every tick.
const slRetryAfter = 2 * time.Minute

// SLEvent describes one stop-loss trigger for alerting.
type SLEvent struct {
	Position *Position
	ExitedAt float64 // limit price the exit was placed/booked at
	PnL      float64
	Exited   bool   // false when auto-exit is off or the order failed
	Pending  bool   // live: the exit order is placed and awaiting its fill
	Err      string // why the exit did not go through
	Reason   string // SL, TSL (trailed stop) or HALT (daily loss flatten)
}

// Monitor is what CheckStops needs from the broker. Kept as an interface so
// the stop logic is testable without live orders.
type Monitor interface {
	PlaceLimit(ticker string, qty int, limit float64, sell bool) (string, error)
}

// FyersMonitor wires CheckStops to real Fyers limit orders.
type FyersMonitor struct {
	Fy          *fyers.FyersClient
	ProductType string
}

// exitLimit prices a closing order through the market so it actually fills:
// selling a long goes below the bid side, buying back a short goes above.
// Still a LIMIT order — stock options reject market orders.
func exitLimit(ltp, bufferPct float64, closeBySelling bool) float64 {
	if bufferPct <= 0 {
		return ltp
	}
	if closeBySelling {
		p := ltp * (1 - bufferPct/100)
		if p < 0.05 {
			p = 0.05 // exchange tick floor
		}
		return p
	}
	return ltp * (1 + bufferPct/100)
}

// CheckStops reports every position whose loss has reached its risk budget,
// and closes them when autoExit is on. Call it after Refresh, so LTPs are
// current — a stale quote must never trigger a stop.
//
// Each position's own mode decides: a paper position is closed in the book
// only; a live one gets a marketable limit order first and the book is
// updated only if it was accepted — an unfilled or rejected order must not
// leave the book claiming a position is closed when it is not. The global
// LIVE_ORDERS setting never decides this: flipping it with positions open
// must neither send real orders for paper positions nor book-close live ones
// without an order.
func (b *Book) CheckStops(m Monitor, autoExit bool, bufferPct float64) []SLEvent {
	var triggered []*Position
	now := time.Now()

	b.mu.Lock()
	for _, p := range b.Positions {
		if !p.Managed() {
			continue // unfilled, exit already working, or disagrees with the broker
		}
		if !p.StopHit() && !p.TargetHit() {
			continue
		}
		if !p.SLHitAt.IsZero() && now.Sub(p.SLHitAt) < slRetryAfter {
			continue // a recent attempt failed; back off rather than spam
		}
		p.SLHitAt = now
		triggered = append(triggered, p)
	}
	b.mu.Unlock()

	var events []SLEvent
	for _, p := range triggered {
		reason := "SL"
		switch {
		case p.TargetHit():
			reason = "TARGET"
		case p.Trailed:
			reason = "TSL"
		}
		// Auto-trader positions always exit on their stop: nobody is
		// watching them, so alert-only mode would leave them unprotected.
		events = append(events, b.closeAtMarket(p, m, autoExit || p.Auto, bufferPct, reason))
	}
	return events
}

// FlattenAll closes every open position with a marketable limit order — used
// when the daily loss limit trips with FlattenOnHalt set. Same safety as a
// stop: the book changes only after the broker accepts.
func (b *Book) FlattenAll(m Monitor, bufferPct float64, reason string) []SLEvent {
	return b.CloseWhere(m, bufferPct, reason, func(*Position) bool { return true })
}

// CloseWhere closes every open position matching keep, at a marketable
// limit, with the given reason.
func (b *Book) CloseWhere(m Monitor, bufferPct float64, reason string, keep func(*Position) bool) []SLEvent {
	var events []SLEvent
	for _, p := range b.List() {
		if !keep(p) || p.State != StateOpen {
			continue // an order is already working for it
		}
		if p.LTP <= 0 {
			events = append(events, SLEvent{Position: p, Reason: reason, Err: "no LTP — close manually"})
			continue
		}
		events = append(events, b.closeAtMarket(p, m, true, bufferPct, reason))
	}
	return events
}

// ClosePaperAt book-closes every open paper position matching keep at its
// last known LTP (priced through the exit buffer), stamping each trade at
// at(p). No order is ever sent, and live positions are never touched — a
// live close must go through SubmitExit.
func (b *Book) ClosePaperAt(bufferPct float64, reason string, keep func(*Position) bool, at func(*Position) time.Time) []SLEvent {
	var events []SLEvent
	for _, p := range b.List() {
		if p.LiveOrder || p.State != StateOpen || !keep(p) {
			continue
		}
		if p.LTP <= 0 {
			events = append(events, SLEvent{Position: p, Reason: reason, Err: "no LTP — close manually"})
			continue
		}
		limit := exitLimit(p.LTP, bufferPct, !p.IsShort())
		ev := SLEvent{Position: p, ExitedAt: limit, Reason: reason}
		tr, err := b.ExitAt(p.Ticker, limit, reason, at(p))
		if err != nil {
			ev.Err = err.Error()
		} else {
			log.Printf("%s: %s %s x%d entry %.2f → exit %.2f, P&L ₹%.0f (booked %s)",
				reason, p.SideLabel(), p.Ticker, p.Qty(), p.Entry, limit, tr.PnL, tr.ExitTime.Format("2006-01-02 15:04"))
			ev.PnL, ev.Exited = tr.PnL, true
		}
		events = append(events, ev)
	}
	return events
}

// closeAtMarket prices a closing limit through the LTP and books the exit.
func (b *Book) closeAtMarket(p *Position, m Monitor, autoExit bool, bufferPct float64, reason string) SLEvent {
	closeBySelling := !p.IsShort()
	limit := exitLimit(p.LTP, bufferPct, closeBySelling)
	ev := SLEvent{Position: p, ExitedAt: limit, Reason: reason}

	if !autoExit {
		// Alert-only mode: report it, leave the position open.
		ev.Err = "auto-exit disabled (AUTO_SL_EXIT=false)"
		return ev
	}
	tr, pending, err := b.SubmitExit(p.Ticker, limit, reason, m, time.Now())
	switch {
	case err != nil:
		log.Printf("%s exit %s failed: %v", reason, p.Ticker, err)
		ev.Err = err.Error()
	case pending:
		log.Printf("%s: exit order placed for %s x%d @ %.2f — awaiting fill", reason, p.Ticker, p.Qty(), limit)
		ev.Pending = true
	default:
		log.Printf("%s: %s %s x%d entry %.2f → exit %.2f, P&L ₹%.0f",
			reason, p.SideLabel(), p.Ticker, p.Qty(), p.Entry, limit, tr.PnL)
		ev.PnL, ev.Exited = tr.PnL, true
	}
	return ev
}

// Describe renders an SL event for Telegram or the log.
func (e SLEvent) Describe() string {
	p := e.Position
	icon, label := "🔴", "SL HIT"
	switch e.Reason {
	case "TARGET":
		icon, label = "🟢", "TARGET HIT — profit booked"
	case "TSL":
		label = "TRAILING SL HIT"
	case "HALT":
		label = "FLATTENED (daily loss limit)"
	case "AUTO_FLIP":
		label = "AUTO EXIT — the call flipped to the other side"
	case "AUTO_EOD":
		label = "AUTO SQUARE-OFF (end of day)"
	}
	head := fmt.Sprintf("%s %s — %s %s %.0f %s (%s)\n   entry ₹%.2f → ₹%.2f · SL ₹%.2f",
		icon, label, p.Underlying, p.SideLabel(), p.Strike, p.Right, p.Expiry,
		p.Entry, p.LTP, p.SLPrice)
	if e.Pending {
		return head + fmt.Sprintf("\n   ⏳ exit order placed @ limit ₹%.2f — awaiting fill (still open until it fills)", e.ExitedAt)
	}
	if !e.Exited {
		return head + "\n   ⚠️ NOT exited: " + e.Err + "\n   → close it manually"
	}
	return head + fmt.Sprintf("\n   exited @ limit ₹%.2f · P&L ₹%.0f", e.ExitedAt, e.PnL)
}

// trail tightens the stop once the position has moved triggerPct in its
// favour, keeping it trailPct behind the best premium seen. A stop only ever
// tightens — it never moves back toward a bigger loss. Returns true the first
// time the stop is trailed, for a one-off alert.
func (p *Position) trail(triggerPct, trailPct float64) (armed bool) {
	if p.TrailTrigger < 0 {
		return false // this position never trails (fixed-exit auto trades)
	}
	if p.TrailTrigger > 0 && p.TrailBy > 0 {
		triggerPct, trailPct = p.TrailTrigger, p.TrailBy
	}
	if triggerPct <= 0 || trailPct <= 0 || p.LTP <= 0 || p.Entry <= 0 {
		return false
	}
	if p.BestLTP <= 0 {
		p.BestLTP = p.Entry
	}
	if p.IsShort() {
		p.BestLTP = math.Min(p.BestLTP, p.LTP)
		if p.BestLTP > p.Entry*(1-triggerPct/100) {
			return false // not far enough in profit yet
		}
		sl := p.BestLTP * (1 + trailPct/100)
		if p.SLPrice > 0 && sl >= p.SLPrice {
			return false
		}
		p.SLPrice = sl
	} else {
		p.BestLTP = math.Max(p.BestLTP, p.LTP)
		if p.BestLTP < p.Entry*(1+triggerPct/100) {
			return false
		}
		sl := p.BestLTP * (1 - trailPct/100)
		if sl <= p.SLPrice {
			return false
		}
		p.SLPrice = sl
	}
	armed = !p.Trailed
	p.Trailed = true
	return armed
}

// UpdateTrails applies the trailing rule to every open position. Call it
// after Refresh and before CheckStops. Returns the positions whose stop was
// trailed for the first time.
func (b *Book) UpdateTrails(triggerPct, trailPct float64) []*Position {
	b.mu.Lock()
	defer b.mu.Unlock()
	var armed []*Position
	changed := false
	for _, p := range b.Positions {
		if !p.Managed() {
			continue
		}
		sl, best := p.SLPrice, p.BestLTP
		if p.trail(triggerPct, trailPct) {
			armed = append(armed, p)
		}
		changed = changed || p.SLPrice != sl || p.BestLTP != best
	}
	if changed {
		b.save()
	}
	return armed
}
