---
name: nse-timemachine
description: >-
  NSE Time Machine. Replays every possible outcome of a trade plan before you
  risk a rupee: 10,000 simulated sessions built from the stock's own 5-minute
  bars over the last 60 days (NSE's 75-bar session, 09:15–15:30), plus the same
  rule re-run on every past session. Prints the odds of target first, stop
  first or neither by the flat-by time, where the price sits then, the average
  R, the history and one honest verdict. Use when the user says "run the time
  machine", "replay this trade", "what are the odds", "simulate this trade",
  "monte carlo", "backtest this setup", "how often does this hit the target",
  or after /nse-exit, before /nse-budget.
argument-hint: "[-replays 10000] [-until 15:15] [-days 60]"
---

# nse-timemachine

```bash
desk sim                      # ~/.nse-desk/plan.json, 10,000 replays, seed 19
desk sim -until 14:30         # end each replay earlier
desk sim -days 90 -window 3   # bigger pool, looser time-of-day match
desk sim -json
```

(Needs the `desk` CLI — `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`, then `desk login fyers`; see the repo README. If `desk` is not found, tell the user how to install it rather than guessing.)

How it works: only full sessions (all 75 five-minute bars) go in the pool.
Each replay starts at the bar after the entry and draws every later bar from
the same time of day (± `-window` bars) of a random past session, scaled to
the running price. Stop is checked before target; a bar touching both counts
as the stop; a gap through the stop still counts as −1R here (a real fill can
be worse). The history re-runs the plan's rule (that day's opening range,
first close beyond it, the same stop rule or distance, the same R) on every
past session. Writes `~/.nse-desk/timemachine.json`.

The verdict compares target-first among replays that reached a side with
break-even, 1 in (1 + R): 33% at 2R. Within 5 points: "close to break-even";
5–15: "leans"; 15+: "strongly".

## Reporting

Give the four odds, average R, the history line and the verdict. Always say
it is a replay of recent bars, not a forecast — and that intraday trading
loses most people money. If history and replays disagree (e.g. replays +0.4R,
history −0.04R), say so: the history is what actually happened.
