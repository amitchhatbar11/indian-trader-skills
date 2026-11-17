package desk

import (
	"fmt"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/breeze"
)

// BreezeFeed reads ICICI Breeze history (the project's read-only client:
// throttle, daily call ledger, disk cache). Breeze serves no quotes here, so
// it only fills in bars. Options need their expiry date, which Expiry
// supplies (from NSE's F&O bhavcopy).
type BreezeFeed struct {
	C      *breeze.Client
	Codes  map[string]string // NSE symbol → Breeze stock code
	Expiry func(in Instrument) (time.Time, error)
}

func (b *BreezeFeed) Name() string { return "breeze" }

func (b *BreezeFeed) Quotes([]string) (map[string]Quote, error) { return nil, ErrNoQuotes }

func (b *BreezeFeed) Bars(symbol, res string, from, to time.Time) ([]Bar, error) {
	in := ParseInstrument(symbol)
	code, ok := b.Codes[in.Underlying]
	if !ok {
		return nil, fmt.Errorf("breeze has no stock code for %s", in.Underlying)
	}
	iv := map[string]string{"1": "1minute", "5": "5minute", "D": "1day"}[res]
	if iv == "" {
		return nil, fmt.Errorf("breeze: resolution %q not supported", res)
	}
	r := breeze.Request{StockCode: code, Exchange: "NSE", Interval: iv}
	if in.Option {
		if b.Expiry == nil {
			return nil, fmt.Errorf("breeze: no expiry lookup for %s", symbol)
		}
		e, err := b.Expiry(in)
		if err != nil {
			return nil, err
		}
		right := "call"
		if in.Right == "PE" {
			right = "put"
		}
		r = breeze.Request{StockCode: code, Exchange: "NFO", Product: "options", Interval: iv, Expiry: e, Right: right, Strike: in.Strike}
	}
	cs, err := b.C.History(r, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]Bar, 0, len(cs))
	for _, c := range cs {
		out = append(out, Bar{T: c.Time.In(IST), O: c.Open, H: c.High, L: c.Low, C: c.Close, Volume: c.Volume})
	}
	return SortBars(out), nil
}
