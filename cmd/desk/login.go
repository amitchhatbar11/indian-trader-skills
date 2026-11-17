package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/amitchhatbar11/indian-trader-skills/internal/brokers"
	"github.com/amitchhatbar11/indian-trader-skills/internal/fyers"
)

// cmdLogin handles broker logins. Fyers is an OAuth paste flow (tokens
// expire around 06:00 IST, so run it each trading day); the others take a
// token you set in ~/.nse-desk/.env.
func cmdLogin(e *env, args []string) error {
	broker := "fyers"
	if len(args) > 0 {
		broker = strings.ToLower(args[0])
	}
	switch broker {
	case "fyers":
		c := e.cfg
		if c.FyersAppID == "" || c.FyersAppSecret == "" || c.FyersRedirectURI == "" {
			return fmt.Errorf("set FYERS_APP_ID, FYERS_APP_SECRET and FYERS_REDIRECT_URI in %s (create an API app at https://myapi.fyers.in)",
				filepath.Join(c.DataDir, ".env"))
		}
		fy := fyers.New(c.FyersAppID, c.FyersAppSecret, "")
		fy.SetRedirectURI(c.FyersRedirectURI)
		fy.SetTokenPath(filepath.Join(c.DataDir, "fyers_token.json"))
		u, err := fy.GenerateAuthURL()
		if err != nil {
			return err
		}
		fmt.Printf("\n1) Open this link, log in to Fyers and approve:\n\n   %s\n", u)
		fmt.Printf("\n2) Paste the whole URL Fyers redirects you to (or just its auth_code) and press Enter:\n\n> ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		code, state := parsePasted(strings.TrimSpace(line))
		if code == "" {
			return fmt.Errorf("no auth_code in what you pasted")
		}
		if err := fy.ExchangeCode(code, state); err != nil {
			return fmt.Errorf("token exchange failed: %w", err)
		}
		fmt.Println("\n✓ Fyers token saved to " + filepath.Join(c.DataDir, "fyers_token.json") + " (valid until ~06:00 IST)")
		return nil
	case "upstox":
		fmt.Println("Upstox: create an app at https://account.upstox.com/developer/apps, get the day's access token\n" +
			"(OAuth, valid until ~03:30 IST) and set UPSTOX_ACCESS_TOKEN in " + filepath.Join(e.cfg.DataDir, ".env"))
	case "zerodha", "kite":
		fmt.Println("Zerodha: Kite Connect app at https://developers.kite.trade (historical data is a paid add-on);\n" +
			"set KITE_API_KEY and the day's KITE_ACCESS_TOKEN in " + filepath.Join(e.cfg.DataDir, ".env"))
	case "breeze":
		fmt.Println("ICICI Breeze: app at https://api.icicidirect.com; open\n   https://api.icicidirect.com/apiuser/login?api_key=<YOUR_KEY>\n" +
			"log in and set BREEZE_API_KEY and the day's BREEZE_SESSION (the apisession value) in " + filepath.Join(e.cfg.DataDir, ".env"))
	case "dhan":
		fmt.Println("Dhan: generate an access token at https://web.dhan.co (Profile → DhanHQ Trading APIs) and set DHAN_CLIENT_ID\n" +
			"and DHAN_ACCESS_TOKEN in " + filepath.Join(e.cfg.DataDir, ".env") + ". Dhan only accepts orders from a whitelisted static IP.")
	case "angelone", "angel":
		return loginAngel(e)
	default:
		return fmt.Errorf("login fyers | upstox | zerodha | dhan | angelone | breeze")
	}
	return nil
}

// loginAngel starts an Angel One SmartAPI session. The user types their
// client code, MPIN and TOTP into their own terminal; only the session
// token is kept (angel_token, readable by the user only).
func loginAngel(e *env) error {
	if e.cfg.AngelAPIKey == "" {
		return fmt.Errorf("set ANGEL_API_KEY in %s first (SmartAPI app at https://smartapi.angelone.in)", filepath.Join(e.cfg.DataDir, ".env"))
	}
	in := bufio.NewReader(os.Stdin)
	ask := func(q string) string {
		fmt.Print(q)
		s, _ := in.ReadString('\n')
		return strings.TrimSpace(s)
	}
	fmt.Println("Angel One login — type these yourself; they go to Angel One only and are not stored.")
	client := ask("Client code: ")
	mpin := ask("MPIN (shown as you type): ")
	totp := ask("Current 6-digit TOTP from your authenticator: ")
	jwt, err := brokers.AngelLogin(e.cfg.AngelAPIKey, client, mpin, totp)
	if err != nil {
		return err
	}
	p := e.dir.Path("angel_token")
	if err := os.WriteFile(p, []byte(jwt+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Println("✓ Angel One session saved to " + p + " (valid for the day). Restart desk serve to use it.")
	return nil
}

// parsePasted accepts the full redirect URL or a bare auth_code.
func parsePasted(s string) (string, string) {
	if strings.Contains(s, "auth_code=") || strings.HasPrefix(s, "http") {
		if u, err := url.Parse(s); err == nil {
			q := u.Query()
			if code := q.Get("auth_code"); code != "" {
				return code, q.Get("state")
			}
		}
	}
	return s, ""
}
