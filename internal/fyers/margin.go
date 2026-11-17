package fyers

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SpanURL is Fyers' SPAN margin calculator. It still lives on the v2 API host.
const SpanURL = "https://api.fyers.in/api/v2"

// MarginLeg is one leg of a basket for the margin calculator. Qty is in
// units (lots × lot size); Sell marks a short.
type MarginLeg struct {
	Symbol string // without the "NSE:" prefix
	Qty    int
	Sell   bool
}

// Margin is the calculator's answer for a whole basket: SPAN plus exposure,
// with any benefit from combining the legs already taken off Total.
type Margin struct {
	Span    float64 `json:"span"`
	Expo    float64 `json:"expo"`
	Total   float64 `json:"total"`
	Benefit float64 `json:"benefit"`
}

// SpanMargin asks Fyers' SPAN calculator what a basket of option legs would
// block. Read-only (it places nothing), at most 20 legs, and through the
// same throttle as every other call. The legs are priced as LIMIT orders at
// 0 — the calculator reads the premium itself — so no market order type is
// ever sent, not even to the calculator.
func (c *FyersClient) SpanMargin(legs []MarginLeg, productType string) (*Margin, error) {
	if len(legs) == 0 || len(legs) > 20 {
		return nil, fmt.Errorf("span margin: %d legs (1–20 allowed)", len(legs))
	}
	type item struct {
		Symbol      string  `json:"symbol"`
		Qty         int     `json:"qty"`
		Side        int     `json:"side"`
		Type        int     `json:"type"`
		ProductType string  `json:"productType"`
		LimitPrice  float64 `json:"limitPrice"`
		StopLoss    float64 `json:"stopLoss"`
	}
	req := struct {
		Data []item `json:"data"`
	}{}
	for _, l := range legs {
		side := 1
		if l.Sell {
			side = -1
		}
		req.Data = append(req.Data, item{Symbol: "NSE:" + l.Symbol, Qty: l.Qty, Side: side, Type: 1, ProductType: productType})
	}
	body, _ := json.Marshal(req)
	base := SpanURL
	if c.spanURL != "" {
		base = c.spanURL
	}
	data, err := c.doRequestBase(base, "POST", "/span_margin", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	var resp struct {
		S       string `json:"s"`
		Message string `json:"message"`
		Data    Margin `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("span margin parse failed: %w", err)
	}
	if resp.S != "ok" {
		return nil, fmt.Errorf("span margin: %s", resp.Message)
	}
	if resp.Data.Total <= 0 {
		return nil, fmt.Errorf("span margin: no total in the response")
	}
	return &resp.Data, nil
}
