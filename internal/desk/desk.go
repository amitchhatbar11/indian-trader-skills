// Package desk is the Indian trading desk behind the nse-* agent skills: a
// scanner, entry timer, exit planner, time machine, position sizer, risk
// check, PAPER broker, price alerts, news, results analyst and a bulk/block
// deal tracker, for NSE cash stocks (intraday) and stock options.
//
// It is read-only towards every broker: prices come from Fyers, Upstox,
// Zerodha or ICICI Breeze (whichever is configured, in that order), and
// trades go only to the desk's own paper ledger (~/.nse-desk/paper.json). No
// file here calls an order API — desk_test.go fails the build if one does,
// and the bundled Fyers client has no order methods at all.
//
// The skills hand work to each other through JSON files in ~/.nse-desk/
// (watchlist → entry → plan → orders → risk → paper).
package desk

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// IST is India Standard Time.
var IST = time.FixedZone("IST", 5*3600+1800)

// Dir is where the desk's files live (~/.nse-desk, or $NSE_DESK_HOME).
type Dir string

func (d Dir) Path(name string) string { return filepath.Join(string(d), name) }

// Read loads name into v; a missing file is os.ErrNotExist.
func (d Dir) Read(name string, v any) error {
	data, err := os.ReadFile(d.Path(name))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", d.Path(name), err)
	}
	return nil
}

// Write saves v as indented JSON (atomically: temp file + rename).
func (d Dir) Write(name string, v any) error {
	p := d.Path(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Exists reports whether name is there.
func (d Dir) Exists(name string) bool {
	_, err := os.Stat(d.Path(name))
	return err == nil
}

// IsMissing reports a missing file from Read.
func IsMissing(err error) bool { return errors.Is(err, os.ErrNotExist) }

// Instrument is a plain NSE stock ("RELIANCE") or a stock option in Fyers'
// format without the exchange prefix ("RELIANCE26NOV1440CE").
type Instrument struct {
	Symbol     string // as given, upper case
	Underlying string // the stock
	Option     bool
	Year       int        // options: 2026
	Month      time.Month // options: the contract month
	Strike     float64
	Right      string // CE / PE
}

var optRe = regexp.MustCompile(`^([A-Z0-9&\-_]+?)(\d{2})(JAN|FEB|MAR|APR|MAY|JUN|JUL|AUG|SEP|OCT|NOV|DEC)(\d+(?:\.\d+)?)(CE|PE)$`)

// ParseInstrument reads a stock or a monthly stock-option ticker.
func ParseInstrument(s string) Instrument {
	s = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "NSE:"), "nse:")))
	s = strings.TrimSuffix(s, "-EQ")
	if m := optRe.FindStringSubmatch(s); m != nil {
		yy, _ := strconv.Atoi(m[2])
		mon, _ := time.Parse("Jan", m[3][:1]+strings.ToLower(m[3][1:]))
		k, _ := strconv.ParseFloat(m[4], 64)
		return Instrument{Symbol: s, Underlying: m[1], Option: true, Year: 2000 + yy, Month: mon.Month(), Strike: k, Right: m[5]}
	}
	return Instrument{Symbol: s, Underlying: s}
}

// OptionTicker builds the Fyers-format monthly option ticker.
func OptionTicker(underlying string, expiry time.Time, strike float64, right string) string {
	return fmt.Sprintf("%s%02d%s%s%s", strings.ToUpper(underlying), expiry.Year()%100,
		strings.ToUpper(expiry.Format("Jan")), strconv.FormatFloat(strike, 'f', -1, 64), strings.ToUpper(right))
}

// TickSize is NSE's tick for a price (circulars CMTR67133 / FAOP67134,
// from 15 Apr 2025): stocks and stock futures by their own price; stock
// options by the underlying's price (FAOP70382: ₹0.01 below ₹250, else
// ₹0.05). Pass the option's underlying price as ref for options.
func TickSize(in Instrument, ref float64) float64 {
	if in.Option {
		if ref > 0 && ref < 250 {
			return 0.01
		}
		return 0.05
	}
	switch {
	case ref < 250:
		return 0.01
	case ref <= 1000:
		return 0.05
	case ref <= 5000:
		return 0.10
	case ref <= 10000:
		return 0.50
	case ref <= 20000:
		return 1.00
	}
	return 5.00
}

// RoundTick rounds p to the tick: down (for a long's stop or a sell limit
// that must not overreach) or up.
func RoundTick(p, tick float64, up bool) float64 {
	if tick <= 0 {
		return p
	}
	n := p / tick
	if up {
		n = math.Ceil(n - 1e-9)
	} else {
		n = math.Floor(n + 1e-9)
	}
	return math.Round(n*tick*100) / 100
}

// OnTick reports whether p is a multiple of tick.
func OnTick(p, tick float64) bool {
	if tick <= 0 {
		return true
	}
	n := p / tick
	return math.Abs(n-math.Round(n)) < 1e-6
}

// Rupees formats an amount the Indian way: ₹12,34,567.
func Rupees(v float64) string {
	neg := v < 0
	n := int64(math.Round(math.Abs(v)))
	s := strconv.FormatInt(n, 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	if neg {
		return "−₹" + s
	}
	return "₹" + s
}

func dayOf(t time.Time) time.Time {
	t = t.In(IST)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, IST)
}
