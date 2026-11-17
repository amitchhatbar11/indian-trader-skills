package book

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// Live order tracking.
//
// A broker accepting an order is not the order filling. A limit order can
// sit unfilled, fill in part, or be rejected after it was accepted. So for
// live positions the book never assumes a fill:
//
//   - entry: the order is placed and the position recorded PENDING_ENTRY.
//     ReconcileOrders polls the order: on a fill the position opens at the
//     actual average fill price (stop and target keep their distances); a
//     partial fill is kept and the rest cancelled after entryTimeout; a
//     rejection or a zero fill removes the position.
//   - exit: the order is placed and the position marked PENDING_EXIT — it is
//     still open and exposed. Only a confirmed fill closes it (lot by lot
//     for partial fills). A rejected exit, or one unfilled after
//     exitTimeout (cancelled to be re-priced), returns it to open so the
//     stop monitor tries again.
//
// ReconcilePositions compares live positions with the broker's net
// positions. A disagreement flags the position (Mismatch) and suspends its
// automatic exits; broker option positions the book doesn't know are
// reported once.
//
// Paper positions skip all of this: they fill instantly at their limit.

// Position states.
const (
	StateOpen         = "" // filled — and every paper position
	StatePendingEntry = "PENDING_ENTRY"
	StatePendingExit  = "PENDING_EXIT"
)

// Managed reports whether the stop monitor may act on the position: it is
// filled, has no exit working, and agrees with the broker.
func (p *Position) Managed() bool { return p.State == StateOpen && p.Mismatch == "" }

// OrderState is a working order as the broker reports it.
type OrderState struct {
	Status    int // fyers.OrderCancelled / Filled / Transit / Rejected / Pending / Expired
	Filled    int
	Remaining int
	AvgPrice  float64
	Message   string
}

func (o OrderState) terminal() bool {
	return o.Status == fyers.OrderCancelled || o.Status == fyers.OrderRejected || o.Status == fyers.OrderExpired
}

// Broker is everything live order tracking needs. FyersMonitor implements it.
type Broker interface {
	Monitor
	Order(id string) (OrderState, error)
	Cancel(id string) error
	// NetPositions maps option ticker (no "NSE:" prefix) to net quantity:
	// positive long, negative short.
	NetPositions() (map[string]int, error)
}

var tagSeq atomic.Int64

// newOrderTag is a unique alphanumeric tag so an unacknowledged order can be
// found again in the order book.
func newOrderTag() string {
	return "ga" + strconv.FormatInt(time.Now().UnixMilli(), 36) + strconv.FormatInt(tagSeq.Add(1)%1296, 36)
}

func (m FyersMonitor) PlaceLimit(ticker string, qty int, limit float64, sell bool) (string, error) {
	side := 1
	if sell {
		side = -1
	}
	pt := m.ProductType
	if pt == "" {
		pt = "MARGIN"
	}
	tag := newOrderTag()
	resp, err := m.Fy.PlaceOrder(fyers.OrderRequest{
		Symbol: "NSE:" + ticker, Qty: qty,
		Type: 1, // LIMIT — never market for stock options
		Side: side, ProductType: pt, LimitPrice: limit, Validity: "DAY",
		OrderTag: tag,
	})
	if err != nil {
		// A timeout or 5xx is ambiguous: the order may exist. Look before
		// reporting failure — placement is never retried blindly.
		if id := m.findByTag(tag); id != "" {
			return id, nil
		}
		return "", err
	}
	if resp.Status != "ok" {
		return "", fmt.Errorf("order rejected: %s", resp.Message)
	}
	if resp.OrderID == "" || resp.Code == 201 {
		if id := m.findByTag(tag); id != "" {
			return id, nil
		}
		if resp.OrderID == "" {
			return "", fmt.Errorf("order not acknowledged (code %d) — check the Fyers order book; reconciliation will flag it if it went through", resp.Code)
		}
	}
	return resp.OrderID, nil
}

func (m FyersMonitor) findByTag(tag string) string {
	orders, err := m.Fy.GetOrders()
	if err != nil {
		return ""
	}
	for _, o := range orders {
		if o.OrderTag == tag {
			return o.ID
		}
	}
	return ""
}

func (m FyersMonitor) Order(id string) (OrderState, error) {
	o, err := m.Fy.GetOrder(id)
	if err != nil {
		return OrderState{}, err
	}
	return OrderState{Status: o.Status, Filled: o.FilledQty, Remaining: o.Remaining,
		AvgPrice: o.TradedPrice, Message: o.Message}, nil
}

func (m FyersMonitor) Cancel(id string) error { return m.Fy.CancelOrder(id) }

func (m FyersMonitor) NetPositions() (map[string]int, error) {
	ps, err := m.Fy.GetPositions()
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, p := range ps {
		net := p.NetQty
		// Make the sign unambiguous: short when more was sold than bought.
		if net > 0 && p.SellQty > p.BuyQty {
			net = -net
		}
		out[strings.TrimPrefix(p.Symbol, "NSE:")] += net
	}
	return out, nil
}

// SubmitEntry opens a position. Paper positions fill immediately; a live one
// places a limit order and waits as PENDING_ENTRY for ReconcileOrders.
func (b *Book) SubmitEntry(p *Position, br Monitor, now time.Time) error {
	if !p.LiveOrder {
		return b.Enter(p)
	}
	if br == nil {
		return errors.New("live order but no broker connection")
	}
	b.mu.Lock()
	_, exists := b.Positions[p.Ticker]
	b.mu.Unlock()
	if exists {
		return fmt.Errorf("position already open in %s", p.Ticker)
	}
	id, err := br.PlaceLimit(p.Ticker, p.Qty(), p.Entry, p.IsShort())
	if err != nil {
		return fmt.Errorf("order failed: %w", err)
	}
	p.OrderID, p.State, p.OrderAt = id, StatePendingEntry, now
	return b.Enter(p)
}

// SubmitExit closes a position at a limit. A paper position closes
// immediately and its trade is returned. A live one places the exit order
// and returns pending=true; it stays open until the fill is confirmed. A
// live entry that hasn't filled yet is cancelled instead.
func (b *Book) SubmitExit(ticker string, limit float64, reason string, br Monitor, now time.Time) (tr *TradeRecord, pending bool, err error) {
	b.mu.Lock()
	p, ok := b.Positions[ticker]
	if !ok {
		b.mu.Unlock()
		return nil, false, fmt.Errorf("no open position in %s", ticker)
	}
	if !p.LiveOrder {
		b.mu.Unlock()
		tr, err := b.Exit(ticker, limit, reason)
		return tr, false, err
	}
	state, mismatch, orderID := p.State, p.Mismatch, p.OrderID
	qty, closeBySelling := p.Qty(), !p.IsShort()
	b.mu.Unlock()

	switch {
	case state == StatePendingExit:
		return nil, false, errors.New("an exit order is already working for this position")
	case state == StatePendingEntry:
		brk, ok := br.(Broker)
		if !ok || brk == nil {
			return nil, false, errors.New("entry order still pending and no broker connection to cancel it")
		}
		if err := brk.Cancel(orderID); err != nil {
			return nil, false, fmt.Errorf("entry not filled yet; cancelling it failed: %w", err)
		}
		b.mu.Lock()
		if p2, ok := b.Positions[ticker]; ok {
			p2.CancelSent = true
			b.save()
		}
		b.mu.Unlock()
		return nil, true, nil
	case mismatch != "":
		return nil, false, fmt.Errorf("book and broker disagree (%s) — resolve that before exiting", mismatch)
	case br == nil:
		return nil, false, errors.New("live position but no broker connection")
	}

	id, err := br.PlaceLimit(ticker, qty, limit, closeBySelling)
	if err != nil {
		return nil, false, fmt.Errorf("exit order failed: %w", err)
	}
	b.mu.Lock()
	if p2, ok := b.Positions[ticker]; ok {
		p2.State, p2.ExitOrderID, p2.ExitReason = StatePendingExit, id, reason
		p2.OrderAt, p2.CancelSent = now, false
		b.save()
	}
	b.mu.Unlock()
	return nil, true, nil
}

// ReconEvent is one change worth telling the user about.
type ReconEvent struct {
	Ticker string
	Kind   string // FILLED, PARTIAL, REJECTED, CANCELLING, EXIT_FILLED, EXIT_PARTIAL, EXIT_FAILED, MISMATCH, RESOLVED, UNTRACKED
	Msg    string
	PnL    float64
}

func (e ReconEvent) String() string {
	icon := map[string]string{
		"FILLED": "✅", "PARTIAL": "🟡", "REJECTED": "❌", "CANCELLING": "⏳",
		"EXIT_FILLED": "✅", "EXIT_PARTIAL": "🟡", "EXIT_FAILED": "⚠️",
		"MISMATCH": "🚨", "RESOLVED": "✅", "UNTRACKED": "🚨",
	}[e.Kind]
	return fmt.Sprintf("%s %s — %s", icon, e.Ticker, e.Msg)
}

// ReconcileOrders polls every working live order and applies what happened.
// Call it every few seconds while the market is open.
func (b *Book) ReconcileOrders(br Broker, now time.Time, entryTimeout, exitTimeout time.Duration) []ReconEvent {
	if br == nil {
		return nil
	}
	type work struct {
		ticker, id, state string
		cancelSent        bool
		orderAt           time.Time
	}
	b.mu.Lock()
	var todo []work
	for _, p := range b.Positions {
		switch {
		case !p.LiveOrder:
		case p.State == StatePendingEntry:
			todo = append(todo, work{p.Ticker, p.OrderID, p.State, p.CancelSent, p.OrderAt})
		case p.State == StatePendingExit:
			todo = append(todo, work{p.Ticker, p.ExitOrderID, p.State, p.CancelSent, p.OrderAt})
		}
	}
	b.mu.Unlock()

	var events []ReconEvent
	for _, w := range todo {
		st, err := br.Order(w.id)
		if err != nil {
			log.Printf("reconcile: order %s (%s): %v", w.id, w.ticker, err)
			continue // try again next tick
		}
		timeout := entryTimeout
		if w.state == StatePendingExit {
			timeout = exitTimeout
		}
		working := st.Status == fyers.OrderPending || st.Status == fyers.OrderTransit
		if working && !w.cancelSent && timeout > 0 && now.Sub(w.orderAt) > timeout {
			if err := br.Cancel(w.id); err != nil {
				log.Printf("reconcile: cancel %s (%s): %v", w.id, w.ticker, err)
				continue
			}
			b.mu.Lock()
			if p, ok := b.Positions[w.ticker]; ok {
				p.CancelSent = true
				b.save()
			}
			b.mu.Unlock()
			what := "entry"
			if w.state == StatePendingExit {
				what = "exit (to re-price)"
			}
			events = append(events, ReconEvent{w.ticker, "CANCELLING",
				fmt.Sprintf("%s not filled after %s (%d filled) — cancelling the rest", what, timeout, st.Filled), 0})
			continue
		}
		if ev, ok := b.applyOrder(w.ticker, w.state, st, now); ok {
			events = append(events, ev)
		}
	}
	return events
}

// applyOrder folds one order report into the book.
func (b *Book) applyOrder(ticker, state string, st OrderState, now time.Time) (ReconEvent, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.Positions[ticker]
	if !ok || p.State != state {
		return ReconEvent{}, false // changed underneath us
	}
	done := st.Status == fyers.OrderFilled
	if !done && !st.terminal() {
		return ReconEvent{}, false // still working
	}
	filled := st.Filled
	if done && filled == 0 {
		filled = p.Qty()
	}
	price := st.AvgPrice

	if state == StatePendingEntry {
		if filled == 0 {
			delete(b.Positions, ticker)
			b.save()
			msg := "entry order " + statusWord(st.Status)
			if st.Message != "" {
				msg += ": " + st.Message
			}
			return ReconEvent{ticker, "REJECTED", msg + " — no position opened", 0}, true
		}
		want := p.Qty()
		b.fillEntryLocked(p, filled, price)
		b.save()
		if filled < want {
			return ReconEvent{ticker, "PARTIAL", fmt.Sprintf("entry filled %d of %d @ ₹%.2f — rest cancelled; SL ₹%.2f", filled, want, p.Entry, p.SLPrice), 0}, true
		}
		return ReconEvent{ticker, "FILLED", fmt.Sprintf("entry filled %d @ ₹%.2f · SL ₹%.2f%s", filled, p.Entry, p.SLPrice, targetSuffix(p)), 0}, true
	}

	// PENDING_EXIT
	reason := p.ExitReason
	if reason == "" {
		reason = "MANUAL"
	}
	if filled == 0 {
		p.State, p.ExitOrderID, p.CancelSent = StateOpen, "", false
		b.save()
		msg := "exit order " + statusWord(st.Status)
		if st.Message != "" {
			msg += ": " + st.Message
		}
		return ReconEvent{ticker, "EXIT_FAILED", msg + " — position is STILL OPEN; the stop monitor will try again", 0}, true
	}
	if price <= 0 {
		price = p.LTP
	}
	whole := filled >= p.Qty()
	tr := b.exitQtyLocked(p, filled, price, reason, time.Now())
	if whole {
		return ReconEvent{ticker, "EXIT_FILLED", fmt.Sprintf("%s exit filled %d @ ₹%.2f · P&L ₹%.0f", reason, filled, price, tr.PnL), tr.PnL}, true
	}
	p.State, p.ExitOrderID, p.CancelSent = StateOpen, "", false
	b.save()
	return ReconEvent{ticker, "EXIT_PARTIAL", fmt.Sprintf("exit filled %d @ ₹%.2f (P&L ₹%.0f) — %d still open; the stop monitor will retry", filled, price, tr.PnL, p.Qty()), tr.PnL}, true
}

// fillEntryLocked opens a pending entry at its actual fill: quantity and
// price from the broker, stop and target moved to keep their distances.
func (b *Book) fillEntryLocked(p *Position, filled int, price float64) {
	if price <= 0 {
		price = p.Entry
	}
	shift := price - p.Entry
	if p.SLPrice > 0 {
		p.SLPrice += shift
	}
	if p.TargetPrice > 0 {
		p.TargetPrice += shift
	}
	if want := p.Qty(); want > 0 && filled < want {
		p.Capital *= float64(filled) / float64(want)
	}
	if p.LotSize > 0 {
		p.Lots = filled / p.LotSize
	}
	p.Entry, p.LTP = price, price
	p.State, p.CancelSent = StateOpen, false
}

func statusWord(s int) string {
	switch s {
	case fyers.OrderCancelled:
		return "cancelled"
	case fyers.OrderRejected:
		return "rejected"
	case fyers.OrderExpired:
		return "expired"
	}
	return fmt.Sprintf("status %d", s)
}

func targetSuffix(p *Position) string {
	if p.TargetPrice > 0 {
		return fmt.Sprintf(" · target ₹%.2f", p.TargetPrice)
	}
	return ""
}

// ReconcilePositions compares every filled live position with the broker's
// net position. Run it at startup and every minute or so.
func (b *Book) ReconcilePositions(br Broker) ([]ReconEvent, error) {
	if br == nil {
		return nil, nil
	}
	net, err := br.NetPositions()
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.untrackedSeen == nil {
		b.untrackedSeen = map[string]bool{}
	}
	var events []ReconEvent
	tracked := map[string]bool{}
	changed := false
	for _, p := range b.Positions {
		if !p.LiveOrder {
			continue
		}
		tracked[p.Ticker] = true
		if p.State != StateOpen {
			continue // an order is working; ReconcileOrders owns it
		}
		want := p.Qty()
		if p.IsShort() {
			want = -want
		}
		got := net[p.Ticker]
		if got != want {
			msg := fmt.Sprintf("broker shows %+d, book expects %+d", got, want)
			if p.Mismatch != msg {
				p.Mismatch = msg
				changed = true
				events = append(events, ReconEvent{p.Ticker, "MISMATCH", msg + " — automatic exits SUSPENDED for this position; check Fyers", 0})
			}
		} else if p.Mismatch != "" {
			p.Mismatch = ""
			changed = true
			events = append(events, ReconEvent{p.Ticker, "RESOLVED", "book and broker agree again — automatic exits resumed", 0})
		}
	}
	for t, q := range net {
		if q == 0 || tracked[t] || b.untrackedSeen[t] || !isOption(t) {
			continue
		}
		b.untrackedSeen[t] = true
		events = append(events, ReconEvent{t, "UNTRACKED", fmt.Sprintf("Fyers holds %+d that the book doesn't track — it has no stop or target here", q), 0})
	}
	if changed {
		b.save()
	}
	return events, nil
}

func isOption(ticker string) bool {
	return strings.HasSuffix(ticker, "CE") || strings.HasSuffix(ticker, "PE")
}

// Drop removes a live position from the book without recording a trade —
// for a mismatched position the broker no longer holds.
func (b *Book) Drop(ticker string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.Positions[ticker]
	if !ok {
		return fmt.Errorf("no position in %s", ticker)
	}
	if p.Mismatch == "" {
		return errors.New("only a position flagged as mismatched can be dropped — exit it normally")
	}
	delete(b.Positions, ticker)
	b.save()
	log.Printf("DROPPED %s from the book (%s)", ticker, p.Mismatch)
	return nil
}
