package domain

import "time"

type RequestView struct {
	Side           Side    `json:"side"`
	BaseQuantity   *string `json:"base_qty"`
	QuoteBudget    *string `json:"quote_budget"`
	FeeBps         string  `json:"fee_bps"`
	MaxSlippageBps *string `json:"max_slippage_bps"`
}

type FillView struct {
	Price        string `json:"price"`
	BaseQuantity string `json:"base_qty"`
	GrossQuote   string `json:"gross_quote"`
}

type ResultView struct {
	Status                     Status     `json:"status"`
	StopReason                 StopReason `json:"stop_reason"`
	RequestedBaseQuantity      *string    `json:"requested_base_qty"`
	RequestedQuoteBudget       *string    `json:"requested_quote_budget"`
	FilledBaseQuantity         string     `json:"filled_base_qty"`
	RemainingBaseQuantity      *string    `json:"remaining_base_qty"`
	RemainingQuoteBudget       *string    `json:"remaining_quote_budget"`
	Fills                      []FillView `json:"fills"`
	GrossQuote                 string     `json:"gross_quote"`
	FeeQuote                   string     `json:"fee_quote"`
	QuoteDebit                 string     `json:"quote_debit"`
	QuoteCredit                string     `json:"quote_credit"`
	NotionalRoundingAdjustment string     `json:"notional_rounding_adjustment"`
	VWAP                       *string    `json:"vwap"`
	SlippageBps                *string    `json:"slippage_bps"`
	LevelsUsed                 int        `json:"levels_used"`
	ObservedDepthExhausted     bool       `json:"observed_depth_exhausted"`
	Warnings                   []string   `json:"warnings"`
}

type DataQualityView struct {
	BookState        string    `json:"book_state"`
	FeedLive         bool      `json:"feed_live"`
	ChecksumValid    bool      `json:"checksum_valid"`
	SubscribedDepth  int       `json:"subscribed_depth"`
	LastBookUpdateAt time.Time `json:"last_book_update_at"`
}

type EstimateReport struct {
	SchemaVersion    int             `json:"schema_version"`
	Source           string          `json:"source"`
	Provenance       string          `json:"provenance"`
	Symbol           string          `json:"symbol"`
	BookRevision     uint64          `json:"book_revision"`
	BookDigest       string          `json:"book_digest"`
	InputFingerprint string          `json:"input_fingerprint"`
	EngineVersion    string          `json:"engine_version"`
	FeeModel         string          `json:"fee_model"`
	RoundingPolicy   string          `json:"rounding_policy"`
	DataQuality      DataQualityView `json:"data_quality"`
	Parameters       RequestView     `json:"parameters"`
	Assumptions      []string        `json:"assumptions"`
	Result           ResultView      `json:"result"`
}
