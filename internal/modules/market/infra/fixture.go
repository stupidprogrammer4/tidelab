package infra

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/stupidprogrammer4/tidelab/internal/modules/market/domain"
	"github.com/stupidprogrammer4/tidelab/internal/value"
)

const maxFixtureBytes = 1 << 20

type FileFixtureLoader struct{}

type rawLevel struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}

type rawChange struct {
	Side     domain.Side `json:"side"`
	Price    string      `json:"price"`
	Quantity string      `json:"quantity"`
}

type rawUpdate struct {
	OffsetMS int64       `json:"offset_ms"`
	Changes  []rawChange `json:"changes"`
}

type rawFixture struct {
	Kind              string      `json:"kind"`
	Provenance        string      `json:"provenance"`
	Symbol            string      `json:"symbol"`
	BaseCurrency      string      `json:"base_currency"`
	QuoteCurrency     string      `json:"quote_currency"`
	PriceIncrement    string      `json:"price_increment"`
	QuantityIncrement string      `json:"quantity_increment"`
	QuoteScale        int         `json:"quote_scale"`
	MinimumQuantity   string      `json:"minimum_quantity"`
	MinimumCost       string      `json:"minimum_cost"`
	Status            string      `json:"status"`
	SubscribedDepth   int         `json:"subscribed_depth"`
	StartAt           string      `json:"start_at"`
	Asks              []rawLevel  `json:"asks"`
	Bids              []rawLevel  `json:"bids"`
	Updates           []rawUpdate `json:"updates"`
}

func (FileFixtureLoader) Load(path string) (domain.OfflineBookSequence, error) {
	file, err := os.Open(path)
	if err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("open fixture: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxFixtureBytes+1))
	if err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("read fixture: %w", err)
	}
	if len(content) > maxFixtureBytes {
		return domain.OfflineBookSequence{}, errors.New("fixture exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var raw rawFixture
	if err := decoder.Decode(&raw); err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("decode fixture: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return domain.OfflineBookSequence{}, errors.New("fixture has trailing data")
	}
	if raw.Kind != "synthetic_fixture_metadata" || raw.Provenance == "" {
		return domain.OfflineBookSequence{}, errors.New("fixture must identify synthetic provenance")
	}
	startAt, err := time.Parse(time.RFC3339Nano, raw.StartAt)
	if err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("fixture start_at: %w", err)
	}
	priceIncrement, err := value.ParsePositive(raw.PriceIncrement)
	if err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("price_increment: %w", err)
	}
	quantityIncrement, err := value.ParsePositive(raw.QuantityIncrement)
	if err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("quantity_increment: %w", err)
	}
	minimumQuantity, err := value.ParsePositive(raw.MinimumQuantity)
	if err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("minimum_quantity: %w", err)
	}
	minimumCost, err := value.ParsePositive(raw.MinimumCost)
	if err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("minimum_cost: %w", err)
	}
	instrument := domain.Instrument{
		Symbol:            raw.Symbol,
		BaseCurrency:      raw.BaseCurrency,
		QuoteCurrency:     raw.QuoteCurrency,
		PriceIncrement:    priceIncrement,
		QuantityIncrement: quantityIncrement,
		MinimumQuantity:   minimumQuantity,
		MinimumCost:       minimumCost,
		QuoteScale:        raw.QuoteScale,
		Status:            raw.Status,
	}
	if err := instrument.Validate(); err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("fixture instrument: %w", err)
	}
	asks, err := parseLevels(raw.Asks)
	if err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("fixture asks: %w", err)
	}
	bids, err := parseLevels(raw.Bids)
	if err != nil {
		return domain.OfflineBookSequence{}, fmt.Errorf("fixture bids: %w", err)
	}
	sequence := domain.OfflineBookSequence{
		Provenance: raw.Provenance, Instrument: instrument, Depth: raw.SubscribedDepth,
		StartAt: startAt, SnapshotAsks: asks, SnapshotBids: bids,
	}
	lastOffset := int64(0)
	for _, update := range raw.Updates {
		if update.OffsetMS < lastOffset || update.OffsetMS > 3_600_000 {
			return domain.OfflineBookSequence{}, errors.New("fixture update offsets must be ordered within one hour")
		}
		changes := make([]domain.Change, 0, len(update.Changes))
		for _, change := range update.Changes {
			price, err := value.ParsePositive(change.Price)
			if err != nil {
				return domain.OfflineBookSequence{}, fmt.Errorf("fixture change price: %w", err)
			}
			quantity, err := value.Parse(change.Quantity)
			if err != nil {
				return domain.OfflineBookSequence{}, fmt.Errorf("fixture change quantity: %w", err)
			}
			changes = append(changes, domain.Change{Side: change.Side, Price: price, Quantity: quantity})
		}
		sequence.Updates = append(sequence.Updates, domain.TimedUpdate{Offset: time.Duration(update.OffsetMS) * time.Millisecond, Changes: changes})
		lastOffset = update.OffsetMS
	}
	return sequence, nil
}

func parseLevels(raw []rawLevel) ([]domain.Level, error) {
	levels := make([]domain.Level, 0, len(raw))
	for _, level := range raw {
		price, err := value.ParsePositive(level.Price)
		if err != nil {
			return nil, fmt.Errorf("price: %w", err)
		}
		quantity, err := value.ParsePositive(level.Quantity)
		if err != nil {
			return nil, fmt.Errorf("quantity: %w", err)
		}
		levels = append(levels, domain.Level{Price: price, Quantity: quantity})
	}
	return levels, nil
}
