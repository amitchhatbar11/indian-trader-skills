package corpact

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseSubject(t *testing.T) {
	cases := []struct {
		subject string
		kind    string
		factor  float64
	}{
		{"Bonus 1:1", Bonus, 0.5},
		{" Bonus 2:5", Bonus, 5.0 / 7},
		{"Bonus 1:1/Interim Dividend Rs 2/- Per Share (Purpose Revised)", Bonus, 0.5},
		{"Face Value Split (Sub-Division) - From Rs 10/- Per Share To Rs 2/- Per Share", Split, 0.2},
		{"Face Value Split (Sub-Division) - From Rs 2/- Per Share To Re 1/- Per Share", Split, 0.5},
		{"Face Value Split From Rs.10/- To Rs.5/-", Split, 0.5},
		{"Fv Split Rs.10 To Re.1", Split, 0.1},
		{" Fv Splt Frm Rs 10 To Re 1", Split, 0.1},
		{"Consolidation Of Shares From Re 1/- To Rs 10/-", Consolidation, 10},
		{"Rights 1:5 @ Premium Rs 1196/-", Rights, 0},
		{"Demerger", Demerger, 0},
		{"Scheme Of Arrangement", Demerger, 0},
		{"Dividend - Rs 10 Per Share", "", 0},
		{"Buy Back", "", 0},
		{"Int Div-Rs.5/- Per Share", "", 0},
	}
	for _, c := range cases {
		kind, f := ParseSubject(c.subject)
		if kind != c.kind || math.Abs(f-c.factor) > 1e-9 {
			t.Errorf("%q → %s %v, want %s %v", c.subject, kind, f, c.kind, c.factor)
		}
	}
}

func d(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestRegistry(t *testing.T) {
	renames, err := ParseRenames(strings.NewReader(`NAME OF THE COMPANY,OLD SYMBOL,NEW SYMBOL,EFFECTIVE DATE
ETERNAL LIMITED,ZOMATO,ETERNAL,09-APR-2025
X Ltd,AAA,BBB,01-JAN-2020
X Ltd,BBB,CCC,01-JAN-2022
`))
	if err != nil {
		t.Fatal(err)
	}
	actions, err := ParseActions([]byte(`[
		{"symbol":"RELIANCE","series":"EQ","subject":"Bonus 1:1","exDate":"07-Sep-2017"},
		{"symbol":"RELIANCE","series":"EQ","subject":"Bonus 1:1","exDate":"28-Oct-2024"},
		{"symbol":"RELIANCE","series":"EQ","subject":"Dividend - Rs 10 Per Share","exDate":"19-Aug-2024"},
		{"symbol":"RELIANCE","series":"BL","subject":"Bonus 1:1","exDate":"28-Oct-2024"},
		{"symbol":"AAA","series":"EQ","subject":"Face Value Split From Rs.10/- To Rs.2/-","exDate":"15-Jun-2019"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	r := New(actions, renames)

	// Renames: as of a date before them, a stock maps to today's name.
	for _, c := range []struct{ sym, asOf, want string }{
		{"ZOMATO", "2024-01-01", "ETERNAL"},
		{"ZOMATO", "2025-04-09", "ZOMATO"}, // already renamed: not the same listing
		{"AAA", "2019-01-01", "CCC"},       // chained
		{"BBB", "2021-01-01", "CCC"},
		{"RELIANCE", "2010-01-01", "RELIANCE"},
	} {
		if got := r.Current(c.sym, d(c.asOf)); got != c.want {
			t.Errorf("Current(%s, %s) = %s, want %s", c.sym, c.asOf, got, c.want)
		}
	}

	// Factors: price from `from` in `to`'s terms; the ex-date itself counts.
	for _, c := range []struct {
		sym, from, to string
		want          float64
	}{
		{"RELIANCE", "2017-09-06", "2017-09-07", 0.5},
		{"RELIANCE", "2017-09-07", "2024-10-25", 1},
		{"RELIANCE", "2015-01-01", "2026-01-01", 0.25},
		{"CCC", "2019-01-01", "2026-01-01", 0.2}, // action listed under the old name
	} {
		if got := r.Factor(c.sym, d(c.from), d(c.to)); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("Factor(%s, %s→%s) = %v, want %v", c.sym, c.from, c.to, got, c.want)
		}
	}
	if n := len(r.Between("RELIANCE", d("2000-01-01"), d("2026-01-01"))); n != 2 {
		t.Errorf("RELIANCE actions = %d, want 2 (dividend and the BL series dropped)", n)
	}
	if n := len(r.RenamesBetween(d("2019-12-31"), d("2022-01-01"))); n != 2 {
		t.Errorf("renames in range = %d, want 2", n)
	}
}
