package desk

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UpstoxFeed reads Upstox's market-quote and historical-candle APIs (v2
// quotes, v3 candles) with a daily access token. Instruments map through
// Upstox's public NSE instrument file (cached a day). Read-only.
//
// Docs: https://upstox.com/developer/api-documentation/
type UpstoxFeed struct {
	Token    string
	CacheDir string
	API      string // default https://api.upstox.com
	Assets   string // default https://assets.upstox.com
	HTTP     *http.Client

	once sync.Once
	keys map[string]string // desk symbol → instrument key
	err  error
}

func (u *UpstoxFeed) Name() string { return "upstox" }

func (u *UpstoxFeed) init() {
	if u.API == "" {
		u.API = "https://api.upstox.com"
	}
	if u.Assets == "" {
		u.Assets = "https://assets.upstox.com"
	}
	if u.HTTP == nil {
		u.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
}

type upstoxInstrument struct {
	Segment        string  `json:"segment"`
	TradingSymbol  string  `json:"trading_symbol"`
	InstrumentKey  string  `json:"instrument_key"`
	InstrumentType string  `json:"instrument_type"`
	Underlying     string  `json:"underlying_symbol"`
	Strike         float64 `json:"strike_price"`
	Expiry         int64   `json:"expiry"` // ms since epoch
}

// loadKeys builds the symbol → instrument-key map: NSE_EQ stocks by their
// trading symbol, NSE_FO stock options as desk option tickers.
func (u *UpstoxFeed) loadKeys() (map[string]string, error) {
	u.once.Do(func() {
		u.init()
		data, err := u.cached(u.Assets+"/market-quote/instruments/exchange/NSE.json.gz", "upstox_NSE.json.gz")
		if err != nil {
			u.err = err
			return
		}
		u.keys, u.err = parseUpstoxInstruments(data)
	})
	return u.keys, u.err
}

func parseUpstoxInstruments(gz []byte) (map[string]string, error) {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("upstox instruments: %w", err)
	}
	var list []upstoxInstrument
	if err := json.NewDecoder(r).Decode(&list); err != nil {
		return nil, fmt.Errorf("upstox instruments: %w", err)
	}
	keys := map[string]string{}
	for _, in := range list {
		switch {
		case in.Segment == "NSE_EQ" && in.InstrumentType == "EQ":
			keys[strings.ToUpper(in.TradingSymbol)] = in.InstrumentKey
		case in.Segment == "NSE_FO" && (in.InstrumentType == "CE" || in.InstrumentType == "PE") && in.Underlying != "":
			exp := time.UnixMilli(in.Expiry).In(IST)
			keys[OptionTicker(in.Underlying, exp, in.Strike, in.InstrumentType)] = in.InstrumentKey
		}
	}
	return keys, nil
}

func (u *UpstoxFeed) cached(url, name string) ([]byte, error) {
	path := ""
	if u.CacheDir != "" {
		path = filepath.Join(u.CacheDir, name)
		if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) < 20*time.Hour {
			return os.ReadFile(path)
		}
	}
	resp, err := u.HTTP.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("upstox %s: HTTP %d", url, resp.StatusCode)
	}
	if path != "" && os.MkdirAll(u.CacheDir, 0o755) == nil {
		os.WriteFile(path, data, 0o644)
	}
	return data, nil
}

func (u *UpstoxFeed) get(path string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, u.API+path, nil)
	req.Header.Set("Authorization", "Bearer "+u.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("upstox %s: HTTP %d %s", strings.SplitN(path, "?", 2)[0], resp.StatusCode, snippet(data))
	}
	return json.Unmarshal(data, out)
}

// InstrumentKeys is the desk symbol → Upstox instrument key map (stocks and
// stock options), from Upstox's public instrument file.
func (u *UpstoxFeed) InstrumentKeys() (map[string]string, error) { return u.loadKeys() }

func (u *UpstoxFeed) key(sym string) (string, error) {
	keys, err := u.loadKeys()
	if err != nil {
		return "", err
	}
	k, ok := keys[ParseInstrument(sym).Symbol]
	if !ok {
		return "", fmt.Errorf("upstox has no instrument %s", sym)
	}
	return k, nil
}

func (u *UpstoxFeed) Quotes(symbols []string) (map[string]Quote, error) {
	u.init()
	back := map[string]string{}
	var ks []string
	for _, s := range symbols {
		if k, err := u.key(s); err == nil {
			back[k] = s
			ks = append(ks, k)
		} else if len(symbols) == 1 {
			return nil, err
		}
	}
	out := map[string]Quote{}
	for i := 0; i < len(ks); i += 100 {
		end := min(i+100, len(ks))
		var resp struct {
			Status string `json:"status"`
			Data   map[string]struct {
				LastPrice float64 `json:"last_price"`
				NetChange float64 `json:"net_change"`
				Volume    float64 `json:"volume"`
				Token     string  `json:"instrument_token"`
				OHLC      struct {
					Open, High, Low float64
				} `json:"ohlc"`
				Depth struct {
					Buy  []struct{ Price float64 } `json:"buy"`
					Sell []struct{ Price float64 } `json:"sell"`
				} `json:"depth"`
			} `json:"data"`
		}
		if err := u.get("/v2/market-quote/quotes?instrument_key="+url.QueryEscape(strings.Join(ks[i:end], ",")), &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			s, ok := back[d.Token]
			if !ok {
				continue
			}
			q := Quote{Symbol: s, LTP: d.LastPrice, PrevClose: d.LastPrice - d.NetChange, Open: d.OHLC.Open,
				High: d.OHLC.High, Low: d.OHLC.Low, Volume: d.Volume, Source: "upstox"}
			if len(d.Depth.Buy) > 0 {
				q.Bid = d.Depth.Buy[0].Price
			}
			if len(d.Depth.Sell) > 0 {
				q.Ask = d.Depth.Sell[0].Price
			}
			out[s] = q
		}
	}
	return out, nil
}

func (u *UpstoxFeed) Bars(symbol, res string, from, to time.Time) ([]Bar, error) {
	u.init()
	k, err := u.key(symbol)
	if err != nil {
		return nil, err
	}
	unit, iv, step := "minutes", res, 27
	if res == "D" {
		unit, iv, step = "days", "1", 3650
	}
	var out []Bar
	ke := url.PathEscape(k)
	today := dayOf(time.Now())
	for s := dayOf(from); !s.After(dayOf(to)); s = s.AddDate(0, 0, step+1) {
		e := s.AddDate(0, 0, step)
		if e.After(to) {
			e = dayOf(to)
		}
		bs, err := u.candles(fmt.Sprintf("/v3/historical-candle/%s/%s/%s/%s/%s", ke, unit, iv, e.Format("2006-01-02"), s.Format("2006-01-02")))
		if err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	if res != "D" && !dayOf(to).Before(today) {
		bs, err := u.candles(fmt.Sprintf("/v3/historical-candle/intraday/%s/%s/%s", ke, unit, iv))
		if err == nil {
			out = append(out, bs...)
		}
	}
	return SortBars(out), nil
}

func (u *UpstoxFeed) candles(path string) ([]Bar, error) {
	var resp struct {
		Data struct {
			Candles [][]any `json:"candles"`
		} `json:"data"`
	}
	if err := u.get(path, &resp); err != nil {
		return nil, err
	}
	return candleRows(resp.Data.Candles, time.RFC3339)
}

// candleRows reads [time, o, h, l, c, v, ...] rows (Upstox and Kite).
func candleRows(rows [][]any, layout string) ([]Bar, error) {
	var out []Bar
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		ts, _ := r[0].(string)
		t, err := time.Parse(layout, ts)
		if err != nil {
			t, err = time.Parse("2006-01-02T15:04:05-0700", ts)
			if err != nil {
				return nil, fmt.Errorf("candle time %q: %w", ts, err)
			}
		}
		f := func(i int) float64 { v, _ := r[i].(float64); return v }
		out = append(out, Bar{T: t.In(IST), O: f(1), H: f(2), L: f(3), C: f(4), Volume: f(5)})
	}
	return out, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
