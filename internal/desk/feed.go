package desk

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// Bar is one OHLCV candle; T is its start, IST.
type Bar struct {
	T                  time.Time
	O, H, L, C, Volume float64
}

// Quote is a live quote.
type Quote struct {
	Symbol    string
	LTP       float64
	Bid, Ask  float64
	PrevClose float64
	Open      float64
	High, Low float64
	Volume    float64
	Source    string
}

// ChangePct is the move from the previous close, %.
func (q Quote) ChangePct() float64 {
	if q.PrevClose <= 0 {
		return 0
	}
	return (q.LTP/q.PrevClose - 1) * 100
}

// Feed is one broker's market data. Symbols are desk instruments
// ("RELIANCE", "RELIANCE26NOV1440CE"). Resolution is "1", "5" (minutes) or
// "D". Every implementation is read-only.
type Feed interface {
	Name() string
	Quotes(symbols []string) (map[string]Quote, error)
	Bars(symbol, resolution string, from, to time.Time) ([]Bar, error)
}

// ErrNoQuotes marks a feed that serves history only (Breeze here).
var ErrNoQuotes = errors.New("this feed has no live quotes")

// Multi tries feeds in order: a quote missing from one is asked of the
// next; bars come from the first feed that returns any.
type Multi struct {
	Feeds []Feed
	Log   func(string)
}

func (m *Multi) Name() string {
	var n []string
	for _, f := range m.Feeds {
		n = append(n, f.Name())
	}
	return strings.Join(n, "→")
}

func (m *Multi) logf(f string, a ...any) {
	if m.Log != nil {
		m.Log(fmt.Sprintf(f, a...))
	}
}

func (m *Multi) Quotes(symbols []string) (map[string]Quote, error) {
	out := map[string]Quote{}
	want := append([]string(nil), symbols...)
	var errs []string
	for _, f := range m.Feeds {
		if len(want) == 0 {
			break
		}
		qs, err := f.Quotes(want)
		if err != nil {
			if !errors.Is(err, ErrNoQuotes) {
				errs = append(errs, f.Name()+": "+err.Error())
				m.logf("%s quotes failed: %v — trying the next feed", f.Name(), err)
			}
			continue
		}
		var rest []string
		for _, s := range want {
			if q, ok := qs[s]; ok && q.LTP > 0 {
				out[s] = q
			} else {
				rest = append(rest, s)
			}
		}
		want = rest
	}
	if len(out) == 0 && len(symbols) > 0 {
		if len(errs) == 0 {
			errs = append(errs, "no configured feed has quotes")
		}
		return nil, fmt.Errorf("no quotes: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

func (m *Multi) Bars(symbol, res string, from, to time.Time) ([]Bar, error) {
	var errs []string
	for _, f := range m.Feeds {
		bs, err := f.Bars(symbol, res, from, to)
		if err != nil {
			errs = append(errs, f.Name()+": "+err.Error())
			m.logf("%s bars failed: %v — trying the next feed", f.Name(), err)
			continue
		}
		if len(bs) > 0 {
			return bs, nil
		}
	}
	if len(errs) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("no bars for %s: %s", symbol, strings.Join(errs, "; "))
}

// SortBars orders bars oldest first and drops duplicates.
func SortBars(bs []Bar) []Bar {
	sort.Slice(bs, func(i, j int) bool { return bs[i].T.Before(bs[j].T) })
	out := bs[:0]
	for i, b := range bs {
		if i > 0 && b.T.Equal(out[len(out)-1].T) {
			continue
		}
		out = append(out, b)
	}
	return out
}

// ── Fyers ───────────────────────────────────────────────────────────────────

// FyersFeed uses the bundled, throttled, read-only Fyers client.
type FyersFeed struct{ C *fyers.FyersClient }

func (f FyersFeed) Name() string { return "fyers" }

func fyersSym(s string) string {
	in := ParseInstrument(s)
	if in.Option {
		return in.Symbol
	}
	return fyers.StockSpotSymbol(in.Symbol)
}

func (f FyersFeed) Quotes(symbols []string) (map[string]Quote, error) {
	out := map[string]Quote{}
	for i := 0; i < len(symbols); i += 50 {
		end := min(i+50, len(symbols))
		var fs []string
		back := map[string]string{}
		for _, s := range symbols[i:end] {
			k := fyersSym(s)
			fs = append(fs, k)
			back["NSE:"+k] = s
		}
		qs, err := f.C.GetQuotes(fs)
		if err != nil {
			return nil, err
		}
		for k, q := range qs {
			if s, ok := back[k]; ok {
				out[s] = Quote{Symbol: s, LTP: q.LTP, Bid: q.Bid, Ask: q.Ask, PrevClose: q.PrevClose,
					Open: q.Open, High: q.High, Low: q.Low, Volume: float64(q.Volume), Source: "fyers"}
			}
		}
	}
	return out, nil
}

func (f FyersFeed) Bars(symbol, res string, from, to time.Time) ([]Bar, error) {
	step := 99 // intraday: at most 100 days a request
	if res == "D" {
		step = 365
	}
	var out []Bar
	for s := dayOf(from); !s.After(dayOf(to)); s = s.AddDate(0, 0, step+1) {
		e := s.AddDate(0, 0, step)
		if e.After(to) {
			e = to
		}
		cs, err := f.C.GetCandles(fyersSym(symbol), res, s, e)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			out = append(out, Bar{T: c.Time.In(IST), O: c.Open, H: c.High, L: c.Low, C: c.Close, Volume: c.Volume})
		}
	}
	return SortBars(out), nil
}
