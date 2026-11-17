package bhavcopy

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// NSE's F&O bhavcopy lists every contract of a session — which is what a
// backtest needs to know the strike chain, expiry and lot size on a past
// day, none of which the live F&O master can tell. NSE switched formats on
// 2024-07-08: UDiFF files from then on, the older fo<DD><MON><YYYY>bhav
// files before (archived back to the 2000s).
const (
	foURLFmt    = "https://nsearchives.nseindia.com/content/fo/BhavCopy_NSE_FO_0_0_0_%s_F_0000.csv.zip"
	foOldURLFmt = "https://nsearchives.nseindia.com/content/historical/DERIVATIVES/%s/%s/fo%sbhav.csv.zip"
)

// UDiFFStart is the first session published in NSE's UDiFF format.
var UDiFFStart = time.Date(2024, 7, 8, 0, 0, 0, 0, time.UTC)

func isUDiFF(d time.Time) bool { return d.Format("2006-01-02") >= UDiFFStart.Format("2006-01-02") }

// FOContract is one listed stock option on a session.
type FOContract struct {
	Strike    float64
	Right     string // CE / PE
	Name      string // trading symbol, e.g. RELIANCE26NOV1440CE (built for old-format days)
	Close     float64
	Contracts float64 // contracts traded on the day (0 when the file does not say)
	OI        float64 // open interest at the close, in shares (0 when absent)
}

// FOChain is one underlying's stock options on a session.
type FOChain struct {
	Symbol string
	// LotSize is the market lot. UDiFF files state it; for older files it is
	// worked out from turnover (see estimateLot) and LotEstimated is set.
	LotSize      int
	LotEstimated bool
	// Lots is the lot by expiry date ("2006-01-02") where the file states
	// it: NSE revises lot sizes from a later contract month on, so the
	// expiries listed on one day can carry different lots. Use LotFor.
	Lots map[string]int
	// Contracts by expiry date ("2006-01-02"), each sorted by strike.
	Contracts map[string][]FOContract

	lotExp string // the expiry LotSize was taken from
}

// LotFor is the lot of the contracts expiring on e (falling back to
// LotSize).
func (c *FOChain) LotFor(e time.Time) int {
	if lot := c.Lots[e.Format("2006-01-02")]; lot > 0 {
		return lot
	}
	return c.LotSize
}

// Expiries lists the chain's expiry dates, earliest first.
func (c *FOChain) Expiries() []time.Time {
	var out []time.Time
	for k := range c.Contracts {
		if t, err := time.Parse("2006-01-02", k); err == nil {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// Nearest is the earliest expiry on or after day — the contract month the
// live scanner trades (it keeps the nearest expiry not yet past).
func (c *FOChain) Nearest(day time.Time) (time.Time, bool) {
	d := day.Format("2006-01-02")
	for _, e := range c.Expiries() {
		if e.Format("2006-01-02") >= d {
			return e, true
		}
	}
	return time.Time{}, false
}

// FODay is every stock-option chain of one session, keyed by the symbol the
// stock traded under that day.
type FODay struct {
	Date   time.Time
	Chains map[string]*FOChain
}

// FetchFO returns the F&O bhavcopy for date, from cacheDir when it has it
// ("" = no cache). ErrNotPublished means no session that day.
func (c *Client) FetchFO(date time.Time, cacheDir string) (*FODay, error) {
	url := fmt.Sprintf(foURLFmt, date.Format("20060102"))
	if !isUDiFF(date) {
		url = fmt.Sprintf(foOldURLFmt, date.Format("2006"), strings.ToUpper(date.Format("Jan")), strings.ToUpper(date.Format("02Jan2006")))
	}
	data, err := c.cached(url, cacheDir, "fo_"+date.Format("20060102")+".csv.zip")
	if err != nil {
		return nil, err
	}
	r, err := firstCSV(data)
	if err != nil {
		return nil, fmt.Errorf("F&O bhavcopy %s: %w", date.Format("2006-01-02"), err)
	}
	defer r.Close()
	var day *FODay
	if isUDiFF(date) {
		day, err = ParseFO(r)
	} else {
		day, err = ParseFOOld(r)
	}
	if err != nil {
		return nil, err
	}
	day.Date = date
	return day, nil
}

// EnsureFO makes sure date's F&O bhavcopy is in cacheDir without parsing it.
// downloaded reports a fresh fetch (false = already there).
func (c *Client) EnsureFO(date time.Time, cacheDir string) (downloaded bool, err error) {
	url := fmt.Sprintf(foURLFmt, date.Format("20060102"))
	if !isUDiFF(date) {
		url = fmt.Sprintf(foOldURLFmt, date.Format("2006"), strings.ToUpper(date.Format("Jan")), strings.ToUpper(date.Format("02Jan2006")))
	}
	return c.ensure(url, cacheDir, "fo_"+date.Format("20060102")+".csv.zip")
}

// EnsureCM is EnsureFO for the equity bhavcopy.
func (c *Client) EnsureCM(date time.Time, cacheDir string) (downloaded bool, err error) {
	url := fmt.Sprintf(cmURLFmt, date.Format("20060102"))
	if !isUDiFF(date) {
		url = fmt.Sprintf(cmOldURLFmt, date.Format("2006"), strings.ToUpper(date.Format("Jan")), strings.ToUpper(date.Format("02Jan2006")))
	}
	return c.ensure(url, cacheDir, "cm_"+date.Format("20060102")+".csv.zip")
}

func (c *Client) ensure(url, cacheDir, name string) (bool, error) {
	path := filepath.Join(cacheDir, name)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	if _, err := os.Stat(path + ".none"); err == nil {
		return false, ErrNotPublished
	}
	_, err := c.cached(url, cacheDir, name)
	return err == nil, err
}

// cached returns url's body, from cacheDir/name when present. A download is
// saved there and followed by the client's Pause, to stay polite to NSE.
func (c *Client) cached(url, cacheDir, name string) ([]byte, error) {
	path := ""
	if cacheDir != "" {
		path = filepath.Join(cacheDir, name)
		if data, err := os.ReadFile(path); err == nil {
			return data, nil
		}
		if _, err := os.Stat(path + ".none"); err == nil {
			return nil, ErrNotPublished // a known holiday
		}
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Referer", "https://www.nseindia.com/")
	resp, err := c.HTTP.Do(req)
	if c.Pause > 0 {
		defer time.Sleep(c.Pause)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		if path != "" && os.MkdirAll(cacheDir, 0o755) == nil {
			os.WriteFile(path+".none", nil, 0o644)
		}
		return nil, ErrNotPublished
	default:
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err != nil {
		return nil, fmt.Errorf("%s: not a zip archive", url)
	}
	if path != "" && os.MkdirAll(cacheDir, 0o755) == nil {
		os.WriteFile(path, data, 0o644)
	}
	return data, nil
}

func firstCSV(data []byte) (io.ReadCloser, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("no CSV in the archive")
}

// columns maps a CSV header to column indexes, checking the needed ones.
func columns(cr *csv.Reader, need ...string) (func([]string, string) string, error) {
	head, err := cr.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	for _, n := range need {
		if _, ok := col[n]; !ok {
			return nil, fmt.Errorf("column %s missing — format change?", n)
		}
	}
	return func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}, nil
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// ParseFO reads the UDiFF F&O CSV, keeping stock options (FinInstrmTp STO).
func ParseFO(r io.Reader) (*FODay, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	get, err := columns(cr, "FinInstrmTp", "TckrSymb", "XpryDt", "StrkPric", "OptnTp", "FinInstrmNm", "ClsPric", "TtlTradgVol", "NewBrdLotQty")
	if err != nil {
		return nil, fmt.Errorf("F&O bhavcopy: %w", err)
	}
	day := &FODay{Chains: map[string]*FOChain{}}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil || get(rec, "FinInstrmTp") != "STO" {
			continue
		}
		right := get(rec, "OptnTp")
		strike := num(get(rec, "StrkPric"))
		exp := get(rec, "XpryDt")
		if (right != "CE" && right != "PE") || strike <= 0 {
			continue
		}
		if _, err := time.Parse("2006-01-02", exp); err != nil {
			continue
		}
		ch := day.chain(get(rec, "TckrSymb"))
		if lot, err := strconv.Atoi(get(rec, "NewBrdLotQty")); err == nil && lot > 0 {
			if ch.Lots == nil {
				ch.Lots = map[string]int{}
			}
			ch.Lots[exp] = lot
			if ch.LotSize == 0 || exp < ch.lotExp {
				ch.LotSize, ch.lotExp = lot, exp // the nearest expiry's lot
			}
		}
		// TtlTradgVol counts contracts (same unit as the old format's
		// CONTRACTS: RELIANCE 3200 CE, 61,624 on 2024-07-05 old, 50,349 on
		// 2024-07-08 new).
		traded := num(get(rec, "TtlTradgVol"))
		ct := FOContract{Strike: strike, Right: right, Name: get(rec, "FinInstrmNm"), Close: num(get(rec, "ClsPric")),
			OI: num(get(rec, "OpnIntrst"))}
		ct.Contracts = traded
		ch.Contracts[exp] = append(ch.Contracts[exp], ct)
	}
	return day.finish()
}

// ParseFOOld reads the pre-July-2024 F&O CSV (fo<DD><MON><YYYY>bhav.csv),
// keeping stock options (INSTRUMENT OPTSTK). That format has no lot size, so
// it is worked out from turnover (estimateLot).
func ParseFOOld(r io.Reader) (*FODay, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	get, err := columns(cr, "INSTRUMENT", "SYMBOL", "EXPIRY_DT", "STRIKE_PR", "OPTION_TYP", "HIGH", "CLOSE", "CONTRACTS", "VAL_INLAKH")
	if err != nil {
		return nil, fmt.Errorf("F&O bhavcopy (old format): %w", err)
	}
	day := &FODay{Chains: map[string]*FOChain{}}
	lotLow := map[string]float64{} // tightest lower bound on each symbol's lot
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil || get(rec, "INSTRUMENT") != "OPTSTK" {
			continue
		}
		right := get(rec, "OPTION_TYP")
		strike := num(get(rec, "STRIKE_PR"))
		exp, err := time.Parse("02-Jan-2006", get(rec, "EXPIRY_DT"))
		if (right != "CE" && right != "PE") || strike <= 0 || err != nil {
			continue
		}
		sym := get(rec, "SYMBOL")
		ch := day.chain(sym)
		contracts := num(get(rec, "CONTRACTS"))
		key := exp.Format("2006-01-02")
		ch.Contracts[key] = append(ch.Contracts[key], FOContract{
			Strike: strike, Right: right, Close: num(get(rec, "CLOSE")), Contracts: contracts, OI: num(get(rec, "OPEN_INT")),
			Name: sym + strings.ToUpper(exp.Format("06Jan02")) + strconv.FormatFloat(strike, 'f', -1, 64) + right,
		})
		// Option turnover is notional: Σ qty × (strike + premium). With the
		// day's high premium that gives a lower bound on qty per contract.
		if hi, val := num(get(rec, "HIGH")), num(get(rec, "VAL_INLAKH")); contracts > 0 && hi > 0 && val > 0 {
			if b := val * 1e5 / (contracts * (strike + hi)); b > lotLow[sym] {
				lotLow[sym] = b
			}
		}
	}
	for sym, ch := range day.Chains {
		ch.LotSize, ch.LotEstimated = estimateLot(lotLow[sym]), true
	}
	return day.finish()
}

// estimateLot turns the tightest turnover bound into a lot size. NSE rounds
// turnover to ₹1,000, so the bound sits a hair under the true lot; checked
// against the stated lots of 2024-07-08, this is exact for 179 of 181 stocks
// and the other two are off by one from a round lot, which the snap fixes.
func estimateLot(lowerBound float64) int {
	if lowerBound <= 0 {
		return 0
	}
	lot := int(math.Ceil(lowerBound - 0.02))
	if r := int(math.Round(float64(lot)/25)) * 25; r > 0 && absInt(lot-r) <= 2 {
		lot = r
	}
	return lot
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (d *FODay) chain(sym string) *FOChain {
	ch := d.Chains[sym]
	if ch == nil {
		ch = &FOChain{Symbol: sym, Contracts: map[string][]FOContract{}}
		d.Chains[sym] = ch
	}
	return ch
}

func (d *FODay) finish() (*FODay, error) {
	if len(d.Chains) == 0 {
		return nil, fmt.Errorf("F&O bhavcopy: no stock options found — format change?")
	}
	for _, ch := range d.Chains {
		for k := range ch.Contracts {
			cs := ch.Contracts[k]
			sort.Slice(cs, func(i, j int) bool {
				if cs[i].Strike != cs[j].Strike {
					return cs[i].Strike < cs[j].Strike
				}
				return cs[i].Right < cs[j].Right
			})
		}
	}
	return d, nil
}
