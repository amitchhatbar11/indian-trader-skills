// Package livesrv is `desk serve`: the one process that may place LIVE
// orders (real money) for the desk. The desk CLI never calls a broker; it
// hands an order the user approved to this server, which places it through
// the position book (internal/book) and tracks it:
//
//   - entries go through book.PreEntry (daily loss halt, F&O ban, liquidity,
//     price band) — any warning refuses — then SubmitEntry: one LIMIT order,
//     sent exactly once, PENDING_ENTRY until the fill is confirmed;
//   - ReconcileOrders follows every working order to its fill (partial fills,
//     cancels, rejections); ReconcilePositions compares the book with Fyers
//     and suspends automatic exits on a mismatch;
//   - the stop monitor closes a position at its stop or target with a LIMIT
//     order through SubmitExit (a rejected exit leaves it open);
//   - the daily loss limit halts new entries.
//
// It listens on loopback only and answers only requests carrying the token
// it writes to ~/.nse-desk/live.token (fresh each start). It refuses to
// start unless DESK_LIVE=true. Live orders are stock options only, sent to
// one broker (ORDER_BROKER: fyers, upstox, zerodha, dhan, angelone).
package livesrv

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/book"
	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// Strategy tags the positions the desk opened.
const Strategy = "desk"

// Server is the live engine.
type Server struct {
	Book   *book.Book
	Broker book.Broker // where orders go: Fyers, Upstox, Zerodha, Dhan or Angel One
	// Quote and LTPs are the market data the guards and the stop monitor
	// read (any configured feed). Ready says the session is usable.
	Quote         book.QuoteFunc
	LTPs          func(tickers []string) (map[string]float64, error)
	Ready         func() error
	Rules         book.Rules
	Ban           *fyers.BanList
	SellMarginPct float64
	BufferPct     float64 // exit limits are priced this % through the last price
	TokenFile     string
	Notify        func(string)

	token string
}

// Listen starts the HTTP endpoints on addr (must be a loopback address) and
// writes a fresh token.
func (s *Server) Listen(addr string) (*http.Server, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("desk serve listens on this machine only (127.0.0.1), not %s", host)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	s.token = hex.EncodeToString(b)
	if err := os.WriteFile(s.TokenFile, []byte(s.token+"\n"), 0o600); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desk/order", s.order)
	mux.HandleFunc("/api/desk/exit", s.exit)
	mux.HandleFunc("/api/desk/positions", s.positions)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return srv, nil
}

// Run is the monitor loop; it returns when ctx ends.
func (s *Server) Run(ctx context.Context, cal *desk.Calendar) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	var lastPos, lastStops time.Time
	startup := true
	for {
		now := time.Now()
		if s.Ready() == nil {
			open := cal.At(now).Open
			if startup || open && now.Sub(lastPos) >= time.Minute {
				if evs, err := s.Book.ReconcilePositions(s.Broker); err != nil {
					s.say("reconcile positions: %v", err)
				} else {
					startup, lastPos = false, now
					s.recon(evs)
				}
			}
			if open {
				s.recon(s.Book.ReconcileOrders(s.Broker, now, 2*time.Minute, time.Minute))
				if s.Ban != nil {
					s.Ban.Ensure(now)
				}
			}
			if open && now.Sub(lastStops) >= 30*time.Second {
				lastStops = now
				if err := s.Book.RefreshWith(s.LTPs); err != nil {
					s.say("mark-to-market failed (stops not checked this round): %v", err)
				} else {
					for _, ev := range s.Book.CheckStops(s.Broker, true, s.BufferPct) {
						s.slEvent(ev)
					}
					s.Book.CheckDailyLoss(now, s.Rules)
					if why, ok := s.Book.TakeHaltNotice(now); ok {
						s.say("TRADING HALTED for today — %s. New entries are refused; exits still work.", why)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Server) say(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	log.Print(msg)
	if s.Notify != nil {
		s.Notify(msg)
	}
}

func (s *Server) recon(evs []book.ReconEvent) {
	for _, e := range evs {
		s.say("%s", e.String())
	}
}

func (s *Server) slEvent(ev book.SLEvent) {
	p := ev.Position
	switch {
	case ev.Err != "":
		s.say("%s %s: exit NOT placed — %s", ev.Reason, p.Ticker, ev.Err)
	case ev.Pending:
		s.say("%s %s: exit order placed at ₹%.2f — waiting for the fill", ev.Reason, p.Ticker, ev.ExitedAt)
	case ev.Exited:
		s.say("%s %s: closed at ₹%.2f, P&L ₹%.0f", ev.Reason, p.Ticker, ev.ExitedAt, ev.PnL)
	}
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) auth(w http.ResponseWriter, r *http.Request) bool {
	fail := func(code int, msg string) bool {
		write(w, code, desk.LiveResult{Error: msg})
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return fail(http.StatusForbidden, "desk orders are accepted from this machine only")
	}
	if s.token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Desk-Token")), []byte(s.token)) != 1 {
		return fail(http.StatusUnauthorized, "bad desk token — it changes each time desk serve starts")
	}
	if err := s.Ready(); err != nil {
		return fail(http.StatusServiceUnavailable, err.Error())
	}
	return true
}

func (s *Server) order(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		write(w, http.StatusMethodNotAllowed, desk.LiveResult{Error: "POST only"})
		return
	}
	if !s.auth(w, r) {
		return
	}
	var req desk.LiveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		write(w, http.StatusBadRequest, desk.LiveResult{Error: "bad request: " + err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		write(w, http.StatusUnprocessableEntity, desk.LiveResult{Error: err.Error()})
		return
	}
	in := desk.ParseInstrument(req.Symbol)
	now := time.Now()
	side := book.SideBuy
	if req.Side == "sell" {
		side = book.SideSell
	}
	banned := false
	if s.Ban != nil {
		s.Ban.Ensure(now)
		var known bool
		banned, known = s.Ban.Banned(in.Underlying)
		if !known {
			write(w, http.StatusServiceUnavailable, desk.LiveResult{Error: "the NSE F&O ban list could not be read — not entering blind"})
			return
		}
	}
	warns, err := s.Book.PreEntryWith(s.Quote, now, s.Rules, book.EntryContext{Side: side, Right: in.Right, Banned: banned}, in.Symbol, req.Limit)
	if err != nil {
		write(w, http.StatusUnprocessableEntity, desk.LiveResult{Error: "refused by the entry guard — " + book.OneLine(err)})
		return
	}
	if len(warns) > 0 {
		write(w, http.StatusUnprocessableEntity, desk.LiveResult{Error: "refused: the entry guard warned — " + strings.Join(warns, "; "), Warnings: warns})
		return
	}
	pos := &book.Position{
		Underlying: in.Underlying, Ticker: in.Symbol, Right: in.Right, Side: side, Strike: in.Strike,
		Expiry: strings.ToUpper(time.Date(in.Year, in.Month, 1, 0, 0, 0, 0, time.UTC).Format("Jan-06")),
		Lots:   req.Qty / req.LotSize, LotSize: req.LotSize, Entry: req.Limit, EntryTime: now,
		LiveOrder: true, Strategy: Strategy, AutoWhy: fmt.Sprintf("desk %s · approved %q", req.ID, req.Reply),
		SLPrice: req.Stop, TargetPrice: req.Target, TrailTrigger: -1,
	}
	pos.RiskAmount = absf(req.Limit-req.Stop) * float64(req.Qty)
	pos.Capital = book.EstimateCapital(side, req.Limit, in.Strike, req.Qty, s.SellMarginPct)
	if err := s.Book.SubmitEntry(pos, s.Broker, now); err != nil {
		write(w, http.StatusBadGateway, desk.LiveResult{Error: err.Error()})
		return
	}
	s.say("LIVE %s %d %s LIMIT ₹%.2f placed — order %s, waiting for the fill", side, req.Qty, in.Symbol, req.Limit, pos.OrderID)
	write(w, http.StatusOK, desk.LiveResult{OK: true, OrderID: pos.OrderID, State: pos.State})
}

func (s *Server) exit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		write(w, http.StatusMethodNotAllowed, desk.LiveResult{Error: "POST only"})
		return
	}
	if !s.auth(w, r) {
		return
	}
	var x desk.LiveExit
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&x); err != nil {
		write(w, http.StatusBadRequest, desk.LiveResult{Error: "bad request: " + err.Error()})
		return
	}
	ticker := desk.ParseInstrument(x.Symbol).Symbol
	if !desk.IsYes(x.Reply) || x.Limit <= 0 {
		write(w, http.StatusUnprocessableEntity, desk.LiveResult{Error: "an exit needs a limit price and the user's clear yes"})
		return
	}
	mine := false
	for _, p := range s.Book.List() {
		if p.Ticker == ticker && p.Strategy == Strategy {
			mine = true
		}
	}
	if !mine {
		write(w, http.StatusNotFound, desk.LiveResult{Error: "no desk position in " + ticker})
		return
	}
	if err := book.PreExitWith(s.Quote, s.Rules, ticker, x.Limit); err != nil {
		write(w, http.StatusUnprocessableEntity, desk.LiveResult{Error: err.Error()})
		return
	}
	_, pending, err := s.Book.SubmitExit(ticker, x.Limit, "DESK_EXIT", s.Broker, time.Now())
	if err != nil {
		write(w, http.StatusBadGateway, desk.LiveResult{Error: err.Error()})
		return
	}
	st := "CLOSED"
	if pending {
		st = book.StatePendingExit
	}
	write(w, http.StatusOK, desk.LiveResult{OK: true, State: st})
}

func (s *Server) positions(w http.ResponseWriter, r *http.Request) {
	if !s.auth(w, r) {
		return
	}
	var out []desk.LivePosition
	for _, p := range s.Book.List() {
		if p.Strategy != Strategy {
			continue
		}
		st := p.State
		if st == "" {
			st = "OPEN"
		}
		if p.Mismatch != "" {
			st += " · MISMATCH: " + p.Mismatch
		}
		out = append(out, desk.LivePosition{Symbol: p.Ticker, Side: p.SideLabel(), Qty: p.Qty(), Entry: p.Entry, LTP: p.LTP,
			Stop: p.SLPrice, Target: p.TargetPrice, State: st, OrderID: p.OrderID, Opened: p.EntryTime, PnL: p.UnrealPnL()})
	}
	today := time.Now().Format("2006-01-02")
	for _, t := range s.Book.AllTrades() {
		if t.Strategy == Strategy && t.ExitTime.Format("2006-01-02") == today {
			out = append(out, desk.LivePosition{Symbol: t.Ticker, Side: t.SideLabel(), Qty: t.Lots * t.LotSize, Entry: t.Entry,
				LTP: t.Exit, Opened: t.EntryTime, PnL: t.PnL, Closed: true, Reason: t.Reason, State: "CLOSED"})
		}
	}
	write(w, http.StatusOK, out)
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
