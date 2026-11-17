package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/amitchhatbar11/indian-trader-skills/internal/book"
	"github.com/amitchhatbar11/indian-trader-skills/internal/brokers"
	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
	"github.com/amitchhatbar11/indian-trader-skills/internal/livesrv"
)

// cmdServe runs the live engine (internal/livesrv). Real money: it refuses
// to start unless the user has set DESK_LIVE=true.
func cmdServe(e *env, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if !e.cfg.DeskLive {
		return fmt.Errorf("DESK_LIVE is not true — desk serve places REAL orders, so it only starts when you set DESK_LIVE=true in %s yourself",
			filepath.Join(e.cfg.DataDir, ".env"))
	}
	u, err := url.Parse(e.cfg.DeskLiveURL)
	if err != nil {
		return fmt.Errorf("DESK_LIVE_URL: %w", err)
	}
	lim, err := e.limits()
	if err != nil {
		return err
	}

	// Where orders go.
	name := strings.ToLower(strings.TrimSpace(e.cfg.OrderBroker))
	var broker book.Broker
	ready := func() error { return nil }
	if name == "" || name == "fyers" {
		fy := e.fyersClient()
		if fy == nil {
			return fmt.Errorf("ORDER_BROKER=fyers but no Fyers login — run desk login fyers")
		}
		if err := fy.ValidateToken(); err != nil {
			return fmt.Errorf("the Fyers token does not work (%v) — run desk login fyers", err)
		}
		broker = book.FyersMonitor{Fy: fy, ProductType: "MARGIN"}
		ready = func() error {
			if !fy.IsAuthenticated() || fy.TokenRejected() {
				return errors.New("the Fyers token was rejected — run desk login fyers, then restart desk serve")
			}
			return nil
		}
		name = "fyers"
	} else {
		jwt := e.cfg.AngelJWT
		if b, err := os.ReadFile(e.dir.Path("angel_token")); err == nil && jwt == "" {
			jwt = strings.TrimSpace(string(b))
		}
		broker, err = brokers.Open(name, brokers.Options{CacheDir: e.dir.Path("cache"),
			UpstoxToken: e.cfg.UpstoxAccessToken, KiteAPIKey: e.cfg.KiteAPIKey, KiteToken: e.cfg.KiteAccessToken,
			DhanClientID: e.cfg.DhanClientID, DhanToken: e.cfg.DhanAccessToken, AngelAPIKey: e.cfg.AngelAPIKey, AngelJWT: jwt})
		if err != nil {
			return fmt.Errorf("ORDER_BROKER=%s: %w", name, err)
		}
		// Check the session before taking orders: a positions call is read-only.
		if _, err := broker.NetPositions(); err != nil {
			return fmt.Errorf("ORDER_BROKER=%s: the session does not work (%v) — refresh its token", name, err)
		}
	}

	// Market data for the guards and the stop monitor: the configured feeds.
	feed, err := e.feeds()
	if err != nil {
		return fmt.Errorf("live orders need live quotes: %w", err)
	}
	quote := func(t string) (book.OptQuote, error) {
		qs, err := feed.Quotes([]string{t})
		if err != nil {
			return book.OptQuote{}, err
		}
		q, ok := qs[desk.ParseInstrument(t).Symbol]
		if !ok {
			return book.OptQuote{}, fmt.Errorf("no quote for %s", t)
		}
		return book.OptQuote{LTP: q.LTP, Bid: q.Bid, Ask: q.Ask, Volume: int64(q.Volume)}, nil
	}
	ltps := func(ts []string) (map[string]float64, error) {
		qs, err := feed.Quotes(ts)
		if err != nil {
			return nil, err
		}
		out := map[string]float64{}
		for _, t := range ts {
			out[t] = qs[desk.ParseInstrument(t).Symbol].LTP
		}
		return out, nil
	}

	dir := filepath.Join(e.cfg.DataDir, "live", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dailyPct := 0.0
	if lim.Capital > 0 {
		dailyPct = lim.MaxDailyLoss / lim.Capital * 100
	}
	s := &livesrv.Server{
		Book: book.New(dir), Broker: broker, Quote: quote, LTPs: ltps, Ready: ready,
		// The desk's risk check has already applied the lot-based volume
		// floor; the book re-checks the halt, ban, spread and price band.
		Rules:         book.Rules{Capital: lim.Capital, DailyLossPct: dailyPct, LimitBandPct: 10, MaxSpreadPct: lim.MaxSpreadPct},
		Ban:           fyers.NewBanList(),
		SellMarginPct: lim.OptionMarginPct, BufferPct: 1,
		TokenFile: e.dir.Path("live.token"),
	}
	if e.cfg.TelegramBotToken != "" && e.cfg.TelegramChatID != 0 {
		s.Notify = func(m string) { sendTelegram(e.cfg.TelegramBotToken, e.cfg.TelegramChatID, "desk: "+m) }
	}
	if _, err := s.Listen(u.Host); err != nil {
		return err
	}
	log.SetFlags(log.LstdFlags)
	log.Printf("⚠️  desk serve — LIVE orders (real money) to %s, stock options only, on %s. Quotes: %s. Book: %s",
		strings.ToUpper(name), e.cfg.DeskLiveURL, feed.Name(), dir)
	log.Printf("Only orders you approve with desk broker approve -live go out. Ctrl-C stops new orders; open positions stay at the broker.")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s.Run(ctx, e.calendar())
	os.Remove(s.TokenFile)
	log.Print("desk serve stopped — no new live orders. Positions still open at the broker are NOT watched until you start it again.")
	return nil
}
