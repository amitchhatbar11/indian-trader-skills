---
name: nse-deals
description: >-
  NSE Deals Tracker — the Indian take on "what is the smart money buying".
  Watches NSE's bulk and block deals (any client trading over 0.5% of a
  company in a day, and large negotiated block trades) and insider trades
  disclosed under SEBI's insider-trading rules (PIT: promoters, directors, key
  staff). Nets out high-frequency firms that buy and sell the same stock all
  day, shows only what is new, largest first, and flags stocks you hold
  (paper account, or symbols listed in ~/.nse-desk/holdings.txt). Keeps a
  daily copy so a history builds up.
  Use when the user says "who's buying", "bulk deals today", "block deals",
  "promoter buying", "insider trades", "did any fund buy X", "what are big
  investors doing", or "track deals in my stocks".
argument-hint: "[SYMBOL ...] [-min-cr 5] [-churn] [-all] [-insider=false]"
---

# nse-deals

```bash
desk deals                    # today's new deals ≥ ₹5 cr + insider trades (7 days)
desk deals KAYNES -all        # one stock, including already-shown
desk deals -min-cr 25         # only the big ones
desk deals -churn             # also the same-day buy-and-sell churners (netted)
```

(Needs the `desk` CLI. If `desk version` fails, tell the user to install it — macOS/Linux: `curl -fsSL https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.sh | sh`; Windows PowerShell: `irm https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.ps1 | iex`; Go users: `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`. No Go or Python is needed. A broker login (e.g. `desk login fyers`) is needed for live prices; see the repo README. Never run the installer without the user asking.)

Sources: NSE's `bulk.csv` and `block.csv` (today's deals; published after the
close) and the PIT disclosures API. NSE's historical-deals API refuses
automated requests, so `~/.nse-desk/deals/YYYY-MM-DD.json` is kept each day the
command runs. The PIT endpoint sometimes answers with an empty list: the
command says so rather than claiming there were no insider trades.

## Reporting

Lead with positions actually built or cut (a fund or promoter buying, not a
churner), with value in ₹ crore, and anything in a stock the user holds.
Treat deals as context, not signals: a bulk buyer may be one side of a
pre-arranged transfer.
