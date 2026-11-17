package breeze

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// securityMasterURL is ICICI's public scrip master (no login needed).
const securityMasterURL = "https://directlink.icicidirect.com/MotherAppMaster/SecurityMaster.zip"

// StockCodes maps NSE symbols (RELIANCE, M&M) to Breeze stock codes
// (RELIND, MAHMAH). Breeze names every instrument by its own short code, so
// every history call needs this. The NSE cash master is downloaded once and
// kept in dir (re-downloaded when older than a week).
func StockCodes(dir string) (map[string]string, error) {
	path := filepath.Join(dir, "NSEScripMaster.txt")
	data, err := os.ReadFile(path)
	if fi, statErr := os.Stat(path); err != nil || statErr != nil || time.Since(fi.ModTime()) > 7*24*time.Hour {
		fresh, dlErr := downloadNSEMaster()
		switch {
		case dlErr == nil:
			data = fresh
			if os.MkdirAll(dir, 0o755) == nil {
				os.WriteFile(path, data, 0o644)
			}
		case err != nil:
			return nil, fmt.Errorf("breeze security master: %w", dlErr)
		}
	}
	return ParseStockCodes(bytes.NewReader(data))
}

func downloadNSEMaster() ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, securityMasterURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	zipped, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name == "NSEScripMaster.txt" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("NSEScripMaster.txt not in the archive")
}

// ParseStockCodes reads NSEScripMaster.txt: a quoted CSV whose "ShortName"
// column is the Breeze code and "ExchangeCode" the NSE symbol. EQ series
// wins when a symbol is listed in several.
func ParseStockCodes(r io.Reader) (map[string]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("security master: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	short, okS := col["ShortName"]
	exch, okE := col["ExchangeCode"]
	series, okR := col["Series"]
	if !okS || !okE || !okR {
		return nil, fmt.Errorf("security master: ShortName/ExchangeCode/Series columns missing — format change?")
	}
	out := map[string]string{}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if len(rec) <= short || len(rec) <= exch || len(rec) <= series {
			continue
		}
		sym, code := strings.TrimSpace(rec[exch]), strings.TrimSpace(rec[short])
		if sym == "" || code == "" {
			continue
		}
		if _, seen := out[sym]; seen && strings.TrimSpace(rec[series]) != "EQ" {
			continue
		}
		out[sym] = code
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("security master parsed to nothing — format change?")
	}
	return out, nil
}
