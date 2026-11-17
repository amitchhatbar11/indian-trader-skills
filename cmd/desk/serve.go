package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/amitchhatbar11/indian-trader-skills/internal/book"
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
	fy := e.fyersClient()
	if fy == nil {
		return fmt.Errorf("no Fyers login — run desk login fyers first")
	}
	if err := fy.ValidateToken(); err != nil {
		return fmt.Errorf("the Fyers token does not work (%v) — run desk login fyers", err)
	}
	u, err := url.Parse(e.cfg.DeskLiveURL)
	if err != nil {
		return fmt.Errorf("DESK_LIVE_URL: %w", err)
	}
	lim, err := e.limits()
	if err != nil {
		return err
	}
	dir := filepath.Join(e.cfg.DataDir, "live")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dailyPct := 0.0
	if lim.Capital > 0 {
		dailyPct = lim.MaxDailyLoss / lim.Capital * 100
	}
	s := &livesrv.Server{
		Book: book.New(dir), Fyers: fy, Broker: book.FyersMonitor{Fy: fy, ProductType: "MARGIN"},
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
	log.Printf("⚠️  desk serve — LIVE orders (real money) enabled on %s, Fyers, stock options only. Book: %s", e.cfg.DeskLiveURL, dir)
	log.Printf("Only orders you approve with desk broker approve -live go out. Ctrl-C stops new orders; open positions stay at Fyers.")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s.Run(ctx, e.calendar())
	os.Remove(s.TokenFile)
	log.Print("desk serve stopped — no new live orders. Positions still open at Fyers are NOT watched until you start it again.")
	return nil
}
