// Package corpact is the record of what changed a stock's identity or price
// scale over time: NSE symbol changes (ZOMATO → ETERNAL) and corporate
// actions (bonus, split, consolidation, rights, demerger).
//
// A backtest needs both. Old sessions list a stock under the name it had
// then, and a past price is only comparable with today's after the splits
// and bonuses in between are applied. Sources are NSE's own public files:
// the symbol-change list and the corporate-actions API, cached on disk.
package corpact

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Action kinds.
const (
	Bonus         = "BONUS"
	Split         = "SPLIT"
	Consolidation = "CONSOLIDATION"
	Rights        = "RIGHTS"
	Demerger      = "DEMERGER" // also schemes of arrangement and capital reductions
)

// Action is one corporate action on a stock.
type Action struct {
	Symbol  string // canonical (current) symbol
	Listed  string // symbol as NSE listed the action
	ExDate  time.Time
	Kind    string
	Subject string
	// Factor scales a price from before ExDate into post-ExDate terms
	// (0.5 for a 1:1 bonus or a ₹10→₹5 split). 0 = the exchange's adjustment
	// is not a fixed ratio (rights, demerger): recorded, not applied.
	Factor float64
}

// Rename is one NSE symbol change; New trades from Date on.
type Rename struct {
	Old, New string
	Date     time.Time
	Company  string
}

// Registry holds the actions and renames.
type Registry struct {
	Renames []Rename
	actions map[string][]Action // canonical symbol → by ExDate
	byOld   map[string][]Rename
}

// New builds a registry from parsed records.
func New(actions []Action, renames []Rename) *Registry {
	r := &Registry{Renames: renames, actions: map[string][]Action{}, byOld: map[string][]Rename{}}
	sort.Slice(r.Renames, func(i, j int) bool { return r.Renames[i].Date.Before(r.Renames[j].Date) })
	for _, rn := range r.Renames {
		r.byOld[rn.Old] = append(r.byOld[rn.Old], rn)
	}
	seen := map[string]bool{}
	for _, a := range actions {
		a.Symbol = r.Current(a.Listed, a.ExDate.AddDate(0, 0, -1))
		key := a.Symbol + "|" + a.ExDate.Format("2006-01-02") + "|" + a.Kind
		if seen[key] {
			continue
		}
		seen[key] = true
		r.actions[a.Symbol] = append(r.actions[a.Symbol], a)
	}
	for _, as := range r.actions {
		sort.Slice(as, func(i, j int) bool { return as[i].ExDate.Before(as[j].ExDate) })
	}
	return r
}

// Current is the symbol a stock listed as sym on asOf trades under today,
// following every later rename.
func (r *Registry) Current(sym string, asOf time.Time) string {
	if r == nil {
		return sym
	}
	day := asOf.Format("2006-01-02")
	for hops := 0; hops < 10; hops++ {
		next := ""
		for _, rn := range r.byOld[sym] {
			if d := rn.Date.Format("2006-01-02"); d > day {
				next, day = rn.New, d
				break
			}
		}
		if next == "" || next == sym {
			return sym
		}
		sym = next
	}
	return sym
}

// Factor is the product of the price factors of sym's actions with an
// ex-date in (from, to]: it turns a price from date from into date to's
// terms.
func (r *Registry) Factor(sym string, from, to time.Time) float64 {
	f := 1.0
	for _, a := range r.Between(sym, from, to) {
		if a.Factor > 0 {
			f *= a.Factor
		}
	}
	return f
}

// Between lists sym's actions with an ex-date in (from, to].
func (r *Registry) Between(sym string, from, to time.Time) []Action {
	if r == nil {
		return nil
	}
	lo, hi := from.Format("2006-01-02"), to.Format("2006-01-02")
	var out []Action
	for _, a := range r.actions[sym] {
		if d := a.ExDate.Format("2006-01-02"); d > lo && d <= hi {
			out = append(out, a)
		}
	}
	return out
}

// RenamesBetween lists the renames effective in (from, to].
func (r *Registry) RenamesBetween(from, to time.Time) []Rename {
	if r == nil {
		return nil
	}
	lo, hi := from.Format("2006-01-02"), to.Format("2006-01-02")
	var out []Rename
	for _, rn := range r.Renames {
		if d := rn.Date.Format("2006-01-02"); d > lo && d <= hi {
			out = append(out, rn)
		}
	}
	return out
}

var (
	reBonus = regexp.MustCompile(`(?i)bonus\s*(\d+)\s*:\s*(\d+)`)
	reFV    = regexp.MustCompile(`(?i)(?:r[se])\.?\s*([\d.]+)\D+?(?:r[se])\.?\s*([\d.]+)`)
	reSplit = regexp.MustCompile(`(?i)split|splt|sub-?division`)
	reCons  = regexp.MustCompile(`(?i)consolidat`)
	reRight = regexp.MustCompile(`(?i)\brights?\b`)
	reDemer = regexp.MustCompile(`(?i)demerg|scheme of arrangement|capital reduction|reduction of capital`)
)

// ParseSubject classifies an NSE corporate-action subject. kind is "" for
// actions that do not change the price scale (dividends, buybacks,
// meetings).
func ParseSubject(s string) (kind string, factor float64) {
	if m := reBonus.FindStringSubmatch(s); m != nil {
		a, _ := strconv.ParseFloat(m[1], 64)
		b, _ := strconv.ParseFloat(m[2], 64)
		if a > 0 && b > 0 {
			return Bonus, b / (a + b) // a new shares for every b held
		}
	}
	for _, k := range []struct {
		re   *regexp.Regexp
		kind string
	}{{reSplit, Split}, {reCons, Consolidation}} {
		if !k.re.MatchString(s) {
			continue
		}
		if m := reFV.FindStringSubmatch(s); m != nil {
			from, _ := strconv.ParseFloat(strings.TrimRight(m[1], "."), 64)
			to, _ := strconv.ParseFloat(strings.TrimRight(m[2], "."), 64)
			if from > 0 && to > 0 && from != to {
				return k.kind, to / from
			}
		}
		return k.kind, 0
	}
	switch {
	case reDemer.MatchString(s):
		return Demerger, 0
	case reRight.MatchString(s):
		return Rights, 0
	}
	return "", 0
}

// ParseActions reads the NSE corporate-actions API response, keeping the
// actions that change a price's scale or need recording.
func ParseActions(data []byte) ([]Action, error) {
	var rows []struct {
		Symbol  string `json:"symbol"`
		Series  string `json:"series"`
		Subject string `json:"subject"`
		ExDate  string `json:"exDate"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("corporate actions: %w", err)
	}
	var out []Action
	for _, r := range rows {
		if r.Series != "" && r.Series != "EQ" {
			continue
		}
		kind, f := ParseSubject(r.Subject)
		ex, err := time.Parse("02-Jan-2006", strings.TrimSpace(r.ExDate))
		if kind == "" || err != nil {
			continue
		}
		out = append(out, Action{Listed: strings.TrimSpace(r.Symbol), ExDate: ex, Kind: kind,
			Subject: strings.TrimSpace(r.Subject), Factor: f})
	}
	return out, nil
}

// ParseRenames reads NSE's symbolchange.csv.
func ParseRenames(r io.Reader) ([]Rename, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	recs, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("symbol changes: %w", err)
	}
	var out []Rename
	for i, rec := range recs {
		if i == 0 || len(rec) < 4 {
			continue
		}
		d, err := time.Parse("02-Jan-2006", strings.TrimSpace(rec[3]))
		if err != nil {
			continue
		}
		old, nw := strings.TrimSpace(rec[1]), strings.TrimSpace(rec[2])
		if old != "" && nw != "" && old != nw {
			out = append(out, Rename{Old: old, New: nw, Date: d, Company: strings.TrimSpace(rec[0])})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("symbol changes: nothing parsed — format change?")
	}
	return out, nil
}

const (
	renamesURL = "https://nsearchives.nseindia.com/content/equities/symbolchange.csv"
	actionsURL = "https://www.nseindia.com/api/corporates-corporateActions?index=equities&from_date=01-01-%d&to_date=31-12-%d"
	browserUA  = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

// Load builds the registry for fromYear..toYear from NSE, caching every file
// in dir. Past years are fetched once; the current year and the rename list
// are refreshed when a day (a week for renames) old. A failed refresh falls
// back to the cached copy.
func Load(dir string, fromYear, toYear int) (*Registry, error) {
	hc := &http.Client{Timeout: 90 * time.Second}
	thisYear := time.Now().Year()
	get := func(url, name string, maxAge time.Duration) ([]byte, error) {
		path := filepath.Join(dir, name)
		fi, statErr := os.Stat(path)
		if statErr == nil && (maxAge == 0 || time.Since(fi.ModTime()) < maxAge) {
			return os.ReadFile(path)
		}
		data, err := fetch(hc, url)
		if err != nil {
			if statErr == nil {
				return os.ReadFile(path) // stale beats nothing
			}
			return nil, err
		}
		if os.MkdirAll(dir, 0o755) == nil {
			os.WriteFile(path, data, 0o644)
		}
		time.Sleep(time.Second) // polite to NSE between fresh fetches
		return data, nil
	}

	data, err := get(renamesURL, "symbolchange.csv", 7*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("NSE symbol changes: %w", err)
	}
	renames, err := ParseRenames(strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	var actions []Action
	for y := fromYear; y <= toYear; y++ {
		maxAge := time.Duration(0)
		if y >= thisYear-1 {
			maxAge = 24 * time.Hour // late filings for recent years
		}
		data, err := get(fmt.Sprintf(actionsURL, y, y), fmt.Sprintf("actions_%d.json", y), maxAge)
		if err != nil {
			return nil, fmt.Errorf("NSE corporate actions %d: %w", y, err)
		}
		as, err := ParseActions(data)
		if err != nil {
			return nil, fmt.Errorf("%d: %w", y, err)
		}
		actions = append(actions, as...)
	}
	return New(actions, renames), nil
}

func fetch(hc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "application/json,text/csv,*/*")
	req.Header.Set("Referer", "https://www.nseindia.com/companies-listing/corporate-filings-actions")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Describe is a one-line account of an action for the backtest record.
func (a Action) Describe() string {
	switch {
	case a.Factor > 0:
		return fmt.Sprintf("%s (%s) — earlier prices ×%s", a.Kind, a.Subject, trimFloat(a.Factor))
	default:
		return fmt.Sprintf("%s (%s) — no fixed ratio; prices across it not adjusted", a.Kind, a.Subject)
	}
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(math.Round(f*1e6)/1e6, 'f', -1, 64)
}
