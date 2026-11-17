package fyers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// GetExpiredCandles fetches candles for an expired F&O contract through
// Fyers' expired-F&O API (intraday resolutions need the Prime plan). symbol
// is without the "NSE:" prefix, e.g. "RELIANCE24AUG3000CE". Intraday
// requests may span up to 100 days — a stock option's whole life — and NSE
// history starts on 2018-10-10. A valid request with no candles ("no_data")
// is an empty result, not an error. Read-only, and through the same
// throttle as every other call.
func (c *FyersClient) GetExpiredCandles(symbol, resolution string, from, to time.Time) ([]Candle, error) {
	q := url.Values{}
	q.Set("symbol", "NSE:"+symbol)
	q.Set("resolution", resolution)
	q.Set("date_format", "1")
	q.Set("range_from", from.Format("2006-01-02"))
	q.Set("range_to", to.Format("2006-01-02"))
	data, err := c.doDataRequest("GET", "/history/fno/expired/historical-data?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	return parseExpired(data)
}

func parseExpired(data []byte) ([]Candle, error) {
	var resp struct {
		Status  string          `json:"s"`
		Message string          `json:"message"`
		Candles [][]json.Number `json:"candles"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("expired history parse failed: %w", err)
	}
	switch resp.Status {
	case "ok":
	case "no_data":
		return nil, nil
	default:
		return nil, fmt.Errorf("expired history API returned status %q: %s", resp.Status, resp.Message)
	}
	out := make([]Candle, 0, len(resp.Candles))
	for _, row := range resp.Candles {
		if len(row) < 6 {
			continue
		}
		var v [6]float64
		ok := true
		for i := 0; i < 6; i++ {
			f, err := row[i].Float64()
			if err != nil {
				ok = false
				break
			}
			v[i] = f
		}
		if ok {
			out = append(out, Candle{Time: time.Unix(int64(v[0]), 0), Open: v[1], High: v[2], Low: v[3], Close: v[4], Volume: v[5]})
		}
	}
	return out, nil
}
