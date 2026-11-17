package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/amitchhatbar11/indian-trader-skills/internal/desk"
)

// ── broker (paper only) ─────────────────────────────────────────────────────

func cmdBroker(e *env, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("broker show [-live] | approve -index N -reply \"yes\" [-live] | place -index N | sync | flatten -symbol S | log | live | live-exit -symbol S -limit P -reply \"yes\"")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("broker "+sub, flag.ExitOnError)
	now := e.common(fs)
	index := fs.Int("index", 0, "the order's number in ~/.nse-desk/orders.json (from broker show)")
	reply := fs.String("reply", "", "the user's reply, word for word (approve)")
	symbol := fs.String("symbol", "", "the symbol (flatten, live-exit)")
	live := fs.Bool("live", false, "show/approve as a LIVE order (real money; needs DESK_LIVE=true and the live engine running)")
	exitLimit := fs.Float64("limit", 0, "live-exit: the LIMIT price to close at")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	lim, err := e.limits()
	if err != nil {
		return err
	}
	led, err := e.ledger(lim)
	if err != nil {
		return err
	}
	var orders []desk.Order
	e.dir.Read("orders.json", &orders)
	var verdicts []desk.Verdict
	e.dir.Read("risk.json", &verdicts)
	pick := func() (desk.Order, error) {
		if *index < 1 || *index > len(orders) {
			return desk.Order{}, fmt.Errorf("no order #%d (there are %d) — see broker show", *index, len(orders))
		}
		return orders[*index-1], nil
	}

	switch sub {
	case "show":
		if e.jsonOut {
			e.emit(map[string]any{"orders": orders, "verdicts": verdicts, "ledger": led})
			return nil
		}
		if *live {
			fmt.Println("⚠️  LIVE — REAL MONEY. These tickets would be placed on your broker account (ORDER_BROKER) by the live engine.")
			if !e.cfg.DeskLive {
				fmt.Println("   (DESK_LIVE is off: live orders are refused until you set it and restart the live engine.)")
			}
		}
		fmt.Printf("%s broker — tickets (~/.nse-desk/orders.json):\n", yn(*live, "LIVE", "PAPER"))
		if len(orders) == 0 {
			fmt.Println("  none — run desk size -write, then desk risk")
		}
		for i, o := range orders {
			state := "NOT CHECKED — run desk risk"
			if _, ok := desk.PassFor(verdicts, o); ok {
				state = "READY (passed the risk check)"
			} else {
				for _, v := range verdicts {
					if v.ID == o.ID && v.Fingerprint == desk.Fingerprint(o) && !v.Pass {
						state = "BLOCKED: " + strings.Join(v.Blocks, "; ")
					} else if v.ID == o.ID && v.Fingerprint != desk.Fingerprint(o) {
						state = "CHANGED since the risk check — run desk risk again"
					}
				}
			}
			for _, x := range led.Orders {
				if x.ID == o.ID {
					state = strings.ToUpper(x.Status)
					if x.Live {
						state = "LIVE · " + state
					}
					if x.FillPx > 0 {
						state += fmt.Sprintf(" at ₹%.2f", x.FillPx)
					}
					if x.OrderID != "" {
						state += " · broker order " + x.OrderID
					}
					if x.Note != "" {
						state += " · " + x.Note
					}
				}
			}
			if *live {
				if in := desk.ParseInstrument(o.Symbol); !in.Option {
					state += " · NOT LIVE-ELIGIBLE: live orders are stock options only"
				}
				fmt.Printf("\n#%d  LIVE ORDER — REAL MONEY — %s\n", i+1, ticket(o))
			} else {
				fmt.Printf("\n#%d  %s\n", i+1, ticket(o))
			}
			fmt.Printf("    %s\n", state)
		}
		printLedger(led, lim)
		return nil

	case "approve":
		o, err := pick()
		if err != nil {
			return err
		}
		if _, ok := desk.PassFor(verdicts, o); !ok {
			return fmt.Errorf("order #%d has no PASS from the risk check for its current content — run desk risk", *index)
		}
		if *live {
			if !e.cfg.DeskLive {
				return fmt.Errorf("DESK_LIVE is off — live orders are disabled (the user sets DESK_LIVE=true and restarts the live engine)")
			}
			req := liveRequest(o, *reply)
			if err := req.Validate(); err != nil {
				return fmt.Errorf("not approvable as LIVE: %w", err)
			}
		}
		po, err := led.Approve(o, *reply, *live, e.now)
		if err != nil {
			return err
		}
		if err := led.Save(e.dir); err != nil {
			return err
		}
		fmt.Printf("Approved #%d as %s with the reply %q at %s. Next: desk broker place -index %d.\n",
			*index, yn(po.Live, "a LIVE order (real money)", "a PAPER order"), po.Reply, e.now.Format("15:04:05"), *index)
		return nil

	case "place":
		o, err := pick()
		if err != nil {
			return err
		}
		if _, ok := desk.PassFor(verdicts, o); !ok {
			return fmt.Errorf("order #%d no longer matches a PASS from the risk check — run desk risk again", *index)
		}
		if po := led.Approved(o.ID); po != nil && po.Live {
			return placeLive(e, led, po)
		}
		qs, err := e.quotes([]string{o.Symbol})
		if err != nil {
			return err
		}
		po, err := led.Place(o.ID, qs[desk.ParseInstrument(o.Symbol).Symbol], e.now)
		if err != nil {
			return err
		}
		if err := led.Save(e.dir); err != nil {
			return err
		}
		if po.Status == "filled" {
			fmt.Printf("PAPER %s %d %s filled at ₹%.2f (limit ₹%.2f). Bracket: stop ₹%.2f, target ₹%.2f — desk broker sync watches them.\n",
				strings.ToUpper(po.Side), po.Qty, po.Symbol, po.FillPx, po.Limit, po.Stop, po.Target)
		} else {
			fmt.Printf("PAPER %s %d %s resting at limit ₹%.2f (not marketable yet). desk broker sync fills it when the price comes.\n",
				strings.ToUpper(po.Side), po.Qty, po.Symbol, po.Limit)
		}
		fmt.Println("No real order was sent: the desk only trades in ~/.nse-desk/paper.json.")
		return nil

	case "sync":
		var syms []string
		for _, p := range led.Positions {
			syms = append(syms, p.Symbol)
		}
		for _, o := range led.Orders {
			if o.Status == "pending" {
				syms = append(syms, o.Symbol)
			}
		}
		if len(syms) == 0 {
			fmt.Println("Nothing open or pending in the paper account.")
			return nil
		}
		qs, err := e.quotes(syms)
		if err != nil {
			return fmt.Errorf("no fresh quotes, so nothing was checked (a stale price must never trigger a stop): %w", err)
		}
		evs := led.Sync(qs, e.now)
		if err := led.Save(e.dir); err != nil {
			return err
		}
		for _, ev := range evs {
			fmt.Printf("%s  %s %s\n", e.now.Format("15:04"), ev.Symbol, ev.What)
		}
		printLedger(led, lim)
		return nil

	case "flatten":
		sym := desk.ParseInstrument(*symbol).Symbol
		if sym == "" {
			return fmt.Errorf("-symbol")
		}
		qs, err := e.quotes([]string{sym})
		if err != nil {
			return err
		}
		tr, err := led.Flatten(sym, qs[sym], e.now)
		if err != nil {
			return err
		}
		if err := led.Save(e.dir); err != nil {
			return err
		}
		fmt.Printf("PAPER closed %s %d %s at ₹%.2f — gross %s, charges ₹%.2f, net %s\n", tr.Side, tr.Qty, tr.Symbol, tr.Exit,
			desk.Rupees(tr.Gross), tr.Charges, desk.Rupees(tr.Net))
		return nil

	case "live":
		c, err := e.liveClient()
		if err != nil {
			return err
		}
		ps, err := c.Positions()
		if err != nil {
			return err
		}
		if e.jsonOut {
			e.emit(ps)
			return nil
		}
		fmt.Println("LIVE desk positions in the live engine's book (real money):")
		if len(ps) == 0 {
			fmt.Println("  none open, none closed today")
		}
		for _, p := range ps {
			fmt.Printf("  %-8s %s %d %s @ ₹%.2f · LTP ₹%.2f · stop ₹%.2f · target ₹%.2f · P&L %s %s\n", p.State, p.Side, p.Qty, p.Symbol,
				p.Entry, p.LTP, p.Stop, p.Target, desk.Rupees(p.PnL), p.Reason)
		}
		fmt.Println("The live engine tracks fills, stops and reconciliation with your broker.")
		return nil

	case "live-exit":
		if !e.cfg.DeskLive {
			return fmt.Errorf("DESK_LIVE is off")
		}
		c, err := e.liveClient()
		if err != nil {
			return err
		}
		sym := desk.ParseInstrument(*symbol).Symbol
		res, err := c.Exit(desk.LiveExit{Symbol: sym, Limit: *exitLimit, Reply: *reply})
		if err != nil {
			return err
		}
		fmt.Printf("LIVE exit for %s at limit ₹%.2f: %s. The position stays open until the fill is confirmed (desk broker live).\n", sym, *exitLimit, res.State)
		return nil

	case "log":
		if e.jsonOut {
			e.emit(led.Trades)
			return nil
		}
		w := tw()
		fmt.Fprintln(w, "closed\tsymbol\tside\tqty\tentry\texit\treason\tgross\tcharges\tnet")
		for _, t := range led.Trades {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%.2f\t%.2f\t%s\t%.0f\t%.2f\t%.0f\n", t.ClosedAt.In(desk.IST).Format("02 Jan 15:04"),
				t.Symbol, t.Side, t.Qty, t.Entry, t.Exit, t.Reason, t.Gross, t.Charges, t.Net)
		}
		w.Flush()
		printLedger(led, lim)
		return nil
	}
	return fmt.Errorf("unknown broker command %q", sub)
}

// liveRequest turns an approved ticket into the live-engine hand-off.
func liveRequest(o desk.Order, reply string) desk.LiveRequest {
	return desk.LiveRequest{ID: o.ID, Fingerprint: desk.Fingerprint(o), Symbol: o.Symbol, Side: o.Side, Qty: o.Qty,
		LotSize: o.LotSize, Limit: o.Limit, Stop: o.Stop, Target: o.Target, Reply: reply}
}

// placeLive hands an approved LIVE order to the live engine, exactly once.
func placeLive(e *env, led *desk.Ledger, po *desk.PaperOrder) error {
	if !e.cfg.DeskLive {
		return fmt.Errorf("DESK_LIVE is off — nothing was sent")
	}
	c, err := e.liveClient()
	if err != nil {
		return err
	}
	// Mark it before sending: whatever happens next, this ticket is never
	// sent a second time by the desk.
	po.Status, po.PlacedAt = "live_unknown", e.now
	if err := led.Save(e.dir); err != nil {
		return err
	}
	res, err := c.Submit(liveRequest(po.Order, po.Reply))
	switch {
	case errors.Is(err, desk.ErrUnknown):
		po.Note = "no clear answer — check your broker's order book"
	case err != nil:
		po.Status, po.Note = "live_refused", err.Error()
	default:
		po.Status, po.OrderID, po.Note = "live_sent", res.OrderID, res.State
	}
	if serr := led.Save(e.dir); serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		return err
	}
	fmt.Printf("LIVE %s %d %s LIMIT ₹%.2f sent — broker order %s, %s.\n", strings.ToUpper(po.Side), po.Qty, po.Symbol, po.Limit, res.OrderID, res.State)
	fmt.Println("Accepted is not filled: the live engine tracks it to the fill and runs its stop (desk broker live).")
	return nil
}

func (e *env) liveClient() (*desk.LiveClient, error) {
	return desk.NewLiveClient(e.cfg.DeskLiveURL, e.dir.Path("live.token"))
}

func ticket(o desk.Order) string {
	lots := ""
	if o.LotSize > 0 {
		lots = fmt.Sprintf(" (%d lots × %d)", o.Qty/o.LotSize, o.LotSize)
	}
	s := fmt.Sprintf("%s %d %s%s · LIMIT ₹%.2f · %s · value %s · margin %s", strings.ToUpper(o.Side), o.Qty, o.Symbol, lots,
		o.Limit, o.Product, desk.Rupees(o.Value()), desk.Rupees(o.Margin))
	if o.Stop > 0 {
		s += fmt.Sprintf("\n    bracket: stop ₹%.2f, target ₹%.2f · max loss at the stop %s", o.Stop, o.Target, desk.Rupees(o.MaxLoss))
	}
	if o.FlatBy != "" && o.Product == desk.Intraday {
		s += " · flat by " + o.FlatBy
	}
	return s
}

func printLedger(l *desk.Ledger, lim desk.Limits) {
	var open float64
	for _, p := range l.Positions {
		open += p.OpenPnL()
	}
	fmt.Printf("\nPaper account: start %s · realised %s · open %s · margin in use %s\n",
		desk.Rupees(l.StartCapital), desk.Rupees(l.Realized()), desk.Rupees(open), desk.Rupees(l.MarginUsed()))
	for _, p := range l.Positions {
		fmt.Printf("  %s %d %s @ ₹%.2f · LTP ₹%.2f · stop ₹%.2f · target ₹%.2f · open %s\n",
			p.Side, p.Qty, p.Symbol, p.Entry, p.LTP, p.Stop, p.Target, desk.Rupees(p.OpenPnL()))
	}
}

// ── alerts ──────────────────────────────────────────────────────────────────

func cmdAlerts(e *env, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("alerts add | check | list | reset ID")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("alerts "+sub, flag.ExitOnError)
	now := e.common(fs)
	symbol := fs.String("symbol", "", "symbol")
	above := fs.Float64("above", 0, "ring at or above this price")
	below := fs.Float64("below", 0, "ring at or below this price")
	pct := fs.Float64("pct", 0, "ring at this % move from the previous close (negative for down)")
	at := fs.String("at", "", "ring at HH:MM IST on a trading day")
	bell := fs.Bool("bell", false, "ring at the 09:15 opening bell")
	label := fs.String("label", "", "a label")
	fromPlan := fs.Bool("from-plan", false, "stop, target and flat-by alerts from ~/.nse-desk/plan.json")
	telegram := fs.Bool("telegram", false, "check: also send what rang to your Telegram bot (TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := e.setNow(*now); err != nil {
		return err
	}
	var as []desk.Alert
	if err := e.dir.Read("alerts.json", &as); err != nil && !desk.IsMissing(err) {
		return err
	}
	save := func() error { return e.dir.Write("alerts.json", as) }
	sym := desk.ParseInstrument(*symbol).Symbol
	id := func(kind string) string {
		return strings.ToLower(fmt.Sprintf("%s-%s-%s", strings.TrimSpace(sym+" "), kind, e.now.Format("150405")))
	}

	switch sub {
	case "add":
		var add []desk.Alert
		if *fromPlan {
			var p desk.Plan
			if err := e.dir.Read("plan.json", &p); err != nil {
				return fmt.Errorf("no plan: %v", err)
			}
			add = append(add, desk.FromPlan(p, e.now)...)
		}
		if (*above > 0 || *below > 0 || *pct != 0) && sym == "" {
			return fmt.Errorf("price alerts need -symbol")
		}
		if *above > 0 {
			add = append(add, desk.Alert{ID: id("above"), Symbol: sym, Kind: "above", Level: *above, Label: *label, Created: e.now})
		}
		if *below > 0 {
			add = append(add, desk.Alert{ID: id("below"), Symbol: sym, Kind: "below", Level: *below, Label: *label, Created: e.now})
		}
		if *pct != 0 {
			add = append(add, desk.Alert{ID: id("pct"), Symbol: sym, Kind: "pct", Pct: *pct, Label: *label, Created: e.now})
		}
		if *at != "" {
			if _, err := time.Parse("15:04", *at); err != nil {
				return fmt.Errorf("-at HH:MM")
			}
			add = append(add, desk.Alert{ID: "time-" + strings.ReplaceAll(*at, ":", "") + "-" + e.now.Format("150405"), Kind: "time", At: *at, Label: *label, Created: e.now})
		}
		if *bell {
			add = append(add, desk.Alert{ID: "bell-" + e.now.Format("20060102"), Kind: "bell", Label: *label, Created: e.now})
		}
		if len(add) == 0 {
			return fmt.Errorf("nothing to add: -above, -below, -pct, -at, -bell or -from-plan")
		}
		for _, a := range add {
			replaced := false
			for i := range as {
				if as[i].ID == a.ID {
					as[i], replaced = a, true
				}
			}
			if !replaced {
				as = append(as, a)
			}
			fmt.Println("added: " + a.Describe() + "  [" + a.ID + "]")
		}
		return save()

	case "list":
		if e.jsonOut {
			e.emit(as)
			return nil
		}
		for _, a := range as {
			st := "waiting"
			if a.Rung() {
				st = "rang " + a.RungAt.In(desk.IST).Format("02 Jan 15:04")
			}
			fmt.Printf("%-34s %-60s %s\n", a.ID, a.Describe(), st)
		}
		return nil

	case "reset":
		if len(pos) == 0 {
			return fmt.Errorf("alerts reset ID (or all)")
		}
		for i := range as {
			if pos[0] == "all" || as[i].ID == pos[0] {
				as[i].RungAt, as[i].RungPx = time.Time{}, 0
			}
		}
		return save()

	case "check":
		var syms []string
		for _, a := range as {
			if a.Symbol != "" && !a.Rung() {
				syms = append(syms, a.Symbol)
			}
		}
		qs := map[string]desk.Quote{}
		if len(syms) > 0 {
			if q, err := e.quotes(syms); err == nil {
				qs = q
			} else {
				e.note("no quotes, price alerts not checked: %v", err)
			}
		}
		rung := desk.CheckAlerts(as, qs, e.now, e.calendar())
		if err := save(); err != nil {
			return err
		}
		var lines []string
		for _, a := range rung {
			l := "🔔 " + a.Describe()
			if a.RungPx > 0 {
				l += fmt.Sprintf(" — now ₹%.2f", a.RungPx)
			}
			lines = append(lines, l)
		}
		if len(lines) == 0 {
			fmt.Printf("%s — nothing rang (%d waiting)\n", e.now.Format("15:04"), countWaiting(as))
		} else {
			fmt.Println(strings.Join(lines, "\n"))
			if *telegram {
				if err := sendTelegram(e.cfg.TelegramBotToken, e.cfg.TelegramChatID, strings.Join(lines, "\n")); err != nil {
					e.note("Telegram: %v", err)
				}
			}
		}
		e.printNotes()
		return nil
	}
	return fmt.Errorf("unknown alerts command %q", sub)
}

func countWaiting(as []desk.Alert) int {
	n := 0
	for _, a := range as {
		if !a.Rung() {
			n++
		}
	}
	return n
}

// sendTelegram posts to the user's own bot chat.
func sendTelegram(token string, chat int64, text string) error {
	if token == "" || chat == 0 {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID not set")
	}
	body, _ := json.Marshal(map[string]any{"chat_id": chat, "text": text})
	resp, err := http.Post("https://api.telegram.org/bot"+token+"/sendMessage", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
