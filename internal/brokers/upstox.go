package brokers

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/amitchhatbar11/indian-trader-skills/internal/book"
	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
)

// Upstox places LIMIT option orders through Upstox's v2 order API.
// Docs: https://upstox.com/developer/api-documentation/place-order
type Upstox struct {
	Token string
	API   string // https://api.upstox.com (reads)
	HFT   string // https://api-hft.upstox.com (place, cancel)
	keys  *desk.UpstoxFeed
	c     client
}

func NewUpstox(token, cacheDir string) *Upstox {
	return &Upstox{Token: token, API: "https://api.upstox.com", HFT: "https://api-hft.upstox.com",
		keys: &desk.UpstoxFeed{Token: token, CacheDir: cacheDir}}
}

func (u *Upstox) hdr() map[string]string {
	return map[string]string{"Authorization": "Bearer " + u.Token}
}

func (u *Upstox) key(ticker string) (string, error) {
	ks, err := u.keys.InstrumentKeys()
	if err != nil {
		return "", fmt.Errorf("upstox instruments: %w", err)
	}
	k, ok := ks[desk.ParseInstrument(ticker).Symbol]
	if !ok {
		return "", fmt.Errorf("upstox has no instrument %s", ticker)
	}
	return k, nil
}

func (u *Upstox) PlaceLimit(ticker string, qty int, limit float64, sell bool) (string, error) {
	if _, err := optionOnly(ticker); err != nil {
		return "", err
	}
	key, err := u.key(ticker)
	if err != nil {
		return "", err
	}
	side := "BUY"
	if sell {
		side = "SELL"
	}
	tag := newTag()
	body, _ := json.Marshal(map[string]any{
		"quantity": qty, "product": "D", "validity": "DAY", "price": limit, "tag": tag,
		"instrument_token": key, "order_type": "LIMIT", "transaction_type": side,
		"disclosed_quantity": 0, "trigger_price": 0, "is_amo": false,
	})
	return placeOnce(func() (string, error) {
		data, err := u.c.do("POST", u.HFT+"/v2/order/place", u.hdr(), body, "application/json")
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
	}, func() (string, error) { return u.findByTag(tag) })
}

type upstoxOrder struct {
	OrderID   string  `json:"order_id"`
	Status    string  `json:"status"`
	Tag       string  `json:"tag"`
	Filled    int     `json:"filled_quantity"`
	Pending   int     `json:"pending_quantity"`
	AvgPrice  float64 `json:"average_price"`
	StatusMsg string  `json:"status_message"`
}

func (u *Upstox) findByTag(tag string) (string, error) {
	data, err := u.c.do("GET", u.API+"/v2/order/retrieve-all", u.hdr(), nil, "")
	if err != nil {
		return "", err
	}
	var r struct {
		Data []upstoxOrder `json:"data"`
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

func (u *Upstox) Order(id string) (book.OrderState, error) {
	data, err := u.c.do("GET", u.API+"/v2/order/details?order_id="+url.QueryEscape(id), u.hdr(), nil, "")
	if err != nil {
		return book.OrderState{}, err
	}
	var r struct {
		Data upstoxOrder `json:"data"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return book.OrderState{}, fmt.Errorf("upstox order: %w", err)
	}
	o := r.Data
	return book.OrderState{Status: status(o.Status), Filled: o.Filled, Remaining: o.Pending, AvgPrice: o.AvgPrice, Message: o.StatusMsg}, nil
}

func (u *Upstox) Cancel(id string) error {
	_, err := u.c.do("DELETE", u.HFT+"/v2/order/cancel?order_id="+url.QueryEscape(id), u.hdr(), nil, "")
	return err
}

func (u *Upstox) NetPositions() (map[string]int, error) {
	data, err := u.c.do("GET", u.API+"/v2/portfolio/short-term-positions", u.hdr(), nil, "")
	if err != nil {
		return nil, err
	}
	var r struct {
		Data []struct {
			Key string `json:"instrument_token"`
			Qty int    `json:"quantity"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("upstox positions: %w", err)
	}
	ks, err := u.keys.InstrumentKeys()
	if err != nil {
		return nil, err
	}
	back := map[string]string{}
	for sym, k := range ks {
		back[k] = sym
	}
	out := map[string]int{}
	for _, p := range r.Data {
		if sym, ok := back[p.Key]; ok && desk.ParseInstrument(sym).Option {
			out[sym] += p.Qty
		}
	}
	return out, nil
}
