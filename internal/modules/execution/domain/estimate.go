package domain

import (
	"errors"
	"fmt"
	"math/big"

	market "github.com/stupidprogrammer4/tidelab/internal/modules/market/domain"
	"github.com/stupidprogrammer4/tidelab/internal/value"
)

type pricedLevel struct {
	price          value.Value
	priceTicks     *big.Int
	availableUnits *big.Int
}

type preparedBook struct {
	levels        []pricedLevel
	bestPrice     value.Value
	blockedReason StopReason
	blockedPrice  *value.Value
	allObserved   bool
}

type consumption struct {
	grossQuote  value.Value
	filledUnits *big.Int
	fills       []Fill
}

// Estimate calculates one hypothetical child order without changing the book.
func Estimate(revision market.Revision, request Request) (Result, error) {
	if err := revision.CheckEligible(revision.Mode()); err != nil {
		return Result{}, err
	}
	instrument := revision.Instrument()
	if err := validateRequest(request, instrument); err != nil {
		return Result{}, err
	}
	prepared, err := prepareBook(revision, request, instrument)
	if err != nil {
		return Result{}, err
	}
	if request.SizeKind == BaseQuantity {
		return estimateBaseQuantity(prepared, instrument, request)
	}
	return estimateQuoteBudget(prepared, instrument, request)
}

func validateRequest(request Request, instrument market.Instrument) error {
	if request.Side != Buy && request.Side != Sell {
		return &ValidationError{Field: "side", Reason: "must be buy or sell"}
	}
	if request.SizeKind != BaseQuantity && request.SizeKind != QuoteBudget {
		return &ValidationError{Field: "size", Reason: "exactly one base quantity or quote budget is required"}
	}
	if request.SizeKind == QuoteBudget && request.Side != Buy {
		return &ValidationError{Field: "quote_budget", Reason: "is supported only for buys"}
	}
	if err := validateBoundary(request.Amount); err != nil || request.Amount.Sign() <= 0 {
		return &ValidationError{Field: string(request.SizeKind), Reason: "must be a positive bounded decimal"}
	}
	if err := validateBoundary(request.FeeBps); err != nil || request.FeeBps.Sign() < 0 || request.FeeBps.Compare(value.FromInt64(1000)) > 0 {
		return &ValidationError{Field: "fee_bps", Reason: "must be a decimal from 0 to 1000"}
	}
	if request.MaxSlippageBps != nil {
		limit := *request.MaxSlippageBps
		if err := validateBoundary(limit); err != nil || limit.Sign() < 0 || limit.Compare(value.FromInt64(10000)) > 0 {
			return &ValidationError{Field: "max_slippage_bps", Reason: "must be a decimal from 0 to 10000"}
		}
	}
	if request.SizeKind == BaseQuantity {
		if _, err := request.Amount.Units(instrument.QuantityIncrement); err != nil {
			return &ValidationError{Field: "base_qty", Reason: "must be a multiple of quantity_increment"}
		}
		if request.Amount.Compare(instrument.MinimumQuantity) < 0 {
			return &ValidationError{Field: "base_qty", Reason: "is below minimum quantity"}
		}
	}
	return nil
}

func validateBoundary(number value.Value) error {
	canonical, err := number.ExactDecimal()
	if err != nil {
		return err
	}
	_, err = value.Parse(canonical)
	return err
}

func prepareBook(revision market.Revision, request Request, instrument market.Instrument) (preparedBook, error) {
	var observed []market.Level
	if request.Side == Buy {
		observed = revision.Asks()
	} else {
		observed = revision.Bids()
	}
	prepared := preparedBook{blockedReason: ObservedDepthExhausted}
	if len(observed) == 0 {
		return prepared, nil
	}
	prepared.bestPrice = observed[0].Price
	if request.SizeKind == BaseQuantity {
		minimumCostAtBest := request.Amount.Mul(prepared.bestPrice)
		if minimumCostAtBest.Compare(instrument.MinimumCost) < 0 {
			return preparedBook{}, &ValidationError{Field: "base_qty", Reason: "notional at the best price is below minimum cost"}
		}
	}
	for _, level := range observed {
		if request.MaxSlippageBps != nil && exceedsSlippage(level.Price, prepared.bestPrice, request.Side, *request.MaxSlippageBps) {
			price := level.Price
			prepared.blockedReason = SlippageLimit
			prepared.blockedPrice = &price
			break
		}
		quantized, err := level.Quantity.FloorToIncrement(instrument.QuantityIncrement)
		if err != nil {
			return preparedBook{}, err
		}
		quantityUnits, err := quantized.Units(instrument.QuantityIncrement)
		if err != nil {
			return preparedBook{}, err
		}
		if quantityUnits.Sign() == 0 {
			continue
		}
		priceTicks, err := level.Price.Units(instrument.PriceIncrement)
		if err != nil {
			return preparedBook{}, fmt.Errorf("book price is not aligned to price increment: %w", err)
		}
		prepared.levels = append(prepared.levels, pricedLevel{
			price: level.Price, priceTicks: priceTicks, availableUnits: quantityUnits,
		})
	}
	prepared.allObserved = prepared.blockedReason == ObservedDepthExhausted
	return prepared, nil
}

func exceedsSlippage(price, best value.Value, side Side, limit value.Value) bool {
	var difference value.Value
	if side == Buy {
		difference = price.Sub(best)
	} else {
		difference = best.Sub(price)
	}
	left := difference.Mul(value.FromInt64(10000))
	right := limit.Mul(best)
	return left.Compare(right) > 0
}

func estimateBaseQuantity(prepared preparedBook, instrument market.Instrument, request Request) (Result, error) {
	targetUnits, err := request.Amount.Units(instrument.QuantityIncrement)
	if err != nil {
		return Result{}, err
	}
	consumed, err := consume(prepared.levels, targetUnits, instrument, true)
	if err != nil {
		return Result{}, err
	}
	result, err := settle(consumed, prepared.bestPrice, instrument, request)
	if err != nil {
		return Result{}, err
	}
	requested := request.Amount
	result.RequestedBaseQuantity = &requested
	remainingUnits := new(big.Int).Sub(targetUnits, consumed.filledUnits)
	remaining, err := value.FromUnits(remainingUnits, instrument.QuantityIncrement)
	if err != nil {
		return Result{}, err
	}
	result.RemainingBaseQuantity = &remaining
	if remainingUnits.Sign() == 0 {
		result.Status = Filled
		result.StopReason = NoStop
	} else {
		result.Status = Partial
		if consumed.filledUnits.Sign() == 0 {
			result.Status = Unfilled
		}
		result.StopReason = prepared.blockedReason
	}
	result.ObservedDepthExhausted = prepared.allObserved && consumed.filledUnits.Cmp(totalUnits(prepared.levels)) == 0 && len(prepared.levels) > 0
	return result, nil
}

func estimateQuoteBudget(prepared preparedBook, instrument market.Instrument, request Request) (Result, error) {
	if request.Amount.Compare(instrument.MinimumCost) < 0 {
		return unfilledBudget(request, BelowMinimum), nil
	}
	available := totalUnits(prepared.levels)
	chosen, err := affordableUnits(prepared.levels, available, instrument, request)
	if err != nil {
		return Result{}, err
	}
	consumed, err := consume(prepared.levels, chosen, instrument, true)
	if err != nil {
		return Result{}, err
	}
	if chosen.Sign() == 0 && available.Sign() == 0 {
		return unfilledBudget(request, prepared.blockedReason), nil
	}
	nextPrice := nextReferencePrice(prepared)
	moreAffordable := false
	if chosen.Cmp(available) == 0 && nextPrice != nil {
		moreAffordable, err = canAffordNext(consumed.grossQuote, *nextPrice, instrument, request)
		if err != nil {
			return Result{}, err
		}
	}
	filledBase, err := value.FromUnits(chosen, instrument.QuantityIncrement)
	if err != nil {
		return Result{}, err
	}
	belowMinimum := filledBase.Compare(instrument.MinimumQuantity) < 0 || consumed.grossQuote.Compare(instrument.MinimumCost) < 0
	if chosen.Sign() > 0 && belowMinimum && (chosen.Cmp(available) < 0 || !moreAffordable) {
		return unfilledBudget(request, BelowMinimum), nil
	}
	result, err := settle(consumed, prepared.bestPrice, instrument, request)
	if err != nil {
		return Result{}, err
	}
	requested := request.Amount
	result.RequestedQuoteBudget = &requested
	remaining := request.Amount.Sub(result.QuoteDebit)
	if remaining.Sign() < 0 {
		return Result{}, errors.New("budget search exceeded quote budget")
	}
	result.RemainingQuoteBudget = &remaining
	result.ObservedDepthExhausted = prepared.allObserved && chosen.Cmp(available) == 0 && len(prepared.levels) > 0
	switch {
	case chosen.Sign() == 0:
		result.Status = Unfilled
		result.StopReason = BudgetDust
	case chosen.Cmp(available) < 0:
		result.Status = Filled
		result.StopReason = BudgetDust
	case moreAffordable:
		result.Status = Partial
		result.StopReason = prepared.blockedReason
	default:
		result.Status = Filled
		result.StopReason = BudgetDust
	}
	return result, nil
}

func unfilledBudget(request Request, reason StopReason) Result {
	budget := request.Amount
	return Result{
		Status: Unfilled, StopReason: reason, RequestedQuoteBudget: &budget,
		RemainingQuoteBudget: &budget, Fills: []Fill{}, Warnings: []string{},
	}
}

func nextReferencePrice(prepared preparedBook) *value.Value {
	if prepared.blockedPrice != nil {
		return prepared.blockedPrice
	}
	if len(prepared.levels) == 0 {
		return nil
	}
	price := prepared.levels[len(prepared.levels)-1].price
	return &price
}

func canAffordNext(gross, nextPrice value.Value, instrument market.Instrument, request Request) (bool, error) {
	additional := nextPrice.Mul(instrument.QuantityIncrement)
	nextGross := gross.Add(additional)
	debit, err := buyDebit(nextGross, request.FeeBps, instrument.QuoteScale)
	if err != nil {
		return false, err
	}
	return debit.Compare(request.Amount) <= 0, nil
}

func affordableUnits(levels []pricedLevel, available *big.Int, instrument market.Instrument, request Request) (*big.Int, error) {
	low := new(big.Int)
	high := new(big.Int).Add(available, big.NewInt(1))
	for new(big.Int).Sub(high, low).Cmp(big.NewInt(1)) > 0 {
		middle := new(big.Int).Add(low, high)
		middle.Rsh(middle, 1)
		consumed, err := consume(levels, middle, instrument, false)
		if err != nil {
			return nil, err
		}
		debit, err := buyDebit(consumed.grossQuote, request.FeeBps, instrument.QuoteScale)
		if err != nil {
			return nil, err
		}
		if debit.Compare(request.Amount) <= 0 {
			low.Set(middle)
		} else {
			high.Set(middle)
		}
	}
	return low, nil
}

func totalUnits(levels []pricedLevel) *big.Int {
	total := new(big.Int)
	for _, level := range levels {
		total.Add(total, level.availableUnits)
	}
	return total
}

func consume(levels []pricedLevel, requested *big.Int, instrument market.Instrument, collectFills bool) (consumption, error) {
	remaining := new(big.Int).Set(requested)
	filled := new(big.Int)
	result := consumption{filledUnits: filled, fills: []Fill{}}
	tickNotional := instrument.PriceIncrement.Mul(instrument.QuantityIncrement)
	for _, level := range levels {
		if remaining.Sign() == 0 {
			break
		}
		taken := new(big.Int).Set(level.availableUnits)
		if taken.Cmp(remaining) > 0 {
			taken.Set(remaining)
		}
		if taken.Sign() == 0 {
			continue
		}
		quoteTicks := new(big.Int).Mul(level.priceTicks, taken)
		gross, err := value.FromUnits(quoteTicks, tickNotional)
		if err != nil {
			return consumption{}, err
		}
		result.grossQuote = result.grossQuote.Add(gross)
		if collectFills {
			quantity, err := value.FromUnits(taken, instrument.QuantityIncrement)
			if err != nil {
				return consumption{}, err
			}
			result.fills = append(result.fills, Fill{Price: level.price, BaseQuantity: quantity, GrossQuote: gross})
		}
		filled.Add(filled, taken)
		remaining.Sub(remaining, taken)
	}
	return result, nil
}

func settle(consumed consumption, best value.Value, instrument market.Instrument, request Request) (Result, error) {
	filledBase, err := value.FromUnits(consumed.filledUnits, instrument.QuantityIncrement)
	if err != nil {
		return Result{}, err
	}
	fee, err := quoteFee(consumed.grossQuote, request.FeeBps, instrument.QuoteScale)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		FilledBaseQuantity: filledBase, Fills: consumed.fills, GrossQuote: consumed.grossQuote,
		FeeQuote: fee, LevelsUsed: len(consumed.fills), Warnings: []string{},
	}
	if request.Side == Buy {
		rounded, err := consumed.grossQuote.CeilToScale(instrument.QuoteScale)
		if err != nil {
			return Result{}, err
		}
		result.QuoteDebit = rounded.Add(fee)
		result.NotionalRoundingAdjustment = rounded.Sub(consumed.grossQuote)
	} else {
		rounded, err := consumed.grossQuote.FloorToScale(instrument.QuoteScale)
		if err != nil {
			return Result{}, err
		}
		result.QuoteCredit = rounded.Sub(fee)
		result.NotionalRoundingAdjustment = consumed.grossQuote.Sub(rounded)
	}
	if consumed.filledUnits.Sign() == 0 {
		return result, nil
	}
	vwap, err := consumed.grossQuote.Divide(filledBase)
	if err != nil {
		return Result{}, err
	}
	result.VWAP = &vwap
	var distance value.Value
	if request.Side == Buy {
		distance = vwap.Sub(best)
	} else {
		distance = best.Sub(vwap)
	}
	slippage, err := distance.Divide(best)
	if err != nil {
		return Result{}, err
	}
	slippage = slippage.Mul(value.FromInt64(10000))
	result.SlippageBps = &slippage
	if filledBase.Compare(instrument.MinimumQuantity) < 0 || consumed.grossQuote.Compare(instrument.MinimumCost) < 0 {
		result.Warnings = append(result.Warnings, "observed partial fill is below exchange order minimums")
	}
	return result, nil
}

func buyDebit(gross, feeBps value.Value, scale int) (value.Value, error) {
	rounded, err := gross.CeilToScale(scale)
	if err != nil {
		return value.Value{}, err
	}
	fee, err := quoteFee(gross, feeBps, scale)
	if err != nil {
		return value.Value{}, err
	}
	return rounded.Add(fee), nil
}

func quoteFee(gross, feeBps value.Value, scale int) (value.Value, error) {
	raw, err := gross.Mul(feeBps).Divide(value.FromInt64(10000))
	if err != nil {
		return value.Value{}, err
	}
	return raw.CeilToScale(scale)
}
