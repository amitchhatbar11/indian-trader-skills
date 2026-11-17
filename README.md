# Indian Trader Skills

[![skills.sh](https://skills.sh/b/amitchhatbar11/indian-trader-skills)](https://skills.sh/amitchhatbar11/indian-trader-skills)
[![release](https://img.shields.io/github/v/release/amitchhatbar11/indian-trader-skills)](https://github.com/amitchhatbar11/indian-trader-skills/releases)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Eleven **agent skills for trading the Indian market (NSE)**: cash stocks
(intraday, shorts included) and stock options. Scan for setups, time the
entry, plan the stop and target, replay the trade 10,000 times, size it,
check it against your limits and **paper-trade** it — plus news, quarterly
results, bulk/block deals, insider trades, price alerts, and optional live
orders to Fyers, Upstox, Zerodha, Dhan or Angel One.

Built for NSE's rules, not a US template: IST sessions and holidays, NSE tick
sizes (2025 bands), F&O lot sizes per expiry, the F&O ban list, price bands,
Indian charges (STT, stamp duty, GST), SEBI's Integrated Filing XBRL.

> **Paper by default.** Orders go to a local paper account unless you switch on
> live trading yourself (`DESK_LIVE=true` + `desk serve`, below). Live is stock
> options only, one approved LIMIT order at a time.

## Try it — three prompts

Install the skills and the `desk` CLI (below), then ask your agent:

- *"Scan NSE F&O stocks for breakouts on the last close, and tell me which have news."*
- *"Set a stop and target for TCS after its opening range, replay it, and size it to my limits."*
- *"How did TCS's September quarter look, and did the big funds buy it?"*

## Install

**1. The skills** — any agent that reads skills (Claude Code, Codex, Cursor, OpenCode, and [75 more](https://github.com/vercel-labs/skills)):

```bash
npx skills add amitchhatbar11/indian-trader-skills
```

Claude Code users can also install it as a plugin: `/plugin marketplace add amitchhatbar11/indian-trader-skills`

**2. The `desk` CLI** — no Go, no Python, no admin rights:

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.sh | sh

# Windows (PowerShell)
irm https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.ps1 | iex
```

It downloads the binary for your machine from [Releases](https://github.com/amitchhatbar11/indian-trader-skills/releases),
checks its SHA-256, and puts it in `~/.local/bin` (`%LOCALAPPDATA%\desk` on
Windows). Go users: `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`.

```bash
desk version
desk clock        # NSE open/closed, holidays, stock-option expiries, price feeds
```

**3. A price feed** (for live quotes and intraday bars; the daily scanner,
news, results and deals need none):

| Broker | Setting (in `~/.nse-desk/.env`) | Notes |
|---|---|---|
| Fyers | `FYERS_APP_ID`, `FYERS_APP_SECRET`, `FYERS_REDIRECT_URI`, then `desk login fyers` | quotes, 1/5-min bars, SPAN margin |
| Upstox | `UPSTOX_ACCESS_TOKEN` | quotes, bars |
| Zerodha | `KITE_API_KEY`, `KITE_ACCESS_TOKEN` | quotes; bars need Kite's historical add-on |
| ICICI Breeze | `BREEZE_API_KEY`, `BREEZE_SESSION` | bars only |

`DESK_FEEDS` (default `fyers,upstox,zerodha,breeze`) sets the order; brokers
without credentials are skipped, and a failing one falls through to the next.
`desk login upstox|zerodha|dhan|angelone|breeze` explains each token.

## The skills

| Skill | Command | What it does |
|---|---|---|
| `nse-scanner` | `desk scan` | 20-day breakouts/breakdowns, bull flags, 20-DMA reclaims, oversold bounces, overbought, from NSE's bhavcopies (split-adjusted); `-orb` adds today's opening-range breakouts |
| `nse-entry` | `desk entry` | first completed bar to close beyond the opening range (long or short), optional volume check |
| `nse-exit` | `desk exit` | stop (range mid/edge, %, ATR, price) and target (R), rounded to NSE ticks; options 40%/30% of premium |
| `nse-timemachine` | `desk sim` | 10,000 replays from the stock's own 5-min bars + the rule on every past session, with a verdict |
| `nse-budget` | `desk size` | shares or whole lots within max loss, position cap, order value and cash; Fyers SPAN for sold options |
| `nse-risk` | `desk risk` | LIMIT only, ticks, lots, stop, max loss, R:R, daily loss, entry window, holidays, ban list, price bands, option liquidity |
| `nse-broker` | `desk broker` | paper fills, stop/target brackets, flat by 15:15, charges; optional LIVE option orders via `desk serve`; every order needs the user's explicit yes |
| `nse-alerts` | `desk alerts`, `desk clock` | price, % move, time and opening-bell alerts (optional Telegram); NSE holidays and expiries |
| `nse-news` | `desk news` | NSE corporate announcements + Indian news headlines, tagged, only what's new |
| `nse-results` | `desk results` | quarterly results from NSE's XBRL filings (SEBI Integrated Filing), year on year |
| `nse-deals` | `desk deals` | bulk/block deals (same-day churn netted out) and SEBI insider-trading disclosures |

A typical session: `desk scan` → `desk news` → `desk entry RELIANCE` →
`desk exit` → `desk sim` → `desk size -write` → `desk risk` →
`desk broker show / approve / place` → `desk broker sync`.

## Live trading (real money, optional)

Off unless you turn it on. You need an account with one of the brokers below,
its token, a price feed (any broker in the feeds table), and:

```bash
# in ~/.nse-desk/.env
DESK_LIVE=true
ORDER_BROKER=fyers        # or upstox, zerodha, dhan, angelone

# in a terminal you keep open during market hours
desk serve
```

| `ORDER_BROKER` | Credentials | Product used | Notes |
|---|---|---|---|
| `fyers` (default) | `desk login fyers` | MARGIN | |
| `upstox` | `UPSTOX_ACCESS_TOKEN` | D (carry forward) | |
| `zerodha` | `KITE_API_KEY`, `KITE_ACCESS_TOKEN` | NRML | |
| `dhan` | `DHAN_CLIENT_ID`, `DHAN_ACCESS_TOKEN` | MARGIN | Dhan accepts orders only from a whitelisted static IP |
| `angelone` | `ANGEL_API_KEY`, then `desk login angelone` (client code, MPIN, TOTP typed by you) | CARRYFORWARD | |

Dhan and Angel One supply orders only here; their quotes come from your
other feeds (Fyers, Upstox or Zerodha).

`desk serve` is the only part that can place an order. It listens on
127.0.0.1 only, accepts requests only with the token it writes to
`~/.nse-desk/live.token` (new each start), and places orders through a
position book that:

- checks every entry again (daily loss halt, NSE F&O ban list, bid-ask
  spread, price band) and refuses on any warning;
- sends each **LIMIT** order **exactly once** (an unclear answer is looked up
  by its order tag, never resent), and keeps it `PENDING` until the broker
  confirms the fill, partial fills included;
- closes a position at its stop or target with a LIMIT order, keeping it
  open if that exit is rejected;
- compares itself with your broker positions every minute and stops acting
  on anything that disagrees.

The agent can only send an order you approved: `desk broker show -live`
prints the ticket with a "LIVE ORDER — REAL MONEY" heading, your typed yes is
recorded word for word and checked again by `desk serve`, and the place
command runs under your agent's own permission prompt. **Stock options only**
(cash stocks stay paper). When `desk serve` is stopped, nothing watches open
positions: their stops are not monitored.

This code has been tested with unit tests and simulated broker responses
built from each broker's published API, **not against live accounts**.
Start with one lot, and watch the first orders in your broker's app.

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
- **SEBI's retail algo rules:** live orders go through your broker's API, so
  your broker's requirements (static IP whitelisting, order-rate limits, the
  exchange's algo registration where it applies) are yours to meet.
- **Paper fills are optimistic:** at the best visible price, no queue, no
  slippage; a stop fills at the last price, which a gap can carry past.
- **Live trading is at your own risk.** The software is provided as is (MIT,
  no warranty); a bug, an outage or a gap can lose real money.
- **Not investment advice.** SEBI's own studies found that most individual
  intraday and F&O traders lose money. A scan, a replay or a passed risk
  check makes a trade checked, not a winner.

## Development

```bash
go vet ./... && go test ./...     # Go 1.22+
go run ./cmd/desk clock
```

Releases: push a tag (`git tag v0.3.1 && git push --tags`); the workflow
builds every platform and publishes the archives with checksums.

MIT licensed. Issues and pull requests welcome.
