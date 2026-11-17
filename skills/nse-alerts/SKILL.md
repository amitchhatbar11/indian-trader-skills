---
name: nse-alerts
description: >-
  NSE Price Alerts. Rings when a price, a stop or the opening bell hits. Turns
  the trade plan into its alerts in one go (the stop, the target and "flat by
  close" at 15:15), plus above / below levels, % moves from the previous
  close, any time of day (IST) and the 09:15 opening bell — each ringing once.
  Knows NSE hours and holidays (from NSE's holiday list), reads quotes through
  DESK_FEEDS, and can send what rang to your own Telegram bot. Use when the
  user says "alert me when", "tell me if RELIANCE hits 1350", "ping me at the
  stop", "remind me to close by 3:15", "wake me at the open", "is the market
  open", "next holiday", "when is expiry", or after /nse-exit.
argument-hint: "add ... | check [-telegram] | list | reset ID|all | (clock)"
---

# nse-alerts

```bash
desk clock                                   # open/closed, next session, holidays, expiries, feeds
desk alerts add -from-plan                   # stop, target, flat-by from ~/.nse-desk/plan.json
desk alerts add -symbol RELIANCE -above 1350
desk alerts add -symbol TCS -below 2380 -label "support"
desk alerts add -symbol INFY -pct -3         # down 3% on the day
desk alerts add -at 15:10 -label "close intraday"
desk alerts add -bell
desk alerts check                            # rings what is due (once each)
desk alerts check -telegram                  # …and sends it to your Telegram bot
desk alerts list
desk alerts reset all
```

(Needs the `desk` CLI — `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`, then `desk login fyers`; see the repo README. If `desk` is not found, tell the user how to install it rather than guessing.)

Alerts live in `~/.nse-desk/alerts.json`. `check` is one pass: to keep watching,
run it on a schedule (the user's cron, or a loop they ask for). Telegram uses
`TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` from `~/.nse-desk/.env` and goes only to the
user's own chat — only add `-telegram` when they ask for it.

## Reporting

`clock`: state, next session, upcoming holidays, stock-option expiry dates
(from NSE's F&O file — holiday shifts included). `check`: what rang with the
price, or "nothing rang (N waiting)".
