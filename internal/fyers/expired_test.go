package fyers

import "testing"

func TestParseExpired(t *testing.T) {
	ok := `{"s":"ok","columns":["timestamp","open","high","low","close","volume","open_interest"],
		"candles":[[1722483900,80.10,80.15,75.15,78.20,37503,3236000],[1722483960,79,81,78.95,80.25,46000,null]]}`
	cs, err := parseExpired([]byte(ok))
	if err != nil || len(cs) != 2 || cs[0].Close != 78.20 || cs[1].Volume != 46000 ||
		cs[0].Time.UTC().Format("2006-01-02 15:04") != "2024-08-01 03:45" {
		t.Fatalf("ok: %v %+v", err, cs)
	}
	if cs, err := parseExpired([]byte(`{"s":"no_data","candles":[]}`)); err != nil || len(cs) != 0 {
		t.Errorf("no_data should be empty, got %v %v", cs, err)
	}
	if _, err := parseExpired([]byte(`{"s":"error","code":-300,"message":"Invalid symbol provided"}`)); err == nil {
		t.Error("error status should be an error")
	}
}
