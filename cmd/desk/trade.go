package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// parse handles flags before and after positional arguments
// ("entry RELIANCE -side short" as well as "entry -side short RELIANCE").
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func tw() *tabwriter.Writer { return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0) }

// ── clock ───────────────────────────────────────────────────────────────────

func cmdClock(e *env, args []string) error {
	fs := flag.NewFlagSet("clock", flag.ExitOnError)
	now := e.common(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	cal := e.calendar()
	st := cal.At(e.now)
	var expiries []string
	for d, i := e.now, 0; i < 10; d, i = d.AddDate(0, 0, -1), i+1 {
		if fo, err := e.data.FO(d); err == nil {
			seen := map[string]bool{}
			for _, ch := range fo.Chains {
				for _, x := range ch.Expiries() {
					seen[x.Format("Mon 02 Jan 2006")+"|"+x.Format("2006-01-02")] = true
				}
			}
			for k := range seen {
				expiries = append(expiries, k)
			}
			sort.Slice(expiries, func(a, b int) bool { return strings.Split(expiries[a], "|")[1] < strings.Split(expiries[b], "|")[1] })
			break
		}
	}
	var feeds []string
	if f, err := e.feeds(); err == nil {
		for _, x := range f.Feeds {
			feeds = append(feeds, x.Name())
		}
	} else {
		feeds = []string{"none: " + err.Error()}
	}
	hols := cal.Upcoming(e.now, 6)
	if e.jsonOut {
		e.emit(map[string]any{"now": e.now, "status": st, "holidays": hols, "expiries": expiries, "feeds": feeds})
		return nil
	}
	fmt.Printf("%s IST — NSE %s\n", e.now.Format("Mon 02 Jan 2006 15:04"), st.State)
	if st.Open {
		fmt.Printf("Closes at 15:30 (%s left)\n", st.Closes.Sub(e.now).Round(time.Minute))
	} else {
		fmt.Printf("Next session: %s\n", st.NextOpen.Format("Mon 02 Jan 15:04"))
	}
	if len(hols) > 0 {
		fmt.Println("\nUpcoming NSE holidays:")
		for _, h := range hols {
			fmt.Printf("  %s  %s\n", h.Date.Format("Mon 02 Jan 2006"), h.Description)
		}
	}
	if len(expiries) > 0 {
		fmt.Println("\nStock-option expiries listed:")
		for _, x := range expiries {
			fmt.Println("  " + strings.Split(x, "|")[0])
		}
	}
	fmt.Println("\nPrice feeds, in order: " + strings.Join(feeds, " → "))
	e.printNotes()
	return nil
}

// ── quote ───────────────────────────────────────────────────────────────────

func cmdQuote(e *env, args []string) error {
	fs := flag.NewFlagSet("quote", flag.ExitOnError)
	now := e.common(fs)
	write := fs.Bool("write", false, "save to ~/.nse-desk/prices.json")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	syms := upper(pos)
	if len(syms) == 0 {
		return fmt.Errorf("give one or more symbols (RELIANCE, RELIANCE26NOV1440CE)")
	}
	qs, err := e.quotes(syms)
	if err != nil {
		return err
	}
	if *write {
		var old map[string]desk.Quote
		e.dir.Read("prices.json", &old)
		if old == nil {
			old = map[string]desk.Quote{}
		}
		for k, v := range qs {
			old[k] = v
		}
		if err := e.dir.Write("prices.json", old); err != nil {
			return err
		}
	}
	if e.jsonOut {
		e.emit(qs)
		return nil
	}
	w := tw()
	fmt.Fprintln(w, "symbol\tLTP\tchange\tbid\task\tvolume\tsource")
	for _, s := range syms {
		q, ok := qs[desk.ParseInstrument(s).Symbol]
		if !ok {
			fmt.Fprintf(w, "%s\tno quote\t\t\t\t\t\n", s)
			continue
		}
		fmt.Fprintf(w, "%s\t₹%.2f\t%+.2f%%\t%.2f\t%.2f\t%.0f\t%s\n", s, q.LTP, q.ChangePct(), q.Bid, q.Ask, q.Volume, q.Source)
	}
	w.Flush()
	e.printNotes()
	return nil
}

// ── scan ────────────────────────────────────────────────────────────────────

type watchlist struct {
	Day      string      `json:"day"`
	Universe string      `json:"universe"`
	Picks    []desk.Pick `json:"picks"`
	ORB      []desk.ORB  `json:"orb,omitempty"`
	ORBAt    time.Time   `json:"orb_at,omitempty"`
}

func cmdScan(e *env, args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	now := e.common(fs)
	universe := fs.String("universe", "fno", "fno (stocks with options) or all (every EQ stock above the floors)")
	setup := fs.String("setup", "", "only this setup (breakout20, breakdown20, bull_flag, reclaim20, oversold, bounce, overbought)")
	orb := fs.Bool("orb", false, "also scan today's opening-range breakouts on the watchlist (or the symbols given)")
	minutes := fs.Int("minutes", 15, "opening range length")
	limit := fs.Int("limit", 30, "most stocks to print (and to ORB-scan)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	lim, err := e.limits()
	if err != nil {
		return err
	}
	// The last published close: yesterday before NSE publishes (~18:00).
	asOf := e.now
	if e.now.Format("15:04") < "18:30" {
		asOf = e.now.AddDate(0, 0, -1)
	}
	h, err := desk.LoadHistory(e.data, e.corp(), asOf, 70)
	if err != nil {
		return err
	}
	picks := desk.ScanDaily(h, *universe, lim.MinPrice, lim.MinTurnoverCr)
	if *setup != "" {
		var keep []desk.Pick
		for _, p := range picks {
			for _, s := range p.Setups {
				if s == *setup {
					keep = append(keep, p)
				}
			}
		}
		picks = keep
	}
	wl := watchlist{Day: h.Last.Format("2006-01-02"), Universe: *universe, Picks: picks}

	if *orb {
		syms := upper(pos)
		if len(syms) == 0 {
			for i, p := range picks {
				if i >= *limit {
					break
				}
				syms = append(syms, p.Symbol)
			}
		}
		f, err := e.feeds()
		if err != nil {
			return err
		}
		for _, s := range syms {
			bs, err := f.Bars(s, "5", e.now, e.now)
			if err != nil {
				e.note("%s: %v", s, err)
				continue
			}
			wl.ORB = append(wl.ORB, desk.OpeningRange(s, bs, *minutes, 5, e.now))
		}
		wl.ORBAt = e.now
	}
	if err := e.dir.Write("watchlist.json", wl); err != nil {
		return err
	}
	if e.jsonOut {
		e.emit(wl)
		return nil
	}
	fmt.Printf("Daily setups on %s's close (%s universe, price ≥ ₹%.0f, traded ≥ ₹%.0f cr): %d stocks\n\n",
		h.Last.Format("Mon 02 Jan"), *universe, lim.MinPrice, lim.MinTurnoverCr, len(picks))
	w := tw()
	fmt.Fprintln(w, "stock\tclose\tchg\tvol×20d\ttraded ₹cr\tRSI\t20d high\t20d low\tATR\tside\tsetups")
	for i, p := range picks {
		if i >= *limit {
			fmt.Fprintf(w, "… %d more in ~/.nse-desk/watchlist.json\n", len(picks)-*limit)
			break
		}
		fmt.Fprintf(w, "%s%s\t%.2f\t%+.2f%%\t%.1f\t%.0f\t%.0f\t%.2f\t%.2f\t%.2f\t%s\t%s\n", p.Symbol, yn(p.FnO, " (F&O)", ""),
			p.Close, p.ChangePct, p.VolRatio, p.TurnoverCr, p.RSI, p.High20, p.Low20, p.ATR, p.Side, strings.Join(p.Setups, ", "))
	}
	w.Flush()
	if len(wl.ORB) > 0 {
		fmt.Printf("\nOpening range (first %d min) at %s:\n", *minutes, e.now.Format("15:04"))
		w = tw()
		fmt.Fprintln(w, "stock\trange low\trange high\tlast\tstate\tbroke at\tvol×range")
		for _, o := range wl.ORB {
			at := ""
			if !o.BrokeAt.IsZero() {
				at = o.BrokeAt.Format("15:04")
			}
			fmt.Fprintf(w, "%s\t%.2f\t%.2f\t%.2f\t%s\t%s\t%.1f\n", o.Symbol, o.Low, o.High, o.Last, o.State, at, o.VolRatio)
		}
		w.Flush()
	}
	for _, n := range h.Notes {
		e.note("%s", n)
	}
	e.printNotes()
	return nil
}

// ── entry ───────────────────────────────────────────────────────────────────

func cmdEntry(e *env, args []string) error {
	fs := flag.NewFlagSet("entry", flag.ExitOnError)
	now := e.common(fs)
	side := fs.String("side", "long", "long (close above) or short (close below)")
	minutes := fs.Int("minutes", 15, "opening range length, minutes from 09:15")
	bar := fs.Int("bar", 5, "bar size, 1 or 5 minutes")
	level := fs.Float64("level", 0, "a custom line (e.g. the 20-day high) instead of the range edge")
	vol := fs.Float64("vol", 0, "require the trigger bar's volume ≥ this × the range bars' average (0 = off)")
	watch := fs.Bool("watch", false, "keep checking each bar until triggered or the window ends")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	syms := upper(pos)
	if len(syms) != 1 {
		return fmt.Errorf("give one symbol")
	}
	if *side != "long" && *side != "short" {
		return fmt.Errorf("-side long or short")
	}
	lim, err := e.limits()
	if err != nil {
		return err
	}
	cal := e.calendar()
	rule := desk.EntryRule{Side: *side, Minutes: *minutes, BarMin: *bar, Level: *level, MinVolX: *vol, LastEntry: lim.NoEntriesAfter}
	for {
		bs, err := e.todayBars(syms[0], fmt.Sprint(*bar))
		if err != nil {
			return err
		}
		en := desk.TimeEntry(syms[0], bs, rule, e.now, cal)
		if err := e.dir.Write("entry.json", en); err != nil {
			return err
		}
		if e.jsonOut {
			e.emit(en)
		} else {
			printEntry(en, e.now)
		}
		if !*watch || en.State != "waiting" {
			break
		}
		next := e.now.Truncate(time.Duration(*bar) * time.Minute).Add(time.Duration(*bar)*time.Minute + 5*time.Second)
		time.Sleep(time.Until(next))
		e.now = time.Now().In(desk.IST)
	}
	e.printNotes()
	return nil
}

func printEntry(en desk.Entry, now time.Time) {
	dir := "above"
	if en.Side == "short" {
		dir = "below"
	}
	fmt.Printf("%s %s — %d-minute opening range ₹%.2f–₹%.2f, line ₹%.2f (%s, %d-min bars)\n",
		now.Format("15:04"), en.Symbol, en.Minutes, en.RangeLow, en.RangeHigh, en.Level, dir, en.BarMin)
	switch en.State {
	case "triggered":
		fmt.Printf("TRIGGERED %s: the %s bar closed at ₹%.2f %s ₹%.2f (volume %.1f× the range bars).\nWritten to ~/.nse-desk/entry.json — next: desk exit\n",
			strings.ToUpper(en.Side), en.At.Format("15:04"), en.Price, dir, en.Level, en.VolRatio)
	case "waiting":
		fmt.Printf("WAITING: no completed bar has closed %s ₹%.2f yet. %s\n", dir, en.Level, en.Note)
	case "expired":
		fmt.Printf("EXPIRED: %s\n", en.Note)
	case "not_open":
		fmt.Printf("NOT OPEN: %s\n", en.Note)
	}
}

// ── exit ────────────────────────────────────────────────────────────────────

func cmdExit(e *env, args []string) error {
	fs := flag.NewFlagSet("exit", flag.ExitOnError)
	now := e.common(fs)
	stop := fs.String("stop", "", "mid (range midpoint, default for stocks), range, pct (default for options), atr, price")
	stopVal := fs.Float64("stop-val", 0, "the % for pct, the multiple for atr, the price for price")
	r := fs.Float64("r", 2, "target at this many R (stocks); options default to the limits' option_target_pct")
	target := fs.Float64("target", 0, "a target price instead")
	entry := fs.Float64("entry", 0, "plan from this entry price instead of ~/.nse-desk/entry.json")
	symbol := fs.String("symbol", "", "with -entry: the symbol")
	side := fs.String("side", "long", "with -entry: long or short (for options, short = sell the option)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	lim, err := e.limits()
	if err != nil {
		return err
	}
	var en desk.Entry
	if *entry > 0 {
		if *symbol == "" {
			return fmt.Errorf("-entry needs -symbol")
		}
		en = desk.Entry{Symbol: strings.ToUpper(*symbol), Side: *side, Price: *entry, At: e.now, State: "triggered"}
	} else if err := e.dir.Read("entry.json", &en); err != nil {
		return fmt.Errorf("no entry: run desk entry first, or give -entry and -symbol (%v)", err)
	}
	if en.State != "triggered" && *entry == 0 {
		return fmt.Errorf("the entry for %s is %s, not triggered — there is nothing to plan yet", en.Symbol, en.State)
	}
	in := desk.ParseInstrument(en.Symbol)
	rule := desk.ExitRule{Stop: *stop, StopVal: *stopVal, TargetR: *r, TargetPx: *target}
	if rule.Stop == "" {
		rule.Stop = "mid"
		if in.Option || en.RangeHigh <= 0 {
			rule.Stop = "pct"
		}
	}
	if in.Option && *target == 0 && !flagSet(fs, "r") {
		rule.TargetR = 0
	}
	ref := en.Price
	syms := []string{en.Symbol}
	if in.Option {
		syms = append(syms, in.Underlying)
	}
	var q *desk.Quote
	if qs, err := e.quotes(syms); err == nil {
		if x, ok := qs[in.Symbol]; ok {
			q = &x
		}
		if x, ok := qs[in.Underlying]; ok && in.Option {
			ref = x.LTP
		}
	} else {
		e.note("no live quote for the spread check: %v", err)
	}
	if rule.Stop == "atr" {
		h, err := desk.LoadHistory(e.data, nil, e.now.AddDate(0, 0, -1), 20)
		if err == nil {
			if bs := h.Bars[in.Underlying]; len(bs) > 15 {
				rule.ATR = dailyATR(bs)
			}
		}
	}
	p, err := desk.MakePlan(en, rule, lim, q, ref)
	if err != nil {
		return err
	}
	if err := e.dir.Write("plan.json", p); err != nil {
		return err
	}
	if e.jsonOut {
		e.emit(p)
		return nil
	}
	fmt.Printf("%s %s from ₹%.2f — stop ₹%.2f (%s), target ₹%.2f (%.1fR) · risk ₹%.2f a %s · tick ₹%.2f · %s\n",
		strings.ToUpper(p.Side), p.Symbol, p.Entry, p.Stop, p.StopRule, p.Target, p.TargetR, p.RiskPerShr,
		yn(p.Option, "unit", "share"), p.Tick, p.Product)
	for _, n := range p.Notes {
		fmt.Println("  " + n)
	}
	fmt.Println("Written to ~/.nse-desk/plan.json — next: desk sim (time machine), then desk size")
	e.printNotes()
	return nil
}

func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func dailyATR(bs []desk.Bar) float64 {
	n := 14
	var s float64
	for i := len(bs) - n; i < len(bs); i++ {
		s += math.Max(bs[i].H-bs[i].L, math.Max(math.Abs(bs[i].H-bs[i-1].C), math.Abs(bs[i].L-bs[i-1].C)))
	}
	return s / float64(n)
}

// ── sim ─────────────────────────────────────────────────────────────────────

func cmdSim(e *env, args []string) error {
	fs := flag.NewFlagSet("sim", flag.ExitOnError)
	now := e.common(fs)
	replays := fs.Int("replays", 10000, "simulated sessions")
	seed := fs.Int64("seed", 19, "random seed (same seed, same answer)")
	window := fs.Int("window", 2, "draw each bar from ± this many bars of the same time of day")
	until := fs.String("until", "", "end each replay here (default: the plan's flat-by time)")
	days := fs.Int("days", 60, "calendar days of 5-minute history for the pool")
	rangeMin := fs.Int("minutes", 15, "opening range length for the history replay")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	var p desk.Plan
	if err := e.dir.Read("plan.json", &p); err != nil {
		return fmt.Errorf("no plan: run desk exit first (%v)", err)
	}
	u := *until
	if u == "" {
		u = p.FlatBy
	}
	if u == "" {
		u = "15:15"
	}
	f, err := e.feeds()
	if err != nil {
		return err
	}
	bs, err := f.Bars(p.Symbol, "5", e.now.AddDate(0, 0, -*days), e.now)
	if err != nil {
		return err
	}
	sess := desk.Sessions(bs, e.now)
	res, err := desk.Simulate(p, sess, *replays, *seed, *window, u)
	if err != nil {
		return err
	}
	res.Summarize(desk.Replay(p, sess, *rangeMin, u))
	if err := e.dir.Write("timemachine.json", map[string]any{"plan": p, "result": res}); err != nil {
		return err
	}
	if e.jsonOut {
		e.emit(res)
		return nil
	}
	fmt.Printf("Time machine — %s %s ₹%.2f, stop ₹%.2f, target ₹%.2f (%.1fR), until %s\n", strings.ToUpper(p.Side), p.Symbol, p.Entry, p.Stop, p.Target, p.TargetR, u)
	fmt.Printf("%d replays from %d full sessions of the stock's own 5-minute bars (last %d days)\n\n", res.Replays, res.Sessions, *days)
	fmt.Printf("  target first   %5.1f%%\n  stop first     %5.1f%%\n  neither by %s %5.1f%%\n  green          %5.1f%%\n  average        %+.2fR\n",
		res.TargetFirst, res.StopFirst, u, res.Neither, res.Green, res.AvgR)
	if res.Neither > 0 {
		fmt.Printf("  price at %s when neither (5/25/50/75/95%%): %.2f / %.2f / %.2f / %.2f / %.2f\n", u, res.CloseP[0], res.CloseP[1], res.CloseP[2], res.CloseP[3], res.CloseP[4])
	}
	fmt.Printf("\nHistory — the same rule on each past session: %d entries, %d targets, %d stops, average %+.2fR\n",
		res.HistTrades, res.HistTargets, res.HistStops, res.HistAvgR)
	fmt.Printf("\nVerdict: %s\nA replay is built from the stock's recent bars, not a forecast.\n", res.Verdict)
	e.printNotes()
	return nil
}

// ── size ────────────────────────────────────────────────────────────────────

func cmdSize(e *env, args []string) error {
	fs := flag.NewFlagSet("size", flag.ExitOnError)
	now := e.common(fs)
	write := fs.Bool("write", false, "write the order to ~/.nse-desk/orders.json")
	limit := fs.Float64("limit", 0, "a limit price other than the plan's entry")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	lim, err := e.limits()
	if err != nil {
		return err
	}
	var p desk.Plan
	if err := e.dir.Read("plan.json", &p); err != nil {
		return fmt.Errorf("no plan: run desk exit first (%v)", err)
	}
	led, err := e.ledger(lim)
	if err != nil {
		return err
	}
	in := desk.ParseInstrument(p.Symbol)
	si := desk.SizeInput{MarginUsed: led.MarginUsed(), Limit: *limit}
	if in.Option {
		lot, err := e.lotSize(in)
		if err != nil {
			return err
		}
		si.Lot = lot
		if qs, err := e.quotes([]string{in.Underlying}); err == nil {
			si.Spot = qs[in.Underlying].LTP
		}
		if fy := e.fyersClient(); fy != nil && p.Side == "short" {
			si.SpanMargin = func(lots int) (float64, error) {
				m, err := fy.SpanMargin([]fyers.MarginLeg{{Symbol: in.Symbol, Qty: lots * lot, Sell: true}}, desk.Margin)
				if err != nil {
					return 0, err
				}
				return m.Total, nil
			}
		}
	}
	o, err := desk.Size(p, lim, si, e.now)
	if err != nil {
		return err
	}
	if *write {
		if err := e.dir.Write("orders.json", []desk.Order{o}); err != nil {
			return err
		}
	}
	if e.jsonOut {
		e.emit(o)
		return nil
	}
	unit := "shares"
	if in.Option {
		unit = fmt.Sprintf("units (%d lots of %d)", o.Qty/o.LotSize, o.LotSize)
	}
	fmt.Printf("%s %d %s %s LIMIT ₹%.2f · %s\n", strings.ToUpper(o.Side), o.Qty, unit, o.Symbol, o.Limit, o.Product)
	fmt.Printf("  value %s · margin %s (%s) · loss at the stop ₹%.2f: %s · target ₹%.2f\n",
		desk.Rupees(o.Value()), desk.Rupees(o.Margin), o.MarginSrc, o.Stop, desk.Rupees(o.MaxLoss), o.Target)
	fmt.Println("  " + o.Why)
	if *write {
		fmt.Println("Written to ~/.nse-desk/orders.json — next: desk risk. Nothing has been placed.")
	} else {
		fmt.Println("Not written (add -write). Nothing has been placed.")
	}
	e.printNotes()
	return nil
}

// lotSize is an option's lot from the latest F&O bhavcopy.
func (e *env) lotSize(in desk.Instrument) (int, error) {
	exp, err := e.expiryFor(in)
	if err != nil {
		return 0, err
	}
	for d, i := e.now.AddDate(0, 0, -1), 0; i < 10; d, i = d.AddDate(0, 0, -1), i+1 {
		if fo, err := e.data.FO(d); err == nil {
			if ch := fo.Chains[in.Underlying]; ch != nil {
				return ch.LotFor(exp), nil
			}
		}
	}
	return 0, fmt.Errorf("no lot size for %s", in.Underlying)
}

// ── risk ────────────────────────────────────────────────────────────────────

func cmdRisk(e *env, args []string) error {
	fs := flag.NewFlagSet("risk", flag.ExitOnError)
	now := e.common(fs)
	noWrite := fs.Bool("no-write", false, "look without recording the verdicts")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	lim, err := e.limits()
	if err != nil {
		return err
	}
	var orders []desk.Order
	if err := e.dir.Read("orders.json", &orders); err != nil {
		return fmt.Errorf("no orders: run desk size -write first (%v)", err)
	}
	led, err := e.ledger(lim)
	if err != nil {
		return err
	}
	var syms []string
	for _, o := range orders {
		in := desk.ParseInstrument(o.Symbol)
		syms = append(syms, in.Symbol)
		if in.Option {
			syms = append(syms, in.Underlying)
		}
	}
	qs, err := e.quotes(syms)
	if err != nil {
		e.note("no quotes: %v", err)
		qs = map[string]desk.Quote{}
	}
	m := desk.Market{Now: e.now, Cal: e.calendar(), Quotes: qs, Ledger: led}
	if ban, err := fyers.FetchFOBanList(); err == nil {
		m.Banned = ban
	} else {
		e.note("F&O ban list unavailable: %v", err)
	}
	if bands, err := desk.PriceBands(e.nse); err == nil {
		m.Bands = bands
	} else {
		e.note("price bands unavailable: %v", err)
	}
	vs := desk.Check(orders, lim, m)
	if !*noWrite {
		if err := e.dir.Write("risk.json", vs); err != nil {
			return err
		}
	}
	if e.jsonOut {
		e.emit(vs)
		return nil
	}
	for i, v := range vs {
		o := orders[i]
		head := "PASS"
		if !v.Pass {
			head = "BLOCK"
		}
		fmt.Printf("%s  #%d %s %s %d %s LIMIT ₹%.2f%s\n", head, i+1, o.ID, strings.ToUpper(o.Side), o.Qty, o.Symbol, o.Limit, yn(v.Exit, " (exit)", ""))
		for _, b := range v.Blocks {
			fmt.Println("   ✗ " + b)
		}
		for _, w := range v.Warnings {
			fmt.Println("   ! " + w)
		}
	}
	t := led.Today(e.now)
	fmt.Printf("\nToday: %d entries of %d · realised %s · open risk at the stops %s · daily loss limit %s\n",
		t.Entries, lim.MaxTradesPerDay, desk.Rupees(t.Realized), desk.Rupees(t.OpenRisk), desk.Rupees(lim.MaxDailyLoss))
	e.printNotes()
	return nil
}
