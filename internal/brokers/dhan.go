package brokers

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/book"
	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
)

// Dhan places LIMIT option orders through DhanHQ v2. Contracts are found by
// security id in Dhan's scrip master. Dhan requires a whitelisted static IP
// for order placement and cancellation.
// Docs: https://dhanhq.co/docs/v2/orders/
type Dhan struct {
	ClientID, Token string
	API             string // https://api.dhan.co/v2
	MasterURL       string
	CacheDir        string
	c               client

	once sync.Once
	ids  map[string]string // desk ticker → security id
	back map[string]string
	err  error
}

func NewDhan(clientID, token, cacheDir string) *Dhan {
	return &Dhan{ClientID: clientID, Token: token, API: "https://api.dhan.co/v2",
		MasterURL: "https://images.dhan.co/api-data/api-scrip-master.csv", CacheDir: cacheDir}
}

func (d *Dhan) hdr() map[string]string {
	return map[string]string{"access-token": d.Token, "client-id": d.ClientID}
}

func (d *Dhan) load() error {
	d.once.Do(func() {
		data, err := cachedFile(d.MasterURL, d.CacheDir, "dhan_scrip_master.csv")
		if err != nil {
			d.err = err
			return
		}
		d.ids, d.err = parseDhanMaster(data)
		d.back = map[string]string{}
		for t, id := range d.ids {
			d.back[id] = t
		}
	})
	return d.err
}

// parseDhanMaster keeps NSE stock options: RELIANCE-Nov2026-1400-CE → id.
func parseDhanMaster(data []byte) (map[string]string, error) {
	cr := csv.NewReader(strings.NewReader(string(data)))
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("dhan scrip master: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	for _, n := range []string{"SEM_EXM_EXCH_ID", "SEM_SMST_SECURITY_ID", "SEM_INSTRUMENT_NAME", "SEM_TRADING_SYMBOL", "SEM_EXPIRY_DATE", "SEM_STRIKE_PRICE", "SEM_OPTION_TYPE"} {
		if _, ok := col[n]; !ok {
			return nil, fmt.Errorf("dhan scrip master: no %s column", n)
		}
	}
	out := map[string]string{}
	for {
		r, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(r) < len(head) {
			continue
		}
		if r[col["SEM_EXM_EXCH_ID"]] != "NSE" || r[col["SEM_INSTRUMENT_NAME"]] != "OPTSTK" {
			continue
		}
		under := strings.SplitN(r[col["SEM_TRADING_SYMBOL"]], "-", 2)[0]
		exp, err := time.Parse("2006-01-02", strings.SplitN(r[col["SEM_EXPIRY_DATE"]], " ", 2)[0])
		if err != nil {
			continue
		}
		k, _ := strconv.ParseFloat(r[col["SEM_STRIKE_PRICE"]], 64)
		right := r[col["SEM_OPTION_TYPE"]]
		if under == "" || k <= 0 || (right != "CE" && right != "PE") {
			continue
		}
		out[desk.OptionTicker(under, exp, k, right)] = r[col["SEM_SMST_SECURITY_ID"]]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("dhan scrip master: no NSE stock options found")
	}
	return out, nil
}

func (d *Dhan) PlaceLimit(ticker string, qty int, limit float64, sell bool) (string, error) {
	in, err := optionOnly(ticker)
	if err != nil {
		return "", err
	}
	if err := d.load(); err != nil {
		return "", err
	}
	id, ok := d.ids[in.Symbol]
	if !ok {
		return "", fmt.Errorf("dhan has no contract %s", in.Symbol)
	}
	side := "BUY"
	if sell {
		side = "SELL"
	}
	tag := newTag()
	body, _ := json.Marshal(map[string]any{
		"dhanClientId": d.ClientID, "correlationId": tag, "transactionType": side,
		"exchangeSegment": "NSE_FNO", "productType": "MARGIN", "orderType": "LIMIT", "validity": "DAY",
		"securityId": id, "quantity": qty, "price": limit, "afterMarketOrder": false,
	})
	return placeOnce(func() (string, error) {
		data, err := d.c.do("POST", d.API+"/orders", d.hdr(), body, "application/json")
		if err != nil {
			return "", err
		}
		var r struct {
			OrderID     string `json:"orderId"`
			OrderStatus string `json:"orderStatus"`
		}
		if json.Unmarshal(data, &r) != nil {
			return "", fmt.Errorf("%w: %s", errUnclear, short(data))
		}
		if strings.EqualFold(r.OrderStatus, "REJECTED") {
			return "", fmt.Errorf("rejected (order %s)", r.OrderID)
		}
		return r.OrderID, nil
	}, func() (string, error) {
		o, err := d.get("/orders/external/" + url.PathEscape(tag))
		return o.OrderID, err
	})
}

type dhanOrder struct {
	OrderID   string  `json:"orderId"`
	Status    string  `json:"orderStatus"`
	Filled    int     `json:"filledQty"`
	Remaining int     `json:"remainingQuantity"`
	AvgPrice  float64 `json:"averageTradedPrice"`
	ErrDesc   string  `json:"omsErrorDescription"`
}

func (d *Dhan) get(path string) (dhanOrder, error) {
	data, err := d.c.do("GET", d.API+path, d.hdr(), nil, "")
	if err != nil {
		return dhanOrder{}, err
	}
	var o dhanOrder
	if err := json.Unmarshal(data, &o); err != nil {
		var list []dhanOrder // some answers come as a one-element list
		if json.Unmarshal(data, &list) == nil && len(list) > 0 {
			return list[0], nil
		}
		return dhanOrder{}, fmt.Errorf("dhan order: %w", err)
	}
	return o, nil
}

func (d *Dhan) Order(id string) (book.OrderState, error) {
	o, err := d.get("/orders/" + url.PathEscape(id))
	if err != nil {
		return book.OrderState{}, err
	}
	return book.OrderState{Status: status(o.Status), Filled: o.Filled, Remaining: o.Remaining, AvgPrice: o.AvgPrice, Message: o.ErrDesc}, nil
}

func (d *Dhan) Cancel(id string) error {
	_, err := d.c.do("DELETE", d.API+"/orders/"+url.PathEscape(id), d.hdr(), nil, "")
	return err
}

func (d *Dhan) NetPositions() (map[string]int, error) {
	if err := d.load(); err != nil {
		return nil, err
	}
	data, err := d.c.do("GET", d.API+"/positions", d.hdr(), nil, "")
	if err != nil {
		return nil, err
	}
	var ps []struct {
		SecurityID string `json:"securityId"`
		Segment    string `json:"exchangeSegment"`
		NetQty     int    `json:"netQty"`
	}
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("dhan positions: %w", err)
	}
	out := map[string]int{}
	for _, p := range ps {
		if t, ok := d.back[p.SecurityID]; ok && p.Segment == "NSE_FNO" {
			out[t] += p.NetQty
		}
	}
	return out, nil
}

// cachedFile downloads a broker's instrument file once a day.
func cachedFile(u, dir, name string) ([]byte, error) {
	path := ""
	if dir != "" {
		path = filepath.Join(dir, name)
		if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) < 20*time.Hour {
			return os.ReadFile(path)
		}
	}
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	if path != "" && os.MkdirAll(dir, 0o755) == nil {
		os.WriteFile(path, data, 0o644)
	}
	return data, nil
}
