// Command desk is the Indian trading desk behind the nse-* agent skills:
// scanner, entry timer, exit planner, time machine, sizer, risk check, PAPER
// broker, alerts, news, results and deals, for NSE cash stocks (intraday)
// and stock options.
//
//	go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest
//	desk login fyers
//	desk clock
//	desk scan -orb
//	desk entry RELIANCE
//
// It never places a real order: prices come read-only from Fyers, Upstox,
// Zerodha or Breeze (DESK_FEEDS), and trades go to ~/.nse-desk/paper.json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/bhavcopy"
	"github.com/amitchhatbar11/indian-trader-skills/internal/breeze"
	"github.com/amitchhatbar11/indian-trader-skills/internal/corpact"
	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// env is everything a subcommand may need, built lazily.
type env struct {
	cfg     *Config
	dir     desk.Dir
	nse     *desk.NSE
	now     time.Time
	jsonOut bool

	cal     *desk.Calendar
	calErr  error
	calDone bool
	fy      *fyers.FyersClient
	feed    *desk.Multi
	data    desk.NSEData
	notes   []string
}

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	e := &env{cfg: cfg, dir: desk.Dir(cfg.DataDir)}
	e.nse = desk.NewNSE(filepath.Join(string(e.dir), "cache"))
	e.data = desk.NSEData{Client: bhavcopy.New(),
		FODir: filepath.Join(cfg.DataDir, "bhavcopy_fo"), CMDir: filepath.Join(cfg.DataDir, "bhavcopy_cm")}
	e.data.Client.Pause = 300 * time.Millisecond

	cmds := map[string]func(*env, []string) error{
		"clock": cmdClock, "quote": cmdQuote, "scan": cmdScan, "entry": cmdEntry, "exit": cmdExit,
		"sim": cmdSim, "size": cmdSize, "risk": cmdRisk, "broker": cmdBroker, "alerts": cmdAlerts,
		"news": cmdNews, "results": cmdResults, "deals": cmdDeals, "login": cmdLogin,
	}
	f, ok := cmds[cmd]
	if !ok {
		usage()
		os.Exit(2)
	}
	if err := f(e, args); err != nil {
		fmt.Fprintln(os.Stderr, "desk "+cmd+": "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: desk <command> [flags]       (files in ~/.nse-desk, or $NSE_DESK_HOME)
  login     fyers (OAuth, daily) | upstox | zerodha | breeze (how to set their tokens)
  clock     market open/closed, next session, NSE holidays, stock-option expiries, feeds
  quote     live quotes (-write saves ~/.nse-desk/prices.json)
  scan      daily setups from NSE bhavcopies; -orb adds today's opening-range breakouts
  entry     opening-range entry timer for one symbol
  exit      stop and target plan (~/.nse-desk/plan.json)
  sim       time machine: 10,000 replays + the rule on every past session
  size      size the plan into one LIMIT order (~/.nse-desk/orders.json)
  risk      check orders against your limits (~/.nse-desk/risk.json)
  broker    PAPER broker: show | approve | place | sync | flatten | log
  alerts    price/time alerts: add | check | list | reset
  news      NSE announcements + Indian news for symbols
  results   quarterly results from NSE filings (XBRL)
  deals     bulk/block deals and insider (SEBI PIT) trades
Add -h after a command for its flags.`)
}

// common adds -json and -now to a flag set.
func (e *env) common(fs *flag.FlagSet) *string {
	fs.BoolVar(&e.jsonOut, "json", false, "print JSON")
	return fs.String("now", "", `pin the clock, IST ("2026-11-16 10:15")`)
}

func (e *env) setNow(s string) error {
	e.now = time.Now().In(desk.IST)
	if s == "" {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02 15:04", s, desk.IST)
	if err != nil {
		return fmt.Errorf("-now %q: want \"2006-01-02 15:04\"", s)
	}
	e.now = t
	return nil
}

func (e *env) calendar() *desk.Calendar {
	if !e.calDone {
		e.calDone = true
		e.cal, e.calErr = desk.LoadCalendar(e.nse)
		if e.calErr != nil {
			e.note("NSE holidays unavailable (%v) — only weekends are known", e.calErr)
		}
	}
	return e.cal
}

func (e *env) note(f string, a ...any) { e.notes = append(e.notes, fmt.Sprintf(f, a...)) }

func (e *env) printNotes() {
	for _, n := range e.notes {
		fmt.Fprintln(os.Stderr, "note: "+n)
	}
}

// fyersClient is a Fyers client with the token from `desk login fyers`.
func (e *env) fyersClient() *fyers.FyersClient {
	if e.fy != nil {
		return e.fy
	}
	// The token saved by `desk login fyers` (daily) is fresher than .env's.
	fy := fyers.New(e.cfg.FyersAppID, e.cfg.FyersAppSecret, "")
	fy.SetTokenPath(filepath.Join(e.cfg.DataDir, "fyers_token.json"))
	if !fy.LoadToken() && e.cfg.FyersAccessToken != "" {
		fy = fyers.New(e.cfg.FyersAppID, e.cfg.FyersAppSecret, e.cfg.FyersAccessToken)
		fy.SetTokenPath(filepath.Join(e.cfg.DataDir, "fyers_token.json"))
	}
	if !fy.IsAuthenticated() {
		return nil
	}
	e.fy = fy
	return fy
}

// feeds builds the market-data chain from DESK_FEEDS, skipping brokers
// without credentials.
func (e *env) feeds() (*desk.Multi, error) {
	if e.feed != nil {
		return e.feed, nil
	}
	m := &desk.Multi{Log: func(s string) { e.note("%s", s) }}
	cache := filepath.Join(string(e.dir), "cache")
	var skipped []string
	for _, name := range strings.Split(e.cfg.DeskFeeds, ",") {
		switch strings.TrimSpace(strings.ToLower(name)) {
		case "fyers":
			if fy := e.fyersClient(); fy != nil {
				m.Feeds = append(m.Feeds, desk.FyersFeed{C: fy})
			} else {
				skipped = append(skipped, "fyers (no token — desk login fyers)")
			}
		case "upstox":
			if e.cfg.UpstoxAccessToken != "" {
				m.Feeds = append(m.Feeds, &desk.UpstoxFeed{Token: e.cfg.UpstoxAccessToken, CacheDir: cache})
			} else {
				skipped = append(skipped, "upstox (UPSTOX_ACCESS_TOKEN not set)")
			}
		case "zerodha", "kite":
			if e.cfg.KiteAPIKey != "" && e.cfg.KiteAccessToken != "" {
				m.Feeds = append(m.Feeds, &desk.KiteFeed{APIKey: e.cfg.KiteAPIKey, Token: e.cfg.KiteAccessToken, CacheDir: cache})
			} else {
				skipped = append(skipped, "zerodha (KITE_API_KEY / KITE_ACCESS_TOKEN not set)")
			}
		case "breeze":
			if e.cfg.BreezeAPIKey != "" && e.cfg.BreezeSession != "" {
				m.Feeds = append(m.Feeds, &lazyBreeze{e: e})
			} else {
				skipped = append(skipped, "breeze (BREEZE_API_KEY / BREEZE_SESSION not set)")
			}
		}
	}
	if len(m.Feeds) == 0 {
		return nil, fmt.Errorf("no market-data feed is configured: %s", strings.Join(skipped, "; "))
	}
	e.feed = m
	return m, nil
}

// lazyBreeze logs in to Breeze on first use (a session costs a call).
type lazyBreeze struct {
	e   *env
	f   *desk.BreezeFeed
	err error
}

func (l *lazyBreeze) Name() string { return "breeze" }

func (l *lazyBreeze) Quotes([]string) (map[string]desk.Quote, error) { return nil, desk.ErrNoQuotes }

func (l *lazyBreeze) Bars(sym, res string, from, to time.Time) ([]desk.Bar, error) {
	if l.f == nil && l.err == nil {
		dir := filepath.Join(l.e.cfg.DataDir, "breeze")
		c, err := breeze.NewSession(l.e.cfg.BreezeAPIKey, l.e.cfg.BreezeSession, filepath.Join(dir, "cache"))
		if err != nil {
			l.err = err
		} else if codes, err := breeze.StockCodes(dir); err != nil {
			l.err = err
		} else {
			l.f = &desk.BreezeFeed{C: c, Codes: codes, Expiry: l.e.expiryFor}
		}
	}
	if l.err != nil {
		return nil, l.err
	}
	return l.f.Bars(sym, res, from, to)
}

// expiryFor finds an option's expiry date in the latest F&O bhavcopy.
func (e *env) expiryFor(in desk.Instrument) (time.Time, error) {
	for d, i := e.now.AddDate(0, 0, -1), 0; i < 10; d, i = d.AddDate(0, 0, -1), i+1 {
		fo, err := e.data.FO(d)
		if err != nil {
			continue
		}
		ch := fo.Chains[in.Underlying]
		if ch == nil {
			return time.Time{}, fmt.Errorf("%s has no options in the %s F&O bhavcopy", in.Underlying, d.Format("02 Jan"))
		}
		for _, x := range ch.Expiries() {
			if x.Year() == in.Year && x.Month() == in.Month {
				return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, desk.IST), nil
			}
		}
		return time.Time{}, fmt.Errorf("no %s expiry in %s %d", in.Underlying, in.Month, in.Year)
	}
	return time.Time{}, fmt.Errorf("no recent F&O bhavcopy to find the expiry")
}

// corp loads the corporate-action registry (cached; NSE refresh daily).
func (e *env) corp() *corpact.Registry {
	r, err := corpact.Load(filepath.Join(e.cfg.DataDir, "corpact"), e.now.Year()-1, e.now.Year())
	if err != nil {
		e.note("corporate actions unavailable (%v) — prices are not split-adjusted", err)
		return nil
	}
	return r
}

func (e *env) limits() (desk.Limits, error) {
	def := desk.DefaultLimits(e.cfg.Capital, e.cfg.SLRiskPct, e.cfg.DailyLossPct, e.cfg.MaxSpreadPct)
	l, created, err := desk.LoadLimits(e.dir, def)
	if created {
		e.note("wrote your limits to %s — edit it to change them", e.dir.Path("config.json"))
	}
	return l, err
}

func (e *env) ledger(lim desk.Limits) (*desk.Ledger, error) {
	return desk.LoadLedger(e.dir, lim.Capital)
}

func (e *env) emit(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

// quotes fetches quotes through the feed chain.
func (e *env) quotes(syms []string) (map[string]desk.Quote, error) {
	f, err := e.feeds()
	if err != nil {
		return nil, err
	}
	uniq := map[string]bool{}
	var list []string
	for _, s := range syms {
		s = desk.ParseInstrument(s).Symbol
		if s != "" && !uniq[s] {
			uniq[s] = true
			list = append(list, s)
		}
	}
	sort.Strings(list)
	return f.Quotes(list)
}

// todayBars returns today's intraday bars for a symbol.
func (e *env) todayBars(sym, res string) ([]desk.Bar, error) {
	f, err := e.feeds()
	if err != nil {
		return nil, err
	}
	return f.Bars(sym, res, e.now, e.now)
}

func upper(args []string) []string {
	var out []string
	for _, a := range args {
		for _, s := range strings.FieldsFunc(a, func(r rune) bool { return r == ',' || r == ' ' }) {
			out = append(out, strings.ToUpper(strings.TrimSpace(s)))
		}
	}
	return out
}

func yn(b bool, y, n string) string {
	if b {
		return y
	}
	return n
}
