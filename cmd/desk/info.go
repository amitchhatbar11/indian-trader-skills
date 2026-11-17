package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
)

// seen is the "already shown" set for news and deals (~/.nse-desk/state/).
type seen struct {
	e    *env
	name string
	m    map[string]time.Time
}

func (e *env) seenSet(name string) *seen {
	s := &seen{e: e, name: "state/" + name + ".json", m: map[string]time.Time{}}
	e.dir.Read(s.name, &s.m)
	return s
}

func (s *seen) has(k string) bool { _, ok := s.m[k]; return ok }

func (s *seen) add(k string, t time.Time) { s.m[k] = t }

func (s *seen) save() error {
	cut := time.Now().AddDate(0, 0, -60) // forget what is 60 days old
	for k, t := range s.m {
		if t.Before(cut) {
			delete(s.m, k)
		}
	}
	return s.e.dir.Write(s.name, s.m)
}

// symbolsOr returns the given symbols, else the watchlist's and the paper
// holdings' underlyings.
func (e *env) symbolsOr(pos []string, n int) []string {
	syms := upper(pos)
	if len(syms) > 0 {
		return syms
	}
	add := map[string]bool{}
	var wl watchlist
	if e.dir.Read("watchlist.json", &wl) == nil {
		for i, p := range wl.Picks {
			if i < n {
				add[p.Symbol] = true
			}
		}
	}
	for s := range e.holdings() {
		add[s] = true
	}
	for s := range add {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	return syms
}

// holdings are the underlyings held in the paper account, plus any listed
// in ~/.nse-desk/holdings.txt (one symbol a line: your real portfolio, if
// you want deals and news flagged for it).
func (e *env) holdings() map[string]bool {
	out := map[string]bool{}
	var led desk.Ledger
	if e.dir.Read("paper.json", &led) == nil {
		for _, p := range led.Positions {
			out[desk.ParseInstrument(p.Symbol).Underlying] = true
		}
	}
	if data, err := os.ReadFile(e.dir.Path("holdings.txt")); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if s := strings.ToUpper(strings.TrimSpace(l)); s != "" && !strings.HasPrefix(s, "#") {
				out[s] = true
			}
		}
	}
	return out
}

// ── news ────────────────────────────────────────────────────────────────────

func cmdNews(e *env, args []string) error {
	fs := flag.NewFlagSet("news", flag.ExitOnError)
	now := e.common(fs)
	days := fs.Int("days", 3, "look back this many days")
	all := fs.Bool("all", false, "show items already shown before too")
	noWeb := fs.Bool("no-web", false, "NSE announcements only (skip Google News)")
	n := fs.Int("watch", 10, "with no symbols: the top N of the watchlist plus holdings")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	syms := e.symbolsOr(pos, *n)
	if len(syms) == 0 {
		return fmt.Errorf("no symbols: give some, or run desk scan first")
	}
	st := e.seenSet("news_seen")
	from := e.now.AddDate(0, 0, -*days)
	var items []desk.NewsItem
	for _, s := range syms {
		ann, err := desk.Announcements(e.nse, s, from, e.now)
		if err != nil {
			e.note("%s NSE announcements: %v", s, err)
		}
		items = append(items, ann...)
		if !*noWeb {
			web, err := desk.GoogleNews(e.nse, s, s+" share NSE")
			if err != nil {
				e.note("%s news: %v", s, err)
			}
			for _, it := range web {
				if it.Time.After(from) {
					items = append(items, it)
				}
			}
		}
	}
	qs, _ := e.quotes(syms)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Time.After(items[j].Time) })
	var show []desk.NewsItem
	for _, it := range items {
		if *all || !st.has(it.Key) {
			show = append(show, it)
		}
		st.add(it.Key, e.now)
	}
	if err := st.save(); err != nil {
		return err
	}
	if e.jsonOut {
		e.emit(map[string]any{"items": show, "quotes": qs})
		return nil
	}
	fmt.Printf("News for %s since %s — %d new\n", strings.Join(syms, ", "), from.Format("02 Jan"), len(show))
	for _, s := range syms {
		if q, ok := qs[s]; ok {
			fmt.Printf("  %s ₹%.2f (%+.2f%% vs previous close)\n", s, q.LTP, q.ChangePct())
		}
	}
	fmt.Println()
	for _, it := range show {
		fmt.Printf("%s  %-10s [%s] %s · %s\n", it.Time.Format("02 Jan 15:04"), it.Symbol, strings.Join(it.Tags, ","), it.Title, it.Source)
		if it.Detail != "" {
			d := it.Detail
			if len(d) > 220 {
				d = d[:220] + "…"
			}
			fmt.Println("      " + d)
		}
		if it.URL != "" && it.Source == "NSE" { // the filing itself; news links are long redirects (see -json)
			fmt.Println("      " + it.URL)
		}
	}
	e.printNotes()
	return nil
}

// ── results ─────────────────────────────────────────────────────────────────

func cmdResults(e *env, args []string) error {
	fs := flag.NewFlagSet("results", flag.ExitOnError)
	now := e.common(fs)
	n := fs.Int("n", 5, "quarters to show")
	standalone := fs.Bool("standalone", false, "standalone numbers instead of consolidated")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	syms := upper(pos)
	if len(syms) == 0 {
		return fmt.Errorf("give a symbol")
	}
	for _, sym := range syms {
		if err := results(e, sym, *n, *standalone); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", sym, err)
		}
	}
	e.printNotes()
	return nil
}

func results(e *env, sym string, n int, standalone bool) error {
	fs, err := desk.Filings(e.nse, sym)
	if err != nil {
		return err
	}
	// Filings comes back latest-per-quarter, newest first.
	var list []desk.Filing
	for _, f := range fs {
		if f.Consolidated != standalone {
			list = append(list, f)
		}
	}
	if len(list) == 0 && !standalone {
		return results(e, sym, n, true) // no consolidated accounts: use standalone
	}
	get := func(f desk.Filing) (desk.Quarter, error) {
		data, err := e.nse.Get(f.XBRL, 0) // a filing never changes
		if err != nil {
			return desk.Quarter{}, err
		}
		q, err := desk.ParseXBRL(data, f.From, f.To)
		q.Filed, q.Consolidated, q.Source = f.Filed.Format("02-Jan-2006"), f.Consolidated, f.XBRL
		return q, err
	}
	var qs []desk.Quarter
	idx := map[string]int{}
	for i, f := range list {
		if i >= n+4 { // the year-ago quarters too
			break
		}
		q, err := get(f)
		if err != nil {
			e.note("%s %s: %v", sym, f.To.Format("Jan 2006"), err)
			continue
		}
		idx[q.To.Format("2006-01")] = len(qs)
		qs = append(qs, q)
	}
	if len(qs) == 0 {
		return fmt.Errorf("no readable quarterly results")
	}
	type row struct {
		Q        desk.Quarter `json:"quarter"`
		RevYoY   *float64     `json:"revenue_yoy_pct,omitempty"`
		PATYoY   *float64     `json:"pat_yoy_pct,omitempty"`
		OpMargin float64      `json:"operating_margin_pct"`
		NetMarg  float64      `json:"net_margin_pct"`
	}
	var rows []row
	for i, q := range qs {
		if i >= n {
			break
		}
		r := row{Q: q, OpMargin: round1(q.OperatingMargin()), NetMarg: round1(q.NetMargin())}
		if j, ok := idx[q.To.AddDate(-1, 0, 0).Format("2006-01")]; ok {
			y := qs[j]
			if y.Revenue != 0 {
				v := round1((q.Revenue/y.Revenue - 1) * 100)
				r.RevYoY = &v
			}
			if y.PAT != 0 {
				v := round1((q.PAT/math.Abs(y.PAT) - y.PAT/math.Abs(y.PAT)) * 100)
				r.PATYoY = &v
			}
		}
		rows = append(rows, r)
	}
	bms, _ := desk.BoardMeetings(e.nse, sym)
	bms = desk.UpcomingResults(bms, e.now)
	if e.jsonOut {
		e.emit(map[string]any{"symbol": sym, "quarters": rows, "board_meetings": bms})
		return nil
	}
	kind := "consolidated"
	if !qs[0].Consolidated {
		kind = "standalone"
	}
	revenue, opm := "revenue", "operating margin"
	if qs[0].Bank {
		revenue, opm = "interest earned", "—"
		kind += ", bank: revenue is interest earned"
	}
	fmt.Printf("\n%s — quarterly results (%s, ₹ crore) from NSE filings\n", sym, kind)
	w := tw()
	fmt.Fprintf(w, "quarter\t%s\tYoY\t%s\tPAT\tYoY\tnet margin\tEPS ₹\tfiled\n", revenue, opm)
	for _, r := range rows {
		yoy := func(p *float64) string {
			if p == nil {
				return "—"
			}
			return fmt.Sprintf("%+.1f%%", *p)
		}
		op := fmt.Sprintf("%.1f%%", r.OpMargin)
		if r.Q.Bank {
			op = "—"
		}
		fmt.Fprintf(w, "%s\t%.0f\t%s\t%s\t%.0f\t%s\t%.1f%%\t%.2f\t%s\n", r.Q.To.Format("Jan 2006"), r.Q.Revenue, yoy(r.RevYoY),
			op, r.Q.PAT, yoy(r.PATYoY), r.NetMarg, r.Q.EPS, strings.SplitN(r.Q.Filed, " ", 2)[0])
	}
	w.Flush()
	fmt.Println("EPS is as filed: a split or bonus since makes older EPS look higher.")
	for _, b := range bms {
		if strings.EqualFold(b.Symbol, sym) {
			fmt.Printf("Next results: board meeting on %s (%s)\n", b.Date, b.Purpose)
		}
	}
	return nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// ── deals ───────────────────────────────────────────────────────────────────

func cmdDeals(e *env, args []string) error {
	fs := flag.NewFlagSet("deals", flag.ExitOnError)
	now := e.common(fs)
	minCr := fs.Float64("min-cr", 5, "only deals worth at least ₹ this many crore")
	insider := fs.Bool("insider", true, "include SEBI PIT insider disclosures")
	days := fs.Int("days", 7, "insider disclosures from the last N days")
	all := fs.Bool("all", false, "show deals already shown before too")
	churn := fs.Bool("churn", false, "also show clients that bought and sold the same stock the same day (netted to a small position)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	filter := map[string]bool{}
	for _, s := range upper(pos) {
		filter[s] = true
	}
	ds, errs := desk.Deals(e.nse)
	for _, er := range errs {
		e.note("%s", er)
	}
	if *insider {
		in, err := desk.Insider(e.nse, e.now.AddDate(0, 0, -*days), e.now)
		switch {
		case err != nil:
			e.note("insider disclosures unavailable: %v", err)
		case len(in) == 0:
			e.note("NSE returned no insider (PIT) disclosures for the last %d days — its endpoint sometimes answers empty", *days)
		}
		ds = append(ds, in...)
	}
	// Keep a dated copy so a history builds up (NSE's own history API blocks bots).
	if len(ds) > 0 {
		e.dir.Write("deals/"+e.now.Format("2006-01-02")+".json", ds)
	}
	held := e.holdings()
	st := e.seenSet("deals_seen")
	var show []desk.Deal
	hidden := 0
	for _, d := range desk.NetDeals(ds) {
		if len(filter) > 0 && !filter[d.Symbol] {
			continue
		}
		if d.Kind != "insider" && !held[d.Symbol] && (d.Value < *minCr*1e7 || d.Churn && !*churn) {
			if d.Churn {
				hidden++
			}
			continue
		}
		if *all || !st.has(d.Key) {
			show = append(show, d)
		}
		st.add(d.Key, e.now)
	}
	if err := st.save(); err != nil {
		return err
	}
	desk.SortDeals(show)
	if e.jsonOut {
		e.emit(show)
		return nil
	}
	fmt.Printf("Bulk/block deals (≥ ₹%.0f cr, buys and sells netted per client) and insider trades — %d new\n", *minCr, len(show))
	if hidden > 0 {
		fmt.Printf("(%d same-day buy-and-sell churners hidden — high-frequency trading, not positions; -churn shows them)\n", hidden)
	}
	fmt.Println()
	w := tw()
	fmt.Fprintln(w, "date\tkind\tstock\tclient\tside\tqty\tprice\tvalue ₹cr\t")
	for _, d := range show {
		flag := ""
		if held[d.Symbol] {
			flag = "← you hold this"
		}
		if d.Note != "" && d.Kind != "insider" {
			flag = strings.TrimSpace(d.Note + " " + flag)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%.0f\t%.2f\t%.1f\t%s\n", d.Date.Format("02 Jan"), d.Kind, d.Symbol, trunc(d.Client, 40), d.Side,
			d.Qty, d.Price, d.Value/1e7, flag)
	}
	w.Flush()
	e.printNotes()
	return nil
}

func trunc(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
