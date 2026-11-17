---
name: nse-risk
description: >-
  NSE Risk Manager. Checks every proposed order against your limits and stops
  anything over them before it can reach the paper broker: LIMIT only, NSE tick
  and lot multiples, a stop required, loss at the stop within your max loss per
  trade (measured from the worse of the limit and a fresh price), reward:risk
  (1.5 for stocks, 0.5 for sold options), the day's losses plus open risk
  within the daily loss limit, trades and orders per day, entry window
  (09:30–14:30) and NSE hours/holidays, F&O ban list, circuit price bands,
  option liquidity (25 lots traded, spread ≤ 5%), stock shorts only
  intraday, position cap, margin, duplicates. Exits are never blocked by a day
  limit. Prints PASS or BLOCK with reasons and records a fingerprint of each
  order, so the broker only takes an order that passed unchanged. Use when the
  user says "check my order", "is this safe", "risk check", "does this break my
  rules", "can I take this trade", "how much have I risked today", and always
  before /nse-broker.
argument-hint: "[-no-write]"
---

# nse-risk

```bash
desk risk               # ~/.nse-desk/orders.json → ~/.nse-desk/risk.json
desk risk -no-write     # look without recording
desk risk -now "2026-11-23 10:00"   # pin the clock (testing only)
```

(Needs the `desk` CLI — `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`, then `desk login fyers`; see the repo README. If `desk` is not found, tell the user how to install it rather than guessing.)

Orders are checked in file order as if each passed one had filled, so a batch
that is fine order by order but breaks a limit together is caught. A BLOCK is
final for that order: it is not shown to the user for a yes and the broker
refuses it. Editing an order after the check changes its fingerprint: check
again.

Sources: fresh quotes (DESK_FEEDS), NSE's F&O ban list, NSE's price-band list
(`sec_list.csv`; F&O stocks have no fixed band), NSE holidays, and the paper
ledger for today's activity. When one of these can't be read the check says
so (a warning, or a block for a missing quote) rather than guessing.

## Reporting

PASS/BLOCK per order with every reason, then the day line (entries, realised,
open risk, daily limit). Never suggest a way around a BLOCK other than
changing the trade itself (smaller size, a closer stop, waiting for the
window). Don't use `-now` to make a closed market look open.
