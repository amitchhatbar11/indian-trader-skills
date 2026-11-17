package fyers

import (
	"errors"
	"testing"
	"time"
)

func TestBanListCachesPerDay(t *testing.T) {
	calls := 0
	b := &BanList{fetch: func() (map[string]bool, error) {
		calls++
		return map[string]bool{"IDEA": true}, nil
	}}
	if _, known := b.Banned("IDEA"); known {
		t.Error("list should be unknown before the first load")
	}
	day := time.Date(2026, 8, 13, 9, 15, 0, 0, time.Local)
	b.Ensure(day)
	b.Ensure(day.Add(time.Hour))
	if calls != 1 {
		t.Errorf("fetched %d times in one day, want 1", calls)
	}
	if banned, known := b.Banned("idea"); !banned || !known {
		t.Error("IDEA should be banned (case-insensitive)")
	}
	if banned, _ := b.Banned("TCS"); banned {
		t.Error("TCS should not be banned")
	}
	b.Ensure(day.AddDate(0, 0, 1))
	if calls != 2 {
		t.Errorf("new day should refetch, calls = %d", calls)
	}
}

func TestBanListBacksOffOnFailure(t *testing.T) {
	calls := 0
	b := &BanList{fetch: func() (map[string]bool, error) {
		calls++
		return nil, errors.New("NSE down")
	}}
	now := time.Date(2026, 8, 13, 9, 15, 0, 0, time.Local)
	if err := b.Ensure(now); err == nil {
		t.Fatal("expected the fetch error")
	}
	if err := b.Ensure(now.Add(time.Minute)); err == nil || calls != 1 {
		t.Errorf("retried too soon (calls %d) or swallowed the error (%v)", calls, err)
	}
	b.Ensure(now.Add(11 * time.Minute))
	if calls != 2 {
		t.Errorf("should retry after the backoff, calls = %d", calls)
	}
}

func TestNilBanListIsSafe(t *testing.T) {
	var b *BanList
	if err := b.Ensure(time.Now()); err != nil {
		t.Error(err)
	}
	if banned, known := b.Banned("X"); banned || known {
		t.Error("nil list should be unknown, not banned")
	}
}
