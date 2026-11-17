package brokers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/book"
	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
)

// Angel places LIMIT option orders through Angel One SmartAPI. Contracts are
// found by token and trading symbol in Angel's scrip master (strikes there
// are ×100; option symbols carry the expiry day). The session JWT comes from
// the user's own login (AngelLogin), never from the agent.
// Docs: https://smartapi.angelone.in/docs
type Angel struct {
	APIKey, JWT string
	API         string // https://apiconnect.angelone.in
	MasterURL   string
	CacheDir    string
	c           client

	once sync.Once
	byT  map[string]angelScrip // desk ticker → scrip
	back map[string]string     // token → desk ticker
	err  error
}

type angelScrip struct {
	Token  string `json:"token"`
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
	Expiry string `json:"expiry"`
	Strike string `json:"strike"`
	Type   string `json:"instrumenttype"`
	Seg    string `json:"exch_seg"`
}

// angelBaseURL is SmartAPI's host (a variable so tests can point it elsewhere).
var angelBaseURL = "https://apiconnect.angelone.in"

func NewAngel(apiKey, jwt, cacheDir string) *Angel {
	return &Angel{APIKey: apiKey, JWT: jwt, API: angelBaseURL,
		MasterURL: "https://margincalculator.angelbroking.com/OpenAPI_File/files/OpenAPIScripMaster.json", CacheDir: cacheDir}
}

func angelHeaders(apiKey string) map[string]string {
	return map[string]string{"X-UserType": "USER", "X-SourceID": "WEB", "X-ClientLocalIP": "127.0.0.1",
		"X-ClientPublicIP": "127.0.0.1", "X-MACAddress": "00:00:00:00:00:00", "X-PrivateKey": apiKey}
}

func (a *Angel) hdr() map[string]string {
	h := angelHeaders(a.APIKey)
	h["Authorization"] = "Bearer " + a.JWT
	return h
}

func (a *Angel) load() error {
	a.once.Do(func() {
		data, err := cachedFile(a.MasterURL, a.CacheDir, "angel_scrip_master.json")
		if err != nil {
			a.err = err
			return
		}
		a.byT, a.err = parseAngelMaster(data)
		a.back = map[string]string{}
		for t, s := range a.byT {
			a.back[s.Token] = t
		}
	})
	return a.err
}

// parseAngelMaster keeps NFO stock options under desk tickers.
func parseAngelMaster(data []byte) (map[string]angelScrip, error) {
	var list []angelScrip
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("angel scrip master: %w", err)
	}
	out := map[string]angelScrip{}
	for _, s := range list {
		if s.Seg != "NFO" || s.Type != "OPTSTK" {
			continue
		}
		exp, err := parseAngelExpiry(s.Expiry)
		if err != nil {
			continue
		}
		k, _ := strconv.ParseFloat(s.Strike, 64)
		right := s.Symbol[max(0, len(s.Symbol)-2):]
		if k <= 0 || (right != "CE" && right != "PE") {
			continue
		}
		out[desk.OptionTicker(s.Name, exp, k/100, right)] = s
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("angel scrip master: no NFO stock options found")
	}
	return out, nil
}

// parseAngelExpiry reads "23NOV2026".
func parseAngelExpiry(s string) (time.Time, error) {
	if len(s) != 9 {
		return time.Time{}, fmt.Errorf("expiry %q", s)
	}
	return time.Parse("02Jan2006", s[:2]+s[2:3]+strings.ToLower(s[3:5])+s[5:])
}

// angelResp is SmartAPI's envelope.
type angelResp struct {
	Status    bool            `json:"status"`
	Message   string          `json:"message"`
	ErrorCode string          `json:"errorcode"`
	Data      json.RawMessage `json:"data"`
}

func (a *Angel) call(method, path string, body any) (json.RawMessage, error) {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	data, err := a.c.do(method, a.API+path, a.hdr(), b, "application/json")
	if err != nil {
		return nil, err
	}
	var r angelResp
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%w: %s", errUnclear, short(data))
	}
	if !r.Status {
		return nil, fmt.Errorf("angel one: %s %s", r.ErrorCode, r.Message)
	}
	return r.Data, nil
}

func (a *Angel) PlaceLimit(ticker string, qty int, limit float64, sell bool) (string, error) {
	in, err := optionOnly(ticker)
	if err != nil {
		return "", err
	}
	if err := a.load(); err != nil {
		return "", err
	}
	s, ok := a.byT[in.Symbol]
	if !ok {
		return "", fmt.Errorf("angel one has no contract %s", in.Symbol)
	}
	side := "BUY"
	if sell {
		side = "SELL"
	}
	tag := newTag()
	body := map[string]any{
		"variety": "NORMAL", "tradingsymbol": s.Symbol, "symboltoken": s.Token, "transactiontype": side,
		"exchange": "NFO", "ordertype": "LIMIT", "producttype": "CARRYFORWARD", "duration": "DAY",
		"price": fmt.Sprintf("%.2f", limit), "quantity": strconv.Itoa(qty), "ordertag": tag,
	}
	return placeOnce(func() (string, error) {
		data, err := a.call("POST", "/rest/secure/angelbroking/order/v1/placeOrder", body)
		if err != nil {
			return "", err
		}
		var d struct {
			OrderID string `json:"orderid"`
		}
		if json.Unmarshal(data, &d) != nil {
			return "", fmt.Errorf("%w: no order id", errUnclear)
		}
		return d.OrderID, nil
	}, func() (string, error) {
		os, err := a.orders()
		for _, o := range os {
			if o.Tag == tag {
				return o.OrderID, nil
			}
		}
		return "", err
	})
}

type angelOrder struct {
	OrderID  string `json:"orderid"`
	Tag      string `json:"ordertag"`
	Status   string `json:"status"`
	Filled   any    `json:"filledshares"`
	Unfilled any    `json:"unfilledshares"`
	AvgPrice any    `json:"averageprice"`
	Text     string `json:"text"`
}

func (a *Angel) orders() ([]angelOrder, error) {
	data, err := a.call("GET", "/rest/secure/angelbroking/order/v1/getOrderBook", nil)
	if err != nil {
		return nil, err
	}
	var os []angelOrder
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	if err := json.Unmarshal(data, &os); err != nil {
		return nil, fmt.Errorf("angel order book: %w", err)
	}
	return os, nil
}

func (a *Angel) Order(id string) (book.OrderState, error) {
	os, err := a.orders()
	if err != nil {
		return book.OrderState{}, err
	}
	for _, o := range os {
		if o.OrderID == id {
			return book.OrderState{Status: status(o.Status), Filled: int(num(o.Filled)), Remaining: int(num(o.Unfilled)),
				AvgPrice: num(o.AvgPrice), Message: o.Text}, nil
		}
	}
	return book.OrderState{}, fmt.Errorf("angel one order %s not in today's order book", id)
}

func (a *Angel) Cancel(id string) error {
	_, err := a.call("POST", "/rest/secure/angelbroking/order/v1/cancelOrder", map[string]string{"variety": "NORMAL", "orderid": id})
	return err
}

func (a *Angel) NetPositions() (map[string]int, error) {
	if err := a.load(); err != nil {
		return nil, err
	}
	data, err := a.call("GET", "/rest/secure/angelbroking/order/v1/getPosition", nil)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	if len(data) == 0 || string(data) == "null" {
		return out, nil
	}
	var ps []struct {
		Token    string `json:"symboltoken"`
		Exchange string `json:"exchange"`
		NetQty   any    `json:"netqty"`
	}
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("angel positions: %w", err)
	}
	for _, p := range ps {
		if t, ok := a.back[p.Token]; ok && p.Exchange == "NFO" {
			out[t] += int(num(p.NetQty))
		}
	}
	return out, nil
}

// AngelLogin starts a SmartAPI session from the user's client code, MPIN and
// the current 6-digit TOTP, and returns the session JWT. The user types these
// into their own terminal (desk login angelone); they are never stored.
func AngelLogin(apiKey, clientCode, mpin, totp string) (string, error) {
	if apiKey == "" || clientCode == "" || mpin == "" || totp == "" {
		return "", errors.New("API key, client code, MPIN and TOTP are all needed")
	}
	// SmartAPI calls the MPIN field "password"; the value is what the user
	// typed into their terminal just now, never a stored secret.
	type loginReq struct {
		ClientCode string `json:"clientcode"`
		PIN        string `json:"password"`
		TOTP       string `json:"totp"`
	}
	b, _ := json.Marshal(loginReq{ClientCode: clientCode, PIN: mpin, TOTP: totp})
	var c client
	data, err := c.do("POST", angelBaseURL+"/rest/auth/angelbroking/user/v1/loginByPassword", angelHeaders(apiKey), b, "application/json")
	if err != nil {
		return "", err
	}
	var r angelResp
	if err := json.Unmarshal(data, &r); err != nil || !r.Status {
		return "", fmt.Errorf("angel one login failed: %s %s", r.ErrorCode, r.Message)
	}
	var d struct {
		JWT string `json:"jwtToken"`
	}
	if json.Unmarshal(r.Data, &d) != nil || d.JWT == "" {
		return "", errors.New("angel one login: no session token in the answer")
	}
	return strings.TrimPrefix(d.JWT, "Bearer "), nil
}
