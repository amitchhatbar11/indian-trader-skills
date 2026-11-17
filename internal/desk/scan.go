package desk

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/bhavcopy"
	"github.com/amitchhatbar11/indian-trader-skills/internal/corpact"
)

// DailyData is NSE's end-of-day data the scanner reads (cached bhavcopies).
type DailyData interface {
	CM(day time.Time) (*bhavcopy.CMDay, error)
	FO(day time.Time) (*bhavcopy.FODay, error)
}

// History is each stock's recent official daily closes, oldest first,
// adjusted for splits and bonuses into the last day's units.
type History struct {
	Days  []time.Time
	Bars  map[string][]Bar // by today's symbol
	FnO   map[string]bool  // stocks with options on the last day
	Lots  map[string]int   // their lot size (nearest expiry)
	Last  time.Time
	Notes []string
}

// LoadHistory reads the last n sessions up to (and including) the last
// published day on or before asOf.
func LoadHistory(data DailyData, corp *corpact.Registry, asOf time.Time, n int) (*History, error) {
	h := &History{Bars: map[string][]Bar{}, FnO: map[string]bool{}, Lots: map[string]int{}}
	type dayRows struct {
		d    time.Time
		rows map[string]bhavcopy.CMRow
	}
	var days []dayRows
	for d, tries := dayOf(asOf), 0; len(days) < n && tries < n*2+20; d, tries = d.AddDate(0, 0, -1), tries+1 {
		if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		cm, err := data.CM(d)
		if errors.Is(err, bhavcopy.ErrNotPublished) {
			continue
		}
		if err != nil {
			if len(days) == 0 {
				return nil, err
			}
			h.Notes = append(h.Notes, fmt.Sprintf("%s: %v", d.Format("2006-01-02"), err))
			continue
		}
		days = append(days, dayRows{d, cm.Rows})
	}
	if len(days) == 0 {
		return nil, fmt.Errorf("no NSE equity bhavcopy near %s", asOf.Format("2006-01-02"))
	}
	h.Last = days[0].d
	if fo, err := data.FO(h.Last); err == nil {
		for sym, ch := range fo.Chains {
			cur := corp.Current(sym, h.Last)
			h.FnO[cur] = true
			h.Lots[cur] = ch.LotSize
		}
	}
	for i := len(days) - 1; i >= 0; i-- {
		d := days[i]
		h.Days = append(h.Days, d.d)
		for sym, r := range d.rows {
			if r.Close <= 0 {
				continue
			}
			cur := corp.Current(sym, d.d)
			f := 1.0
			if acts := corp.Between(cur, d.d, h.Last); len(acts) > 0 {
				f = corp.Factor(cur, d.d, h.Last)
			}
			h.Bars[cur] = append(h.Bars[cur], Bar{T: d.d, O: r.Open * f, H: r.High * f, L: r.Low * f, C: r.Close * f, Volume: r.Volume / f})
		}
	}
	return h, nil
}

// Setup names.
const (
	SetupBreakout   = "breakout20"  // close above the prior 20 sessions' highest close
	SetupBreakdown  = "breakdown20" // close below the prior 20 sessions' lowest close (short)
	SetupBullFlag   = "bull_flag"   // ≥10% pole in the last 10 sessions, then 3 tight sessions near the high
	SetupReclaim20  = "reclaim20"   // crossed back above the 20-day average
	SetupOversold   = "oversold"    // RSI(14) below 30
	SetupBounce     = "bounce"      // RSI was below 30 yesterday and today closed above yesterday's high
	SetupOverbought = "overbought"  // RSI(14) above 75: a warning on a long, a short-side watch alone
)

// Pick is one stock's daily scan result.
type Pick struct {
	Symbol     string   `json:"symbol"`
	Close      float64  `json:"close"`
	ChangePct  float64  `json:"change_pct"`
	VolRatio   float64  `json:"vol_ratio"` // today's volume ÷ 20-day average
	TurnoverCr float64  `json:"turnover_cr"`
	RSI        float64  `json:"rsi14"`
	SMA20      float64  `json:"sma20"`
	ATR        float64  `json:"atr14"`
	High20     float64  `json:"high20"` // prior 20 sessions' highest close: the breakout level
	Low20      float64  `json:"low20"`
	FnO        bool     `json:"fno"`
	Lot        int      `json:"lot,omitempty"`
	Setups     []string `json:"setups"`
	Side       string   `json:"side"` // long / short: which way the setups point
}

// ScanDaily finds setups on the last session. universe "fno" keeps F&O
// stocks only; "all" every EQ stock above the price and turnover floors.
func ScanDaily(h *History, universe string, minPrice, minTurnoverCr float64) []Pick {
	var out []Pick
	for sym, bs := range h.Bars {
		if len(bs) < 22 || !bs[len(bs)-1].T.Equal(h.Last) {
			continue
		}
		if universe == "fno" && !h.FnO[sym] {
			continue
		}
		last, prev := bs[len(bs)-1], bs[len(bs)-2]
		turn := last.C * last.Volume / 1e7
		if last.C < minPrice || turn < minTurnoverCr {
			continue
		}
		p := Pick{Symbol: sym, Close: last.C, ChangePct: (last.C/prev.C - 1) * 100, TurnoverCr: round2(turn),
			FnO: h.FnO[sym], Lot: h.Lots[sym]}
		prior := bs[len(bs)-21 : len(bs)-1]
		var vsum float64
		p.High20, p.Low20 = math.Inf(-1), math.Inf(1)
		for _, b := range prior {
			vsum += b.Volume
			p.High20 = math.Max(p.High20, b.C)
			p.Low20 = math.Min(p.Low20, b.C)
		}
		if vsum > 0 {
			p.VolRatio = round2(last.Volume / (vsum / 20))
		}
		p.SMA20 = sma(bs, 20)
		p.RSI = rsi(bs, 14)
		p.ATR = atr(bs, 14)
		prevSMA := sma(bs[:len(bs)-1], 20)
		prevRSI := rsi(bs[:len(bs)-1], 14)

		if last.C > p.High20 {
			p.Setups = append(p.Setups, SetupBreakout)
		}
		if last.C < p.Low20 {
			p.Setups = append(p.Setups, SetupBreakdown)
		}
		if isBullFlag(bs) {
			p.Setups = append(p.Setups, SetupBullFlag)
		}
		if prev.C < prevSMA && last.C > p.SMA20 {
			p.Setups = append(p.Setups, SetupReclaim20)
		}
		if p.RSI > 0 && p.RSI < 30 {
			p.Setups = append(p.Setups, SetupOversold)
		}
		if prevRSI > 0 && prevRSI < 30 && last.C > prev.H {
			p.Setups = append(p.Setups, SetupBounce)
		}
		if p.RSI > 75 {
			p.Setups = append(p.Setups, SetupOverbought)
		}
		if len(p.Setups) == 0 {
			continue
		}
		// A breakdown points short, and so does "overbought" on its own; next
		// to a long setup, overbought is only a warning.
		p.Side = "long"
		if slices.Contains(p.Setups, SetupBreakdown) || len(p.Setups) == 1 && p.Setups[0] == SetupOverbought {
			p.Side = "short"
		}
		p.RSI, p.SMA20, p.ATR, p.ChangePct = round2(p.RSI), round2(p.SMA20), round2(p.ATR), round2(p.ChangePct)
		p.High20, p.Low20 = round2(p.High20), round2(p.Low20)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Setups) != len(out[j].Setups) {
			return len(out[i].Setups) > len(out[j].Setups)
		}
		return out[i].TurnoverCr > out[j].TurnoverCr
	})
	return out
}

// isBullFlag: the highest close of the last 10 sessions is ≥10% above the
// close 10 sessions ago (the pole), the last 3 sessions' range is within 4%
// of price (the flag) and the close is within 3% of the pole high.
func isBullFlag(bs []Bar) bool {
	if len(bs) < 12 {
		return false
	}
	n := len(bs)
	base := bs[n-11].C
	var top float64
	for _, b := range bs[n-10 : n-3] {
		top = math.Max(top, b.C)
	}
	if base <= 0 || top/base-1 < 0.10 {
		return false
	}
	hi, lo := math.Inf(-1), math.Inf(1)
	for _, b := range bs[n-3:] {
		hi, lo = math.Max(hi, b.H), math.Min(lo, b.L)
	}
	last := bs[n-1].C
	return (hi-lo)/last <= 0.04 && last >= top*0.97
}

func sma(bs []Bar, n int) float64 {
	if len(bs) < n {
		return 0
	}
	var s float64
	for _, b := range bs[len(bs)-n:] {
		s += b.C
	}
	return s / float64(n)
}

// rsi is Wilder's RSI over n periods.
func rsi(bs []Bar, n int) float64 {
	if len(bs) < n+1 {
		return 0
	}
	var gain, loss float64
	for i := 1; i <= n; i++ {
		d := bs[i].C - bs[i-1].C
		if d > 0 {
			gain += d
		} else {
			loss -= d
		}
	}
	gain /= float64(n)
	loss /= float64(n)
	for i := n + 1; i < len(bs); i++ {
		d := bs[i].C - bs[i-1].C
		g, l := math.Max(d, 0), math.Max(-d, 0)
		gain = (gain*float64(n-1) + g) / float64(n)
		loss = (loss*float64(n-1) + l) / float64(n)
	}
	if loss == 0 {
		return 100
	}
	return 100 - 100/(1+gain/loss)
}

// atr is the average true range over n bars (simple average).
func atr(bs []Bar, n int) float64 {
	if len(bs) < n+1 {
		return 0
	}
	var s float64
	for i := len(bs) - n; i < len(bs); i++ {
		tr := math.Max(bs[i].H-bs[i].L, math.Max(math.Abs(bs[i].H-bs[i-1].C), math.Abs(bs[i].L-bs[i-1].C)))
		s += tr
	}
	return s / float64(n)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// ORB is a stock's opening range and where it trades now.
type ORB struct {
	Symbol   string    `json:"symbol"`
	High     float64   `json:"range_high"`
	Low      float64   `json:"range_low"`
	Minutes  int       `json:"range_minutes"`
	Last     float64   `json:"last"`
	LastBar  time.Time `json:"last_bar"`
	State    string    `json:"state"` // forming / inside / broke_up / broke_down
	BrokeAt  time.Time `json:"broke_at,omitempty"`
	VolRatio float64   `json:"vol_ratio,omitempty"` // breakout bar's volume ÷ the range bars' average
}

// OpeningRange works out the opening range (the first `minutes` from 09:15)
// from today's bars and the first completed bar to CLOSE outside it.
// now limits the bars to those completed by then.
func OpeningRange(sym string, bars []Bar, minutes int, barMin int, now time.Time) ORB {
	o := ORB{Symbol: sym, Minutes: minutes, State: "forming"}
	open := Clock(now, MarketOpen)
	end := open.Add(time.Duration(minutes) * time.Minute)
	var rng []Bar
	o.High, o.Low = math.Inf(-1), math.Inf(1)
	for _, b := range bars {
		if b.T.Before(open) || !dayOf(b.T).Equal(dayOf(now)) {
			continue
		}
		if b.T.Add(time.Duration(barMin) * time.Minute).After(now) {
			break // not completed yet
		}
		o.Last, o.LastBar = b.C, b.T
		if b.T.Before(end) {
			rng = append(rng, b)
			o.High, o.Low = math.Max(o.High, b.H), math.Min(o.Low, b.L)
			continue
		}
		if len(rng) == 0 {
			break
		}
		if o.State == "forming" {
			o.State = "inside"
		}
		if o.BrokeAt.IsZero() && (b.C > o.High || b.C < o.Low) {
			o.BrokeAt = b.T
			o.State = "broke_up"
			if b.C < o.Low {
				o.State = "broke_down"
			}
			var v float64
			for _, r := range rng {
				v += r.Volume
			}
			if v > 0 {
				o.VolRatio = round2(b.Volume / (v / float64(len(rng))))
			}
		}
	}
	if math.IsInf(o.High, 0) {
		o.High, o.Low = 0, 0
	}
	return o
}
