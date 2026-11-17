package desk

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Session times (IST) for NSE's normal market.
const (
	PreOpen    = "09:00" // pre-open call auction 09:00–09:08
	MarketOpen = "09:15"
	MarketEnd  = "15:30"
)

// Holiday is one NSE trading holiday.
type Holiday struct {
	Date        time.Time `json:"date"`
	Description string    `json:"description"`
}

// Calendar knows NSE's trading days. Holidays come from NSE's holiday
// master (cash market "CM" and F&O "FO"; they are the same days in
// practice), cached for a week.
type Calendar struct {
	CM, FO []Holiday
	set    map[string]Holiday
}

// LoadCalendar reads NSE's holiday list; on failure the caller gets an
// error and should say holidays are unknown rather than guess.
func LoadCalendar(n *NSE) (*Calendar, error) {
	data, err := n.API("holiday-master?type=trading", 7*24*time.Hour)
	if err != nil {
		return nil, err
	}
	return ParseHolidays(data)
}

// ParseHolidays reads the holiday-master JSON.
func ParseHolidays(data []byte) (*Calendar, error) {
	var raw map[string][]struct {
		TradingDate string `json:"tradingDate"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("NSE holidays: %w", err)
	}
	c := &Calendar{set: map[string]Holiday{}}
	conv := func(seg string) []Holiday {
		var out []Holiday
		for _, h := range raw[seg] {
			d, err := time.ParseInLocation("02-Jan-2006", h.TradingDate, IST)
			if err != nil {
				continue
			}
			out = append(out, Holiday{Date: d, Description: strings.TrimSpace(h.Description)})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
		return out
	}
	c.CM, c.FO = conv("CM"), conv("FO")
	if len(c.CM) == 0 && len(c.FO) == 0 {
		return nil, fmt.Errorf("NSE holidays: no CM or FO list in the response")
	}
	for _, h := range append(append([]Holiday{}, c.CM...), c.FO...) {
		c.set[h.Date.Format("2006-01-02")] = h
	}
	return c, nil
}

// Holiday reports whether day is an NSE holiday, and which.
func (c *Calendar) Holiday(day time.Time) (Holiday, bool) {
	if c == nil {
		return Holiday{}, false
	}
	h, ok := c.set[dayOf(day).Format("2006-01-02")]
	return h, ok
}

// TradingDay reports a weekday that is not a holiday. A nil calendar knows
// only weekends.
func (c *Calendar) TradingDay(day time.Time) bool {
	d := dayOf(day)
	if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	_, hol := c.Holiday(d)
	return !hol
}

// Status is the market state at t.
type Status struct {
	Open     bool
	State    string    // "open", "pre-open", "closed — <why>"
	NextOpen time.Time // the next 09:15 session start (or now, while open)
	Closes   time.Time // today's 15:30 when open
}

// At says whether the market is open at t.
func (c *Calendar) At(t time.Time) Status {
	t = t.In(IST)
	hm := t.Format("15:04")
	if c.TradingDay(t) {
		switch {
		case hm >= MarketOpen && hm < MarketEnd:
			return Status{Open: true, State: "open", NextOpen: t, Closes: at(t, MarketEnd)}
		case hm >= PreOpen && hm < MarketOpen:
			return Status{State: "pre-open (call auction until 09:08)", NextOpen: at(t, MarketOpen)}
		case hm < PreOpen:
			return Status{State: "closed — opens at 09:15", NextOpen: at(t, MarketOpen)}
		}
	}
	why := "after the close"
	if h, ok := c.Holiday(t); ok {
		why = "holiday: " + h.Description
	} else if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		why = "weekend"
	}
	d := dayOf(t).AddDate(0, 0, 1)
	for i := 0; i < 15 && !c.TradingDay(d); i++ {
		d = d.AddDate(0, 0, 1)
	}
	return Status{State: "closed — " + why, NextOpen: at(d, MarketOpen)}
}

// SessionsBetween counts trading days in (from, to].
func (c *Calendar) SessionsBetween(from, to time.Time) int {
	n := 0
	for d := dayOf(from).AddDate(0, 0, 1); !d.After(dayOf(to)); d = d.AddDate(0, 0, 1) {
		if c.TradingDay(d) {
			n++
		}
	}
	return n
}

// Upcoming lists holidays from day on.
func (c *Calendar) Upcoming(day time.Time, n int) []Holiday {
	var out []Holiday
	if c == nil {
		return nil
	}
	for _, h := range c.CM {
		if !h.Date.Before(dayOf(day)) && len(out) < n {
			out = append(out, h)
		}
	}
	return out
}

// at is clock "HH:MM" on t's day, IST.
func at(t time.Time, hhmm string) time.Time {
	d := dayOf(t)
	var h, m int
	fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	return d.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

// Clock parses "HH:MM" on t's day.
func Clock(t time.Time, hhmm string) time.Time { return at(t, hhmm) }
