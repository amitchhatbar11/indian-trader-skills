package desk

import (
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Price bands ─────────────────────────────────────────────────────────────

// PriceBands reads NSE's security list: each stock's circuit band in %
// (2, 5, 10, 20); 0 = no fixed band (F&O stocks move on dynamic bands).
func PriceBands(n *NSE) (map[string]float64, error) {
	data, err := n.Get("https://nsearchives.nseindia.com/content/equities/sec_list.csv", 12*time.Hour)
	if err != nil {
		return nil, err
	}
	return ParseBands(data)
}

func ParseBands(data []byte) (map[string]float64, error) {
	rows, err := readCSV(data)
	if err != nil {
		return nil, fmt.Errorf("NSE price bands: %w", err)
	}
	out := map[string]float64{}
	for _, r := range rows {
		b, err := strconv.ParseFloat(strings.TrimSpace(r["Band"]), 64)
		if err != nil {
			b = 0 // "No Band"
		}
		if s := strings.TrimSpace(r["Symbol"]); s != "" {
			if _, seen := out[s]; !seen || r["Series"] == "EQ" {
				out[s] = b
			}
		}
	}
	return out, nil
}

// readCSV reads a CSV with a header row into maps (headers trimmed).
func readCSV(data []byte) ([]map[string]string, error) {
	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), string(rune(0xFEFF)))))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	head, err := cr.Read()
	if err != nil {
		return nil, err
	}
	for i := range head {
		head[i] = strings.TrimSpace(head[i])
	}
	var out []map[string]string
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		m := map[string]string{}
		for i, v := range rec {
			if i < len(head) {
				m[head[i]] = strings.TrimSpace(v)
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// ── News ────────────────────────────────────────────────────────────────────

// NewsItem is one announcement or headline.
type NewsItem struct {
	Key    string    `json:"key"` // for "only what's new"
	Symbol string    `json:"symbol"`
	Time   time.Time `json:"time"`
	Source string    `json:"source"` // NSE / news
	Title  string    `json:"title"`
	Detail string    `json:"detail,omitempty"`
	URL    string    `json:"url,omitempty"`
	Tags   []string  `json:"tags"`
}

// Announcements reads a stock's NSE corporate announcements between two days.
func Announcements(n *NSE, sym string, from, to time.Time) ([]NewsItem, error) {
	q := url.Values{}
	q.Set("index", "equities")
	q.Set("symbol", sym)
	q.Set("from_date", from.Format("02-01-2006"))
	q.Set("to_date", to.Format("02-01-2006"))
	data, err := n.API("corporate-announcements?"+q.Encode(), 10*time.Minute)
	if err != nil {
		return nil, err
	}
	return ParseAnnouncements(data)
}

func ParseAnnouncements(data []byte) ([]NewsItem, error) {
	var raw []struct {
		Symbol string `json:"symbol"`
		Desc   string `json:"desc"`
		Text   string `json:"attchmntText"`
		File   string `json:"attchmntFile"`
		Date   string `json:"an_dt"`
		Seq    string `json:"seq_id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("NSE announcements: %w", err)
	}
	var out []NewsItem
	for _, r := range raw {
		t, _ := time.ParseInLocation("02-Jan-2006 15:04:05", r.Date, IST)
		it := NewsItem{Key: "nse:" + r.Seq + r.File, Symbol: r.Symbol, Time: t, Source: "NSE", Title: strings.TrimSpace(r.Desc),
			Detail: strings.TrimSpace(r.Text), URL: r.File}
		if r.Seq == "" && r.File == "" {
			it.Key = "nse:" + r.Symbol + r.Date + r.Desc
		}
		it.Tags = Tag(it.Title + " " + it.Detail)
		out = append(out, it)
	}
	return out, nil
}

// GoogleNews reads Google News (India edition) for a query.
func GoogleNews(n *NSE, sym, query string) ([]NewsItem, error) {
	u := "https://news.google.com/rss/search?" + url.Values{"q": {query}, "hl": {"en-IN"}, "gl": {"IN"}, "ceid": {"IN:en"}}.Encode()
	data, err := n.Get(u, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	return ParseRSS(data, sym)
}

func ParseRSS(data []byte, sym string) ([]NewsItem, error) {
	var rss struct {
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			PubDate string `xml:"pubDate"`
			GUID    string `xml:"guid"`
			Source  string `xml:"source"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(data, &rss); err != nil {
		return nil, fmt.Errorf("news feed: %w", err)
	}
	var out []NewsItem
	for _, it := range rss.Items {
		t, _ := time.Parse(time.RFC1123, it.PubDate)
		if t.IsZero() {
			t, _ = time.Parse(time.RFC1123Z, it.PubDate)
		}
		key := it.GUID
		if key == "" {
			key = it.Link
		}
		ni := NewsItem{Key: "news:" + key, Symbol: sym, Time: t.In(IST), Source: strings.TrimSpace(it.Source), Title: strings.TrimSpace(it.Title), URL: it.Link}
		if ni.Source == "" {
			ni.Source = "news"
		}
		ni.Tags = Tag(ni.Title)
		out = append(out, ni)
	}
	return out, nil
}

var tagWords = []struct {
	tag string
	re  *regexp.Regexp
}{
	{"results", regexp.MustCompile(`(?i)financial results|quarterly results|\bq[1-4](fy\d\d)?\b|earnings|net profit|profit (rises|falls|jumps|drops|up|down|surges|declines)|revenue (rises|falls|grows|up|down)|outcome of board meeting`)},
	{"board_meeting", regexp.MustCompile(`(?i)board meeting|intimation of board`)},
	{"dividend", regexp.MustCompile(`(?i)dividend|record date`)},
	{"split_bonus", regexp.MustCompile(`(?i)split|sub-division|bonus|consolidation of shares`)},
	{"deal", regexp.MustCompile(`(?i)acqui|merger|amalgamation|demerger|stake|joint venture|scheme of arrangement|takeover`)},
	{"order_win", regexp.MustCompile(`(?i)order (win|received|inflow)|bags? order|contract (award|win)|letter of (award|intent)`)},
	{"fundraise", regexp.MustCompile(`(?i)\bqip\b|preferential|rights issue|fund rais|ncd|debenture|ipo|offer for sale|\bofs\b`)},
	{"management", regexp.MustCompile(`(?i)resign|appoint|ceo|managing director|cfo|key managerial`)},
	{"rating", regexp.MustCompile(`(?i)credit rating|rating (upgrade|downgrade|reaffirm)|crisil|icra|care ratings|india ratings`)},
	{"pledge", regexp.MustCompile(`(?i)pledge|encumbrance`)},
	{"regulatory", regexp.MustCompile(`(?i)sebi|penalty|show cause|litigation|court|tribunal|nclt|gst|income tax|raid|search`)},
	{"analyst", regexp.MustCompile(`(?i)target price|upgrade|downgrade|buy rating|sell rating|brokerage|analyst|investor meet|conference call`)},
	{"halt", regexp.MustCompile(`(?i)suspension|suspended|trading halt|surveillance|\bgsm\b|\basm\b`)},
}

// Tag labels an item by what tends to move a stock.
func Tag(s string) []string {
	var out []string
	for _, t := range tagWords {
		if t.re.MatchString(s) {
			out = append(out, t.tag)
		}
	}
	if len(out) == 0 {
		out = []string{"other"}
	}
	return out
}

// ── Results ─────────────────────────────────────────────────────────────────

// Filing is one quarterly-results filing on NSE.
type Filing struct {
	Symbol       string    `json:"symbol"`
	From, To     time.Time `json:"-"`
	Consolidated bool      `json:"consolidated"`
	Filed        time.Time `json:"filed"`
	XBRL         string    `json:"xbrl"`
	Audited      string    `json:"audited"`
	Source       string    `json:"source"` // legacy / integrated
}

// Filings lists a stock's quarterly results filings from both of NSE's
// lists: "corporates-financial-results" holds quarters up to December 2024;
// from the March 2025 quarter SEBI's Integrated Filing (Financials) took
// over, under "integrated-filing-results". A revision replaces the filing
// it revises (the latest per quarter and basis wins).
func Filings(n *NSE, sym string) ([]Filing, error) {
	var out []Filing
	var errs []string
	if data, err := n.API("corporates-financial-results?index=equities&period=Quarterly&symbol="+url.QueryEscape(sym), 24*time.Hour); err != nil {
		errs = append(errs, err.Error())
	} else if fs, err := ParseLegacyFilings(data); err != nil {
		errs = append(errs, err.Error())
	} else {
		out = append(out, fs...)
	}
	q := url.Values{"index": {"equities"}, "type": {"Integrated Filing- Financials"}, "symbol": {sym}, "page": {"1"}, "size": {"60"}}
	if data, err := n.API("integrated-filing-results?"+q.Encode(), 6*time.Hour); err != nil {
		errs = append(errs, err.Error())
	} else if fs, err := ParseIntegratedFilings(data); err != nil {
		errs = append(errs, err.Error())
	} else {
		out = append(out, fs...)
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("NSE results: %s", strings.Join(errs, "; "))
	}
	return LatestFilings(out), nil
}

// ParseLegacyFilings reads corporates-financial-results, keeping quarter
// (not year-to-date) figures.
func ParseLegacyFilings(data []byte) ([]Filing, error) {
	var raw []struct {
		Symbol, Consolidated, Cumulative, FromDate, ToDate, FilingDate, Xbrl, Audited string
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("NSE results list: %w", err)
	}
	var out []Filing
	for _, r := range raw {
		from, e1 := time.ParseInLocation("02-Jan-2006", r.FromDate, IST)
		to, e2 := time.ParseInLocation("02-Jan-2006", r.ToDate, IST)
		filed, _ := time.ParseInLocation("02-Jan-2006 15:04", r.FilingDate, IST)
		if e1 != nil || e2 != nil || r.Cumulative == "Cumulative" || !strings.HasSuffix(r.Xbrl, ".xml") {
			continue
		}
		if days := to.Sub(from).Hours() / 24; days < 80 || days > 100 {
			continue // a half-year, nine months or a year: not a quarter
		}
		out = append(out, Filing{Symbol: r.Symbol, From: from, To: to, Consolidated: r.Consolidated == "Consolidated",
			Filed: filed, XBRL: r.Xbrl, Audited: r.Audited, Source: "legacy"})
	}
	return out, nil
}

// ParseIntegratedFilings reads integrated-filing-results.
func ParseIntegratedFilings(data []byte) ([]Filing, error) {
	var raw struct {
		Data []struct {
			Symbol       string `json:"symbol"`
			QE           string `json:"qe_Date"`
			Consolidated string `json:"consolidated"`
			Audited      string `json:"audited"`
			Created      string `json:"creation_Date"`
			XBRL         string `json:"xbrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		// "no data found" comes back as {"data":0,...}
		return nil, nil
	}
	var out []Filing
	for _, r := range raw.Data {
		to, err := time.ParseInLocation("02-Jan-2006", titleMonth(r.QE), IST)
		if err != nil || !strings.HasSuffix(r.XBRL, ".xml") {
			continue
		}
		filed, _ := time.ParseInLocation("02-Jan-2006 15:04:05", r.Created, IST)
		from := time.Date(to.Year(), to.Month()-2, 1, 0, 0, 0, 0, IST) // the quarter's first day
		out = append(out, Filing{Symbol: r.Symbol, From: from, To: to, Consolidated: r.Consolidated == "Consolidated",
			Filed: filed, XBRL: r.XBRL, Audited: r.Audited, Source: "integrated"})
	}
	return out, nil
}

// LatestFilings keeps the latest filing per quarter and basis, newest
// quarter first.
func LatestFilings(fs []Filing) []Filing {
	best := map[string]Filing{}
	for _, f := range fs {
		k := f.To.Format("2006-01-02") + fmt.Sprint(f.Consolidated)
		if old, ok := best[k]; !ok || f.Filed.After(old.Filed) {
			best[k] = f
		}
	}
	out := make([]Filing, 0, len(best))
	for _, f := range best {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].To.Equal(out[j].To) {
			return out[i].To.After(out[j].To)
		}
		return out[i].Consolidated
	})
	return out
}

// Quarter is one quarter's numbers (₹ crore, EPS in ₹).
type Quarter struct {
	From, To     time.Time
	Consolidated bool
	Revenue      float64 `json:"revenue_cr"`
	OtherIncome  float64 `json:"other_income_cr"`
	Expenses     float64 `json:"expenses_cr"`
	Finance      float64 `json:"finance_cost_cr"`
	Depreciation float64 `json:"depreciation_cr"`
	PBT          float64 `json:"pbt_cr"`
	PAT          float64 `json:"pat_cr"`
	EPS          float64 `json:"eps"`
	Filed        string  `json:"filed"`
	Source       string  `json:"source"`
	Bank         bool    `json:"bank"` // banking form: revenue is interest earned
}

// OperatingMargin is EBIT excluding other income ÷ revenue, %. It means
// nothing for a bank (interest paid is its cost of goods), so it is 0 there.
func (q Quarter) OperatingMargin() float64 {
	if q.Revenue == 0 || q.Bank {
		return 0
	}
	return (q.PBT + q.Finance - q.OtherIncome) / q.Revenue * 100
}

// NetMargin is profit after tax ÷ revenue, %.
func (q Quarter) NetMargin() float64 {
	if q.Revenue == 0 {
		return 0
	}
	return q.PAT / q.Revenue * 100
}

// ParseXBRL reads one results filing's quarter (the context whose period is
// exactly from–to, with no segment) from its Ind-AS XBRL.
func ParseXBRL(data []byte, from, to time.Time) (Quarter, error) {
	q := Quarter{From: from, To: to}
	type ctx struct {
		ID     string `xml:"id,attr"`
		Period struct {
			Start string `xml:"startDate"`
			End   string `xml:"endDate"`
		} `xml:"period"`
		Entity struct {
			Segment *struct{} `xml:"segment"`
		} `xml:"entity"`
		Scenario *struct{} `xml:"scenario"`
	}
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	ctxOK := map[string]bool{}
	facts := map[string]map[string]float64{} // name → context → value
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return q, fmt.Errorf("XBRL: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "context" {
			var c ctx
			if err := dec.DecodeElement(&c, &se); err == nil {
				if c.Entity.Segment == nil && c.Scenario == nil &&
					c.Period.Start == from.Format("2006-01-02") && c.Period.End == to.Format("2006-01-02") {
					ctxOK[c.ID] = true
				}
			}
			continue
		}
		var cref string
		for _, a := range se.Attr {
			if a.Name.Local == "contextRef" {
				cref = a.Value
			}
		}
		if cref == "" {
			continue
		}
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			continue
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			if facts[se.Name.Local] == nil {
				facts[se.Name.Local] = map[string]float64{}
			}
			facts[se.Name.Local][cref] = v
		}
	}
	if len(ctxOK) == 0 {
		return q, fmt.Errorf("XBRL: no context for %s–%s", from.Format("02 Jan 2006"), to.Format("02 Jan 2006"))
	}
	// Several contexts can cover the quarter; read them in a fixed order,
	// the filing's own reporting-period context ("OneD") first, so the same
	// file always gives the same numbers.
	var ctxs []string
	for c := range ctxOK {
		ctxs = append(ctxs, c)
	}
	sort.Slice(ctxs, func(i, j int) bool {
		if (ctxs[i] == "OneD") != (ctxs[j] == "OneD") {
			return ctxs[i] == "OneD"
		}
		return ctxs[i] < ctxs[j]
	})
	get := func(names ...string) (float64, bool) {
		for _, n := range names {
			for _, c := range ctxs {
				if v, ok := facts[n][c]; ok {
					return v, true
				}
			}
		}
		return 0, false
	}
	cr := func(v float64) float64 { return round2(v / 1e7) }
	var found bool
	if v, ok := get("RevenueFromOperations"); ok {
		q.Revenue, found = cr(v), true
	} else if v, ok := get("InterestEarned"); ok { // banks file interest earned instead
		q.Revenue, found, q.Bank = cr(v), true, true
	}
	if v, ok := get("OtherIncome"); ok {
		q.OtherIncome = cr(v)
	}
	if v, ok := get("Expenses"); ok {
		q.Expenses = cr(v)
	}
	if v, ok := get("FinanceCosts"); ok {
		q.Finance = cr(v)
	}
	if v, ok := get("DepreciationDepletionAndAmortisationExpense"); ok {
		q.Depreciation = cr(v)
	}
	if v, ok := get("ProfitBeforeTax"); ok {
		q.PBT = cr(v)
	}
	if v, ok := get("ProfitLossForPeriod", "ProfitLossForThePeriod"); ok {
		q.PAT = cr(v)
	}
	if v, ok := get("BasicEarningsLossPerShareFromContinuingAndDiscontinuedOperations", "BasicEarningsLossPerShareFromContinuingOperations",
		"BasicEarningsLossPerShare", "BasicEarningsPerShareAfterExtraordinaryItems", "BasicEarningsPerShareBeforeExtraordinaryItems"); ok {
		q.EPS = v
	}
	if !found {
		return q, fmt.Errorf("XBRL: no revenue for the quarter (an insurer or an older form files different names)")
	}
	return q, nil
}

// BoardMeeting is an announced board meeting (often the results date).
type BoardMeeting struct {
	Symbol  string `json:"bm_symbol"`
	Date    string `json:"bm_date"`
	Purpose string `json:"bm_purpose"`
	Desc    string `json:"bm_desc"`
}

// UpcomingResults keeps the board meetings on or after day that consider
// financial results.
func UpcomingResults(bms []BoardMeeting, day time.Time) []BoardMeeting {
	var out []BoardMeeting
	for _, b := range bms {
		d, err := time.ParseInLocation("02-Jan-2006", b.Date, IST)
		if err != nil || d.Before(dayOf(day)) {
			continue
		}
		if strings.Contains(strings.ToLower(b.Purpose+" "+b.Desc), "result") {
			out = append(out, b)
		}
	}
	return out
}

// BoardMeetings lists upcoming board meetings (all stocks, or one).
func BoardMeetings(n *NSE, sym string) ([]BoardMeeting, error) {
	p := "corporate-board-meetings?index=equities"
	if sym != "" {
		p += "&symbol=" + url.QueryEscape(sym)
	}
	data, err := n.API(p, 6*time.Hour)
	if err != nil {
		return nil, err
	}
	var bms []BoardMeeting
	if err := json.Unmarshal(data, &bms); err != nil {
		return nil, fmt.Errorf("NSE board meetings: %w", err)
	}
	return bms, nil
}

// ── Bulk / block deals and insider trades ───────────────────────────────────

// Deal is one bulk or block deal, or an insider (SEBI PIT) disclosure.
type Deal struct {
	Key    string    `json:"key"`
	Kind   string    `json:"kind"` // bulk / block / insider
	Date   time.Time `json:"date"`
	Symbol string    `json:"symbol"`
	Name   string    `json:"security,omitempty"`
	Client string    `json:"client"`
	Side   string    `json:"side"` // BUY / SELL
	Qty    float64   `json:"qty"`
	Price  float64   `json:"price"`
	Value  float64   `json:"value"` // ₹
	Note   string    `json:"note,omitempty"`
	Churn  bool      `json:"churn,omitempty"` // the client bought and sold the same day; Qty is the net
}

// Deals reads today's bulk and block deals from NSE's archive files.
func Deals(n *NSE) ([]Deal, []string) {
	var out []Deal
	var errs []string
	for _, kind := range []string{"bulk", "block"} {
		data, err := n.Get("https://nsearchives.nseindia.com/content/equities/"+kind+".csv", 30*time.Minute)
		if err != nil {
			errs = append(errs, kind+": "+err.Error())
			continue
		}
		ds, err := ParseDeals(data, kind)
		if err != nil {
			errs = append(errs, kind+": "+err.Error())
			continue
		}
		out = append(out, ds...)
	}
	return out, errs
}

func ParseDeals(data []byte, kind string) ([]Deal, error) {
	rows, err := readCSV(data)
	if err != nil {
		return nil, err
	}
	var out []Deal
	for _, r := range rows {
		d := Deal{Kind: kind, Symbol: r["Symbol"], Name: r["Security Name"], Client: r["Client Name"], Side: strings.ToUpper(r["Buy/Sell"])}
		if d.Symbol == "" {
			continue
		}
		d.Date, _ = time.ParseInLocation("02-Jan-2006", titleMonth(r["Date"]), IST)
		d.Qty, _ = strconv.ParseFloat(strings.ReplaceAll(r["Quantity Traded"], ",", ""), 64)
		d.Price, _ = strconv.ParseFloat(strings.ReplaceAll(r["Trade Price / Wght. Avg. Price"], ",", ""), 64)
		d.Value = d.Qty * d.Price
		if rm := strings.TrimSpace(r["Remarks"]); rm != "-" {
			d.Note = rm
		}
		d.Key = strings.Join([]string{kind, r["Date"], d.Symbol, d.Client, d.Side, r["Quantity Traded"], r["Trade Price / Wght. Avg. Price"]}, "|")
		out = append(out, d)
	}
	return out, nil
}

// titleMonth turns "16-NOV-2026" into "16-Nov-2026".
func titleMonth(s string) string {
	p := strings.Split(strings.TrimSpace(s), "-")
	if len(p) == 3 && len(p[1]) == 3 {
		p[1] = p[1][:1] + strings.ToLower(p[1][1:])
	}
	return strings.Join(p, "-")
}

// Insider reads SEBI PIT disclosures (insider trades) between two days.
// NSE's endpoint sometimes answers with an empty list; callers say so.
func Insider(n *NSE, from, to time.Time) ([]Deal, error) {
	q := url.Values{}
	q.Set("index", "equities")
	q.Set("from_date", from.Format("02-01-2006"))
	q.Set("to_date", to.Format("02-01-2006"))
	data, err := n.API("corporates-pit?"+q.Encode(), 30*time.Minute)
	if err != nil {
		return nil, err
	}
	return ParseInsider(data)
}

func ParseInsider(data []byte) ([]Deal, error) {
	var raw struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("NSE insider trades: %w", err)
	}
	str := func(m map[string]any, k string) string {
		if v, ok := m[k]; ok && v != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return ""
	}
	num := func(m map[string]any, k string) float64 {
		f, _ := strconv.ParseFloat(strings.ReplaceAll(str(m, k), ",", ""), 64)
		return f
	}
	var out []Deal
	for _, m := range raw.Data {
		d := Deal{Kind: "insider", Symbol: str(m, "symbol"), Name: str(m, "company"), Client: str(m, "acqName"),
			Side: strings.ToUpper(str(m, "tdpTransactionType")), Qty: num(m, "secAcq"), Value: num(m, "secVal"),
			Note: strings.Trim(str(m, "personCategory")+" · "+str(m, "acqMode"), " ·")}
		if d.Qty > 0 {
			d.Price = round2(d.Value / d.Qty)
		}
		d.Date, _ = time.ParseInLocation("02-Jan-2006", str(m, "intimDt"), IST)
		if d.Date.IsZero() {
			d.Date, _ = time.ParseInLocation("02-Jan-2006", str(m, "acqtoDt"), IST)
		}
		d.Key = "insider|" + str(m, "did") + "|" + d.Symbol + "|" + d.Client + "|" + str(m, "acqfromDt") + "|" + str(m, "secAcq")
		out = append(out, d)
	}
	return out, nil
}

// NetDeals folds a client's buys and sells of one stock on one day (bulk
// and block) into one net deal. High-frequency firms buy and sell the same
// stock all day, which shows up as two huge bulk deals that cancel out; the
// net shows whether anyone actually built or cut a position. Netted deals
// have Churn set; insider disclosures pass through untouched.
func NetDeals(ds []Deal) []Deal {
	type agg struct {
		first       Deal
		buyQ, sellQ float64
		buyV, sellV float64
		kinds       map[string]bool
		sides       map[string]bool
		keys        []string
	}
	groups := map[string]*agg{}
	var order []string
	var out []Deal
	for _, d := range ds {
		if d.Kind == "insider" {
			out = append(out, d)
			continue
		}
		k := d.Date.Format("2006-01-02") + "|" + d.Symbol + "|" + strings.ToUpper(d.Client)
		g := groups[k]
		if g == nil {
			g = &agg{first: d, kinds: map[string]bool{}, sides: map[string]bool{}}
			groups[k] = g
			order = append(order, k)
		}
		g.kinds[d.Kind], g.sides[d.Side] = true, true
		g.keys = append(g.keys, d.Key)
		if d.Side == "BUY" {
			g.buyQ, g.buyV = g.buyQ+d.Qty, g.buyV+d.Value
		} else {
			g.sellQ, g.sellV = g.sellQ+d.Qty, g.sellV+d.Value
		}
	}
	for _, k := range order {
		g := groups[k]
		d := g.first
		if len(g.sides) < 2 {
			d.Qty, d.Value = g.buyQ+g.sellQ, g.buyV+g.sellV
			if d.Qty > 0 {
				d.Price = round2(d.Value / d.Qty)
			}
			out = append(out, d)
			continue
		}
		net := g.buyQ - g.sellQ
		d.Side = "BUY"
		if net < 0 {
			d.Side = "SELL"
		}
		avg := (g.buyV + g.sellV) / (g.buyQ + g.sellQ)
		d.Qty, d.Price, d.Value = math.Abs(net), round2(avg), math.Abs(net)*avg
		d.Churn = true
		d.Note = fmt.Sprintf("churn: bought %.0f, sold %.0f the same day (net shown)", g.buyQ, g.sellQ)
		d.Key = strings.Join(g.keys, "+")
		out = append(out, d)
	}
	return out
}

// SortDeals orders deals by value, largest first.
func SortDeals(ds []Deal) {
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].Value > ds[j].Value })
}
