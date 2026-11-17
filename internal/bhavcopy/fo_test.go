package bhavcopy

import (
	"strings"
	"testing"
	"time"
)

func TestParseFO(t *testing.T) {
	src := `TradDt,BizDt,Sgmt,Src,FinInstrmTp,FinInstrmId,ISIN,TckrSymb,SctySrs,XpryDt,FininstrmActlXpryDt,StrkPric,OptnTp,FinInstrmNm,OpnPric,HghPric,LwPric,ClsPric,LastPric,PrvsClsgPric,UndrlygPric,SttlmPric,OpnIntrst,ChngInOpnIntrst,TtlTradgVol,TtlTrfVal,TtlNbOfTxsExctd,SsnId,NewBrdLotQty,Rmks,Rsvd1,Rsvd2,Rsvd3,Rsvd4
2026-11-06,2026-11-06,FO,NSE,STO,3,,ABC,,2026-12-29,2026-12-29,450.00,CE,ABC26DEC450CE,0,0,0,11,0,11,398.74,0.85,0,0,0,0,0,F1,1800,,,,,
2026-11-06,2026-11-06,FO,NSE,STO,1,,ABC,,2026-11-23,2026-11-23,450.00,CE,ABC26NOV450CE,0,0,0,5.35,0,5.35,398.74,0.85,0,0,3100,0,0,F1,1550,,,,,
2026-11-06,2026-11-06,FO,NSE,STO,2,,ABC,,2026-11-23,2026-11-23,440.00,PE,ABC26NOV440PE,0,0,0,9.10,0,9,398.74,0.85,0,0,800,0,0,F1,1550,,,,,
2026-11-06,2026-11-06,FO,NSE,STF,4,,ABC,,2026-11-23,2026-11-23,0,XX,ABC26NOVFUT,0,0,0,399,0,399,398.74,0,0,0,0,0,0,F1,1550,,,,,
2026-11-06,2026-11-06,FO,NSE,IDO,5,,NIFTY,,2026-11-10,2026-11-10,24000,CE,NIFTY26N1024000CE,0,0,0,50,0,50,0,0,0,0,0,0,0,F1,75,,,,,
`
	day, err := ParseFO(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(day.Chains) != 1 {
		t.Fatalf("want only the stock option chain, got %v", day.Chains)
	}
	ch := day.Chains["ABC"]
	// A lot revision from December on: each expiry keeps its own lot, and
	// LotSize is the nearest expiry's whatever the row order.
	novE, decE := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 29, 0, 0, 0, 0, time.UTC)
	if ch.LotSize != 1550 || ch.LotFor(novE) != 1550 || ch.LotFor(decE) != 1800 {
		t.Errorf("lot = %d, Nov %d, Dec %d", ch.LotSize, ch.LotFor(novE), ch.LotFor(decE))
	}
	nov := ch.Contracts["2026-11-23"]
	if len(nov) != 2 || nov[0].Strike != 440 || nov[0].Right != "PE" || nov[0].Name != "ABC26NOV440PE" || nov[1].Contracts != 3100 {
		t.Errorf("nov chain = %+v", nov)
	}
	cases := []struct{ day, want string }{
		{"2026-11-06", "2026-11-23"},
		{"2026-11-23", "2026-11-23"}, // expiry day still trades the expiring month
		{"2026-11-24", "2026-12-29"},
	}
	for _, c := range cases {
		d, _ := time.Parse("2006-01-02", c.day)
		got, ok := ch.Nearest(d)
		if !ok || got.Format("2006-01-02") != c.want {
			t.Errorf("Nearest(%s) = %v %v, want %s", c.day, got, ok, c.want)
		}
	}
}

// The pre-July-2024 format has no lot size; it comes from option turnover.
// Rows are shaped like NSE's fo05JUL2024bhav.csv (RELIANCE's lot then: 250).
func TestParseFOOld(t *testing.T) {
	src := `INSTRUMENT,SYMBOL,EXPIRY_DT,STRIKE_PR,OPTION_TYP,OPEN,HIGH,LOW,CLOSE,SETTLE_PR,CONTRACTS,VAL_INLAKH,OPEN_INT,CHG_IN_OI,TIMESTAMP,
FUTSTK,RELIANCE,25-Jul-2024,0,XX,3170,3200,3160,3190,3190,100000,7975000,0,0,05-JUL-2024,
OPTSTK,RELIANCE,25-Jul-2024,3200,CE,60,62,50,55,55,40000,326195,0,0,05-JUL-2024,
OPTSTK,RELIANCE,25-Jul-2024,3100,PE,30,31,25,28,28,0,0,0,0,05-JUL-2024,
OPTIDX,NIFTY,11-Jul-2024,24000,CE,100,110,90,100,100,1000,100,0,0,05-JUL-2024,
`
	day, err := ParseFOOld(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	ch := day.Chains["RELIANCE"]
	if len(day.Chains) != 1 || ch == nil {
		t.Fatalf("chains = %v", day.Chains)
	}
	// 326195 lakh / (40000 × (3200 + 62)) = 249.996 → 250
	if ch.LotSize != 250 || !ch.LotEstimated {
		t.Errorf("lot = %d (estimated %v), want 250", ch.LotSize, ch.LotEstimated)
	}
	jul := ch.Contracts["2024-07-25"]
	if len(jul) != 2 || jul[0].Name != "RELIANCE24JUL253100PE" || jul[1].Contracts != 40000 {
		t.Errorf("jul = %+v", jul)
	}
}

func TestEstimateLot(t *testing.T) {
	cases := []struct {
		bound float64
		want  int
	}{
		{5399.72, 5400}, {2600.0, 2600}, {4461.1, 4462}, // exact
		{2648.72, 2650}, {3748.76, 3750}, // a hair low: snapped to the round lot
		{504.9, 505}, {0, 0},
	}
	for _, c := range cases {
		if got := estimateLot(c.bound); got != c.want {
			t.Errorf("estimateLot(%v) = %d, want %d", c.bound, got, c.want)
		}
	}
}

func TestParseCMBothFormats(t *testing.T) {
	old := `SYMBOL,SERIES,OPEN,HIGH,LOW,CLOSE,LAST,PREVCLOSE,TOTTRDQTY,TOTTRDVAL,TIMESTAMP,TOTALTRADES,ISIN,
RELIANCE,EQ,1640,1650,1630,1645.4,1645,1632.6,500000,1,06-SEP-2017,1,INE002A01018,
RELIANCE,BL,1640,1640,1640,1640,1640,1640,10,1,06-SEP-2017,1,INE002A01018,
`
	new := `TradDt,BizDt,Sgmt,Src,FinInstrmTp,FinInstrmId,ISIN,TckrSymb,SctySrs,XpryDt,FininstrmActlXpryDt,StrkPric,OptnTp,FinInstrmNm,OpnPric,HghPric,LwPric,ClsPric,LastPric,PrvsClsgPric,UndrlygPric,SttlmPric,OpnIntrst,ChngInOpnIntrst,TtlTradgVol,TtlTrfVal,TtlNbOfTxsExctd,SsnId,NewBrdLotQty,Rmks,Rsvd1,Rsvd2,Rsvd3,Rsvd4
2024-10-28,2024-10-28,CM,NSE,STK,2885,INE002A01018,RELIANCE,EQ,,,,,RELIANCE INDUSTRIES LTD,1340,1345,1320,1334.35,1334,2655.70,,1334.35,,,900000,1,1,F1,1,,,,,
`
	for name, src := range map[string]string{"old": old, "udiff": new} {
		day, err := ParseCM(strings.NewReader(src))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		r, ok := day.Rows["RELIANCE"]
		if !ok || len(day.Rows) != 1 || r.Close <= 0 || r.High < r.Low || r.ISIN != "INE002A01018" {
			t.Errorf("%s: rows = %+v", name, day.Rows)
		}
	}
}
