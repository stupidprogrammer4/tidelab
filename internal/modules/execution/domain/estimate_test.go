package domain

import (
	"errors"
	"testing"
	"time"

	market "github.com/stupidprogrammer4/tidelab/internal/modules/market/domain"
	"github.com/stupidprogrammer4/tidelab/internal/value"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 9, 30, 6, 0, 0, 0, time.UTC) }

func decimal(t *testing.T, input string) value.Value {
	t.Helper()
	parsed, err := value.Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func exact(t *testing.T, number value.Value) string {
	t.Helper()
	result, err := number.ExactDecimal()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func reported(t *testing.T, number *value.Value) string {
	t.Helper()
	if number == nil {
		return "<nil>"
	}
	result, err := number.FormatFixed(value.DefaultReportScale)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func instrument(t *testing.T) market.Instrument {
	t.Helper()
	return market.Instrument{
		Symbol: "TEST/USD", BaseCurrency: "TEST", QuoteCurrency: "USD",
		PriceIncrement: decimal(t, "0.01"), QuantityIncrement: decimal(t, "0.0001"),
		MinimumQuantity: decimal(t, "0.0001"), MinimumCost: decimal(t, "0.0001"),
		QuoteScale: 4, Status: "online",
	}
}

func level(t *testing.T, price, quantity string) market.Level {
	t.Helper()
	return market.Level{Price: decimal(t, price), Quantity: decimal(t, quantity)}
}

func book(t *testing.T, metadata market.Instrument, asks, bids []market.Level) market.Revision {
	t.Helper()
	state, err := market.NewBook(metadata, 2, market.Offline, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ApplySnapshot(asks, bids, false); err != nil {
		t.Fatal(err)
	}
	return state.Current()
}

func goldenBook(t *testing.T) market.Revision {
	t.Helper()
	return book(t, instrument(t),
		[]market.Level{level(t, "100", "1"), level(t, "101", "2")},
		[]market.Level{level(t, "99", "1"), level(t, "98", "2")},
	)
}

func baseRequest(t *testing.T, side Side, amount, fee string) Request {
	t.Helper()
	return Request{Side: side, SizeKind: BaseQuantity, Amount: decimal(t, amount), FeeBps: decimal(t, fee)}
}

func budgetRequest(t *testing.T, amount, fee string) Request {
	t.Helper()
	return Request{Side: Buy, SizeKind: QuoteBudget, Amount: decimal(t, amount), FeeBps: decimal(t, fee)}
}

func TestGoldenExecutionExamples(t *testing.T) {
	revision := goldenBook(t)
	before := revision.Digest()
	for _, test := range []struct {
		name, side, amount, fee       string
		kind                          SizeKind
		limit                         string
		status                        Status
		reason                        StopReason
		filled, gross, feeQuote       string
		debit, credit, vwap, slippage string
		remaining                     string
	}{
		{"buy base", "buy", "2.5", "10", BaseQuantity, "", Filled, NoStop, "2.5", "251.5", "0.2515", "251.7515", "0", "100.6", "60", "0"},
		{"sell base", "sell", "2.5", "10", BaseQuantity, "", Filled, NoStop, "2.5", "246", "0.246", "0", "245.754", "98.4", "60.6060606061", "0"},
		{"observed depth", "buy", "4", "10", BaseQuantity, "", Partial, ObservedDepthExhausted, "3", "302", "0.302", "302.302", "0", "100.6666666667", "66.6666666667", "1"},
		{"budget includes fee", "buy", "100", "10", QuoteBudget, "", Filled, BudgetDust, "0.999", "99.9", "0.0999", "99.9999", "0", "100", "0", "0.0001"},
		{"slippage cap", "buy", "2.5", "10", BaseQuantity, "50", Partial, SlippageLimit, "1", "100", "0.1", "100.1", "0", "100", "0", "1.5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := Request{Side: Side(test.side), SizeKind: test.kind, Amount: decimal(t, test.amount), FeeBps: decimal(t, test.fee)}
			if test.limit != "" {
				limit := decimal(t, test.limit)
				request.MaxSlippageBps = &limit
			}
			result, err := Estimate(revision, request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.status || result.StopReason != test.reason {
				t.Fatalf("status = %s, reason = %s", result.Status, result.StopReason)
			}
			for _, field := range []struct{ name, got, want string }{
				{"filled", exact(t, result.FilledBaseQuantity), test.filled},
				{"gross", exact(t, result.GrossQuote), test.gross},
				{"fee", exact(t, result.FeeQuote), test.feeQuote},
				{"debit", exact(t, result.QuoteDebit), test.debit},
				{"credit", exact(t, result.QuoteCredit), test.credit},
				{"vwap", reported(t, result.VWAP), test.vwap},
				{"slippage", reported(t, result.SlippageBps), test.slippage},
			} {
				if field.got != field.want {
					t.Errorf("%s = %s, want %s", field.name, field.got, field.want)
				}
			}
			if test.kind == BaseQuantity {
				if result.RemainingBaseQuantity == nil || exact(t, *result.RemainingBaseQuantity) != test.remaining || result.RemainingQuoteBudget != nil {
					t.Fatal("incorrect base-size remainder")
				}
			} else if result.RemainingQuoteBudget == nil || exact(t, *result.RemainingQuoteBudget) != test.remaining || result.RemainingBaseQuantity != nil {
				t.Fatal("incorrect budget remainder")
			}
		})
	}
	if revision.Digest() != before || exact(t, revision.Asks()[0].Quantity) != "1" {
		t.Fatal("estimate changed published book")
	}
}

func TestMisalignmentAndUnsupportedRequestsReturnValidationErrors(t *testing.T) {
	revision := goldenBook(t)
	for _, request := range []Request{
		baseRequest(t, Buy, "0.00015", "10"),
		{Side: Sell, SizeKind: QuoteBudget, Amount: decimal(t, "100"), FeeBps: decimal(t, "10")},
		baseRequest(t, Buy, "1", "1001"),
	} {
		_, err := Estimate(revision, request)
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Errorf("request %+v returned %v, want validation error", request, err)
		}
	}
	if exact(t, revision.Asks()[0].Quantity) != "1" {
		t.Fatal("validation mutated book")
	}
	metadata := instrument(t)
	metadata.MinimumCost = decimal(t, "1")
	revision = book(t, metadata, []market.Level{level(t, "100", "1")}, nil)
	_, err := Estimate(revision, baseRequest(t, Buy, "0.0001", "10"))
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Field != "base_qty" {
		t.Fatalf("below-minimum notional returned %v", err)
	}
}

func TestIntegerPriceTicksBeyondFloatPrecision(t *testing.T) {
	metadata := instrument(t)
	metadata.PriceIncrement = decimal(t, "1")
	metadata.QuantityIncrement = decimal(t, "1")
	metadata.QuoteScale = 0
	metadata.MinimumQuantity = decimal(t, "1")
	metadata.MinimumCost = decimal(t, "1")
	revision := book(t, metadata, []market.Level{level(t, "9007199254740993", "2")}, nil)
	result, err := Estimate(revision, baseRequest(t, Buy, "2", "0"))
	if err != nil {
		t.Fatal(err)
	}
	if got := exact(t, result.GrossQuote); got != "18014398509481986" {
		t.Fatalf("large-integer gross = %s", got)
	}
	if got := exact(t, result.QuoteDebit); got != "18014398509481986" {
		t.Fatalf("large-integer debit = %s", got)
	}
}

func TestNonPowerIncrementAndAggregateRounding(t *testing.T) {
	metadata := instrument(t)
	metadata.PriceIncrement = decimal(t, "0.05")
	metadata.QuantityIncrement = decimal(t, "0.03")
	metadata.MinimumQuantity = decimal(t, "0.03")
	metadata.MinimumCost = decimal(t, "0.01")
	revision := book(t, metadata, []market.Level{level(t, "1.05", "0.12")}, nil)
	result, err := Estimate(revision, baseRequest(t, Buy, "0.09", "0"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Filled || exact(t, result.GrossQuote) != "0.0945" || exact(t, result.QuoteDebit) != "0.0945" {
		t.Fatalf("non-power increment result = %+v", result)
	}
	metadata.PriceIncrement = decimal(t, "0.005")
	metadata.QuantityIncrement = decimal(t, "0.1")
	metadata.QuoteScale = 2
	metadata.MinimumQuantity = decimal(t, "0.1")
	revision = book(t, metadata, []market.Level{level(t, "1.005", "1")}, nil)
	result, err = Estimate(revision, budgetRequest(t, "0.21", "100"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Filled || exact(t, result.FilledBaseQuantity) != "0.1" || exact(t, result.GrossQuote) != "0.1005" || exact(t, result.FeeQuote) != "0.01" || exact(t, result.QuoteDebit) != "0.12" {
		t.Fatalf("rounded budget result = %+v", result)
	}
	if result.RemainingQuoteBudget == nil || exact(t, *result.RemainingQuoteBudget) != "0.09" {
		t.Fatalf("remaining budget = %+v", result.RemainingQuoteBudget)
	}
}

func TestEmptySideAndPartialBelowMinimum(t *testing.T) {
	metadata := instrument(t)
	revision := book(t, metadata, nil, []market.Level{level(t, "99", "1")})
	result, err := Estimate(revision, baseRequest(t, Buy, "1", "10"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Unfilled || result.StopReason != ObservedDepthExhausted || result.VWAP != nil || result.SlippageBps != nil || exact(t, result.FeeQuote) != "0" {
		t.Fatalf("empty-side result = %+v", result)
	}
	metadata.MinimumQuantity = decimal(t, "1")
	metadata.MinimumCost = decimal(t, "100")
	revision = book(t, metadata, []market.Level{level(t, "100", "0.5")}, nil)
	result, err = Estimate(revision, baseRequest(t, Buy, "1", "10"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Partial || exact(t, result.FilledBaseQuantity) != "0.5" || len(result.Warnings) != 1 {
		t.Fatalf("partial below-minimum observation = %+v", result)
	}
}

func TestBudgetDepthAndSlippageRemainPartialWhenMoreIsAffordable(t *testing.T) {
	revision := goldenBook(t)
	result, err := Estimate(revision, budgetRequest(t, "500", "10"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Partial || result.StopReason != ObservedDepthExhausted || exact(t, result.FilledBaseQuantity) != "3" {
		t.Fatalf("depth-limited budget = %+v", result)
	}
	request := budgetRequest(t, "500", "10")
	limit := decimal(t, "50")
	request.MaxSlippageBps = &limit
	result, err = Estimate(revision, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Partial || result.StopReason != SlippageLimit || exact(t, result.FilledBaseQuantity) != "1" {
		t.Fatalf("slippage-limited budget = %+v", result)
	}
}

func TestLargeBudgetSearchUsesIntegerUnits(t *testing.T) {
	metadata := instrument(t)
	metadata.PriceIncrement = decimal(t, "1")
	revision := book(t, metadata, []market.Level{level(t, "1", "1000000000000000000000000")}, nil)
	result, err := Estimate(revision, budgetRequest(t, "500000000000000000000000", "0"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Filled || result.StopReason != BudgetDust || exact(t, result.FilledBaseQuantity) != "500000000000000000000000" || exact(t, result.QuoteDebit) != "500000000000000000000000" {
		t.Fatalf("large-budget result = %+v", result)
	}
}

func TestVWAPReportingRoundsHalfToEven(t *testing.T) {
	metadata := instrument(t)
	metadata.PriceIncrement = decimal(t, "0.0000000001")
	metadata.QuantityIncrement = decimal(t, "1")
	metadata.MinimumQuantity = decimal(t, "1")
	metadata.MinimumCost = decimal(t, "1")
	for _, test := range []struct {
		first, second, reportedVWAP string
	}{
		{"1", "1.0000000001", "1"},
		{"1.0000000001", "1.0000000002", "1.0000000002"},
	} {
		revision := book(t, metadata, []market.Level{level(t, test.first, "1"), level(t, test.second, "1")}, nil)
		result, err := Estimate(revision, baseRequest(t, Buy, "2", "0"))
		if err != nil {
			t.Fatal(err)
		}
		if got := reported(t, result.VWAP); got != test.reportedVWAP {
			t.Errorf("VWAP of %s and %s = %s, want %s", test.first, test.second, got, test.reportedVWAP)
		}
	}
}
