# Indian Trader Skills

[![skills.sh](https://skills.sh/b/amitchhatbar11/indian-trader-skills)](https://skills.sh/amitchhatbar11/indian-trader-skills)

Eleven agent skills for trading the **Indian market (NSE)**: cash stocks
(intraday, shorts included) and stock options. Scan for setups, time the entry,
plan the stop and target, replay the trade 10,000 times, size it, check it
against your limits and **paper-trade** it. Plus news, quarterly results,
bulk/block deals and insider trades, and price alerts.

Everything runs in IST, on NSE hours and holidays, NSE tick sizes, F&O lot sizes,
the F&O ban list, price bands and Indian charges (STT, stamp duty, GST).

> **Paper only.** The skills never place a real order. The broker clients
> here are read-only (no order, cancel or positions calls), and a test fails
> the build if one appears. Trades go to a local paper account.

## Install

```bash
# 1. The skills (Claude Code, Codex, Cursor, OpenCode and other agents)
npx skills add amitchhatbar11/indian-trader-skills

# 2. The desk CLI the skills run (needs Go 1.22+)
go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest

# 3. A price feed: Fyers (OAuth, once a trading day)
mkdir -p ~/.nse-desk && cp .env.example ~/.nse-desk/.env   # then fill it in
desk login fyers
desk clock
```

The daily scanner, news, results and deals use NSE's public files and need no
broker login. Live quotes and intraday bars need one of:

| Broker | Setting (in `~/.nse-desk/.env`) | Notes |
|---|---|---|
| Fyers | `FYERS_APP_ID`, `FYERS_APP_SECRET`, `FYERS_REDIRECT_URI`, then `desk login fyers` | quotes, 1/5-min bars, SPAN margin |
| Upstox | `UPSTOX_ACCESS_TOKEN` | quotes, bars |
| Zerodha | `KITE_API_KEY`, `KITE_ACCESS_TOKEN` | quotes; bars need Kite's historical add-on |
| ICICI Breeze | `BREEZE_API_KEY`, `BREEZE_SESSION` | bars only |

`DESK_FEEDS` (default `fyers,upstox,zerodha,breeze`) sets the order; brokers
without credentials are skipped, and a failing one falls through to the next.
`desk login upstox|zerodha|breeze` explains how to get each token.

## The skills

| Skill | Command | What it does |
|---|---|---|
| `nse-scanner` | `desk scan` | 20-day breakouts/breakdowns, bull flags, 20-DMA reclaims, oversold bounces, overbought, from NSE's bhavcopies (split-adjusted); `-orb` adds today's opening-range breakouts |
| `nse-entry` | `desk entry` | first completed bar to close beyond the opening range (long or short), optional volume check |
| `nse-exit` | `desk exit` | stop (range mid/edge, %, ATR, price) and target (R), rounded to NSE ticks; options 40%/30% of premium |
| `nse-timemachine` | `desk sim` | 10,000 replays from the stock's own 5-min bars + the rule on every past session, with a verdict |
| `nse-budget` | `desk size` | shares or whole lots within max loss, position cap, order value and cash; Fyers SPAN for sold options |
| `nse-risk` | `desk risk` | LIMIT only, ticks, lots, stop, max loss, R:R, daily loss, entry window, holidays, ban list, price bands, option liquidity |
| `nse-broker` | `desk broker` | paper fills, stop/target brackets, flat by 15:15, charges; approving needs the user's explicit yes |
| `nse-alerts` | `desk alerts`, `desk clock` | price, % move, time and opening-bell alerts (optional Telegram); NSE holidays and expiries |
| `nse-news` | `desk news` | NSE corporate announcements + Indian news headlines, tagged, only what's new |
| `nse-results` | `desk results` | quarterly results from NSE's XBRL filings (SEBI Integrated Filing), year on year |
| `nse-deals` | `desk deals` | bulk/block deals (same-day churn netted out) and SEBI insider-trading disclosures |

A typical session: `desk scan` → `desk news` → `desk entry RELIANCE` →
`desk exit` → `desk sim` → `desk size -write` → `desk risk` →
`desk broker show / approve / place` → `desk broker sync`.

## Files

Everything lives in `~/.nse-desk/` (or `$NSE_DESK_HOME`): your settings
(`.env`), limits (`config.json`, written on first use; edit it), the
hand-off files (`watchlist.json`, `entry.json`, `plan.json`, `orders.json`,
`risk.json`), the paper account (`paper.json`), alerts, and caches of NSE's
files. Add your real holdings to `holdings.txt` (one symbol a line) to have
news and deals flagged for them.

## Good to know

- **Data sources:** NSE's public website and archives (bhavcopies, holidays,
  announcements, results XBRL, bulk/block deals, the F&O ban list, price
  bands), Google News, and your broker's API. NSE's site blocks some
  automated requests and changes without notice; read
  [NSE's terms of use](https://www.nseindia.com/terms-of-use) and keep
  requests light (the desk caches and paces them).
- **Paper fills are optimistic:** at the best visible price, no queue, no
  slippage; a stop fills at the last price, which a gap can carry past.
- **Not investment advice.** SEBI's own studies found that most individual
  intraday and F&O traders lose money. A scan, a replay or a passed risk
  check makes a trade checked, not a winner.

## Development

```bash
go vet ./... && go test ./...
go run ./cmd/desk clock
```

MIT licensed. Issues and pull requests welcome.
