package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the desk's settings: environment variables, falling back to
// KEY=VALUE lines in $NSE_DESK_HOME/.env (default ~/.nse-desk/.env).
// Everything the desk writes lives under DataDir (= NSE_DESK_HOME).
type Config struct {
	DataDir string

	FyersAppID, FyersAppSecret, FyersRedirectURI, FyersAccessToken string
	UpstoxAccessToken                                              string
	KiteAPIKey, KiteAccessToken                                    string
	BreezeAPIKey, BreezeSession                                    string
	DeskFeeds                                                      string

	// DeskLive allows LIVE option orders (real money) through `desk serve`.
	// Off by default: the user sets DESK_LIVE=true themselves.
	DeskLive    bool
	DeskLiveURL string

	// OrderBroker is where `desk serve` sends live orders: fyers (default),
	// upstox, zerodha, dhan or angelone.
	OrderBroker                   string
	DhanClientID, DhanAccessToken string
	AngelAPIKey, AngelJWT         string

	Capital, SLRiskPct, DailyLossPct, MaxSpreadPct float64

	TelegramBotToken string
	TelegramChatID   int64
}

// home is the desk's folder.
func home() string {
	if h := os.Getenv("NSE_DESK_HOME"); h != "" {
		return h
	}
	if u, err := os.UserHomeDir(); err == nil {
		return filepath.Join(u, ".nse-desk")
	}
	return ".nse-desk"
}

func loadConfig() (*Config, error) {
	dir := home()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file := readDotEnv(filepath.Join(dir, ".env"))
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		if v, ok := file[k]; ok && v != "" {
			return v
		}
		return def
	}
	num := func(k string, def float64) float64 {
		if f, err := strconv.ParseFloat(get(k, ""), 64); err == nil {
			return f
		}
		return def
	}
	chat, _ := strconv.ParseInt(get("TELEGRAM_CHAT_ID", "0"), 10, 64)
	return &Config{
		DataDir:    dir,
		FyersAppID: get("FYERS_APP_ID", ""), FyersAppSecret: get("FYERS_APP_SECRET", ""),
		FyersRedirectURI: get("FYERS_REDIRECT_URI", ""), FyersAccessToken: get("FYERS_ACCESS_TOKEN", ""),
		UpstoxAccessToken: get("UPSTOX_ACCESS_TOKEN", ""),
		KiteAPIKey:        get("KITE_API_KEY", ""), KiteAccessToken: get("KITE_ACCESS_TOKEN", ""),
		BreezeAPIKey: get("BREEZE_API_KEY", ""), BreezeSession: get("BREEZE_SESSION", ""),
		DeskFeeds:    get("DESK_FEEDS", "fyers,upstox,zerodha,breeze"),
		DeskLive:     get("DESK_LIVE", "false") == "true",
		DeskLiveURL:  get("DESK_LIVE_URL", "http://127.0.0.1:8505"),
		OrderBroker:  get("ORDER_BROKER", "fyers"),
		DhanClientID: get("DHAN_CLIENT_ID", ""), DhanAccessToken: get("DHAN_ACCESS_TOKEN", ""),
		AngelAPIKey: get("ANGEL_API_KEY", ""), AngelJWT: get("ANGEL_JWT", ""),
		Capital:          num("DESK_CAPITAL", 1_000_000),
		SLRiskPct:        num("DESK_RISK_PCT", 0.5),
		DailyLossPct:     num("DESK_DAILY_LOSS_PCT", 1),
		MaxSpreadPct:     num("DESK_MAX_SPREAD_PCT", 5),
		TelegramBotToken: get("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:   chat,
	}, nil
}

// readDotEnv reads KEY=VALUE lines (# comments, optional quotes).
func readDotEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 && !strings.HasPrefix(v, `"`) {
			v = strings.TrimSpace(v[:i])
		}
		out[strings.TrimSpace(k)] = strings.Trim(v, `"'`)
	}
	return out
}
