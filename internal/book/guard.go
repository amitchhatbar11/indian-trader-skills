package book

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// Rules are the pre-trade checks every entry and exit goes through, whichever
// surface (dashboard, Telegram) commanded it. A zero value disables a check.
//
// Entries can be refused for any of these reasons. Exits are only ever
// checked for a fat-fingered price — a halt, a ban or a thin market must
// never stop you getting out.
type Rules struct {
	Capital float64

	// DailyLossPct halts new entries for the rest of the day once realised +
	// open P&L reaches -DailyLossPct% of Capital. The halt latches: a later
	// recovery does not re-open trading the same day.
	DailyLossPct float64
	// FlattenOnHalt also closes every open position when the halt trips.
	FlattenOnHalt bool

	// LimitBandPct rejects a limit price further than this % outside the
	// live bid/ask/LTP range — catches typos like an exit at ₹1 on a ₹40
	// option.
	LimitBandPct float64

	// Liquidity gates for entries in a strike.
	MinOptVolume int64   // contracts' traded quantity today (Fyers "volume")
	MaxSpreadPct float64 // bid-ask spread as % of mid

	// Block52wFade refuses (rather than just warns about) selling a CE on a
	// stock rallying at/near its 52-week high, or a PE on one falling at/near
	// its 52-week low.
	Block52wFade bool
}

// OptQuote is the live state of one option contract at order time.
type OptQuote struct {
	LTP    float64
	Bid    float64
	Ask    float64
	Volume int64
}

// SpreadPct is the bid-ask spread as a % of mid; -1 when either side is
// missing (no two-sided market).
func (q OptQuote) SpreadPct() float64 {
	if q.Bid <= 0 || q.Ask <= 0 || q.Ask < q.Bid {
		return -1
	}
	return (q.Ask - q.Bid) / ((q.Ask + q.Bid) / 2) * 100
}

// FetchOptQuote pulls a fresh quote for one option ticker (no NSE: prefix).
func FetchOptQuote(fy *fyers.FyersClient, ticker string) (OptQuote, error) {
	qs, err := fy.GetQuotes([]string{ticker})
	if err != nil {
		return OptQuote{}, err
	}
	q, ok := qs["NSE:"+ticker]
	if !ok {
		return OptQuote{}, fmt.Errorf("no quote for %s", ticker)
	}
	return OptQuote{LTP: q.LTP, Bid: q.Bid, Ask: q.Ask, Volume: q.Volume}, nil
}

// EntryContext is what the underlying looks like when an entry is commanded.
type EntryContext struct {
	Side      string // SideBuy / SideSell
	Right     string // CE / PE
	Banned    bool   // underlying is in the NSE F&O ban period
	Direction string // "UP" / "DOWN" — the underlying's move today
	W52Status string // scanner.Row.W52Status
}

// FadeRisk reports whether an entry sells against a stock pushing through a
// 52-week extreme — a CE on a rally at the high, a PE on a fall at the low.
// Those moves tend to keep going, which is the wrong side for a seller.
func FadeRisk(side, right, direction, w52Status string) bool {
	if side == SideBuy {
		return false
	}
	switch {
	case right == "CE" && direction == "UP":
		return strings.Contains(w52Status, "52W HIGH")
	case right == "PE" && direction == "DOWN":
		return strings.Contains(w52Status, "52W LOW")
	}
	return false
}

// limitBandErr checks limit against the live market with a band of bandPct,
// and at least ₹0.50 of slack so cheap options are not over-constrained.
func limitBandErr(q OptQuote, limit, bandPct float64) error {
	if bandPct <= 0 {
		return nil
	}
	lo, hi := q.LTP, q.LTP
	if q.Bid > 0 {
		lo = math.Min(lo, q.Bid)
		hi = math.Max(hi, q.Bid)
	}
	if q.Ask > 0 {
		lo = math.Min(lo, q.Ask)
		hi = math.Max(hi, q.Ask)
	}
	if hi <= 0 {
		return nil // no market to compare against; the limit order protects itself
	}
	if lo <= 0 {
		lo = hi
	}
	floor := lo - math.Max(lo*bandPct/100, 0.5)
	ceil := hi + math.Max(hi*bandPct/100, 0.5)
	if limit < floor || limit > ceil {
		return fmt.Errorf("limit ₹%.2f is outside ±%.0f%% of the market (LTP ₹%.2f, bid ₹%.2f, ask ₹%.2f) — check the price",
			limit, bandPct, q.LTP, q.Bid, q.Ask)
	}
	return nil
}

// CheckEntry runs every entry rule. A non-nil error means the entry must not
// be placed; warnings are advisory and should be shown with the confirmation.
func (b *Book) CheckEntry(now time.Time, r Rules, ec EntryContext, q OptQuote, limit float64) (warnings []string, err error) {
	var errs []error

	if halted, why := b.Halted(now); halted {
		errs = append(errs, fmt.Errorf("trading halted for today: %s", why))
	}
	if ec.Banned {
		errs = append(errs, errors.New("underlying is in the NSE F&O ban period — no fresh positions allowed"))
	}
	if r.MinOptVolume > 0 && q.Volume < r.MinOptVolume {
		errs = append(errs, fmt.Errorf("strike volume %d is below the %d minimum (thin strike)", q.Volume, r.MinOptVolume))
	}
	if r.MaxSpreadPct > 0 {
		switch sp := q.SpreadPct(); {
		case sp < 0:
			errs = append(errs, errors.New("no two-sided market (bid or ask missing)"))
		case sp > r.MaxSpreadPct:
			errs = append(errs, fmt.Errorf("bid-ask spread %.1f%% exceeds %.1f%% (bid ₹%.2f / ask ₹%.2f)", sp, r.MaxSpreadPct, q.Bid, q.Ask))
		}
	}
	if e := limitBandErr(q, limit, r.LimitBandPct); e != nil {
		errs = append(errs, e)
	}
	if FadeRisk(ec.Side, ec.Right, ec.Direction, ec.W52Status) {
		msg := fmt.Sprintf("selling %s against a stock %s at %s — these moves tend to extend", ec.Right, strings.ToLower(ec.Direction), strings.ToLower(ec.W52Status))
		if r.Block52wFade {
			errs = append(errs, errors.New(msg))
		} else {
			warnings = append(warnings, msg)
		}
	}
	return warnings, errors.Join(errs...)
}

// CheckExit only guards against a mistyped price. It never refuses on halt,
// ban or liquidity grounds.
func CheckExit(r Rules, q OptQuote, limit float64) error {
	return limitBandErr(q, limit, r.LimitBandPct)
}

// ── Daily loss halt ──────────────────────────────────────────────────────────

// haltState is persisted so a restart cannot quietly re-open trading on a day
// the loss limit already tripped.
type haltState struct {
	Day    string    `json:"day"`
	Reason string    `json:"reason"`
	PnL    float64   `json:"pnl"`
	At     time.Time `json:"at"`
	// Notified is set once the monitor loop has alerted (and flattened, if
	// configured), so a halt tripped from an entry attempt is still announced
	// exactly once.
	Notified bool `json:"notified"`
}

func (b *Book) haltPath() string { return filepath.Join(b.dataDir, "book_halt.json") }

func (b *Book) loadHalt() {
	if data, err := os.ReadFile(b.haltPath()); err == nil {
		json.Unmarshal(data, &b.halt)
	}
}

// DayPnL is today's realised P&L (trades exited today) plus open MTM.
func (b *Book) DayPnL(now time.Time) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dayPnLLocked(now)
}

func (b *Book) dayPnLLocked(now time.Time) float64 {
	day := now.Format("2006-01-02")
	var sum float64
	for i := range b.Trades {
		if b.Trades[i].ExitTime.In(now.Location()).Format("2006-01-02") == day {
			sum += b.Trades[i].PnL
		}
	}
	for _, p := range b.Positions {
		sum += p.UnrealPnL()
	}
	return sum
}

// Halted reports whether today's trading is halted, and why.
func (b *Book) Halted(now time.Time) (bool, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.halt.Day == now.Format("2006-01-02") {
		return true, b.halt.Reason
	}
	return false, ""
}

// CheckDailyLoss latches the halt when today's P&L has reached the limit.
// It returns true only on the call that trips it, so the caller alerts once.
// Call it after Refresh, so open P&L is current.
func (b *Book) CheckDailyLoss(now time.Time, r Rules) (tripped bool, pnl float64) {
	if r.Capital <= 0 || r.DailyLossPct <= 0 {
		return false, 0
	}
	limit := -r.Capital * r.DailyLossPct / 100
	day := now.Format("2006-01-02")

	b.mu.Lock()
	defer b.mu.Unlock()
	pnl = b.dayPnLLocked(now)
	if b.halt.Day == day || pnl > limit {
		return false, pnl
	}
	b.halt = haltState{
		Day:    day,
		Reason: fmt.Sprintf("day P&L ₹%.0f hit the %.1f%% loss limit (₹%.0f)", pnl, r.DailyLossPct, limit),
		PnL:    pnl,
		At:     now,
	}
	b.saveHaltLocked()
	return true, pnl
}

func (b *Book) saveHaltLocked() {
	if data, err := json.MarshalIndent(b.halt, "", " "); err == nil {
		os.WriteFile(b.haltPath(), data, 0644)
	}
}

// TakeHaltNotice returns today's halt reason if it has not been announced
// yet, and marks it announced.
func (b *Book) TakeHaltNotice(now time.Time) (reason string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.halt.Day != now.Format("2006-01-02") || b.halt.Notified {
		return "", false
	}
	b.halt.Notified = true
	b.saveHaltLocked()
	return b.halt.Reason, true
}

// PreEntry is the one gate both the dashboard and Telegram call before an
// entry: it refreshes the halt state, fetches a live quote for the contract
// and runs every entry rule.
func (b *Book) PreEntry(fy *fyers.FyersClient, now time.Time, r Rules, ec EntryContext, ticker string, limit float64) ([]string, error) {
	return b.PreEntryWith(FyersQuote(fy), now, r, ec, ticker, limit)
}

// QuoteFunc fetches a fresh quote for one option ticker (no "NSE:" prefix).
// It lets the guards run on any market-data source, not only Fyers.
type QuoteFunc func(ticker string) (OptQuote, error)

// FyersQuote is the Fyers quote source.
func FyersQuote(fy *fyers.FyersClient) QuoteFunc {
	return func(ticker string) (OptQuote, error) { return FetchOptQuote(fy, ticker) }
}

// PreEntryWith is PreEntry with any quote source.
func (b *Book) PreEntryWith(quote QuoteFunc, now time.Time, r Rules, ec EntryContext, ticker string, limit float64) ([]string, error) {
	b.CheckDailyLoss(now, r)
	q, err := quote(ticker)
	if err != nil {
		return nil, fmt.Errorf("no live quote for %s, so the entry checks cannot run: %w", ticker, err)
	}
	return b.CheckEntry(now, r, ec, q, limit)
}

// PreExit checks an exit price against a live quote. If the quote cannot be
// fetched the exit is allowed — getting out must not depend on market data.
func PreExit(fy *fyers.FyersClient, r Rules, ticker string, limit float64) error {
	return PreExitWith(FyersQuote(fy), r, ticker, limit)
}

// PreExitWith is PreExit with any quote source.
func PreExitWith(quote QuoteFunc, r Rules, ticker string, limit float64) error {
	q, err := quote(ticker)
	if err != nil {
		return nil
	}
	return CheckExit(r, q, limit)
}

// OneLine flattens a joined multi-error into a single line for UIs that
// cannot show newlines.
func OneLine(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", "; ")
}
