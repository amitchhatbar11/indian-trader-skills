// Package book tracks the option-selling position book: entries commanded by
// the user (dashboard buttons or Telegram), live mark-to-market, and closed
// trades. Positions are short options; entry/exit prices are always LIMIT
// prices — NSE stock option strikes only accept limit orders on Fyers, so no
// code path here ever places a market order.
package book

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// Side values. Empty is treated as SideSell so books written before buying
// existed keep their original meaning.
const (
	SideSell = "SELL"
	SideBuy  = "BUY"
)

// Position is one open option position, long or short.
type Position struct {
	Underlying string    `json:"underlying"`
	Ticker     string    `json:"ticker"` // Fyers option symbol, no NSE: prefix
	Right      string    `json:"right"`  // CE / PE
	Side       string    `json:"side"`   // BUY (long) / SELL (short); "" = SELL
	Strike     float64   `json:"strike"`
	Expiry     string    `json:"expiry"`
	Lots       int       `json:"lots"`
	LotSize    int       `json:"lot_size"`
	Entry      float64   `json:"entry"` // limit price transacted at
	EntryTime  time.Time `json:"entry_time"`
	LiveOrder  bool      `json:"live_order"` // real order sent to Fyers
	OrderID    string    `json:"order_id,omitempty"`

	// Live order tracking (see live.go). Paper positions are always
	// StateOpen. A live position is PENDING_ENTRY until its entry order
	// fills, and PENDING_EXIT while an exit order is working — it is still
	// open (and exposed) until that exit fills.
	State       string    `json:"state,omitempty"`
	OrderAt     time.Time `json:"order_at,omitempty"`    // when the working order was placed
	CancelSent  bool      `json:"cancel_sent,omitempty"` // a cancel for the working order was sent
	ExitOrderID string    `json:"exit_order_id,omitempty"`
	ExitReason  string    `json:"exit_reason,omitempty"`
	// Mismatch is set when the broker's position disagrees with the book.
	// Automatic exits are suspended while it is set: a "buy back" on a
	// short the broker doesn't hold would open a new position.
	Mismatch string `json:"mismatch,omitempty"`
	// Auto marks a position the paper auto-trader opened (never live);
	// AutoWhy is the suggestion it acted on, for the trade log.
	Auto    bool   `json:"auto,omitempty"`
	AutoWhy string `json:"auto_why,omitempty"`
	// Strategy names a paper strategy that manages the position itself
	// ("strangle": internal/strangle), with the contract's expiry date
	// ("2006-01-02") it is held to. Such positions are not Auto, so the
	// auto-trader's daily square-off never touches them.
	Strategy   string `json:"strategy,omitempty"`
	ExpiryDate string `json:"expiry_date,omitempty"`
	// Capital is the cash tied up: full premium for a long, estimated
	// margin for a short.
	Capital float64 `json:"capital"`

	// Stop loss, fixed at entry: RiskAmount is the rupee loss the account is
	// willing to take, SLPrice the premium at which that loss is reached.
	// SLPrice 0 means no stop applies (see StopLossPrice).
	RiskAmount float64   `json:"risk_amount"`
	SLPrice    float64   `json:"sl_price"`
	SLHitAt    time.Time `json:"sl_hit_at,omitempty"` // last trigger, for retry backoff

	// TargetPrice books the profit: the premium at which the trade has made
	// its per-trade target (below entry for a short, above for a long).
	// 0 = no target.
	TargetPrice float64 `json:"target_price,omitempty"`

	// Trailing stop: BestLTP is the most favourable premium seen since entry
	// (lowest for a short, highest for a long); Trailed is set once the stop
	// has been tightened from its entry level.
	BestLTP float64 `json:"best_ltp,omitempty"`
	Trailed bool    `json:"trailed,omitempty"`
	// Per-position trailing settings; when set they override the global
	// TRAIL_TRIGGER_PCT / TRAIL_PCT. TrailTrigger < 0 means never trail
	// (the auto-trader's fixed-exit positions).
	TrailTrigger float64 `json:"trail_trigger,omitempty"`
	TrailBy      float64 `json:"trail_by,omitempty"`

	// Mark-to-market (refreshed periodically)
	LTP       float64   `json:"ltp"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Qty is the order quantity (lots × lot size).
func (p *Position) Qty() int { return p.Lots * p.LotSize }

// IsShort reports whether this is a sold option. A blank side means SELL,
// which is what every position written before buying was supported was.
func (p *Position) IsShort() bool { return p.Side != SideBuy }

// SideLabel is the side for display, normalising the legacy blank.
func (p *Position) SideLabel() string {
	if p.IsShort() {
		return SideSell
	}
	return SideBuy
}

// UnrealPnL is the open P&L. A short gains as the premium decays; a long
// gains as it rises.
func (p *Position) UnrealPnL() float64 {
	if p.LTP <= 0 || p.State == StatePendingEntry {
		return 0 // an unfilled order has no P&L
	}
	move := p.LTP - p.Entry
	if p.IsShort() {
		move = -move
	}
	return move * float64(p.Qty())
}

// StopLossPrice returns the premium at which the position's loss reaches
// riskAmount rupees.
//
// A long option cannot lose more than the premium paid, so when the risk
// budget exceeds that, the stop would sit at or below zero and there is
// nothing to stop out of — 0 is returned, meaning "no stop applies". Shorts
// have no such ceiling, so their stop is always live.
func StopLossPrice(side string, entry float64, qty int, riskAmount float64) float64 {
	if qty <= 0 || riskAmount <= 0 || entry <= 0 {
		return 0
	}
	move := riskAmount / float64(qty)
	if side == SideBuy {
		if sl := entry - move; sl > 0 {
			return sl
		}
		return 0 // max loss (the premium) is inside the risk budget
	}
	return entry + move
}

// StopHit reports whether the live premium has reached the stop. A long is
// stopped by the premium falling, a short by it rising.
func (p *Position) StopHit() bool {
	if p.SLPrice <= 0 || p.LTP <= 0 {
		return false
	}
	if p.IsShort() {
		return p.LTP >= p.SLPrice
	}
	return p.LTP <= p.SLPrice
}

// TargetPrice returns the premium at which a position has made amount rupees
// — the per-trade profit target, sized off capital like the stop: entry −
// amount/qty for a short (the premium decays), entry + amount/qty for a long.
// It returns 0 ("no target") when a short's target would need the premium
// to fall to the exchange tick or below — the whole premium is not worth
// that much at this size.
func TargetPrice(side string, entry float64, qty int, amount float64) float64 {
	if qty <= 0 || amount <= 0 || entry <= 0 {
		return 0
	}
	move := amount / float64(qty)
	if side == SideBuy {
		return entry + move
	}
	if t := entry - move; t > 0.05 {
		return t
	}
	return 0
}

// TargetHit reports whether the live premium has reached the target.
func (p *Position) TargetHit() bool {
	if p.TargetPrice <= 0 || p.LTP <= 0 {
		return false
	}
	if p.IsShort() {
		return p.LTP <= p.TargetPrice
	}
	return p.LTP >= p.TargetPrice
}

// MaxLoss is the worst case for a long (the premium paid). Shorts are
// unbounded, so 0 is returned.
func (p *Position) MaxLoss() float64 {
	if p.IsShort() {
		return 0
	}
	return p.Entry * float64(p.Qty())
}

// EstimateCapital returns the cash a position ties up. A long option costs
// its full premium. A short blocks margin, which Fyers computes from SPAN +
// exposure; marginPct of the underlying notional is a rough stand-in, so
// treat short numbers as an estimate, not a quote.
func EstimateCapital(side string, premium, spot float64, qty int, marginPct float64) float64 {
	if side == SideBuy {
		return premium * float64(qty)
	}
	return spot * float64(qty) * marginPct / 100
}

// TradeRecord is one closed position.
type TradeRecord struct {
	Underlying string    `json:"underlying"`
	Ticker     string    `json:"ticker"`
	Right      string    `json:"right"`
	Side       string    `json:"side"` // BUY / SELL; "" = SELL (legacy)
	Strike     float64   `json:"strike"`
	Expiry     string    `json:"expiry"`
	Lots       int       `json:"lots"`
	LotSize    int       `json:"lot_size"`
	Entry      float64   `json:"entry"`
	Exit       float64   `json:"exit"`
	EntryTime  time.Time `json:"entry_time"`
	ExitTime   time.Time `json:"exit_time"`
	Reason     string    `json:"reason"`
	LiveOrder  bool      `json:"live_order"`
	Auto       bool      `json:"auto,omitempty"`
	AutoWhy    string    `json:"auto_why,omitempty"`
	Strategy   string    `json:"strategy,omitempty"`
	ExpiryDate string    `json:"expiry_date,omitempty"`
	Capital    float64   `json:"capital"`
	PnL        float64   `json:"pnl"`
}

// SideLabel normalises the legacy blank side to SELL.
func (t *TradeRecord) SideLabel() string {
	if t.Side == SideBuy {
		return SideBuy
	}
	return SideSell
}

type Book struct {
	mu        sync.Mutex
	dataDir   string
	Positions map[string]*Position // keyed by ticker
	Trades    []TradeRecord
	halt      haltState // today's daily-loss halt, if tripped

	untrackedSeen map[string]bool // broker positions already reported as untracked
}

func New(dataDir string) *Book {
	b := &Book{
		dataDir:   dataDir,
		Positions: map[string]*Position{},
	}
	b.load()
	b.loadHalt()
	return b
}

func (b *Book) posPath() string   { return filepath.Join(b.dataDir, "book_positions.json") }
func (b *Book) tradePath() string { return filepath.Join(b.dataDir, "book_trades.json") }

func (b *Book) load() {
	if data, err := os.ReadFile(b.posPath()); err == nil {
		json.Unmarshal(data, &b.Positions)
	}
	if b.Positions == nil {
		b.Positions = map[string]*Position{}
	}
	if data, err := os.ReadFile(b.tradePath()); err == nil {
		json.Unmarshal(data, &b.Trades)
	}
}

// save persists both files; callers must hold b.mu.
func (b *Book) save() {
	if data, err := json.MarshalIndent(b.Positions, "", " "); err == nil {
		os.WriteFile(b.posPath(), data, 0644)
	}
	if data, err := json.MarshalIndent(b.Trades, "", " "); err == nil {
		os.WriteFile(b.tradePath(), data, 0644)
	}
}

// Enter records a new short position. One position per option ticker.
func (b *Book) Enter(p *Position) error {
	if p.Ticker == "" || p.Lots < 1 || p.LotSize < 1 {
		return fmt.Errorf("invalid position: %+v", p)
	}
	if p.Entry <= 0 {
		return fmt.Errorf("entry limit price must be > 0")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.Positions[p.Ticker]; exists {
		return fmt.Errorf("position already open in %s", p.Ticker)
	}
	if p.EntryTime.IsZero() {
		p.EntryTime = time.Now()
	}
	p.LTP = p.Entry
	p.UpdatedAt = p.EntryTime
	b.Positions[p.Ticker] = p
	b.save()
	return nil
}

// Exit closes a position at the given limit price and records the trade.
func (b *Book) Exit(ticker string, price float64, reason string) (*TradeRecord, error) {
	return b.ExitAt(ticker, price, reason, time.Now())
}

// ExitAt is Exit with the trade stamped at the given time — used to book a
// missed square-off on the day it was due, not the day it was noticed.
func (b *Book) ExitAt(ticker string, price float64, reason string, at time.Time) (*TradeRecord, error) {
	if price <= 0 {
		return nil, fmt.Errorf("exit limit price must be > 0")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.Positions[ticker]
	if !ok {
		return nil, fmt.Errorf("no open position in %s", ticker)
	}
	return b.exitQtyLocked(p, p.Qty(), price, reason, at), nil
}

// exitQtyLocked books the close of qty units of p at price (all of it, or a
// partial fill) and removes p once nothing is left. Callers hold b.mu.
func (b *Book) exitQtyLocked(p *Position, qty int, price float64, reason string, at time.Time) *TradeRecord {
	if qty > p.Qty() || qty <= 0 {
		qty = p.Qty()
	}
	move := price - p.Entry
	if p.IsShort() {
		move = -move
	}
	lots := p.Lots
	if p.LotSize > 0 {
		lots = qty / p.LotSize
	}
	share := float64(qty) / float64(p.Qty())
	tr := TradeRecord{
		Underlying: p.Underlying, Ticker: p.Ticker, Right: p.Right,
		Side:   p.SideLabel(),
		Strike: p.Strike, Expiry: p.Expiry, Lots: lots, LotSize: p.LotSize,
		Entry: p.Entry, Exit: price, EntryTime: p.EntryTime,
		ExitTime: at, Reason: reason, LiveOrder: p.LiveOrder,
		Auto: p.Auto, AutoWhy: p.AutoWhy,
		Strategy: p.Strategy, ExpiryDate: p.ExpiryDate,
		Capital: p.Capital * share,
		PnL:     move * float64(qty),
	}
	if qty >= p.Qty() {
		delete(b.Positions, p.Ticker)
	} else {
		p.Lots -= lots
		p.Capital -= tr.Capital
	}
	b.Trades = append(b.Trades, tr)
	b.save()
	return &tr
}

// Find returns open positions for an underlying (0, 1 or 2 — CE and/or PE).
func (b *Book) Find(underlying string) []*Position {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*Position
	for _, p := range b.Positions {
		if strings.EqualFold(p.Underlying, underlying) {
			out = append(out, p)
		}
	}
	return out
}

// List returns open positions sorted by entry time (oldest first).
func (b *Book) List() []*Position {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*Position, 0, len(b.Positions))
	for _, p := range b.Positions {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EntryTime.Before(out[j].EntryTime) })
	return out
}

// AllTrades returns a copy of every closed trade, in the order closed.
func (b *Book) AllTrades() []TradeRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]TradeRecord(nil), b.Trades...)
}

// RecentTrades returns the last n closed trades, newest first.
func (b *Book) RecentTrades(n int) []TradeRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]TradeRecord, 0, n)
	for i := len(b.Trades) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, b.Trades[i])
	}
	return out
}

// Refresh updates the mark-to-market LTP of all open positions (one batched
// quotes call). No-op with an empty book.
func (b *Book) Refresh(fy *fyers.FyersClient) error {
	b.mu.Lock()
	tickers := make([]string, 0, len(b.Positions))
	for t := range b.Positions {
		tickers = append(tickers, t)
	}
	b.mu.Unlock()
	if len(tickers) == 0 {
		return nil
	}

	quotes, err := fy.GetQuotes(tickers)
	if err != nil {
		return err
	}
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range tickers {
		if q, ok := quotes["NSE:"+t]; ok && q.LTP > 0 {
			if p, open := b.Positions[t]; open {
				p.LTP = q.LTP
				p.UpdatedAt = now
			}
		}
	}
	b.save()
	return nil
}

// AutoActivity summarises the auto-trader's footprint for a day: open auto
// positions, the margin they tie up, how many auto entries were made that
// day, and every underlying already traded (auto or manual) or held.
type AutoActivity struct {
	Open    int
	Capital float64
	Entries int
	Touched map[string]bool // underlyings with an open position, or an auto trade today
}

func (b *Book) AutoActivity(day string) AutoActivity {
	b.mu.Lock()
	defer b.mu.Unlock()
	a := AutoActivity{Touched: map[string]bool{}}
	for _, p := range b.Positions {
		a.Touched[p.Underlying] = true
		if p.Auto {
			a.Open++
			a.Capital += p.Capital
			if p.EntryTime.Format("2006-01-02") == day {
				a.Entries++
			}
		}
	}
	for i := range b.Trades {
		t := &b.Trades[i]
		if t.Auto && t.EntryTime.Format("2006-01-02") == day {
			a.Entries++
			a.Touched[t.Underlying] = true
		}
	}
	return a
}

// TotalUnrealPnL sums open MTM P&L.
func (b *Book) TotalUnrealPnL() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	var sum float64
	for _, p := range b.Positions {
		sum += p.UnrealPnL()
	}
	return sum
}

// TotalCapital sums the cash tied up by open positions.
func (b *Book) TotalCapital() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	var sum float64
	for _, p := range b.Positions {
		sum += p.Capital
	}
	return sum
}

// PlaceLimitOrder sends a LIMIT order to Fyers (sell=true to sell). Always
// Type=1 (limit) — stock option strikes reject market orders. Returns the
// order ID. Accepted is not filled: use SubmitEntry/SubmitExit so the book
// tracks the fill.
func PlaceLimitOrder(fy *fyers.FyersClient, ticker string, qty int, limit float64, sell bool, productType string) (string, error) {
	return FyersMonitor{Fy: fy, ProductType: productType}.PlaceLimit(ticker, qty, limit, sell)
}
