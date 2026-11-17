package desk

// Limits are the desk's risk and sizing rules (~/.nse-desk/config.json). The
// risk check enforces them; the sizer sizes to them. Exits are never
// blocked by them.
type Limits struct {
	Capital          float64 `json:"capital"`            // ₹ in the paper account
	MaxLossPerTrade  float64 `json:"max_loss_per_trade"` // ₹ lost at the stop, per trade
	MaxDailyLoss     float64 `json:"max_daily_loss"`     // ₹: today's losses + open risk at the stops
	MaxTradesPerDay  int     `json:"max_trades_per_day"` //
	MaxPositionPct   float64 `json:"max_position_pct"`   // one position's value (cash) or margin (F&O), % of capital
	MaxOrderValue    float64 `json:"max_order_value"`    // ₹ value of one order
	CashBufferPct    float64 `json:"cash_buffer_pct"`    // keep this % of capital unused
	IntradayLeverage float64 `json:"intraday_leverage"`  // equity intraday margin = value ÷ this (SEBI peak margin: ~5×)
	MinRewardRisk    float64 `json:"min_reward_risk"`    // target distance ÷ stop distance
	// MinRewardRiskOptionSell is the floor for selling options, whose edge is
	// a high win rate with a reward under the risk (40% stop / 30% target ≈ 0.75).
	MinRewardRiskOptionSell float64  `json:"min_reward_risk_option_sell"`
	RequireStop             bool     `json:"require_stop"`        //
	NoEntriesBefore         string   `json:"no_entries_before"`   // "09:30"
	NoEntriesAfter          string   `json:"no_entries_after"`    // "14:30"
	FlatBy                  string   `json:"flat_by"`             // intraday positions closed at "15:15"
	AllowShort              bool     `json:"allow_short"`         // intraday equity shorts (allowed in India, MIS only)
	AllowOptions            bool     `json:"allow_options"`       //
	AllowOptionSell         bool     `json:"allow_option_sell"`   //
	OptionStopPct           float64  `json:"option_stop_pct"`     // default option stop: premium moves this % against you
	OptionTargetPct         float64  `json:"option_target_pct"`   // default option target: this % in your favour
	OptionMarginPct         float64  `json:"option_margin_pct"`   // short-option margin estimate, % of notional, when SPAN is unavailable
	MinOptVolumeLots        int      `json:"min_opt_volume_lots"` // an option strike must have traded this many lots today
	MaxSpreadPct            float64  `json:"max_spread_pct"`      // bid-ask spread, % of mid
	MinPrice                float64  `json:"min_price"`           // no stocks below ₹ (penny stocks)
	MinTurnoverCr           float64  `json:"min_turnover_cr"`     // scanner: yesterday's traded value, ₹ crore
	Blocked                 []string `json:"blocked"`             //
	MaxOrdersPerDay         int      `json:"max_orders_per_day"`  //
	DefaultProduct          string   `json:"default_product"`     // INTRADAY or CNC for stocks
}

// DefaultLimits builds sensible limits from the capital, risk per trade %,
// daily loss % and max spread % settings (DESK_CAPITAL, DESK_RISK_PCT,
// DESK_DAILY_LOSS_PCT, DESK_MAX_SPREAD_PCT).
func DefaultLimits(capital, riskPct, dailyLossPct, maxSpreadPct float64) Limits {
	if capital <= 0 {
		capital = 1_000_000
	}
	if riskPct <= 0 {
		riskPct = 0.5
	}
	if dailyLossPct <= 0 {
		dailyLossPct = 1
	}
	if maxSpreadPct <= 0 {
		maxSpreadPct = 5
	}
	return Limits{
		Capital: capital, MaxLossPerTrade: capital * riskPct / 100, MaxDailyLoss: capital * dailyLossPct / 100,
		MaxTradesPerDay: 4, MaxPositionPct: 25, MaxOrderValue: capital * 1.25, CashBufferPct: 10,
		IntradayLeverage: 5, MinRewardRisk: 1.5, MinRewardRiskOptionSell: 0.5, RequireStop: true,
		NoEntriesBefore: "09:30", NoEntriesAfter: "14:30", FlatBy: "15:15",
		AllowShort: true, AllowOptions: true, AllowOptionSell: true,
		OptionStopPct: 40, OptionTargetPct: 30, OptionMarginPct: 20,
		MinOptVolumeLots: 25, MaxSpreadPct: maxSpreadPct, MinPrice: 50, MinTurnoverCr: 10,
		MaxOrdersPerDay: 20, DefaultProduct: Intraday,
	}
}

// LoadLimits reads ~/.nse-desk/config.json, writing the defaults there first
// if it does not exist yet (so the user has a file to edit).
// Keys missing from the file (limits added later) keep their defaults.
func LoadLimits(d Dir, def Limits) (Limits, bool, error) {
	l := def
	err := d.Read("config.json", &l)
	if IsMissing(err) {
		return def, true, d.Write("config.json", def)
	}
	if err != nil {
		return def, false, err
	}
	return l, false, nil
}
