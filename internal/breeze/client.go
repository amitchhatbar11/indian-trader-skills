// Package breeze is a read-only client for ICICI Direct's Breeze API, used
// for historical candles (stocks and stock options, including expired
// contracts) to backtest the strategy.
//
// It has no order, position or funds methods, and must never get any: the
// trading path is Fyers-only, and this package exists for research.
//
// Auth: log in once a day at LoginURL(apiKey) in a browser; the redirect
// carries an "apisession" value. NewSession trades that (with the API key)
// for the session token every data call sends. Sessions last a day.
//
// Limits: Breeze allows ~100 calls a minute and ~5000 a day, so every call
// goes through a throttle, and finished days are cached on disk — a re-run
// of the same backtest makes no calls at all.
package breeze

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAPIURL  = "https://api.icicidirect.com/breezeapi/api/v1/"
	defaultHistURL = "https://breezeapi.icicidirect.com/api/v2/"
	browserUA      = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

// IST is the exchange's time zone; Breeze timestamps are IST wall-clock.
var IST = time.FixedZone("IST", 5*3600+1800)

// ErrDailyLimit means the client has used its call budget for this run.
var ErrDailyLimit = errors.New("breeze: call budget used up — re-run tomorrow; finished data is cached")

// LoginURL is where the user logs in to get the day's API session value.
func LoginURL(apiKey string) string {
	return "https://api.icicidirect.com/apiuser/login?api_key=" + url.QueryEscape(apiKey)
}

// Candle is one OHLCV bar. Time is the bar's start, in IST.
type Candle struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	OI     float64
}

// Client calls Breeze. Create with NewSession (or New in tests).
type Client struct {
	APIKey   string
	session  string // base64 session token sent as X-SessionToken
	HTTP     *http.Client
	APIURL   string
	HistURL  string
	CacheDir string // "" = no disk cache
	// MinGap is the minimum spacing between calls (100/min → 0.65s).
	MinGap time.Duration
	// MaxCalls caps network calls for this client's lifetime; 0 = no cap,
	// negative = no calls at all (cache only).
	MaxCalls int
	// UsagePath is a ledger of calls per IST day shared by every command, so
	// separate runs on one day together stay under DailyLimit. "" = off.
	UsagePath  string
	DailyLimit int
	// Now is the clock (tests inject it); a range ending today is never cached.
	Now func() time.Time

	mu    sync.Mutex
	last  time.Time
	calls int
}

// UsedToday is today's call count from the ledger (0 without one).
func (c *Client) UsedToday() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readUsage()[c.Now().In(IST).Format("2006-01-02")]
}

func (c *Client) readUsage() map[string]int {
	m := map[string]int{}
	if c.UsagePath == "" {
		return m
	}
	if data, err := os.ReadFile(c.UsagePath); err == nil {
		json.Unmarshal(data, &m)
	}
	return m
}

// recordCall adds one call to today's ledger entry (callers hold c.mu).
func (c *Client) recordCall() {
	if c.UsagePath == "" {
		return
	}
	m := c.readUsage()
	day := c.Now().In(IST).Format("2006-01-02")
	m[day]++
	cutoff := c.Now().In(IST).AddDate(0, 0, -30).Format("2006-01-02")
	for k := range m {
		if k < cutoff {
			delete(m, k)
		}
	}
	if data, err := json.Marshal(m); err == nil && os.MkdirAll(filepath.Dir(c.UsagePath), 0o755) == nil {
		os.WriteFile(c.UsagePath, data, 0o644)
	}
}

// New builds a client around an already-exchanged session token.
func New(apiKey, sessionToken, cacheDir string) *Client {
	return &Client{
		APIKey: apiKey, session: sessionToken,
		HTTP:   &http.Client{Timeout: 60 * time.Second},
		APIURL: defaultAPIURL, HistURL: defaultHistURL,
		CacheDir: cacheDir, MinGap: 650 * time.Millisecond, MaxCalls: 4500,
		Now: time.Now,
	}
}

// NewSession exchanges the day's API session value (from the login
// redirect) for a session token.
func NewSession(apiKey, apiSession, cacheDir string) (*Client, error) {
	c := New(apiKey, "", cacheDir)
	if err := c.login(apiSession); err != nil {
		return nil, err
	}
	return c, nil
}

// Calls is how many network calls this client has made.
func (c *Client) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *Client) login(apiSession string) error {
	if c.APIKey == "" || apiSession == "" {
		return errors.New("breeze: BREEZE_API_KEY and the day's session value are both needed")
	}
	body, _ := json.Marshal(map[string]string{"SessionToken": apiSession, "AppKey": c.APIKey})
	// Breeze's customerdetails is a GET that carries a JSON body.
	req, err := http.NewRequest(http.MethodGet, c.APIURL+"customerdetails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", browserUA)
	raw, err := c.do(req)
	if err != nil {
		return fmt.Errorf("breeze login: %w", err)
	}
	var out struct {
		Success *struct {
			SessionToken string `json:"session_token"`
		} `json:"Success"`
		Status int    `json:"Status"`
		Error  string `json:"Error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("breeze login: bad response: %w", err)
	}
	if out.Success == nil || out.Success.SessionToken == "" {
		switch out.Error {
		case "Resource not available.":
			return errors.New("breeze login: session expired — log in again at the login URL")
		case "Public Key does not exist.":
			return errors.New("breeze login: BREEZE_API_KEY is wrong")
		}
		return fmt.Errorf("breeze login failed: %s (status %d)", out.Error, out.Status)
	}
	c.session = out.Success.SessionToken
	return nil
}

// Request is one historical-data query (Breeze historicalcharts v2).
type Request struct {
	StockCode string    // Breeze code, e.g. RELIND (see StockCodes)
	Exchange  string    // NSE (cash) or NFO
	Product   string    // "" for cash; "options" or "futures" on NFO
	Interval  string    // 1minute, 5minute, 30minute, 1day
	Expiry    time.Time // NFO only
	Right     string    // "call" / "put" for options
	Strike    float64   // options only
}

// chunkDays is how many calendar days one call may span for an interval,
// keeping each response under Breeze's ~1000-candle cap.
func chunkDays(interval string) int {
	switch interval {
	case "1minute":
		return 2
	case "5minute":
		return 10
	case "30minute":
		return 60
	}
	return 400 // 1day: a calendar year in one call
}

// History returns candles for [from, to] (dates, inclusive), oldest first.
// Long ranges are split into calls under the per-call candle cap; each
// finished chunk is cached on disk.
func (c *Client) History(r Request, from, to time.Time) ([]Candle, error) {
	from = dayStart(from)
	to = dayStart(to)
	if to.Before(from) {
		return nil, nil
	}
	step := chunkDays(r.Interval)
	var out []Candle
	for s := from; !s.After(to); s = s.AddDate(0, 0, step) {
		e := s.AddDate(0, 0, step-1)
		if e.After(to) {
			e = to
		}
		part, err := c.chunk(r, s, e)
		if err != nil {
			return out, err
		}
		out = append(out, part...)
	}
	return out, nil
}

func dayStart(t time.Time) time.Time {
	t = t.In(IST)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, IST)
}

func (c *Client) chunk(r Request, from, to time.Time) ([]Candle, error) {
	key := cacheKey(r, from, to)
	if cs, ok := c.readCache(key); ok {
		return cs, nil
	}
	q := url.Values{}
	q.Set("interval", r.Interval)
	// Breeze reads these as IST dates; the whole day either side covers the
	// session whichever way the "Z" is interpreted.
	q.Set("from_date", from.Format("2006-01-02")+"T00:00:00.000Z")
	q.Set("to_date", to.Format("2006-01-02")+"T23:59:59.000Z")
	q.Set("stock_code", r.StockCode)
	q.Set("exch_code", r.Exchange)
	if r.Product != "" {
		q.Set("product_type", r.Product)
	}
	if !r.Expiry.IsZero() {
		q.Set("expiry_date", r.Expiry.In(IST).Format("2006-01-02")+"T07:00:00.000Z")
	}
	if r.Right != "" {
		q.Set("right", r.Right)
	}
	if r.Strike > 0 {
		q.Set("strike_price", strconv.FormatFloat(r.Strike, 'f', -1, 64))
	}
	req, err := http.NewRequest(http.MethodGet, c.HistURL+"historicalcharts?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("X-SessionToken", c.session)
	req.Header.Set("apikey", c.APIKey)
	raw, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("breeze %s %s: %w", r.StockCode, r.Interval, err)
	}
	cs, err := parseHistory(raw)
	if err != nil {
		return nil, fmt.Errorf("breeze %s %s: %w", r.StockCode, r.Interval, err)
	}
	// Keep only bars inside the asked range (Breeze may pad either side).
	end := to.AddDate(0, 0, 1)
	kept := cs[:0]
	for _, k := range cs {
		if !k.Time.Before(from) && k.Time.Before(end) {
			kept = append(kept, k)
		}
	}
	// A range that includes today may still change; cache only finished days.
	if to.Before(dayStart(c.Now())) {
		c.writeCache(key, kept)
	}
	return kept, nil
}

// do sends one GET through the throttle, retrying transient failures. Only
// reads go through here — a retried GET is always safe.
func (c *Client) do(req *http.Request) ([]byte, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := c.wait(); err != nil {
			return nil, err
		}
		if body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch {
		case err != nil:
			lastErr = err
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
		default:
			return raw, nil
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Second)
	}
	return nil, lastErr
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func (c *Client) wait() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.MaxCalls != 0 && c.calls >= c.MaxCalls {
		return ErrDailyLimit
	}
	if c.DailyLimit > 0 && c.readUsage()[c.Now().In(IST).Format("2006-01-02")] >= c.DailyLimit {
		return ErrDailyLimit
	}
	if d := c.MinGap - time.Since(c.last); d > 0 {
		time.Sleep(d)
	}
	c.last = time.Now()
	c.calls++
	c.recordCall()
	return nil
}

// num accepts a JSON number or a numeric string ("1452.3", "" → 0).
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*n = num(f)
	return nil
}

// parseHistory reads a historicalcharts response. "No data" is an empty
// result, not an error — an option that never traded that day has no bars.
func parseHistory(raw []byte) ([]Candle, error) {
	var out struct {
		Success []struct {
			Datetime string `json:"datetime"`
			Open     num    `json:"open"`
			High     num    `json:"high"`
			Low      num    `json:"low"`
			Close    num    `json:"close"`
			Volume   num    `json:"volume"`
			OI       num    `json:"open_interest"`
		} `json:"Success"`
		Status int    `json:"Status"`
		Error  string `json:"Error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("bad response: %w (%s)", err, snippet(raw))
	}
	if out.Success == nil && out.Error != "" && !strings.Contains(strings.ToLower(out.Error), "no data") {
		return nil, fmt.Errorf("%s (status %d)", out.Error, out.Status)
	}
	cs := make([]Candle, 0, len(out.Success))
	for _, r := range out.Success {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", r.Datetime, IST)
		if err != nil {
			return nil, fmt.Errorf("bad datetime %q", r.Datetime)
		}
		cs = append(cs, Candle{Time: t, Open: float64(r.Open), High: float64(r.High), Low: float64(r.Low),
			Close: float64(r.Close), Volume: float64(r.Volume), OI: float64(r.OI)})
	}
	// Oldest first, as every consumer expects.
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Time.Before(cs[j].Time) })
	return cs, nil
}

func cacheKey(r Request, from, to time.Time) string {
	exp := ""
	if !r.Expiry.IsZero() {
		exp = r.Expiry.Format("2006-01-02")
	}
	id := strings.Join([]string{r.Exchange, r.StockCode, r.Product, r.Interval, exp, r.Right,
		strconv.FormatFloat(r.Strike, 'f', -1, 64), from.Format("2006-01-02"), to.Format("2006-01-02")}, "|")
	h := sha1.Sum([]byte(id))
	return filepath.Join(strings.ToUpper(r.Exchange), r.StockCode, hex.EncodeToString(h[:8])+".json")
}

type cachedCandle struct {
	T int64     `json:"t"`
	V []float64 `json:"v"` // open, high, low, close, volume, oi
}

func (c *Client) readCache(key string) ([]Candle, bool) {
	if c.CacheDir == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(c.CacheDir, key))
	if err != nil {
		return nil, false
	}
	var rows []cachedCandle
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, false
	}
	cs := make([]Candle, 0, len(rows))
	for _, r := range rows {
		if len(r.V) < 6 {
			return nil, false
		}
		cs = append(cs, Candle{Time: time.Unix(r.T, 0).In(IST), Open: r.V[0], High: r.V[1], Low: r.V[2],
			Close: r.V[3], Volume: r.V[4], OI: r.V[5]})
	}
	return cs, true
}

func (c *Client) writeCache(key string, cs []Candle) {
	if c.CacheDir == "" {
		return
	}
	rows := make([]cachedCandle, len(cs))
	for i, k := range cs {
		rows[i] = cachedCandle{T: k.Time.Unix(), V: []float64{k.Open, k.High, k.Low, k.Close, k.Volume, k.OI}}
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return
	}
	path := filepath.Join(c.CacheDir, key)
	if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
		os.WriteFile(path, data, 0o644)
	}
}
