package livesrv

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amitchhatbar11/indian-trader-skills/internal/book"
	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
)

func post(s *Server, remote, token, body string) (int, desk.LiveResult) {
	r := httptest.NewRequest(http.MethodPost, "/api/desk/order", strings.NewReader(body))
	r.RemoteAddr = remote
	r.Header.Set("X-Desk-Token", token)
	w := httptest.NewRecorder()
	s.order(w, r)
	var res desk.LiveResult
	json.NewDecoder(w.Body).Decode(&res)
	return w.Code, res
}

const body = `{"id":"x","symbol":"RELIANCE26NOV1400CE","side":"sell","qty":500,"lot_size":500,"limit":2.4,"stop":3.4,"reply":"yes"}`

func TestGates(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Book: book.New(dir), TokenFile: filepath.Join(dir, "live.token"),
		Ready: func() error { return errors.New("no valid broker session — refresh its token") }}
	// Only loopback addresses can be listened on.
	if _, err := s.Listen("0.0.0.0:0"); err == nil {
		t.Fatal("listened on all interfaces")
	}
	srv, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	fi, err := os.Stat(s.TokenFile)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file %v %v", fi, err)
	}
	if code, _ := post(s, "10.0.0.5:4000", s.token, body); code != http.StatusForbidden {
		t.Errorf("remote: %d", code)
	}
	if code, _ := post(s, "127.0.0.1:4000", "nope", body); code != http.StatusUnauthorized {
		t.Errorf("bad token: %d", code)
	}
	// No working broker session: refused before any order logic.
	if code, res := post(s, "127.0.0.1:4000", s.token, body); code != http.StatusServiceUnavailable || !strings.Contains(res.Error, "session") {
		t.Errorf("no login: %d %+v", code, res)
	}
	if len(s.Book.List()) != 0 {
		t.Error("a position was recorded")
	}
}
