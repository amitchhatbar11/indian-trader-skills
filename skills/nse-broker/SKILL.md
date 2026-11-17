---
name: nse-broker
description: >-
  NSE Broker — PAPER by default, LIVE (real money) only when the user asks for
  it. Paper: sends approved orders to the desk's paper account
  (~/.nse-desk/paper.json), fills a marketable limit at the touch or rests it,
  and sync runs fills, stops, targets and the flat-by close with Indian charges
  (STT, exchange, stamp duty, GST, brokerage). Live: hands one user-approved
  stock-option LIMIT order at a time to `desk serve`, which places it on Fyers
  through its position book and tracks it to the fill, runs its stop and
  reconciles with the broker — only with DESK_LIVE=true, a typed yes for that
  exact order, and the agent's permission prompt. Use when the user says
  "place it", "send the order", "buy it", "paper trade this", "place it live",
  "real order", "show my orders", "close my position", "my live positions",
  "how's my paper account", or after /nse-risk passes.
argument-hint: "show [-live] | approve -index N -reply \"yes\" [-live] | place -index N | sync | flatten -symbol S | log | live | live-exit"
---

# nse-broker

Two modes. **Paper is the default and the answer to any order request that
doesn't explicitly say live / real money.**

(Needs the `desk` CLI — `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`, then `desk login fyers`; see the repo README. If `desk` is not found, tell the user how to install it rather than guessing.)

```bash
desk broker show                          # tickets: READY / BLOCKED / CHANGED / FILLED…
desk broker approve -index 1 -reply "yes" # the user's reply, word for word
desk broker place -index 1                # paper fill or resting limit
desk broker sync                          # paper: fills, stops, targets, flat-by
desk broker flatten -symbol TCS           # paper: close now at the touch
desk broker log                           # paper: closed trades and the account
```

## Paper approval

1. Run `broker show` and show the user the ticket exactly as printed.
2. Ask: "Place this as a PAPER order? (yes/no)". Wait for their answer.
3. Only a clear yes ("yes", "y", "confirm", "place it", "go ahead") counts;
   pass their words verbatim to `-reply`. The command refuses anything else.
4. Never approve on the user's behalf, never reuse a yes for another order,
   never approve without a PASS from `/nse-risk` for the order's current
   content (the command refuses those too).

## LIVE orders (real money)

The desk CLI never talks to Fyers' order API. A live order is handed to
`desk serve` — a separate process the **user** starts and keeps running in a
terminal — which places it through its position book: the entry guard (daily
loss halt, F&O ban, spread, price band; any warning refuses), one LIMIT order
sent exactly once, PENDING until Fyers confirms the fill, the stop monitor,
reconciliation with Fyers.

Preconditions — if any is missing, stop and tell the user; never work around it:
- The user explicitly asked for a LIVE / real-money order in this conversation.
- `DESK_LIVE=true` in `~/.nse-desk/.env` and `desk serve` running. **Only the
  user sets DESK_LIVE and starts desk serve. Never edit their .env, never
  start desk serve for them, never suggest flipping DESK_LIVE to get past a
  refusal.**
- Stock options only (whole lots). Cash-stock orders stay paper for now.
- A PASS from `/nse-risk` for this exact order.

Steps:
1. `desk broker show -live` and show the ticket **exactly** as printed, with
   its "LIVE ORDER — REAL MONEY" heading.
2. Ask: "Place this as a LIVE order with real money on your Fyers account?
   (yes/no)". One order per question. Never "yes to all". A paper yes never
   counts for live.
3. On a clear yes: `desk broker approve -index N -reply "<their words>" -live`.
4. Then `desk broker place -index N` — run it so the user's own permission
   prompt appears; never from a scheduled or background task.
5. Report the result word for word: the Fyers order id and PENDING_ENTRY
   ("accepted is not filled"), or the refusal reason. **If it says the answer
   was UNKNOWN, do not run place again** — tell the user to check
   `desk broker live` and the Fyers order book; the desk marks it so it can
   never be resent.

After entry: `desk broker live` shows the live positions (fill, LTP, stop,
P&L). `desk serve` closes them at their stop or target by itself while it is
running — if it is stopped, nothing watches them. To close one by hand, show
the exit and ask the same kind of yes, then
`desk broker live-exit -symbol SYM -limit PRICE -reply "<their words>"`.
The exit stays PENDING until Fyers confirms it.

## Reporting

Paper: filled or resting; after sync each event and the account line (paper
fills are optimistic: no queue, no slippage; a stop fills at the last price).
Live: the order id and state, never a fill you have not seen confirmed.
