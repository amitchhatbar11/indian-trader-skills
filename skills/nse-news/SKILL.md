---
name: nse-news
description: >-
  NSE News Monitor. Tracks what moves the stocks you watch: each company's NSE
  corporate announcements (results, board meetings, dividends, splits/bonus,
  deals, order wins, fund-raises, management changes, credit ratings, pledges,
  SEBI/regulatory, surveillance) and Indian news headlines (Google News India),
  with how far each stock is from yesterday's close. Shows only what is new
  since the last check, newest first, in IST, tagged, never repeated. With no
  symbols it covers the scanner's watchlist plus everything held (paper desk
  account and ~/.nse-desk/holdings.txt). Use when the user says "any news on RELIANCE", "why
  is this moving", "what's the news on my watchlist", "morning briefing",
  "pre-market news", "did anything break", "check announcements", or after
  /nse-scanner.
argument-hint: "[SYMBOL ...] [-days 3] [-all] [-no-web]"
---

# nse-news

```bash
desk news RELIANCE TCS        # new items from the last 3 days
desk news                     # watchlist top 10 + holdings
desk news INFY -days 7 -all   # include items already shown
desk news HDFCBANK -no-web    # NSE filings only
```

(Needs the `desk` CLI. If `desk version` fails, tell the user to install it — macOS/Linux: `curl -fsSL https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.sh | sh`; Windows PowerShell: `irm https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.ps1 | iex`; Go users: `go install github.com/amitchhatbar11/indian-trader-skills/cmd/desk@latest`. No Go or Python is needed. A broker login (e.g. `desk login fyers`) is needed for live prices; see the repo README. Never run the installer without the user asking.)

"Already shown" is kept in `~/.nse-desk/state/news_seen.json` (60 days).
NSE filings print their PDF link; news links are long redirects, in `-json`.

## Reporting

Group by stock; lead with what can move a price (results, deals, order wins,
regulatory, pledges, surveillance), then the rest briefly. Pair the move
("−3.2% today") with the item that may explain it, but say when nothing in
the feed explains a move. Headlines are third-party; NSE filings are the
company's own.
