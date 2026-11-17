package desk

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func liveReq() LiveRequest {
	return LiveRequest{ID: "x1", Symbol: "RELIANCE26NOV1400CE", Side: "sell", Qty: 1000, LotSize: 500,
		Limit: 2.40, Stop: 3.40, Target: 1.70, Reply: "yes"}
}

func TestLiveValidate(t *testing.T) {
	if err := liveReq().Validate(); err != nil {
		t.Fatal(err)
	}
	for want, mod := range map[string]func(*LiveRequest){
		"options-only":   func(r *LiveRequest) { r.Symbol = "RELIANCE" },
		"whole number":   func(r *LiveRequest) { r.Qty = 750 },
		"LIMIT":          func(r *LiveRequest) { r.Limit = 0 },
		"carry its stop": func(r *LiveRequest) { r.Stop = 0 },
		"wrong side":     func(r *LiveRequest) { r.Stop = 2 },
		"clear yes":      func(r *LiveRequest) { r.Reply = "ok?" },
	} {
		r := liveReq()
		mod(&r)
		if err := r.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}

func client(t *testing.T, h http.HandlerFunc) (*LiveClient, *int32) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("X-Desk-Token") != "tok" {
			t.Errorf("token %q", r.Header.Get("X-Desk-Token"))
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	f := filepath.Join(t.TempDir(), "live.token")
	os.WriteFile(f, []byte("tok\n"), 0o600)
	c, err := NewLiveClient(srv.URL, f)
	if err != nil {
		t.Fatal(err)
	}
	return c, &calls
}

func TestLiveClient(t *testing.T) {
	c, calls := client(t, func(w http.ResponseWriter, r *http.Request) {
		var req LiveRequest
		json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/api/desk/order" || req.Reply != "yes" || req.Qty != 1000 {
			t.Errorf("%s %+v", r.URL.Path, req)
		}
		json.NewEncoder(w).Encode(LiveResult{OK: true, OrderID: "26111700001", State: "PENDING_ENTRY"})
	})
	res, err := c.Submit(liveReq())
	if err != nil || res.OrderID != "26111700001" || *calls != 1 {
		t.Fatalf("%+v %v calls=%d", res, err, *calls)
	}
	// An invalid request never leaves the desk.
	bad := liveReq()
	bad.Reply = "maybe"
	if _, err := c.Submit(bad); err == nil || *calls != 1 {
		t.Errorf("sent an unapproved order (calls=%d)", *calls)
	}

	// A refusal is a clear error with the trader's reason.
	c2, _ := client(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(LiveResult{Error: "refused by the entry guard — strike volume 10"})
	})
	if _, err := c2.Submit(liveReq()); err == nil || !strings.Contains(err.Error(), "entry guard") {
		t.Errorf("refusal: %v", err)
	}

	// No answer in time: UNKNOWN, sent exactly once (no retry).
	c3, calls3 := client(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) })
	c3.HTTP.Timeout = 50 * time.Millisecond
	if _, err := c3.Submit(liveReq()); !errors.Is(err, ErrUnknown) || *calls3 != 1 {
		t.Errorf("timeout: %v calls=%d", err, *calls3)
	}
}

func TestLiveApprovalIsPerMode(t *testing.T) {
	now := ist("2026-11-23 10:00")
	l := &Ledger{}
	o := Order{ID: "o1", Symbol: "RELIANCE26NOV1400CE", Side: "sell", Qty: 500, LotSize: 500, Type: "LIMIT", Limit: 2.4, Stop: 3.4, MaxLoss: 500}
	if _, err := l.Approve(o, "yes", true, now); err != nil {
		t.Fatal(err)
	}
	// A LIVE approval can't be filled on paper, and vice versa it can't be approved twice.
	if _, err := l.Place("o1", Quote{LTP: 2.4}, now); err == nil || !strings.Contains(err.Error(), "LIVE") {
		t.Errorf("paper place of a live approval: %v", err)
	}
	if _, err := l.Approve(o, "yes", false, now); err == nil {
		t.Error("approved twice")
	}
	// A live order counts towards today's entries and open risk once sent.
	l.Orders[0].Status, l.Orders[0].PlacedAt = "live_sent", now
	if td := l.Today(now); td.Entries != 1 || td.OpenRisk != 500 {
		t.Errorf("today %+v", td)
	}
	l.Orders[0].Status = "live_refused"
	if td := l.Today(now); td.Entries != 0 || td.OpenRisk != 0 {
		t.Errorf("a refused order counted: %+v", td)
	}
}
