package fyers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func writeToken(t *testing.T, path, tok string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"access_token":"`+tok+`","created_at":"now"}`), 0600); err != nil {
		t.Fatal(err)
	}
}

// The exact failure seen live: the process holds a stale token in memory
// while a fresh one sits on disk. A 401 must trigger a reload and retry
// rather than failing every batch until someone restarts the bot.
func TestRequestReloadsTokenOn401(t *testing.T) {
	var calls int32
	var sawTokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		auth := r.Header.Get("Authorization")
		sawTokens = append(sawTokens, auth)
		if auth != "APP:fresh" {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Please provide valid token","code":-15,"s":"error"}`))
			return
		}
		w.Write([]byte(`{"s":"ok"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "fyers_token.json")
	writeToken(t, path, "fresh") // disk has the good token

	c := New("APP", "secret", "stale") // memory has the old one
	c.SetTokenPath(path)

	data, err := c.doRequestBase(srv.URL, "GET", "/quotes", nil)
	if err != nil {
		t.Fatalf("should have recovered by reloading the token, got %v", err)
	}
	if string(data) != `{"s":"ok"}` {
		t.Errorf("body = %s", data)
	}
	if calls != 2 {
		t.Errorf("want 2 calls (401 then retry), got %d", calls)
	}
	if c.Token != "fresh" {
		t.Errorf("token not adopted: %q", c.Token)
	}
	if c.TokenRejected() {
		t.Error("a recovered request should clear the rejected flag")
	}
}

// If the disk token is the same stale one, retrying is pointless — fail
// fast and flag it so the dashboard can prompt for a reconnect.
func TestRequest401NoRetryWhenDiskTokenSame(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"Please provide valid token","code":-15,"s":"error"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "fyers_token.json")
	writeToken(t, path, "stale")

	c := New("APP", "secret", "stale")
	c.SetTokenPath(path)

	if _, err := c.doRequestBase(srv.URL, "GET", "/quotes", nil); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("should not retry when disk token is identical, got %d calls", calls)
	}
	if !c.TokenRejected() {
		t.Error("TokenRejected should be set so the UI can prompt a reconnect")
	}
}

func TestReloadTokenFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.json")

	c := New("APP", "s", "old")
	if c.reloadTokenFromDisk() {
		t.Error("no token path configured should return false")
	}
	c.SetTokenPath(path)
	if c.reloadTokenFromDisk() {
		t.Error("missing file should return false")
	}
	writeToken(t, path, "old")
	if c.reloadTokenFromDisk() {
		t.Error("identical token should return false (no point retrying)")
	}
	writeToken(t, path, "new")
	if !c.reloadTokenFromDisk() || c.Token != "new" {
		t.Errorf("newer token should be adopted, got %q", c.Token)
	}
	os.WriteFile(path, []byte("not json"), 0600)
	if c.reloadTokenFromDisk() {
		t.Error("corrupt file should return false")
	}
	if c.Token != "new" {
		t.Errorf("corrupt file must not clobber a working token, got %q", c.Token)
	}
}

func TestTokenRejectedClearedOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"s":"ok"}`))
	}))
	defer srv.Close()
	c := New("APP", "s", "tok")
	c.setTokenRejected(true)
	if _, err := c.doRequestBase(srv.URL, "GET", "/x", nil); err != nil {
		t.Fatal(err)
	}
	if c.TokenRejected() {
		t.Error("successful request should clear the flag")
	}
}
