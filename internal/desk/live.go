package desk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Live orders.
//
// The desk itself never talks to a broker's order API. A LIVE order is
// handed to the live engine — `desk serve`
// — whose position book places it and tracks it to the fill: PENDING until
// the broker confirms, partial fills, rejected exits leaving the position
// open, broker reconciliation and the stop monitor. The hand-off is one
// local HTTP request:
//
//   - only to 127.0.0.1, with the token the engine writes to live.token in
//     the desk folder when DESK_LIVE=true;
//   - stock options only, LIMIT only, whole lots;
//   - only an order the user approved with a clear yes, which travels with
//     the request and is checked again by the live engine;
//   - sent exactly once: an unclear answer (timeout, broken connection) is
//     reported as UNKNOWN and must be checked, never resent automatically.

// LiveRequest is an approved order handed to the live engine.
type LiveRequest struct {
	ID          string  `json:"id"`
	Fingerprint string  `json:"fingerprint"`
	Symbol      string  `json:"symbol"`
	Side        string  `json:"side"` // buy / sell
	Qty         int     `json:"qty"`
	LotSize     int     `json:"lot_size"`
	Limit       float64 `json:"limit"`
	Stop        float64 `json:"stop"`
	Target      float64 `json:"target"`
	Reply       string  `json:"reply"` // the user's yes, word for word
}

// LiveExit asks the live engine to close a desk position with a LIMIT order.
type LiveExit struct {
	Symbol string  `json:"symbol"`
	Limit  float64 `json:"limit"`
	Reply  string  `json:"reply"`
}

// LiveResult is the live engine's answer.
type LiveResult struct {
	OK       bool     `json:"ok"`
	OrderID  string   `json:"order_id,omitempty"`
	State    string   `json:"state,omitempty"` // PENDING_ENTRY / PENDING_EXIT / …
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// LivePosition is a desk position in the live engine's book.
type LivePosition struct {
	Symbol  string    `json:"symbol"`
	Side    string    `json:"side"`
	Qty     int       `json:"qty"`
	Entry   float64   `json:"entry"`
	LTP     float64   `json:"ltp"`
	Stop    float64   `json:"stop"`
	Target  float64   `json:"target"`
	State   string    `json:"state"`
	OrderID string    `json:"order_id"`
	Opened  time.Time `json:"opened"`
	PnL     float64   `json:"pnl"`
	Closed  bool      `json:"closed"`
	Reason  string    `json:"reason,omitempty"`
}

// ErrUnknown means the request may or may not have reached the broker.
var ErrUnknown = errors.New("no clear answer from the live engine — the order MAY have been placed; check `desk broker live` and the Fyers order book before doing anything else, and do not resend")

// Validate is the shape check both sides run on a live request.
func (r LiveRequest) Validate() error {
	in := ParseInstrument(r.Symbol)
	switch {
	case !in.Option:
		return fmt.Errorf("%s is not a stock option — live orders are options-only for now", r.Symbol)
	case r.Side != "buy" && r.Side != "sell":
		return fmt.Errorf("side %q", r.Side)
	case r.LotSize <= 0 || r.Qty <= 0 || r.Qty%r.LotSize != 0:
		return fmt.Errorf("quantity %d is not a whole number of %d-unit lots", r.Qty, r.LotSize)
	case r.Limit <= 0:
		return errors.New("no limit price (only LIMIT orders are sent)")
	case r.Stop <= 0:
		return errors.New("no stop: a live order must carry its stop")
	case r.Side == "buy" && r.Stop >= r.Limit || r.Side == "sell" && r.Stop <= r.Limit:
		return fmt.Errorf("stop ₹%.2f is on the wrong side of the limit ₹%.2f", r.Stop, r.Limit)
	case !IsYes(r.Reply):
		return fmt.Errorf("no clear yes from the user (%q)", r.Reply)
	}
	return nil
}

// LiveClient hands orders to the live engine.
type LiveClient struct {
	URL   string // e.g. http://127.0.0.1:8505
	Token string
	HTTP  *http.Client
}

// NewLiveClient reads the token the live engine wrote.
func NewLiveClient(url, tokenFile string) (*LiveClient, error) {
	b, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("no live token at %s — is the live engine (desk serve) running with DESK_LIVE=true? (%v)", tokenFile, err)
	}
	return &LiveClient{URL: strings.TrimRight(url, "/"), Token: strings.TrimSpace(string(b)),
		HTTP: &http.Client{Timeout: 45 * time.Second}}, nil
}

// Submit sends an entry once.
func (c *LiveClient) Submit(r LiveRequest) (LiveResult, error) {
	if err := r.Validate(); err != nil {
		return LiveResult{}, err
	}
	return c.post("/api/desk/order", r)
}

// Exit sends an exit once.
func (c *LiveClient) Exit(x LiveExit) (LiveResult, error) {
	if !IsYes(x.Reply) {
		return LiveResult{}, fmt.Errorf("no clear yes from the user (%q)", x.Reply)
	}
	if x.Limit <= 0 {
		return LiveResult{}, errors.New("no limit price")
	}
	return c.post("/api/desk/exit", x)
}

// Positions lists the desk's live positions and today's closed ones.
func (c *LiveClient) Positions() ([]LivePosition, error) {
	req, _ := http.NewRequest(http.MethodGet, c.URL+"/api/desk/positions", nil)
	req.Header.Set("X-Desk-Token", c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("live engine not reachable at %s: %w", c.URL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("live engine: HTTP %d %s", resp.StatusCode, snippet(body))
	}
	var ps []LivePosition
	return ps, json.Unmarshal(body, &ps)
}

func (c *LiveClient) post(path string, v any) (LiveResult, error) {
	body, _ := json.Marshal(v)
	req, _ := http.NewRequest(http.MethodPost, c.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Desk-Token", c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Refused before anything was sent is clear; anything else is not.
		if strings.Contains(err.Error(), "connection refused") {
			return LiveResult{}, fmt.Errorf("live engine not running at %s — nothing was sent", c.URL)
		}
		return LiveResult{}, ErrUnknown
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return LiveResult{}, ErrUnknown
	}
	var res LiveResult
	if json.Unmarshal(data, &res) != nil {
		if resp.StatusCode >= 500 {
			return LiveResult{}, ErrUnknown
		}
		return LiveResult{}, fmt.Errorf("live engine: HTTP %d %s", resp.StatusCode, snippet(data))
	}
	if !res.OK {
		if res.Error == "" {
			res.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return res, errors.New(res.Error)
	}
	return res, nil
}
