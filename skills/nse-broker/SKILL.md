---
name: nse-broker
description: >-
  NSE Paper Broker. Sends approved orders to the desk's PAPER account
  (~/.nse-desk/paper.json) — never to a real broker. Shows each order as a plain
  ticket (side, qty, LIMIT price, product, stop, target, max loss, margin),
  records the user's yes word for word, then "places" it: a marketable limit
  fills at the better of the limit and the touch, otherwise it rests until the
  price comes. Sync marks the account to market, fills resting orders, closes
  at the stop or target, flattens intraday positions at the flat-by time and
  cancels unfilled entries then. Flatten closes a position now. Log shows
  closed trades with Indian charges (STT, exchange, stamp duty, GST,
  brokerage). Use when the user says "place it", "send the order", "buy it",
  "paper trade this", "show my orders", "close my position", "how's my paper
  account", or after /nse-risk passes.
argument-hint: "show | approve -index N -reply \"yes\" | place -index N | sync | flatten -symbol S | log"
---

# nse-broker (paper only)

**This skill never places a real order.** The desk has no order API: its
broker clients are read-only, and a test fails the build if an order call
appears. If the user asks for a live order, say this skill can't place one
and that they should use their broker's own app or terminal; do not try to
reach a broker another way (no scripts, no API calls).

```bash
desk broker show                          # tickets: READY / BLOCKED / CHANGED / FILLED…
desk broker approve -index 1 -reply "yes" # the user's reply, word for word
desk broker place -index 1                # paper fill or resting limit
desk broker sync                          # fills, stops, targets, flat-by
desk broker flatten -symbol TCS           # close now at the touch
desk broker log                           # closed trades and the account
```

(Needs the `desk` CLI — `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`, then `desk login fyers`; see the repo README. If `desk` is not found, tell the user how to install it rather than guessing.)

## The approval rule

1. Run `broker show` and show the user the ticket exactly as printed.
2. Ask: "Place this as a PAPER order? (yes/no)". Wait for their answer.
3. Only a clear yes ("yes", "y", "confirm", "place it", "go ahead") counts;
   pass their words verbatim to `-reply`. The command itself refuses anything
   else ("ok?", "maybe", "yes but smaller").
4. Never approve on the user's behalf, never reuse an earlier yes for a
   different order, and never approve an order without a PASS from
   `/nse-risk` for its current content (the command refuses those too).

## Reporting

After place: filled (price) or resting (limit). After sync: each event
(fill, STOP, TARGET, FLAT_BY) and the account line. Paper fills are
optimistic: no queue position or slippage beyond the touch; a stop fills at
the last price, which a gap can carry past the stop.
