package desk

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/bhavcopy"
)

// NSEData reads NSE's daily bhavcopies (equity "CM" and F&O "FO") through
// an on-disk cache.
type NSEData struct {
	Client       *bhavcopy.Client
	FODir, CMDir string
	Now          func() time.Time // nil = time.Now
}

func (n NSEData) FO(d time.Time) (*bhavcopy.FODay, error) {
	n.dropRecentMarker(d, n.FODir, "fo_")
	day, err := n.Client.FetchFO(d, n.FODir)
	n.forgetMiss(err, d, n.FODir, "fo_")
	return day, err
}

func (n NSEData) CM(d time.Time) (*bhavcopy.CMDay, error) {
	n.dropRecentMarker(d, n.CMDir, "cm_")
	day, err := n.Client.FetchCM(d, n.CMDir)
	n.forgetMiss(err, d, n.CMDir, "cm_")
	return day, err
}

func (n NSEData) forgetMiss(err error, d time.Time, dir, prefix string) {
	if errors.Is(err, bhavcopy.ErrNotPublished) {
		n.dropRecentMarker(d, dir, prefix)
	}
}

// dropRecentMarker removes the cache's "not published" marker for a day
// under 5 days old (or in the future), so the day is asked of NSE again:
// NSE publishes in the evening, and a miss before that only means "not yet".
func (n NSEData) dropRecentMarker(d time.Time, dir, prefix string) {
	if dir == "" {
		return
	}
	now := time.Now()
	if n.Now != nil {
		now = n.Now()
	}
	if now.Sub(d) < 5*24*time.Hour {
		os.Remove(filepath.Join(dir, prefix+d.Format("20060102")+".csv.zip.none"))
	}
}
