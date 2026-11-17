// Package bhavcopy downloads and parses NSE's public end-of-day "full"
// bhavcopy (sec_bhavdata_full_DDMMYYYY.csv). It needs no credentials — the
// archive is public — so it is the data source for the public money-flux
// site, kept entirely separate from the authenticated Fyers path.
package bhavcopy

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// urlFmt is the NSE archive path; the date is DDMMYYYY.
const urlFmt = "https://nsearchives.nseindia.com/products/content/sec_bhavdata_full_%s.csv"

// browserUA is required: the archive sits behind a CDN that 403s default
// library user agents. See [[fyers-api-needs-browser-ua]] — same cause.
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// ErrNotPublished means the archive has no file for that date — a weekend,
// a holiday, or a session that has not settled yet. Callers walk back a day.
var ErrNotPublished = errors.New("bhavcopy not published for this date")

// Row is one EQ-series stock's settled numbers for a session.
type Row struct {
	Symbol    string
	PrevClose float64
	Open      float64
	Close     float64
	Volume    float64
}

// Day is one trading session, its EQ rows keyed by symbol.
type Day struct {
	Date time.Time
	Rows map[string]Row
}

// Client fetches bhavcopy files. The zero value is not usable; call New.
type Client struct {
	HTTP *http.Client
	// Pause is slept between requests in History so a full walk stays polite.
	Pause time.Duration
}

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Pause: 300 * time.Millisecond}
}

// Fetch downloads and parses one day. want filters to those symbols (nil = all).
func (c *Client) Fetch(date time.Time, want map[string]bool) (*Day, error) {
	url := fmt.Sprintf(urlFmt, date.Format("02012006"))
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Referer", "https://www.nseindia.com/")
	req.Header.Set("Accept", "text/csv,*/*")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNotPublished
	default:
		return nil, fmt.Errorf("bhavcopy %s: HTTP %d", url, resp.StatusCode)
	}

	day, err := Parse(resp.Body, want)
	if err != nil {
		return nil, err
	}
	day.Date = date
	return day, nil
}

// Parse reads the sec_bhavdata_full CSV. Its header names and cell values
// carry leading spaces, so every field is trimmed before use.
func Parse(r io.Reader, want map[string]bool) (*Day, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	records, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, errors.New("bhavcopy has no data rows")
	}

	col := map[string]int{}
	for i, h := range records[0] {
		col[strings.TrimSpace(h)] = i
	}
	for _, n := range []string{"SYMBOL", "SERIES", "PREV_CLOSE", "OPEN_PRICE", "CLOSE_PRICE", "TTL_TRD_QNTY"} {
		if _, ok := col[n]; !ok {
			return nil, fmt.Errorf("bhavcopy missing column %q", n)
		}
	}
	field := func(rec []string, name string) string {
		i := col[name]
		if i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	day := &Day{Rows: make(map[string]Row)}
	for _, rec := range records[1:] {
		if field(rec, "SERIES") != "EQ" {
			continue
		}
		sym := field(rec, "SYMBOL")
		if sym == "" || (want != nil && !want[sym]) {
			continue
		}
		pc, e1 := strconv.ParseFloat(field(rec, "PREV_CLOSE"), 64)
		op, e2 := strconv.ParseFloat(field(rec, "OPEN_PRICE"), 64)
		cl, e3 := strconv.ParseFloat(field(rec, "CLOSE_PRICE"), 64)
		vol, e4 := strconv.ParseFloat(field(rec, "TTL_TRD_QNTY"), 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			continue // a suspended or non-numeric row — skip, don't fail the day
		}
		day.Rows[sym] = Row{Symbol: sym, PrevClose: pc, Open: op, Close: cl, Volume: vol}
	}
	return day, nil
}

// History walks back from `from`, collecting up to n published trading days,
// and returns them oldest-first. Weekend dates are skipped without a request;
// weekday holidays cost one 404 each. It gives up after a bounded number of
// calendar days so a long market break cannot loop forever.
func (c *Client) History(from time.Time, n int, want map[string]bool) ([]*Day, error) {
	days := make([]*Day, 0, n)
	d := from
	for attempts := 0; attempts < n*2+25 && len(days) < n; attempts++ {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			day, err := c.Fetch(d, want)
			switch {
			case err == nil:
				days = append(days, day)
			case errors.Is(err, ErrNotPublished):
				// holiday or not-yet-settled — just move on
			default:
				return nil, fmt.Errorf("fetch %s: %w", d.Format("2006-01-02"), err)
			}
			time.Sleep(c.Pause)
		}
		d = d.AddDate(0, 0, -1)
	}
	if len(days) < n {
		return days, fmt.Errorf("found only %d of %d trading days walking back from %s",
			len(days), n, from.Format("2006-01-02"))
	}
	for i, j := 0, len(days)-1; i < j; i, j = i+1, j-1 {
		days[i], days[j] = days[j], days[i]
	}
	return days, nil
}
