---
name: nse-results
description: >-
  NSE Results Analyst. Reads a company's quarterly results straight from its
  NSE filings — the XBRL of SEBI's Integrated Filing (March 2025 quarter on)
  and the older results list before that — and sums up what changed: revenue
  (interest earned for banks), operating margin, profit after tax, net margin
  and EPS, quarter by quarter, each against the same quarter a year earlier,
  consolidated (or standalone), revisions applied. Also gives the next results
  date from NSE's board-meeting list. Use when the user says "how did TCS's
  results look", "analyze the results", "what changed this quarter", "is
  revenue growing", "compare margins", "when are results", "does it report
  before expiry", or before trading a stock around its results.
argument-hint: "SYMBOL [...] [-n 5] [-standalone]"
---

# nse-results

```bash
desk results TCS              # last 5 quarters, consolidated, with YoY
desk results HDFCBANK -n 8
desk results ITC -standalone
desk results TCS -json
```

(Needs the `desk` CLI. If `desk version` fails, tell the user to install it — macOS/Linux: `curl -fsSL https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.sh | sh`; Windows PowerShell: `irm https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.ps1 | iex`; Go users: `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`. No Go or Python is needed. A broker login (e.g. `desk login fyers`) is needed for live prices; see the repo README. Never run the installer without the user asking.)

Figures are ₹ crore from the filing's own XBRL (the full-rupee facts ÷ 10⁷).
Operating margin = (PBT + finance costs − other income) ÷ revenue; it is
left out for banks. EPS is as filed: a split or bonus since then makes older
EPS look higher (HDFC Bank's 1:1 bonus in 2025 halved it). Insurers and some
older forms use other element names and may show a note instead of numbers.

## Reporting

The latest quarter first: revenue and PAT with YoY, margin trend over the
quarters, anything unusual (a one-off drop, a revision). Then the next
results date if one is announced — and if it falls before an option position's
expiry, say so: results are the biggest gap risk for an option seller.
