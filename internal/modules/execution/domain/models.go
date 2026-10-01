package domain

import "github.com/stupidprogrammer4/tidelab/internal/value"

type Side string

const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

type SizeKind string

const (
	BaseQuantity SizeKind = "base_qty"
	QuoteBudget  SizeKind = "quote_budget"
)

type Request struct {
	Side           Side
	SizeKind       SizeKind
	Amount         value.Value
	FeeBps         value.Value
	MaxSlippageBps *value.Value
}

type Status string

const (
	Filled   Status = "filled"
	Partial  Status = "partial"
	Unfilled Status = "unfilled"
)

type StopReason string

const (
	NoStop                 StopReason = ""
	ObservedDepthExhausted StopReason = "observed_depth_exhausted"
	SlippageLimit          StopReason = "slippage_limit"
	BudgetDust             StopReason = "budget_dust"
	BelowMinimum           StopReason = "below_minimum"
)

type Fill struct {
	Price        value.Value
	BaseQuantity value.Value
	GrossQuote   value.Value
}

type Result struct {
	Status                     Status
	StopReason                 StopReason
	RequestedBaseQuantity      *value.Value
	RequestedQuoteBudget       *value.Value
	FilledBaseQuantity         value.Value
	RemainingBaseQuantity      *value.Value
	RemainingQuoteBudget       *value.Value
	Fills                      []Fill
	GrossQuote                 value.Value
	FeeQuote                   value.Value
	QuoteDebit                 value.Value
	QuoteCredit                value.Value
	NotionalRoundingAdjustment value.Value
	VWAP                       *value.Value
	SlippageBps                *value.Value
	LevelsUsed                 int
	ObservedDepthExhausted     bool
	Warnings                   []string
}

// ValidationError is a bad scenario, distinct from an eligible zero-fill estimate.
type ValidationError struct {
	Field  string
	Reason string
}

func (err *ValidationError) Error() string {
	return "invalid_request: " + err.Field + ": " + err.Reason
}
