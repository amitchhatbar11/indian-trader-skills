package brokers

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/amitchhatbar11/indian-trader-skills/internal/book"
	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
)

// Kite places LIMIT option orders through Zerodha's Kite Connect v3. Kite's
// monthly option symbols are the desk's tickers (RELIANCE26NOV1400CE) on NFO.
// Docs: https://kite.trade/docs/connect/v3/orders/
type Kite struct {
	APIKey, Token string
	API           string // https://api.kite.trade
	c             client
}

func NewKite(apiKey, token string) *Kite {
	return &Kite{APIKey: apiKey, Token: token, API: "https://api.kite.trade"}
}

func (k *Kite) hdr() map[string]string {
	return map[string]string{"X-Kite-Version": "3", "Authorization": "token " + k.APIKey + ":" + k.Token}
}

func (k *Kite) PlaceLimit(ticker string, qty int, limit float64, sell bool) (string, error) {
	in, err := optionOnly(ticker)
	if err != nil {
		return "", err
	}
	side := "BUY"
	if sell {
		side = "SELL"
	}
	tag := newTag()
	form := url.Values{
		"tradingsymbol": {in.Symbol}, "exchange": {"NFO"}, "transaction_type": {side},
		"order_type": {"LIMIT"}, "quantity": {fmt.Sprint(qty)}, "product": {"NRML"},
		"price": {fmt.Sprintf("%.2f", limit)}, "validity": {"DAY"}, "tag": {tag},
	}
	return placeOnce(func() (string, error) {
		data, err := k.c.do("POST", k.API+"/orders/regular", k.hdr(), []byte(form.Encode()), "application/x-www-form-urlencoded")
		if err != nil {
			return "", err
		}
		var r struct {
			Status string `json:"status"`
			Data   struct {
				OrderID string `json:"order_id"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &r) != nil || r.Status != "success" {
			return "", fmt.Errorf("%w: %s", errUnclear, short(data))
		}
		return r.Data.OrderID, nil
	}, func() (string, error) { return k.findByTag(tag) })
}

type kiteOrder struct {
	OrderID   string  `json:"order_id"`
	Status    string  `json:"status"`
	Tag       string  `json:"tag"`
	Filled    int     `json:"filled_quantity"`
	Pending   int     `json:"pending_quantity"`
	AvgPrice  float64 `json:"average_price"`
	StatusMsg string  `json:"status_message"`
}

func (k *Kite) findByTag(tag string) (string, error) {
	data, err := k.c.do("GET", k.API+"/orders", k.hdr(), nil, "")
	if err != nil {
		return "", err
	}
	var r struct {
		Data []kiteOrder `json:"data"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", err
	}
	for _, o := range r.Data {
		if o.Tag == tag {
			return o.OrderID, nil
		}
	}
	return "", nil
}

// Order reads the order's history; its last entry is the current state.
func (k *Kite) Order(id string) (book.OrderState, error) {
	data, err := k.c.do("GET", k.API+"/orders/"+url.PathEscape(id), k.hdr(), nil, "")
	if err != nil {
		return book.OrderState{}, err
	}
	var r struct {
		Data []kiteOrder `json:"data"`
	}
	if err := json.Unmarshal(data, &r); err != nil || len(r.Data) == 0 {
		return book.OrderState{}, fmt.Errorf("kite order %s: no history (%v)", id, err)
	}
	o := r.Data[len(r.Data)-1]
	return book.OrderState{Status: status(o.Status), Filled: o.Filled, Remaining: o.Pending, AvgPrice: o.AvgPrice, Message: o.StatusMsg}, nil
}

func (k *Kite) Cancel(id string) error {
	_, err := k.c.do("DELETE", k.API+"/orders/regular/"+url.PathEscape(id), k.hdr(), nil, "")
	return err
}

func (k *Kite) NetPositions() (map[string]int, error) {
	data, err := k.c.do("GET", k.API+"/portfolio/positions", k.hdr(), nil, "")
	if err != nil {
		return nil, err
	}
	var r struct {
		Data struct {
			Net []struct {
				Symbol   string `json:"tradingsymbol"`
				Exchange string `json:"exchange"`
				Qty      int    `json:"quantity"`
			} `json:"net"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("kite positions: %w", err)
	}
	out := map[string]int{}
	for _, p := range r.Data.Net {
		if in := desk.ParseInstrument(p.Symbol); p.Exchange == "NFO" && in.Option {
			out[in.Symbol] += p.Qty
		}
	}
	return out, nil
}
