---
name: nse-budget
description: >-
  NSE Budget Manager. Sizes a trade to your capital and your max loss per
  trade: shares for stocks (intraday margin at value ÷ 5, delivery at full
  value), whole lots for stock options (a bought option costs its premium; a
  sold one blocks Fyers SPAN margin, or an estimate). Finds the most that fits
  four limits — max loss at the stop, position cap, max order value, cash
  after your buffer and open margin — says which limit decided, and writes ONE
  LIMIT order with its stop and target to ~/.nse-desk/orders.json. A stop too far
  for even one share or lot is a refusal, not a smaller order. Nothing is
  placed. Use when the user says "how many shares", "how many lots", "size this
  trade", "how much should I buy", "I want to risk ₹2,000", or after
  /nse-timemachine, before /nse-risk.
argument-hint: "[-write] [-limit PRICE]"
---

# nse-budget

```bash
desk size              # show the sized order (from ~/.nse-desk/plan.json)
desk size -write       # write it to ~/.nse-desk/orders.json for the risk check
desk size -limit 1345.5 -write   # a limit other than the plan's entry
```

(Needs the `desk` CLI. If `desk version` fails, tell the user to install it — macOS/Linux: `curl -fsSL https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.sh | sh`; Windows PowerShell: `irm https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.ps1 | iex`; Go users: `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`. No Go or Python is needed. A broker login (e.g. `desk login fyers`) is needed for live prices; see the repo README. Never run the installer without the user asking.)

## The limits (~/.nse-desk/config.json)

Written on first use from the settings (`~/.nse-desk/.env` or the
environment): `capital` = DESK_CAPITAL (default ₹10L), `max_loss_per_trade` =
DESK_RISK_PCT of it (0.5%), `max_daily_loss` = DESK_DAILY_LOSS_PCT (1%).
Edit `~/.nse-desk/config.json` to change them (or have the user tell you,
and edit it for them). Others:
`max_position_pct` 25, `max_order_value`, `cash_buffer_pct` 10,
`intraday_leverage` 5, `option_margin_pct` 20 (estimate when SPAN fails).

Every order is LIMIT — NSE stock-option strikes reject market orders, and the
desk never sends one for stocks either. Option sizes are whole lots (the lot
for that expiry from NSE's F&O file).

## Reporting

The order in one line (side, qty, symbol, limit, product), then value,
margin (and whether it is SPAN or an estimate), loss at the stop, target and
**which limit decided the size**. Say plainly that nothing was placed.
Next: `/nse-risk`.
