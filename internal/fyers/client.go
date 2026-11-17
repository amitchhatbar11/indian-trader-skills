// Package fyers is a Fyers API v3 client: login (OAuth), quotes, candle
// history, expired-contract history, the SPAN margin calculator, and LIMIT
// order placement and tracking, behind a throttle that keeps clear of Fyers'
// rate limits. Orders are placed only by internal/book, inside `desk serve`
// with DESK_LIVE=true: placement and cancellation are sent exactly once.
package fyers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	BaseURL   = "https://api-t1.fyers.in/api/v3"
	DataAPI   = "https://api-t1.fyers.in/data"
	SocketURL = "wss://socket.fyers.in/api/v3"
)

type FyersClient struct {
	// baseURL overrides BaseURL (tests point it at an httptest server).
	baseURL string
	// spanURL overrides SpanURL (tests).
	spanURL string

	AppID       string
	AppSecret   string
	Token       string
	RedirectURI string
	httpClient  *http.Client

	// internal state
	tokenPath  string // path to persist token file
	oauthState string // current OAuth state for CSRF protection
	limiter    rateLimiter
	tokenMu    sync.Mutex
	tokenBad   bool // last request was rejected with 401
}

// rateLimiter throttles API calls. Fyers documents 10 req/s / 200 req/min
// per API, but its Cloudflare edge bans sustained bursts well below that
// (observed: ~4/s over 200 calls → HTTP 429 "error code: 1015"), so we run
// gentler: >=333ms between calls (max 3/s), a rolling 60s window capped at
// 180 calls, and a global cool-down after any 429. During the cool-down,
// wait() fails fast instead of sleeping: poking a banned IP re-arms the ban,
// so the correct response is to stop calling entirely until it lapses.
//
// Calibration history: 500ms/2/s was clean for a full day; 200ms/5/s got the
// edge banned within seconds of a 210-call warm-up. 333ms/3/s sits halfway
// between the two, below the ~4/s ban line.
type rateLimiter struct {
	mu         sync.Mutex
	last       time.Time
	window     []time.Time // request timestamps in the last 60s
	pauseUntil time.Time   // global cool-down after a 429
	pausePath  string      // file persisting pauseUntil across restarts
	banLevel   int         // successive bans without a success: escalate
}

const (
	rlMinGap    = 333 * time.Millisecond
	rlPerMinute = 180
	// rlCooldown pauses ALL requests after a 429. It must outlast the
	// Cloudflare ban (~60s minimum, observed up to an hour once an IP is
	// flagged), because a probe made while the ban is live extends it. On
	// repeated bans without a success in between, the pause escalates.
	rlCooldown = 5 * time.Minute

	// rlMaxCooldown caps the escalation: a flagged IP is probed at most once
	// an hour instead of every minute, which is what lets the ban lapse.
	rlMaxCooldown = time.Hour
)

// ErrCoolingDown is returned instead of a request while the client is
// cooling down after a 429. It is deliberately not retried: retrying into an
// active Cloudflare ban extends the ban.
var ErrCoolingDown = errors.New("fyers: cooling down after rate limit")

// ErrTokenBad is returned instead of a request while the current token is
// known-rejected and no new token has appeared. Flooding the API with 401s is
// exactly what trips the Cloudflare edge's rate limiter.
var ErrTokenBad = errors.New("fyers: token rejected — awaiting re-authentication")

// wait returns ErrCoolingDown immediately while the client is paused, and
// otherwise blocks until a request slot is available.
func (r *rateLimiter) wait() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		now := time.Now()
		// drop timestamps older than 60s
		keep := r.window[:0]
		for _, t := range r.window {
			if now.Sub(t) < time.Minute {
				keep = append(keep, t)
			}
		}
		r.window = keep

		// Fail fast while paused: every request made during a ban re-arms it.
		if r.pauseUntil.After(now) {
			return ErrCoolingDown
		}

		var sleep time.Duration
		if gap := rlMinGap - now.Sub(r.last); gap > sleep {
			sleep = gap
		}
		if len(r.window) >= rlPerMinute {
			if until := time.Minute - now.Sub(r.window[0]); until > sleep {
				sleep = until
			}
		}
		if sleep <= 0 {
			r.last = now
			r.window = append(r.window, now)
			return nil
		}
		r.mu.Unlock()
		time.Sleep(sleep)
		r.mu.Lock()
	}
}

// pauseState is the on-disk form of the rate-limit pause. Persisting the
// escalation level as well as the deadline keeps the backoff intact across
// restarts, so a restart mid-ban cannot reset it to the gentlest step.
type pauseState struct {
	Until time.Time `json:"until"`
	Level int       `json:"level"`
}

// cooldown pauses all requests after a 429 edge ban. Repeated bans without
// a successful request in between escalate the pause (5m, 10m, 20m, 40m, 1h
// cap): a flagged IP must be left alone for it to recover, and every probe
// made while the ban is live extends it. The pause is persisted to disk so a
// restart does not immediately re-burst into the still-active ban.
func (r *rateLimiter) cooldown() {
	r.mu.Lock()
	d := rlCooldown << uint(r.banLevel)
	if d > rlMaxCooldown {
		d = rlMaxCooldown
	}
	r.banLevel++
	if until := time.Now().Add(d); until.After(r.pauseUntil) {
		r.pauseUntil = until
	}
	ps := pauseState{Until: r.pauseUntil, Level: r.banLevel}
	r.mu.Unlock()
	if r.pausePath != "" {
		if b, err := json.Marshal(ps); err == nil {
			os.WriteFile(r.pausePath, b, 0644)
		}
	}
}

// banLapsed resets the escalation once any request succeeds.
func (r *rateLimiter) banLapsed() {
	r.mu.Lock()
	r.banLevel = 0
	r.mu.Unlock()
}

// loadPause restores a persisted pause from a previous run, if still active.
// Accepts the JSON pauseState and the older bare-RFC3339 format.
func (r *rateLimiter) loadPause() {
	if r.pausePath == "" {
		return
	}
	b, err := os.ReadFile(r.pausePath)
	if err != nil {
		return
	}
	var ps pauseState
	if err := json.Unmarshal(b, &ps); err != nil {
		// Legacy format: a bare RFC3339Nano deadline.
		t, err2 := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
		if err2 != nil {
			return
		}
		ps = pauseState{Until: t, Level: 1}
	}
	if ps.Level < 1 {
		ps.Level = 1
	}
	r.mu.Lock()
	// Only adopt a pause that is still in the future. An expired file is
	// yesterday's ban: keep its escalation out of the fresh process and
	// clean it up so a stale file cannot confuse the next run either.
	if !ps.Until.After(time.Now()) {
		r.mu.Unlock()
		os.Remove(r.pausePath)
		return
	}
	if ps.Until.After(r.pauseUntil) {
		r.pauseUntil = ps.Until
		r.banLevel = ps.Level // keep the escalation the previous run reached
	}
	r.mu.Unlock()
}

// pauseRemaining reports how long the current cool-down has left (0 if none).
func (r *rateLimiter) pauseRemaining() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if until := r.pauseUntil.Sub(time.Now()); until > 0 {
		return until
	}
	return 0
}

type QuoteData struct {
	Symbol    string  `json:"n"`
	LTP       float64 `json:"lp"`
	Volume    int64   `json:"volume"`
	Bid       float64 `json:"bid"`
	Ask       float64 `json:"ask"`
	High      float64 `json:"high_price"`
	Low       float64 `json:"low_price"`
	Open      float64 `json:"open_price"`
	PrevClose float64 `json:"prev_close_price"`
}

// quoteResponseItem is the new v3 data API response item format
type quoteResponseItem struct {
	Symbol string      `json:"n"`
	Values quoteValues `json:"v"`
	Status string      `json:"s"`
}

type quoteValues struct {
	LTP            float64 `json:"lp"`
	Volume         int64   `json:"volume"`
	Bid            float64 `json:"bid"`
	Ask            float64 `json:"ask"`
	HighPrice      float64 `json:"high_price"`
	LowPrice       float64 `json:"low_price"`
	OpenPrice      float64 `json:"open_price"`
	PrevClosePrice float64 `json:"prev_close_price"`
}

type OrderRequest struct {
	Symbol string `json:"symbol"`
	Qty    int    `json:"qty"`
	// Type 1=Limit, 2=Market. Stock option strikes only accept LIMIT orders
	// on Fyers — never send market orders for them.
	Type         int     `json:"type"`
	Side         int     `json:"side"` // 1=Buy, -1=Sell
	ProductType  string  `json:"productType"`
	LimitPrice   float64 `json:"limitPrice"`
	StopPrice    float64 `json:"stopPrice"` // required by the v3 API; 0 for limit orders
	Validity     string  `json:"validity"`
	DisclosedQty int     `json:"disclosedQty"`
	OfflineOrder bool    `json:"offlineOrder"`
	StopLoss     float64 `json:"stopLoss,omitempty"`
	TakeProfit   float64 `json:"takeProfit,omitempty"`
	// OrderTag lets an order be found in the order book when the placement
	// response was not acknowledged (code 201). Alphanumeric.
	OrderTag string `json:"orderTag,omitempty"`
}

type OrderResponse struct {
	Status  string `json:"s"`
	Code    int    `json:"code"` // 201 = request made but not acknowledged: check the order book
	OrderID string `json:"id"`
	Message string `json:"message"`
}

// Order status codes (GET /orders).
const (
	OrderCancelled = 1
	OrderFilled    = 2
	OrderTransit   = 4
	OrderRejected  = 5
	OrderPending   = 6
	OrderExpired   = 7
)

// Order is one entry of the order book.
type Order struct {
	ID          string  `json:"id"`
	Symbol      string  `json:"symbol"`
	Qty         int     `json:"qty"`
	FilledQty   int     `json:"filledQty"`
	Remaining   int     `json:"remainingQuantity"`
	TradedPrice float64 `json:"tradedPrice"` // average fill price
	LimitPrice  float64 `json:"limitPrice"`
	Status      int     `json:"status"`
	Side        int     `json:"side"`
	Message     string  `json:"message"`
	OrderTag    string  `json:"orderTag"`
}

// NetPosition is one entry of GET /positions.
type NetPosition struct {
	Symbol      string  `json:"symbol"`
	NetQty      int     `json:"netQty"`
	BuyQty      int     `json:"buyQty"`
	SellQty     int     `json:"sellQty"`
	NetAvg      float64 `json:"netAvg"`
	ProductType string  `json:"productType"`
}

type savedToken struct {
	AccessToken string `json:"access_token"`
	CreatedAt   string `json:"created_at"`
}

// ── Constructor ──────────────────────────────────────────────────────────────

func New(appID, appSecret, token string) *FyersClient {
	return &FyersClient{
		AppID:      appID,
		AppSecret:  appSecret,
		Token:      token,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// ── OAuth Configuration ─────────────────────────────────────────────────────

func (c *FyersClient) SetRedirectURI(uri string) {
	c.RedirectURI = uri
}

// SetTokenPath sets the file path for persisting the access token, and
// restores any rate-limit pause left by a previous run.
func (c *FyersClient) SetTokenPath(path string) {
	c.tokenPath = path
	if path != "" {
		c.limiter.pausePath = path + ".pause"
		c.limiter.loadPause()
	}
}

// ── OAuth Flow ──────────────────────────────────────────────────────────────

// IsAuthenticated returns true if a token is set.
func (c *FyersClient) IsAuthenticated() bool {
	return c.Token != ""
}

// GenerateAuthURL builds the Fyers OAuth authorization URL.
// Stores a random state internally for CSRF validation on callback.
func (c *FyersClient) GenerateAuthURL() (string, error) {
	if c.AppID == "" {
		return "", errors.New("FYERS_APP_ID not set")
	}
	if c.RedirectURI == "" {
		return "", errors.New("FYERS_REDIRECT_URI not set")
	}

	// generate random state for CSRF protection
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}
	c.oauthState = hex.EncodeToString(b)

	u := fmt.Sprintf("%s/generate-authcode?client_id=%s&redirect_uri=%s&response_type=code&state=%s",
		BaseURL,
		url.QueryEscape(c.AppID),
		url.QueryEscape(c.RedirectURI),
		url.QueryEscape(c.oauthState),
	)
	return u, nil
}

// ExchangeCode exchanges an auth_code for an access token.
// The state parameter is validated against the stored OAuth state.
// Fyers v3 uses SHA-256 hash of app_id:app_secret for authentication.
func (c *FyersClient) ExchangeCode(authCode, state string) error {
	if c.AppSecret == "" {
		return errors.New("FYERS_APP_SECRET not set — cannot exchange auth code")
	}
	if c.oauthState != "" && state != "" && state != c.oauthState {
		return errors.New("OAuth state mismatch — possible CSRF attack")
	}
	if authCode == "" {
		return errors.New("auth_code is empty")
	}

	// Compute appIdHash: SHA-256 of "app_id:app_secret"
	hashInput := c.AppID + ":" + c.AppSecret
	appIDHash := sha256.Sum256([]byte(hashInput))

	// Build JSON request body
	reqBody := struct {
		GrantType string `json:"grant_type"`
		AppIDHash string `json:"appIdHash"`
		Code      string `json:"code"`
	}{
		GrantType: "authorization_code",
		AppIDHash: hex.EncodeToString(appIDHash[:]),
		Code:      authCode,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal exchange request: %w", err)
	}

	// POST to Fyers validate-authcode endpoint.
	// Respect the rate-limit pause: re-authenticating while the IP is banned
	// would just poke the ban (and a new token is unusable until it lapses).
	if err := c.limiter.wait(); err != nil {
		return fmt.Errorf("cannot exchange code while rate-limited: %w", err)
	}
	// Use manual http request for clearer control
	reqURL := BaseURL + "/validate-authcode"
	reqBodyReader := strings.NewReader(string(bodyBytes))
	req, err := http.NewRequest("POST", reqURL, reqBodyReader)
	if err != nil {
		return fmt.Errorf("failed to create validate request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("validate-authcode request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var result struct {
		Status      string `json:"s"`
		Code        int    `json:"code"`
		AccessToken string `json:"access_token"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("failed to parse validate-authcode response: %w (body: %s)", err, string(respBody))
	}

	if result.Status != "ok" {
		msg := result.Message
		if msg == "" {
			msg = string(respBody)
		}
		return fmt.Errorf("Fyers auth failed: %s", msg)
	}

	if result.AccessToken == "" {
		return fmt.Errorf("Fyers returned ok but no access_token in response: %s", string(respBody))
	}

	// Fyers returns the token as "app_id:token_value". We strip the prefix
	// since we prepend AppID in the Authorization header.
	token := result.AccessToken
	if strings.HasPrefix(token, c.AppID+":") {
		token = strings.TrimPrefix(token, c.AppID+":")
	}
	c.Token = token
	c.setTokenRejected(false) // fresh token: clear any rejection state

	// Persist to file if a token path is configured
	if c.tokenPath != "" {
		if err := c.saveTokenToFile(); err != nil {
			return fmt.Errorf("token exchanged but failed to persist: %w", err)
		}
	}

	return nil
}

// reloadTokenFromDisk re-reads the token file and adopts it if it differs
// from the token currently in memory. Returns true when a *different* token
// was adopted, i.e. a retry is worth attempting.
func (c *FyersClient) reloadTokenFromDisk() bool {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.tokenPath == "" {
		return false
	}
	data, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return false
	}
	var st savedToken
	if err := json.Unmarshal(data, &st); err != nil || st.AccessToken == "" {
		return false
	}
	if st.AccessToken == c.Token {
		return false // same token — reloading would not help
	}
	c.Token = st.AccessToken
	c.tokenBad = false // a token we have not tried yet is not rejected
	return true
}

// TokenRejected reports whether the last request failed authentication, so
// the UI can prompt for a reconnect instead of showing raw 401 noise.
func (c *FyersClient) TokenRejected() bool {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	return c.tokenBad
}

func (c *FyersClient) setTokenRejected(bad bool) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.tokenBad = bad
}

// LoadToken attempts to load a previously saved token from the token file.
// Returns true if a token was loaded.
func (c *FyersClient) LoadToken() bool {
	if c.tokenPath == "" {
		return false
	}
	data, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return false
	}
	var st savedToken
	if err := json.Unmarshal(data, &st); err != nil {
		return false
	}
	if st.AccessToken == "" {
		return false
	}
	c.Token = st.AccessToken
	return true
}

// saveTokenToFile persists the current token to the configured token file.
func (c *FyersClient) saveTokenToFile() error {
	st := savedToken{
		AccessToken: c.Token,
		CreatedAt:   time.Now().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	// ensure directory exists
	if idx := strings.LastIndex(c.tokenPath, "/"); idx >= 0 {
		os.MkdirAll(c.tokenPath[:idx], 0755)
	}
	if err := os.WriteFile(c.tokenPath, data, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	return nil
}

// ValidateToken checks whether the current token is still valid by making
// a lightweight API call (profile endpoint). Returns nil if valid.
func (c *FyersClient) ValidateToken() error {
	if !c.IsAuthenticated() {
		return errors.New("no token set")
	}
	_, err := c.doRequest("GET", "/profile", nil)
	return err
}

// ClearToken removes the current token and deletes the persisted token file.
func (c *FyersClient) ClearToken() {
	c.Token = ""
	c.setTokenRejected(false)
	if c.tokenPath != "" {
		os.Remove(c.tokenPath)
	}
}

// tokenStale reports whether the API last rejected the current token and the
// token on disk is no different — meaning another request would just burn a
// 401 (and rate-limit budget). A new token on disk clears the block.
func (c *FyersClient) tokenStale() bool {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if !c.tokenBad || c.tokenPath == "" {
		return false
	}
	b, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return true // cannot see a newer token — stay blocked
	}
	var st savedToken
	if err := json.Unmarshal(b, &st); err != nil || st.AccessToken == "" {
		return true
	}
	return st.AccessToken == c.Token
}

// ── API Requests ─────────────────────────────────────────────────────────────

// doRequestOnce makes a single attempt — no retry ladder — for requests
// that must never be repeated blindly (placing or cancelling an order).
func (c *FyersClient) doRequestOnce(method, endpoint string, body io.Reader) ([]byte, error) {
	data, status, err := c.doOnce(c.apiBase(), method, endpoint, body)
	if err != nil {
		if status == 401 {
			c.setTokenRejected(true)
		}
		return nil, err
	}
	c.setTokenRejected(false)
	return data, nil
}

// GetOrders returns today's order book.
func (c *FyersClient) GetOrders() ([]Order, error) {
	return c.orders("/orders")
}

// GetOrder returns one order by its Fyers order ID.
func (c *FyersClient) GetOrder(id string) (*Order, error) {
	os, err := c.orders("/orders?id=" + url.QueryEscape(id))
	if err != nil {
		return nil, err
	}
	for i := range os {
		if os[i].ID == id {
			return &os[i], nil
		}
	}
	return nil, fmt.Errorf("order %s not in the order book", id)
}

func (c *FyersClient) orders(endpoint string) ([]Order, error) {
	data, err := c.doRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		S         string  `json:"s"`
		Message   string  `json:"message"`
		OrderBook []Order `json:"orderBook"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("order book parse failed: %w", err)
	}
	if resp.S != "ok" {
		return nil, fmt.Errorf("order book: %s", resp.Message)
	}
	return resp.OrderBook, nil
}

// CancelOrder cancels a pending order. Sent once, like PlaceOrder.
func (c *FyersClient) CancelOrder(id string) error {
	body, _ := json.Marshal(map[string]string{"id": id})
	data, err := c.doRequestOnce("DELETE", "/orders/sync", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	var resp OrderResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("cancel parse failed: %w", err)
	}
	if resp.Status != "ok" {
		return fmt.Errorf("cancel rejected: %s", resp.Message)
	}
	return nil
}

// GetPositions returns today's net positions.
func (c *FyersClient) GetPositions() ([]NetPosition, error) {
	data, err := c.doRequest("GET", "/positions", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		S            string        `json:"s"`
		Message      string        `json:"message"`
		NetPositions []NetPosition `json:"netPositions"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("positions parse failed: %w", err)
	}
	if resp.S != "ok" {
		return nil, fmt.Errorf("positions: %s", resp.Message)
	}
	return resp.NetPositions, nil
}

func (c *FyersClient) doRequest(method, endpoint string, body io.Reader) ([]byte, error) {
	return c.doRequestBase(c.apiBase(), method, endpoint, body)
}

func (c *FyersClient) apiBase() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return BaseURL
}

func (c *FyersClient) doDataRequest(method, endpoint string, body io.Reader) ([]byte, error) {
	return c.doRequestBase(DataAPI, method, endpoint, body)
}

func (c *FyersClient) doRequestBase(baseURL, method, endpoint string, body io.Reader) ([]byte, error) {
	// Transient failures (5xx, connection errors) get a short backoff-and-
	// retry ladder. 429s never do: the limiter already cooled the client down
	// and every retry would just re-arm the Cloudflare ban.
	backoffs := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second}

	// Buffer the body so it can be replayed on retry.
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		var rdr io.Reader
		if bodyBytes != nil {
			rdr = strings.NewReader(string(bodyBytes))
		}
		data, status, err := c.doOnce(baseURL, method, endpoint, rdr)
		if err == nil {
			c.setTokenRejected(false)
			c.limiter.banLapsed() // a success ends any ban-escalation cycle
			return data, nil
		}
		// Cooling down, or a rejected token: fail fast, never retry.
		if errors.Is(err, ErrCoolingDown) || errors.Is(err, ErrTokenBad) {
			return nil, err
		}
		if status == 401 {
			c.setTokenRejected(true)
		}
		lastErr = err
		// 401 usually means the in-memory token went stale while a fresh one
		// was written to disk (re-auth elsewhere, or a manual paste). Reload
		// once and retry before giving up, so a running bot self-heals
		// instead of 401-ing every batch until someone restarts it.
		if status == 401 && attempt == 0 && c.reloadTokenFromDisk() {
			log.Println("fyers: 401 — reloaded a newer token from disk, retrying")
			continue
		}
		if status == 429 {
			// Edge ban: pause the whole client and give up on this request.
			// Callers handle the error per-symbol/per-batch and retry
			// naturally on their next tick, once the pause has lapsed.
			c.limiter.cooldown()
			log.Printf("fyers: 429 rate-limited — cooling down all requests (pause %v)",
				c.limiter.pauseRemaining())
			return nil, lastErr
		}
		retryable := status >= 500 ||
			strings.Contains(strings.ToLower(err.Error()), "request limit reached")
		if !retryable || attempt >= len(backoffs) {
			return nil, lastErr
		}
		log.Printf("fyers: %s (retry %d/%d in %v)", err, attempt+1, len(backoffs), backoffs[attempt])
		time.Sleep(backoffs[attempt])
	}
}

func (c *FyersClient) doOnce(baseURL, method, endpoint string, body io.Reader) ([]byte, int, error) {
	// Don't burn API calls with a token the API just rejected — the 401
	// flood that results is what trips the rate limiter. Resume as soon as a
	// different token shows up (a fresh login).
	if c.tokenStale() {
		return nil, 401, ErrTokenBad
	}
	if err := c.limiter.wait(); err != nil {
		return nil, 0, err
	}

	url := baseURL + endpoint
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, 0, fmt.Errorf("request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("%s:%s", c.AppID, c.Token))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read failed: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, resp.StatusCode, fmt.Errorf("API error %d: %s", resp.StatusCode, string(data))
	}
	return data, resp.StatusCode, nil
}

// ── Trading & Data Methods ──────────────────────────────────────────────────

// NIFTY token (NSE index)
const NIFTY_TOKEN = "NSE:NIFTY50-INDEX"

// GetNiftySpot fetches current NIFTY index price
func (c *FyersClient) GetNiftySpot() (float64, error) {
	data, err := c.doDataRequest("GET", "/quotes?symbols="+url.QueryEscape(NIFTY_TOKEN), nil)
	if err != nil {
		return 0, err
	}

	var resp struct {
		Status string              `json:"s"`
		Data   []quoteResponseItem `json:"d"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, fmt.Errorf("parse failed: %w", err)
	}

	if len(resp.Data) == 0 {
		return 0, fmt.Errorf("no data for NIFTY")
	}
	return resp.Data[0].Values.LTP, nil
}

// GetQuotes fetches quotes for option symbols
func (c *FyersClient) GetQuotes(symbols []string) (map[string]QuoteData, error) {
	syms := make([]string, len(symbols))
	for i, s := range symbols {
		syms[i] = fmt.Sprintf("NSE:%s", s)
	}
	q := url.QueryEscape(strings.Join(syms, ","))
	data, err := c.doDataRequest("GET", "/quotes?symbols="+q, nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Status string              `json:"s"`
		Data   []quoteResponseItem `json:"d"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}
	if resp.Status != "ok" {
		return nil, fmt.Errorf("quotes API returned status %q", resp.Status)
	}

	result := make(map[string]QuoteData)
	for _, item := range resp.Data {
		result[item.Symbol] = QuoteData{
			Symbol:    item.Symbol,
			LTP:       item.Values.LTP,
			Volume:    item.Values.Volume,
			Bid:       item.Values.Bid,
			Ask:       item.Values.Ask,
			High:      item.Values.HighPrice,
			Low:       item.Values.LowPrice,
			Open:      item.Values.OpenPrice,
			PrevClose: item.Values.PrevClosePrice,
		}
	}
	return result, nil
}

// Candle is one bar from the history API.
type Candle struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// GetDailyCandles fetches daily OHLCV history for a symbol (pass without
// the "NSE:" prefix, e.g. "RELIANCE-EQ") between from and to inclusive.
func (c *FyersClient) GetDailyCandles(symbol string, from, to time.Time) ([]Candle, error) {
	return c.GetCandles(symbol, "D", from, to)
}

// GetCandles fetches OHLCV history at any Fyers resolution ("5" = 5-minute,
// "15", "D" = daily, ...). Note the API caps a single request at 366 days for
// daily/weekly/monthly and 100 days for intraday resolutions.
func (c *FyersClient) GetCandles(symbol, resolution string, from, to time.Time) ([]Candle, error) {
	q := url.Values{}
	q.Set("symbol", "NSE:"+symbol)
	q.Set("resolution", resolution)
	q.Set("date_format", "1")
	q.Set("range_from", from.Format("2006-01-02"))
	q.Set("range_to", to.Format("2006-01-02"))
	q.Set("cont_flag", "1")

	data, err := c.doDataRequest("GET", "/history?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Status  string      `json:"s"`
		Candles [][]float64 `json:"candles"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("history parse failed: %w", err)
	}
	if resp.Status != "ok" {
		return nil, fmt.Errorf("history API returned status %q", resp.Status)
	}
	out := make([]Candle, 0, len(resp.Candles))
	for _, c := range resp.Candles {
		if len(c) < 6 {
			continue
		}
		out = append(out, Candle{
			Time: time.Unix(int64(c[0]), 0), Open: c[1], High: c[2],
			Low: c[3], Close: c[4], Volume: c[5],
		})
	}
	return out, nil
}

// PlaceOrder places an order via Fyers
func (c *FyersClient) PlaceOrder(req OrderRequest) (*OrderResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal failed: %w", err)
	}

	// v3 places orders at /orders/sync. Sent ONCE: a transient-error retry
	// could duplicate an order the server had already accepted.
	data, err := c.doRequestOnce("POST", "/orders/sync", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	var resp OrderResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}
	return &resp, nil
}

// BuildOptionSymbol builds NIFTY option symbol using v3 format
// e.g., NIFTY2671424100CE (YY=26, M=7, dd=14, strike=24100, type=CE)
func BuildOptionSymbol(strike int, optType string, expiry time.Time) string {
	yearStr := fmt.Sprintf("%02d", expiry.Year()%100)
	monthStr := fmt.Sprintf("%d", expiry.Month()) // no leading zero
	dayStr := fmt.Sprintf("%02d", expiry.Day())
	return fmt.Sprintf("NIFTY%s%s%s%d%s", yearStr, monthStr, dayStr, strike, strings.ToUpper(optType))
}

// GetNearestWeeklyExpiry returns the next weekly expiry.
// NIFTY weekly options expire on Tuesdays.
// If today is Tuesday, returns next Tuesday (skip current expiry day).
func GetNearestWeeklyExpiry(from time.Time) time.Time {
	daysUntilTuesday := (2 - int(from.Weekday()) + 7) % 7
	if daysUntilTuesday == 0 {
		daysUntilTuesday = 7 // Next Tuesday, not today
	}
	return from.AddDate(0, 0, daysUntilTuesday)
}

// GetNearestThursday returns next Thursday expiry (legacy, kept for reference)
func GetNearestThursday(from time.Time) time.Time {
	daysUntilThursday := (4 - int(from.Weekday()) + 7) % 7
	if daysUntilThursday == 0 {
		daysUntilThursday = 7 // Next Thursday, not today
	}
	return from.AddDate(0, 0, daysUntilThursday)
}

// GetATMStrike returns ATM strike (nearest 50)
func GetATMStrike(spot float64) int {
	return int(spot/50+0.5) * 50
}
