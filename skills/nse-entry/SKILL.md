---
name: nse-entry
description: >-
  NSE Entry Timer. Watches an NSE stock (or a stock option) bar by bar and says
  the moment to get in: builds the opening range from 09:15 (15 minutes by
  default) and calls the entry on the first completed 5- or 1-minute bar that
  CLOSES above the range high (long) or below the range low (short — intraday
  shorts are allowed in India), or beyond a level you give (like the 20-day
  high from the scanner), with an optional volume check. Knows NSE hours and
  holidays. Writes ~/.nse-desk/entry.json for /nse-exit. Never places an order.
  Use when the user says "tell me when to get in", "watch RELIANCE", "has it
  broken out", "did it break the opening range", "entry signal", "ORB on TCS",
  or after /nse-scanner.
argument-hint: "SYMBOL [-side long|short] [-minutes 15] [-level PRICE]"
---

# nse-entry

```bash
desk entry RELIANCE                # long: first 5-min close above the 15-min range high
desk entry TCS -side short         # short: first close below the range low
desk entry RELIANCE -level 1347.5  # a custom line (e.g. the scanner's high20)
desk entry RELIANCE -vol 1.5       # trigger bar volume ≥ 1.5× the range bars' average
desk entry RELIANCE -bar 1         # 1-minute bars
desk entry RELIANCE -watch         # keep checking each bar until triggered/expired
desk entry RELIANCE -now "2026-11-16 11:00"   # replay a moment of a past session
```

(Needs the `desk` CLI — `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`, then `desk login fyers`; see the repo README. If `desk` is not found, tell the user how to install it rather than guessing.)

States: **TRIGGERED** (time and price of the trigger bar), **WAITING** (and
how far), **EXPIRED** (no close beyond the line before `no_entries_after`,
default 14:30), **NOT OPEN** (holiday, weekend, before 09:15). Only completed
bars count — a bar still forming never triggers.

Prices come from the first working feed in `DESK_FEEDS`
(fyers → upstox → zerodha → breeze). An option ticker works too
(`RELIANCE26NOV1440CE`).

## Reporting

Say the state in one line, then the range, the line and the trigger bar.
On a trigger, hand off to `/nse-exit` to set the stop and target **before**
anything is sized. `-watch` blocks until it resolves: use it only when the
user asks you to watch.
