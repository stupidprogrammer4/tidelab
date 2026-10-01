package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	execution "github.com/stupidprogrammer4/tidelab/internal/modules/execution/domain"
	marketdomain "github.com/stupidprogrammer4/tidelab/internal/modules/market/domain"
	market "github.com/stupidprogrammer4/tidelab/internal/modules/market/services"
	"github.com/stupidprogrammer4/tidelab/internal/value"
)

const (
	EngineVersion  = "execution-v1"
	feeModel       = "explicit_bps_on_aggregate_notional"
	roundingPolicy = "quote_scale: buy_notional_ceil, sell_notional_floor, fee_ceil; ratios_half_even_10"
)

type OfflineBookSource interface {
	Load(path string) (market.OfflineBook, error)
}

type Input struct {
	Side           string
	BaseQuantity   string
	QuoteBudget    string
	FeeBps         string
	MaxSlippageBps string
}

type EstimateOffline struct {
	Source OfflineBookSource
}

func (service EstimateOffline) Run(path string, input Input) (execution.EstimateReport, error) {
	request, err := parseInput(input)
	if err != nil {
		return execution.EstimateReport{}, err
	}
	loaded, err := service.Source.Load(path)
	if err != nil {
		return execution.EstimateReport{}, err
	}
	result, err := execution.Estimate(loaded.Revision, request)
	if err != nil {
		return execution.EstimateReport{}, err
	}
	parameters, err := requestView(request)
	if err != nil {
		return execution.EstimateReport{}, err
	}
	resultView, err := viewResult(result)
	if err != nil {
		return execution.EstimateReport{}, err
	}
	fingerprint, err := inputFingerprint(loaded.Revision.Digest(), loaded.Revision.Instrument(), parameters)
	if err != nil {
		return execution.EstimateReport{}, err
	}
	return execution.EstimateReport{
		SchemaVersion: 1, Source: "synthetic_fixture", Provenance: loaded.Provenance,
		Symbol: loaded.Revision.Instrument().Symbol, BookRevision: loaded.Revision.Number(),
		BookDigest: loaded.Revision.Digest(), InputFingerprint: fingerprint,
		EngineVersion: EngineVersion, FeeModel: feeModel, RoundingPolicy: roundingPolicy,
		DataQuality: execution.DataQualityView{
			BookState: string(loaded.Revision.State()), FeedLive: loaded.Revision.FeedLive(),
			ChecksumValid: loaded.Revision.ChecksumValid(), SubscribedDepth: loaded.Revision.Depth(),
			LastBookUpdateAt: loaded.Revision.LastBookUpdateAt(),
		},
		Parameters: parameters,
		Assumptions: []string{
			"Hypothetical fill against observed book depth only; no hidden liquidity or queue priority.",
			"Fee basis points are caller supplied and charged once on aggregate gross notional.",
			"Quote scale is TideLab simulation quantization, not account settlement precision.",
		},
		Result: resultView,
	}, nil
}

func parseInput(input Input) (execution.Request, error) {
	if (input.BaseQuantity == "") == (input.QuoteBudget == "") {
		return execution.Request{}, &execution.ValidationError{Field: "size", Reason: "exactly one of base_qty or quote_budget is required"}
	}
	if input.FeeBps == "" {
		return execution.Request{}, &execution.ValidationError{Field: "fee_bps", Reason: "is required"}
	}
	request := execution.Request{Side: execution.Side(input.Side)}
	var amountText string
	if input.BaseQuantity != "" {
		request.SizeKind = execution.BaseQuantity
		amountText = input.BaseQuantity
	} else {
		request.SizeKind = execution.QuoteBudget
		amountText = input.QuoteBudget
	}
	amount, err := value.ParsePositive(amountText)
	if err != nil {
		return execution.Request{}, &execution.ValidationError{Field: string(request.SizeKind), Reason: err.Error()}
	}
	request.Amount = amount
	fee, err := value.Parse(input.FeeBps)
	if err != nil {
		return execution.Request{}, &execution.ValidationError{Field: "fee_bps", Reason: err.Error()}
	}
	request.FeeBps = fee
	if input.MaxSlippageBps != "" {
		limit, err := value.Parse(input.MaxSlippageBps)
		if err != nil {
			return execution.Request{}, &execution.ValidationError{Field: "max_slippage_bps", Reason: err.Error()}
		}
		request.MaxSlippageBps = &limit
	}
	return request, nil
}

func requestView(request execution.Request) (execution.RequestView, error) {
	amount, err := request.Amount.ExactDecimal()
	if err != nil {
		return execution.RequestView{}, err
	}
	fee, err := request.FeeBps.ExactDecimal()
	if err != nil {
		return execution.RequestView{}, err
	}
	view := execution.RequestView{Side: request.Side, FeeBps: fee}
	if request.SizeKind == execution.BaseQuantity {
		view.BaseQuantity = &amount
	} else {
		view.QuoteBudget = &amount
	}
	if request.MaxSlippageBps != nil {
		limit, err := request.MaxSlippageBps.ExactDecimal()
		if err != nil {
			return execution.RequestView{}, err
		}
		view.MaxSlippageBps = &limit
	}
	return view, nil
}

func viewResult(result execution.Result) (execution.ResultView, error) {
	view := execution.ResultView{
		Status: result.Status, StopReason: result.StopReason, LevelsUsed: result.LevelsUsed,
		ObservedDepthExhausted: result.ObservedDepthExhausted, Warnings: result.Warnings,
		Fills: []execution.FillView{},
	}
	var err error
	if view.RequestedBaseQuantity, err = optionalDecimal(result.RequestedBaseQuantity); err != nil {
		return execution.ResultView{}, err
	}
	if view.RequestedQuoteBudget, err = optionalDecimal(result.RequestedQuoteBudget); err != nil {
		return execution.ResultView{}, err
	}
	if view.RemainingBaseQuantity, err = optionalDecimal(result.RemainingBaseQuantity); err != nil {
		return execution.ResultView{}, err
	}
	if view.RemainingQuoteBudget, err = optionalDecimal(result.RemainingQuoteBudget); err != nil {
		return execution.ResultView{}, err
	}
	if view.VWAP, err = optionalReported(result.VWAP); err != nil {
		return execution.ResultView{}, err
	}
	if view.SlippageBps, err = optionalReported(result.SlippageBps); err != nil {
		return execution.ResultView{}, err
	}
	for _, field := range []struct {
		value value.Value
		out   *string
	}{
		{result.FilledBaseQuantity, &view.FilledBaseQuantity},
		{result.GrossQuote, &view.GrossQuote},
		{result.FeeQuote, &view.FeeQuote},
		{result.QuoteDebit, &view.QuoteDebit},
		{result.QuoteCredit, &view.QuoteCredit},
		{result.NotionalRoundingAdjustment, &view.NotionalRoundingAdjustment},
	} {
		*field.out, err = field.value.ExactDecimal()
		if err != nil {
			return execution.ResultView{}, err
		}
	}
	for _, fill := range result.Fills {
		price, err := fill.Price.ExactDecimal()
		if err != nil {
			return execution.ResultView{}, err
		}
		quantity, err := fill.BaseQuantity.ExactDecimal()
		if err != nil {
			return execution.ResultView{}, err
		}
		gross, err := fill.GrossQuote.ExactDecimal()
		if err != nil {
			return execution.ResultView{}, err
		}
		view.Fills = append(view.Fills, execution.FillView{Price: price, BaseQuantity: quantity, GrossQuote: gross})
	}
	return view, nil
}

func optionalDecimal(number *value.Value) (*string, error) {
	if number == nil {
		return nil, nil
	}
	decimal, err := number.ExactDecimal()
	if err != nil {
		return nil, err
	}
	return &decimal, nil
}

func optionalReported(number *value.Value) (*string, error) {
	if number == nil {
		return nil, nil
	}
	decimal, err := number.FormatFixed(value.DefaultReportScale)
	if err != nil {
		return nil, err
	}
	return &decimal, nil
}

func inputFingerprint(bookDigest string, instrument marketdomain.Instrument, parameters execution.RequestView) (string, error) {
	canonical, err := json.Marshal(struct {
		EngineVersion string                  `json:"engine_version"`
		BookDigest    string                  `json:"book_digest"`
		Instrument    marketdomain.Instrument `json:"instrument"`
		Parameters    execution.RequestView   `json:"parameters"`
	}{EngineVersion: EngineVersion, BookDigest: bookDigest, Instrument: instrument, Parameters: parameters})
	if err != nil {
		return "", fmt.Errorf("encode estimate fingerprint: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}
