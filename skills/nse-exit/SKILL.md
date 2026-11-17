---
name: nse-exit
description: >-
  NSE Exit Planner. Sets the stop and the target before you enter. Reads the
  entry from /nse-entry (or a price you give), puts the stop at the
  opening-range midpoint by default (or the range edge, a percent, a multiple
  of the daily ATR, or your price), rounds it to NSE's tick in the safe
  direction (₹0.01 below ₹250, ₹0.05, ₹0.10, ₹0.50, ₹1, ₹5 by price; options
  ₹0.01/₹0.05), and sets the target at 2R (or your R or price). For options it
  uses a premium stop/target (default 40% stop / 30% target).
  Refuses a stop on the wrong side, a stop inside the bid-ask spread, or under
  3 ticks, and says why. Writes ~/.nse-desk/plan.json. Use when the user says
  "where's my stop", "set my stop and target", "plan the exit", "when do I get
  out", "what's my risk", or after /nse-entry triggers.
argument-hint: "[-stop mid|range|pct|atr|price] [-stop-val N] [-r 2] [-target PRICE]"
---

# nse-exit

```bash
desk exit                               # from ~/.nse-desk/entry.json: stop at range mid, 2R
desk exit -stop range                   # stop at the range low (long) / high (short)
desk exit -stop pct -stop-val 1         # 1% from the entry
desk exit -stop atr -stop-val 0.5       # half a daily ATR
desk exit -stop price -stop-val 1338 -r 3
desk exit -symbol TCS -side short -entry 2408   # no entry.json: plan from a price
desk exit -symbol RELIANCE26NOV1400CE -side short -entry 2.40   # sold option: 40% stop, 30% target
```

(Needs the `desk` CLI — `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`, then `desk login fyers`; see the repo README. If `desk` is not found, tell the user how to install it rather than guessing.)

Product: stocks default to INTRADAY (`default_product` in
`~/.nse-desk/config.json`), with a flat-by of 15:15 (brokers square off MIS
around 15:20). A sold option is MARGIN, a bought one INTRADAY.

## Reporting

Entry, stop (and its rule), target, R, risk per share/unit, tick, product.
If it refuses, quote the reason and suggest the fix (a wider stop rule, or
skip the trade). Next: `/nse-timemachine`, then `/nse-budget`.
