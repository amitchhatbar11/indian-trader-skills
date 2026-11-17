package desk

import (
	"encoding/csv"
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

// KiteFeed reads Zerodha Kite Connect quotes and historical candles (the
// historical API is a paid Kite add-on). Instrument tokens come from Kite's
// instrument dumps for NSE and NFO, cached a day. Read-only.
//
// Docs: https://kite.trade/docs/connect/v3/
type KiteFeed struct {
	APIKey, Token string
	CacheDir      string
	API           string // default https://api.kite.trade
	HTTP          *http.Client

	once   sync.Once
	tokens map[string]string // desk symbol → instrument_token
	err    error
}

func (k *KiteFeed) Name() string { return "zerodha" }

func (k *KiteFeed) init() {
	if k.API == "" {
		k.API = "https://api.kite.trade"
	}
	if k.HTTP == nil {
		k.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
}

func (k *KiteFeed) do(path string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, k.API+path, nil)
	req.Header.Set("X-Kite-Version", "3")
	req.Header.Set("Authorization", "token "+k.APIKey+":"+k.Token)
	resp, err := k.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("kite %s: HTTP %d %s", strings.SplitN(path, "?", 2)[0], resp.StatusCode, snippet(data))
	}
	return data, nil
}

// kiteExchange is the Kite exchange of a desk symbol.
func kiteExchange(sym string) string {
	if ParseInstrument(sym).Option {
		return "NFO"
	}
	return "NSE"
}

func (k *KiteFeed) loadTokens() (map[string]string, error) {
	k.once.Do(func() {
		k.init()
		k.tokens = map[string]string{}
		for _, ex := range []string{"NSE", "NFO"} {
			data, err := k.cachedDump(ex)
			if err != nil {
				k.err = err
				return
			}
			if err := parseKiteInstruments(data, k.tokens); err != nil {
				k.err = err
				return
			}
		}
	})
	return k.tokens, k.err
}

func (k *KiteFeed) cachedDump(ex string) ([]byte, error) {
	path := ""
	if k.CacheDir != "" {
		path = filepath.Join(k.CacheDir, "kite_"+ex+".csv")
		if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) < 20*time.Hour {
			return os.ReadFile(path)
		}
	}
	data, err := k.do("/instruments/" + ex)
	if err != nil {
		return nil, err
	}
	if path != "" && os.MkdirAll(k.CacheDir, 0o755) == nil {
		os.WriteFile(path, data, 0o644)
	}
	return data, nil
}

// parseKiteInstruments reads a Kite instrument dump: stocks (EQ) by symbol,
// stock options (CE/PE, segment NFO-OPT) as desk tickers — Kite's monthly
// option symbols already are SYMBOL+YY+MON+STRIKE+CE.
func parseKiteInstruments(data []byte, into map[string]string) error {
	cr := csv.NewReader(strings.NewReader(string(data)))
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return fmt.Errorf("kite instruments: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[h] = i
	}
	for _, need := range []string{"instrument_token", "tradingsymbol", "instrument_type", "segment"} {
		if _, ok := col[need]; !ok {
			return fmt.Errorf("kite instruments: no %s column", need)
		}
	}
	for {
		r, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		ts, typ, seg := r[col["tradingsymbol"]], r[col["instrument_type"]], r[col["segment"]]
		switch {
		case seg == "NSE" && typ == "EQ":
			into[ts] = r[col["instrument_token"]]
		case seg == "NFO-OPT" && (typ == "CE" || typ == "PE"):
			if in := ParseInstrument(ts); in.Option {
				into[in.Symbol] = r[col["instrument_token"]]
			}
		}
	}
	return nil
}

func (k *KiteFeed) Quotes(symbols []string) (map[string]Quote, error) {
	k.init()
	out := map[string]Quote{}
	for i := 0; i < len(symbols); i += 200 {
		end := min(i+200, len(symbols))
		q := url.Values{}
		back := map[string]string{}
		for _, s := range symbols[i:end] {
			key := kiteExchange(s) + ":" + ParseInstrument(s).Symbol
			q.Add("i", key)
			back[key] = s
		}
		data, err := k.do("/quote?" + q.Encode())
		if err != nil {
			return nil, err
		}
		var resp struct {
			Data map[string]struct {
				LastPrice float64 `json:"last_price"`
				Volume    float64 `json:"volume"`
				OHLC      struct {
					Open, High, Low, Close float64
				} `json:"ohlc"`
				Depth struct {
					Buy  []struct{ Price float64 } `json:"buy"`
					Sell []struct{ Price float64 } `json:"sell"`
				} `json:"depth"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("kite quote: %w", err)
		}
		for key, d := range resp.Data {
			s, ok := back[key]
			if !ok {
				continue
			}
			qt := Quote{Symbol: s, LTP: d.LastPrice, PrevClose: d.OHLC.Close, Open: d.OHLC.Open,
				High: d.OHLC.High, Low: d.OHLC.Low, Volume: d.Volume, Source: "zerodha"}
			if len(d.Depth.Buy) > 0 {
				qt.Bid = d.Depth.Buy[0].Price
			}
			if len(d.Depth.Sell) > 0 {
				qt.Ask = d.Depth.Sell[0].Price
			}
			out[s] = qt
		}
	}
	return out, nil
}

func (k *KiteFeed) Bars(symbol, res string, from, to time.Time) ([]Bar, error) {
	k.init()
	toks, err := k.loadTokens()
	if err != nil {
		return nil, err
	}
	tok, ok := toks[ParseInstrument(symbol).Symbol]
	if !ok {
		return nil, fmt.Errorf("kite has no instrument %s", symbol)
	}
	iv, step := "minute", 59
	switch res {
	case "5":
		iv, step = "5minute", 99
	case "D":
		iv, step = "day", 1999
	}
	var out []Bar
	for s := dayOf(from); !s.After(dayOf(to)); s = s.AddDate(0, 0, step+1) {
		e := s.AddDate(0, 0, step)
		if e.After(to) {
			e = dayOf(to)
		}
		q := url.Values{}
		q.Set("from", s.Format("2006-01-02")+" 00:00:00")
		q.Set("to", e.Format("2006-01-02")+" 23:59:59")
		data, err := k.do(fmt.Sprintf("/instruments/historical/%s/%s?%s", tok, iv, q.Encode()))
		if err != nil {
			return nil, err
		}
		var resp struct {
			Data struct {
				Candles [][]any `json:"candles"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("kite historical: %w", err)
		}
		bs, err := candleRows(resp.Data.Candles, "2006-01-02T15:04:05-0700")
		if err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	return SortBars(out), nil
}
