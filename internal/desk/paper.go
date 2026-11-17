package desk

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// The paper broker. It is the only place the desk "trades", and it trades
// in ~/.nse-desk/paper.json alone: no broker API, no real order, ever.

// PaperPos is an open paper position.
type PaperPos struct {
	ID       string    `json:"id"`
	Symbol   string    `json:"symbol"`
	Side     string    `json:"side"` // long / short
	Qty      int       `json:"qty"`
	LotSize  int       `json:"lot_size,omitempty"`
	Entry    float64   `json:"entry"`
	Stop     float64   `json:"stop,omitempty"`
	Target   float64   `json:"target,omitempty"`
	Product  string    `json:"product"`
	FlatBy   string    `json:"flat_by,omitempty"`
	OpenedAt time.Time `json:"opened_at"`
	LTP      float64   `json:"ltp"`
	Margin   float64   `json:"margin"`
	MaxLoss  float64   `json:"max_loss"`
}

// OpenPnL is the position's mark-to-market P&L before charges.
func (p *PaperPos) OpenPnL() float64 {
	if p.LTP <= 0 {
		return 0
	}
	d := p.LTP - p.Entry
	if p.Side == "short" {
		d = -d
	}
	return d * float64(p.Qty)
}

// PaperOrder is a ticket's life in the broker: paper, or LIVE when Live is
// set (handed to the live engine; see live.go).
type PaperOrder struct {
	Order
	// Status: approved / pending / filled / cancelled / rejected (paper);
	// live_sent / live_refused / live_unknown (LIVE).
	Status     string    `json:"status"`
	Reply      string    `json:"reply"` // the user's yes, word for word
	Live       bool      `json:"live,omitempty"`
	OrderID    string    `json:"order_id,omitempty"` // the broker's id (LIVE)
	ApprovedAt time.Time `json:"approved_at"`
	PlacedAt   time.Time `json:"placed_at,omitempty"`
	FilledAt   time.Time `json:"filled_at,omitempty"`
	FillPx     float64   `json:"fill_price,omitempty"`
	Note       string    `json:"note,omitempty"`
}

// PaperTrade is a closed paper position.
type PaperTrade struct {
	Symbol   string    `json:"symbol"`
	Side     string    `json:"side"`
	Qty      int       `json:"qty"`
	Entry    float64   `json:"entry"`
	Exit     float64   `json:"exit"`
	OpenedAt time.Time `json:"opened_at"`
	ClosedAt time.Time `json:"closed_at"`
	Reason   string    `json:"reason"` // STOP / TARGET / FLAT_BY / MANUAL / EXIT_ORDER
	Gross    float64   `json:"gross"`
	Charges  float64   `json:"charges"`
	Net      float64   `json:"net"`
	MaxLoss  float64   `json:"max_loss"`
}

// Ledger is the paper account (~/.nse-desk/paper.json).
type Ledger struct {
	StartCapital float64       `json:"start_capital"`
	Positions    []*PaperPos   `json:"positions"`
	Orders       []*PaperOrder `json:"orders"`
	Trades       []PaperTrade  `json:"trades"`
}

const ledgerFile = "paper.json"

// LoadLedger reads the paper account, starting a fresh one with capital.
func LoadLedger(d Dir, capital float64) (*Ledger, error) {
	var l Ledger
	if err := d.Read(ledgerFile, &l); err != nil {
		if IsMissing(err) {
			return &Ledger{StartCapital: capital}, nil
		}
		return nil, err
	}
	return &l, nil
}

func (l *Ledger) Save(d Dir) error { return d.Write(ledgerFile, l) }

// Realized is the net P&L of closed trades.
func (l *Ledger) Realized() float64 {
	var s float64
	for _, t := range l.Trades {
		s += t.Net
	}
	return s
}

// MarginUsed is the margin blocked by open positions and pending entries.
func (l *Ledger) MarginUsed() float64 {
	var s float64
	for _, p := range l.Positions {
		s += p.Margin
	}
	for _, o := range l.Orders {
		if o.Status == "pending" && !o.Exit {
			s += o.Margin
		}
	}
	return s
}

// Today is the day's activity for the risk check.
type Today struct {
	Entries  int     // entry orders placed today
	Orders   int     // all orders placed today
	Realized float64 // net P&L of trades closed today
	OpenRisk float64 // max loss at the stops of open positions and pending entries
	Symbols  map[string]bool
}

func (l *Ledger) Today(now time.Time) Today {
	t := Today{Symbols: map[string]bool{}}
	day := dayOf(now)
	for _, o := range l.Orders {
		if o.PlacedAt.IsZero() || !dayOf(o.PlacedAt).Equal(day) {
			continue
		}
		t.Orders++
		if o.Status == "live_refused" {
			continue // the live engine refused it: nothing reached the broker
		}
		if !o.Exit {
			t.Entries++
			t.Symbols[o.Symbol+"|"+o.Side] = true
		}
		// A live order's risk counts for the whole day: its fills and exits
		// are tracked by the live engine's book, not here.
		if (o.Status == "pending" || o.Live) && !o.Exit {
			t.OpenRisk += o.MaxLoss
		}
	}
	for _, tr := range l.Trades {
		if dayOf(tr.ClosedAt).Equal(day) {
			t.Realized += tr.Net
		}
	}
	for _, p := range l.Positions {
		t.OpenRisk += p.MaxLoss
	}
	return t
}

// Holding is the open position in a symbol, if any.
func (l *Ledger) Holding(sym string) *PaperPos {
	for _, p := range l.Positions {
		if p.Symbol == sym {
			return p
		}
	}
	return nil
}

var yesRe = regexp.MustCompile(`(?i)^\s*(yes|y|yes please|yes,? place it|place it|confirm(ed)?|go ahead|approved?|do it)\s*[.!]*\s*$`)

// IsYes reports an unambiguous yes. Anything else ("ok?", "maybe",
// "yes but smaller") is not an approval.
func IsYes(reply string) bool { return yesRe.MatchString(reply) }

// Approve records the user's yes for an order that passed the risk check,
// for the paper broker or (live) for a LIVE order. A yes is for one mode:
// a paper approval never becomes a live one.
func (l *Ledger) Approve(o Order, reply string, live bool, now time.Time) (*PaperOrder, error) {
	if !IsYes(reply) {
		return nil, fmt.Errorf("%q is not a clear yes — nothing was approved", reply)
	}
	for _, x := range l.Orders {
		if x.ID == o.ID && x.Status != "cancelled" && x.Status != "rejected" && x.Status != "live_refused" {
			return nil, fmt.Errorf("order %s was already approved (%s)", o.ID, x.Status)
		}
	}
	po := &PaperOrder{Order: o, Status: "approved", Reply: reply, ApprovedAt: now, Live: live}
	l.Orders = append(l.Orders, po)
	return po, nil
}

// Place sends an approved order to the paper market. A marketable limit
// fills at once at the better of the limit and the touch; otherwise it
// rests as pending until Sync sees the price come to it.
func (l *Ledger) Place(id string, q Quote, now time.Time) (*PaperOrder, error) {
	var po *PaperOrder
	for _, x := range l.Orders {
		if x.ID == id && x.Status == "approved" {
			po = x
		}
	}
	if po == nil {
		return nil, fmt.Errorf("no approved order %s — approve it with the user's yes first", id)
	}
	if po.Live {
		return nil, fmt.Errorf("order %s was approved as LIVE — it goes to the live engine, not the paper market", id)
	}
	if po.Type != "LIMIT" || po.Limit <= 0 {
		return nil, fmt.Errorf("order %s is not a limit order — refused", id)
	}
	po.PlacedAt, po.Status = now, "pending"
	l.tryFill(po, q, now)
	return po, nil
}

// Approved finds the approved (not yet sent) order with this id.
func (l *Ledger) Approved(id string) *PaperOrder {
	for _, x := range l.Orders {
		if x.ID == id && x.Status == "approved" {
			return x
		}
	}
	return nil
}

// tryFill fills a pending order when the market reaches its limit.
func (l *Ledger) tryFill(po *PaperOrder, q Quote, now time.Time) bool {
	if po.Status != "pending" || q.LTP <= 0 {
		return false
	}
	buy := po.Side == "buy"
	touch := q.LTP
	if buy && q.Ask > 0 {
		touch = q.Ask
	} else if !buy && q.Bid > 0 {
		touch = q.Bid
	}
	if buy && touch > po.Limit || !buy && touch < po.Limit {
		return false
	}
	px := po.Limit
	if buy {
		px = math.Min(po.Limit, touch)
	} else {
		px = math.Max(po.Limit, touch)
	}
	po.Status, po.FilledAt, po.FillPx = "filled", now, round2(px)
	if po.Exit {
		if p := l.Holding(po.Symbol); p != nil {
			l.close(p, px, "EXIT_ORDER", now)
		}
		return true
	}
	side := "long"
	if !buy {
		side = "short"
	}
	l.Positions = append(l.Positions, &PaperPos{ID: po.ID, Symbol: po.Symbol, Side: side, Qty: po.Qty, LotSize: po.LotSize,
		Entry: round2(px), Stop: po.Stop, Target: po.Target, Product: po.Product, FlatBy: po.FlatBy, OpenedAt: now,
		LTP: q.LTP, Margin: po.Margin, MaxLoss: round2(math.Abs(px-po.Stop) * float64(po.Qty))})
	return true
}

// close books a paper position closed at px.
func (l *Ledger) close(p *PaperPos, px float64, reason string, now time.Time) PaperTrade {
	px = round2(px)
	d := px - p.Entry
	if p.Side == "short" {
		d = -d
	}
	buyPx, sellPx := p.Entry, px
	if p.Side == "short" {
		buyPx, sellPx = px, p.Entry
	}
	tr := PaperTrade{Symbol: p.Symbol, Side: p.Side, Qty: p.Qty, Entry: p.Entry, Exit: px, OpenedAt: p.OpenedAt,
		ClosedAt: now, Reason: reason, Gross: round2(d * float64(p.Qty)), MaxLoss: p.MaxLoss}
	tr.Charges = ChargesFor(ParseInstrument(p.Symbol), p.Product).RoundTrip(buyPx, sellPx, p.Qty)
	tr.Net = round2(tr.Gross - tr.Charges)
	l.Trades = append(l.Trades, tr)
	for i, x := range l.Positions {
		if x == p {
			l.Positions = append(l.Positions[:i], l.Positions[i+1:]...)
			break
		}
	}
	return tr
}

// Event is something Sync did.
type Event struct {
	Symbol string
	What   string
	Trade  *PaperTrade
}

// Sync marks the account to market: fills pending orders the price has
// reached, closes positions at their stop or target, flattens intraday
// positions at their flat-by time and cancels unfilled entries then.
// quotes must be fresh: a stale price must never trigger a stop.
func (l *Ledger) Sync(quotes map[string]Quote, now time.Time) []Event {
	var evs []Event
	hm := now.In(IST).Format("15:04")
	for _, po := range l.Orders {
		if po.Status != "pending" {
			continue
		}
		if q, ok := quotes[po.Symbol]; ok && l.tryFill(po, q, now) {
			evs = append(evs, Event{Symbol: po.Symbol, What: fmt.Sprintf("%s %d filled at ₹%.2f", strings.ToUpper(po.Side), po.Qty, po.FillPx)})
			continue
		}
		if !po.Exit && po.FlatBy != "" && hm >= po.FlatBy {
			po.Status, po.Note = "cancelled", "unfilled at the flat-by time"
			evs = append(evs, Event{Symbol: po.Symbol, What: "unfilled entry cancelled at " + po.FlatBy})
		}
	}
	for _, p := range append([]*PaperPos(nil), l.Positions...) {
		q, ok := quotes[p.Symbol]
		if !ok || q.LTP <= 0 {
			continue
		}
		p.LTP = q.LTP
		long := p.Side == "long"
		var px float64
		reason := ""
		switch {
		case p.Stop > 0 && (long && q.LTP <= p.Stop || !long && q.LTP >= p.Stop):
			// A stop fills at the market, which a gap can carry past the stop.
			px, reason = q.LTP, "STOP"
		case p.Target > 0 && (long && q.LTP >= p.Target || !long && q.LTP <= p.Target):
			px, reason = p.Target, "TARGET" // a resting limit at the target
		case p.Product == Intraday && p.FlatBy != "" && hm >= p.FlatBy:
			px, reason = q.LTP, "FLAT_BY"
		}
		if reason == "" {
			continue
		}
		tr := l.close(p, px, reason, now)
		evs = append(evs, Event{Symbol: p.Symbol, What: fmt.Sprintf("%s at ₹%.2f, net %s", reason, px, Rupees(tr.Net)), Trade: &tr})
	}
	return evs
}

// Flatten closes a position now at the touch (bid for a long, ask for a
// short, else the LTP).
func (l *Ledger) Flatten(sym string, q Quote, now time.Time) (PaperTrade, error) {
	p := l.Holding(sym)
	if p == nil {
		return PaperTrade{}, fmt.Errorf("no open paper position in %s", sym)
	}
	px := q.LTP
	if p.Side == "long" && q.Bid > 0 {
		px = q.Bid
	} else if p.Side == "short" && q.Ask > 0 {
		px = q.Ask
	}
	if px <= 0 {
		return PaperTrade{}, fmt.Errorf("no price for %s", sym)
	}
	return l.close(p, px, "MANUAL", now), nil
}
