package fyers

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// FetchFOBanList returns the NSE symbols currently in the F&O ban period.
// Source: NSE's public daily CSV. Format: a header line, then "N,SYMBOL" rows.
// Stocks in ban allow only position reduction — no fresh entries.
func FetchFOBanList() (map[string]bool, error) {
	req, err := http.NewRequest("GET", "https://nsearchives.nseindia.com/content/fo/fo_secban.csv", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ban list fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ban list HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	banned := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ",")
		if len(parts) != 2 {
			continue // header or blank
		}
		if sym := strings.TrimSpace(parts[1]); sym != "" {
			banned[sym] = true
		}
	}
	return banned, nil
}

// BanList caches the NSE ban list for the day. NSE republishes it each
// evening for the next session, so one successful fetch per day is enough; a
// failed fetch is retried after banRetry rather than on every call.
type BanList struct {
	mu      sync.Mutex
	day     string
	set     map[string]bool
	tried   time.Time
	lastErr error
	fetch   func() (map[string]bool, error)
}

const banRetry = 10 * time.Minute

func NewBanList() *BanList { return &BanList{fetch: FetchFOBanList} }

// NewStaticBanList returns a ban list fixed to syms, for tests.
func NewStaticBanList(syms ...string) *BanList {
	set := map[string]bool{}
	for _, s := range syms {
		set[s] = true
	}
	return &BanList{fetch: func() (map[string]bool, error) { return set, nil }}
}

// Ensure loads today's list if it is not already loaded. Safe to call on
// every scan.
func (b *BanList) Ensure(now time.Time) error {
	if b == nil {
		return nil
	}
	day := now.Format("2006-01-02")
	b.mu.Lock()
	if b.day == day || (!b.tried.IsZero() && now.Sub(b.tried) < banRetry) {
		err := b.lastErr
		if b.day == day {
			err = nil
		}
		b.mu.Unlock()
		return err
	}
	b.tried = now
	b.mu.Unlock()

	set, err := b.fetch()

	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastErr = err
	if err != nil {
		return err
	}
	b.set, b.day = set, day
	return nil
}

// Banned reports whether sym is in today's ban list. Known is false when no
// list has loaded yet, so callers can say "unknown" instead of "not banned".
func (b *BanList) Banned(sym string) (banned, known bool) {
	if b == nil {
		return false, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.set == nil {
		return false, false
	}
	return b.set[strings.ToUpper(sym)], true
}

// List returns today's banned symbols.
func (b *BanList) List() []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.set))
	for s := range b.set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
