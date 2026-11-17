package bhavcopy

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"time"
)

// NSE's equity (CM) bhavcopy: each stock's official open/high/low/close for
// a session, at the price it actually traded that day — never adjusted for
// later splits or bonuses. That is the truth a backtest prices against.
const (
	cmURLFmt    = "https://nsearchives.nseindia.com/content/cm/BhavCopy_NSE_CM_0_0_0_%s_F_0000.csv.zip"
	cmOldURLFmt = "https://nsearchives.nseindia.com/content/historical/EQUITIES/%s/%s/cm%sbhav.csv.zip"
)

// CMRow is one EQ-series stock's session.
type CMRow struct {
	Open, High, Low, Close, Volume float64
	ISIN                           string
}

// CMDay is one session's EQ stocks, keyed by the symbol they traded under
// that day.
type CMDay struct {
	Date time.Time
	Rows map[string]CMRow
}

// FetchCM returns the equity bhavcopy for date (cached in cacheDir when not
// ""). ErrNotPublished means no session that day.
func (c *Client) FetchCM(date time.Time, cacheDir string) (*CMDay, error) {
	url := fmt.Sprintf(cmURLFmt, date.Format("20060102"))
	if !isUDiFF(date) {
		url = fmt.Sprintf(cmOldURLFmt, date.Format("2006"), strings.ToUpper(date.Format("Jan")), strings.ToUpper(date.Format("02Jan2006")))
	}
	data, err := c.cached(url, cacheDir, "cm_"+date.Format("20060102")+".csv.zip")
	if err != nil {
		return nil, err
	}
	r, err := firstCSV(data)
	if err != nil {
		return nil, fmt.Errorf("equity bhavcopy %s: %w", date.Format("2006-01-02"), err)
	}
	defer r.Close()
	day, err := ParseCM(r)
	if err != nil {
		return nil, fmt.Errorf("equity bhavcopy %s: %w", date.Format("2006-01-02"), err)
	}
	day.Date = date
	return day, nil
}

// ParseCM reads either equity bhavcopy format (detected from the header),
// keeping the EQ series.
func ParseCM(r io.Reader) (*CMDay, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	names := map[string]string{ // field → UDiFF column
		"sym": "TckrSymb", "series": "SctySrs", "open": "OpnPric", "high": "HghPric", "low": "LwPric",
		"close": "ClsPric", "vol": "TtlTradgVol", "isin": "ISIN",
	}
	if _, udiff := col["TckrSymb"]; !udiff {
		names = map[string]string{
			"sym": "SYMBOL", "series": "SERIES", "open": "OPEN", "high": "HIGH", "low": "LOW",
			"close": "CLOSE", "vol": "TOTTRDQTY", "isin": "ISIN",
		}
	}
	for _, n := range names {
		if _, ok := col[n]; !ok {
			return nil, fmt.Errorf("column %s missing — format change?", n)
		}
	}
	get := func(rec []string, f string) string {
		if i := col[names[f]]; i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	day := &CMDay{Rows: map[string]CMRow{}}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil || get(rec, "series") != "EQ" {
			continue
		}
		row := CMRow{Open: num(get(rec, "open")), High: num(get(rec, "high")), Low: num(get(rec, "low")),
			Close: num(get(rec, "close")), Volume: num(get(rec, "vol")), ISIN: get(rec, "isin")}
		if row.Close > 0 {
			day.Rows[get(rec, "sym")] = row
		}
	}
	if len(day.Rows) == 0 {
		return nil, fmt.Errorf("no EQ rows — format change?")
	}
	return day, nil
}
