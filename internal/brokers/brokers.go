// Package brokers places and tracks LIVE orders with Indian brokers other
// than Fyers (book.FyersMonitor): Upstox, Zerodha (Kite Connect), Dhan and
// Angel One (SmartAPI). Each adapter implements book.Broker, so the position
// book's safety applies unchanged whichever broker is used: PENDING until a
// fill is confirmed, partial fills, rejected exits leaving the position
// open, reconciliation against the broker's net positions, the stop monitor.
//
// Rules every adapter keeps (CLAUDE.md):
//   - LIMIT orders only, stock options only, carry-forward product;
//   - placement and cancellation are sent exactly once — no retry. When the
//     answer is unclear (timeout, broken connection, 5xx) the order is looked
//     up by its tag before anything is reported; it is never resent;
//   - a fill is reported only with the broker's filled quantity and average
//     price.
//
// These adapters follow each broker's published API (checked Nov 2026) and
// are tested against recorded response shapes, not against live accounts.
package brokers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/book"
	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// Names lists the brokers that can place orders.
func Names() []string { return []string{"fyers", "upstox", "zerodha", "dhan", "angelone"} }

// errUnclear marks an answer that does not say whether the order exists.
var errUnclear = errors.New("unclear answer from the broker")

// client is the HTTP side every adapter shares: one attempt per call, a
// minimum gap between calls.
type client struct {
	HTTP *http.Client
	Gap  time.Duration
	mu   sync.Mutex
	last time.Time
}

func (c *client) wait() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Gap == 0 {
		c.Gap = 250 * time.Millisecond
	}
	if d := c.Gap - time.Since(c.last); d > 0 {
		time.Sleep(d)
	}
	c.last = time.Now()
}

// do sends one request, once. A transport error or a 5xx is errUnclear; a
// 4xx is a definite refusal carrying the broker's message.
func (c *client) do(method, url string, headers map[string]string, body []byte, contentType string) ([]byte, error) {
	c.wait()
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUnclear, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUnclear, err)
	}
	switch {
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: HTTP %d %s", errUnclear, resp.StatusCode, short(data))
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, brokerMessage(data))
	}
	return data, nil
}

// brokerMessage pulls a human message out of an error body.
func brokerMessage(data []byte) string {
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		for _, k := range []string{"message", "errorMessage", "error_message", "text"} {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
		if errs, ok := m["errors"].([]any); ok && len(errs) > 0 {
			if e, ok := errs[0].(map[string]any); ok {
				if s, ok := e["message"].(string); ok {
					return s
				}
			}
		}
	}
	return short(data)
}

func short(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

var tagSeq atomic.Int64

// newTag is a short alphanumeric order tag (Zerodha and Angel One allow 20
// characters), unique enough to find an unacknowledged order again.
func newTag() string {
	return "dk" + strconv.FormatInt(time.Now().UnixMilli(), 36) + strconv.FormatInt(tagSeq.Add(1)%1296, 36)
}

// placeOnce runs a placement and, when the answer was unclear, looks the
// order up by its tag. It never places twice.
func placeOnce(place func() (string, error), findByTag func() (string, error)) (string, error) {
	id, err := place()
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, errUnclear) {
		return "", fmt.Errorf("order rejected: %w", err)
	}
	if found, ferr := findByTag(); ferr == nil && found != "" {
		return found, nil
	}
	if err == nil {
		err = errors.New("no order id in the answer")
	}
	return "", fmt.Errorf("order not acknowledged (%v) — check the broker's order book; reconciliation will flag it if it went through", err)
}

// status maps a broker's order status word onto the book's codes.
func status(word string) int {
	switch strings.ToUpper(strings.TrimSpace(word)) {
	case "COMPLETE", "TRADED", "FILLED", "EXECUTED":
		return fyers.OrderFilled
	case "REJECTED":
		return fyers.OrderRejected
	case "CANCELLED", "CANCELED", "CANCELLED AFTER MARKET ORDER":
		return fyers.OrderCancelled
	case "EXPIRED":
		return fyers.OrderExpired
	case "TRANSIT", "PUT ORDER REQ RECEIVED", "VALIDATION PENDING", "OPEN PENDING":
		return fyers.OrderTransit
	}
	return fyers.OrderPending // open, part-traded, trigger pending, modify/cancel pending…
}

// optionOnly refuses anything but a stock option.
func optionOnly(ticker string) (desk.Instrument, error) {
	in := desk.ParseInstrument(ticker)
	if !in.Option {
		return in, fmt.Errorf("%s is not a stock option — live orders are options-only", ticker)
	}
	return in, nil
}

// num reads a JSON number that may come as a string.
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}

// Options holds every broker's credentials; each adapter reads its own.
type Options struct {
	CacheDir string // instrument files

	UpstoxToken string

	KiteAPIKey, KiteToken string

	DhanClientID, DhanToken string

	AngelAPIKey, AngelJWT string
}

// Open returns the order broker by name ("fyers" is book.FyersMonitor and is
// built by the caller).
func Open(name string, o Options) (book.Broker, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "upstox":
		if o.UpstoxToken == "" {
			return nil, errors.New("UPSTOX_ACCESS_TOKEN not set")
		}
		return NewUpstox(o.UpstoxToken, o.CacheDir), nil
	case "zerodha", "kite":
		if o.KiteAPIKey == "" || o.KiteToken == "" {
			return nil, errors.New("KITE_API_KEY / KITE_ACCESS_TOKEN not set")
		}
		return NewKite(o.KiteAPIKey, o.KiteToken), nil
	case "dhan":
		if o.DhanClientID == "" || o.DhanToken == "" {
			return nil, errors.New("DHAN_CLIENT_ID / DHAN_ACCESS_TOKEN not set")
		}
		return NewDhan(o.DhanClientID, o.DhanToken, o.CacheDir), nil
	case "angelone", "angel":
		if o.AngelAPIKey == "" || o.AngelJWT == "" {
			return nil, errors.New("ANGEL_API_KEY not set, or no Angel One login (desk login angelone)")
		}
		return NewAngel(o.AngelAPIKey, o.AngelJWT, o.CacheDir), nil
	}
	return nil, fmt.Errorf("unknown order broker %q (fyers, upstox, zerodha, dhan, angelone)", name)
}
