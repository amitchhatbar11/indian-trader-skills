---
name: nse-scanner
description: >-
  NSE Chart Scanner. Scans every F&O stock (or every NSE stock) for setups on the
  last close — 20-day breakout or breakdown, bull flag, reclaimed the 20-day
  average, oversold, oversold bounce, overbought — from NSE's own bhavcopies,
  split- and bonus-adjusted, then optionally checks today's opening-range
  breakouts on 5-minute bars (Fyers / Upstox / Zerodha / Breeze). Writes
  ~/.nse-desk/watchlist.json for nse-news and nse-entry. Use when the user says
  "scan the market", "find me setups", "what is breaking out", "NSE breakouts",
  "opening range breakouts", "scan for bull flags", "oversold stocks", "which
  F&O stocks to watch today", or before /nse-entry.
argument-hint: "[-universe fno|all] [-setup NAME] [-orb]"
---

# nse-scanner

Part of the Indian trading desk (the `desk` CLI). Read-only: it never places
anything.

(Needs the `desk` CLI. If `desk version` fails, tell the user to install it — macOS/Linux: `curl -fsSL https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.sh | sh`; Windows PowerShell: `irm https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.ps1 | iex`; Go users: `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`. No Go or Python is needed. The daily scan needs no broker login; `-orb` needs one (`desk login fyers`); see the repo README. Never run the installer without the user asking.)

```bash
desk scan                        # F&O stocks, last close (watchlist.json)
desk scan -universe all          # every EQ stock above the price/turnover floors
desk scan -setup breakout20      # one setup only
desk scan -orb                   # + today's opening-range breakouts for the top 30
desk scan -orb RELIANCE TCS      # ORB status for these stocks
desk scan -json
```

## What it reads and writes

| | |
|---|---|
| `~/.nse-desk/bhavcopy_cm`, `bhavcopy_fo` | NSE's official daily files (downloaded once and cached) |
| `~/.nse-desk/corpact` | NSE's splits/bonuses, so a 2:1 split is not a "breakdown" |
| price feed | `-orb` only: today's 5-minute bars, through `DESK_FEEDS` |
| `~/.nse-desk/config.json` | `min_price` (₹50) and `min_turnover_cr` (₹10 cr) floors |
| `~/.nse-desk/watchlist.json` | written: the picks, and ORB states with `-orb` |

The daily scan uses the **last published close**: before ~18:30 that is the
previous session (NSE publishes in the evening).

## Setups

| setup | rule | side |
|---|---|---|
| breakout20 | close above the prior 20 sessions' highest close | long |
| breakdown20 | close below the prior 20 sessions' lowest close | short |
| bull_flag | ≥10% pole in the last 10 sessions, then 3 sessions within 4%, close within 3% of the pole high | long |
| reclaim20 | closed back above the 20-day average | long |
| oversold | RSI(14) < 30 | long (watch) |
| bounce | RSI < 30 yesterday and today closed above yesterday's high | long |
| overbought | RSI(14) > 75 — a warning next to a long setup; a short watch on its own | — |

ORB states: `forming` (range not done), `inside`, `broke_up` / `broke_down`
with the time and the breakout bar's volume ÷ the range bars' average.

## Reporting

Lead with the handful that matter: several setups at once, high `vol×20d`,
F&O stocks if the user trades options. Say which close it is ("Monday's
close"). Mention `overbought` as a caution on longs. Point to the next step:
`/nse-news` on the names, then `/nse-entry SYMBOL` during the session.
A scan is a list of candidates, not a recommendation.
