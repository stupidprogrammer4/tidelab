package domain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/stupidprogrammer4/tidelab/internal/value"
)

// Instrument contains the rules recorded for one spot-market pair.
type Instrument struct {
	Symbol            string
	BaseCurrency      string
	QuoteCurrency     string
	PriceIncrement    value.Value
	QuantityIncrement value.Value
	MinimumQuantity   value.Value
	MinimumCost       value.Value
	QuoteScale        int
	Status            string
}

func (instrument Instrument) Validate() error {
	if instrument.Symbol == "" || instrument.BaseCurrency == "" || instrument.QuoteCurrency == "" {
		return errors.New("instrument symbol and currencies are required")
	}
	if !strings.Contains(instrument.Symbol, "/") {
		return errors.New("instrument symbol must identify a pair")
	}
	if instrument.PriceIncrement.Sign() <= 0 || instrument.QuantityIncrement.Sign() <= 0 {
		return errors.New("price and quantity increments must be positive")
	}
	if instrument.MinimumQuantity.Sign() <= 0 || instrument.MinimumCost.Sign() <= 0 {
		return errors.New("minimum quantity and cost must be positive")
	}
	for _, field := range []struct {
		name  string
		value value.Value
	}{
		{"price increment", instrument.PriceIncrement},
		{"quantity increment", instrument.QuantityIncrement},
		{"minimum quantity", instrument.MinimumQuantity},
		{"minimum cost", instrument.MinimumCost},
	} {
		if err := validateFiniteDecimal(field.value); err != nil {
			return fmt.Errorf("%s: %w", field.name, err)
		}
	}
	if instrument.QuoteScale < 0 || instrument.QuoteScale > 24 {
		return fmt.Errorf("quote scale %d is outside 0 to 24", instrument.QuoteScale)
	}
	if instrument.Status != "online" {
		return fmt.Errorf("instrument status %q is not supported", instrument.Status)
	}
	return nil
}

func validateFiniteDecimal(number value.Value) error {
	canonical, err := number.ExactDecimal()
	if err != nil {
		return err
	}
	if _, err := value.Parse(canonical); err != nil {
		return err
	}
	return nil
}

type Side string

const (
	Ask Side = "ask"
	Bid Side = "bid"
)

type Level struct {
	Price    value.Value
	Quantity value.Value
}

type Change struct {
	Side     Side
	Price    value.Value
	Quantity value.Value // Zero deletes the level.
}
