package desk

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// NSE reads NSE's public website: the JSON API (holidays, announcements,
// results, board meetings) and the archive files (bulk/block deals, price
// bands, the ban list). Responses are cached on disk for a TTL so the skills
// stay polite; NSE's bot protection blocks some endpoints (live quotes,
// historical deals), which the callers treat as "not available".
type NSE struct {
	Base     string // API host, default https://www.nseindia.com
	CacheDir string // "" = no cache
	HTTP     *http.Client
	Pause    time.Duration // between fresh requests

	mu   sync.Mutex
	last time.Time
}

const nseUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func NewNSE(cacheDir string) *NSE {
	jar, _ := cookiejar.New(nil)
	return &NSE{Base: "https://www.nseindia.com", CacheDir: cacheDir,
		HTTP: &http.Client{Timeout: 40 * time.Second, Jar: jar}, Pause: 700 * time.Millisecond}
}

// API fetches /api/<path> (e.g. "holiday-master?type=trading").
func (n *NSE) API(path string, ttl time.Duration) ([]byte, error) {
	return n.Get(n.Base+"/api/"+path, ttl)
}

// Get fetches any NSE URL through the cache.
func (n *NSE) Get(url string, ttl time.Duration) ([]byte, error) {
	cache := ""
	if n.CacheDir != "" {
		h := sha1.Sum([]byte(url))
		cache = filepath.Join(n.CacheDir, hex.EncodeToString(h[:8]))
		if fi, err := os.Stat(cache); err == nil && (ttl <= 0 || time.Since(fi.ModTime()) < ttl) {
			return os.ReadFile(cache)
		}
	}
	data, err := n.fetch(url)
	if err != nil {
		if cache != "" {
			if old, e := os.ReadFile(cache); e == nil {
				return old, nil // stale beats nothing
			}
		}
		return nil, err
	}
	if cache != "" && os.MkdirAll(n.CacheDir, 0o755) == nil {
		os.WriteFile(cache, data, 0o644)
	}
	return data, nil
}

func (n *NSE) fetch(url string) ([]byte, error) {
	n.mu.Lock()
	if wait := n.Pause - time.Since(n.last); wait > 0 {
		time.Sleep(wait)
	}
	n.last = time.Now()
	n.mu.Unlock()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", nseUA)
	req.Header.Set("Referer", "https://www.nseindia.com/")
	req.Header.Set("Accept", "application/json,text/csv,application/xml,*/*")
	resp, err := n.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NSE %s: HTTP %d (NSE's site blocks some automated requests)", url, resp.StatusCode)
	}
	return body, nil
}
