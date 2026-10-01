package kraken

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stupidprogrammer4/tidelab/internal/modules/market/domain"
	"github.com/stupidprogrammer4/tidelab/internal/value"
)

// Adapter owns the protocol-specific representation and one live book. Handle is
// called by one ordered capture worker; Current is safe for concurrent readers.
type Adapter struct {
	mu         sync.RWMutex
	symbol     string
	depth      int
	clock      domain.Clock
	instrument *domain.Instrument
	book       *domain.Book
	asks       map[string]rawLevel
	bids       map[string]rawLevel
}

type HandleResult struct {
	InstrumentReady bool
	MetadataChanged bool
	BookChanged     bool
}

type rawLevel struct {
	price, quantity         value.Value
	priceText, quantityText string
}

type wireMessage struct {
	Channel string          `json:"channel"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
	Method  string          `json:"method"`
	Success *bool           `json:"success"`
	Error   string          `json:"error"`
}

type wireBook struct {
	Symbol   string          `json:"symbol"`
	Asks     []wireLevel     `json:"asks"`
	Bids     []wireLevel     `json:"bids"`
	Checksum json.RawMessage `json:"checksum"`
}

type wireLevel struct {
	Price json.RawMessage `json:"price"`
	Qty   json.RawMessage `json:"qty"`
}

type wireInstrument struct {
	Pairs []struct {
		Symbol         string          `json:"symbol"`
		Base           string          `json:"base"`
		Quote          string          `json:"quote"`
		PriceIncrement json.RawMessage `json:"price_increment"`
		QtyIncrement   json.RawMessage `json:"qty_increment"`
		QtyMin         json.RawMessage `json:"qty_min"`
		CostMin        json.RawMessage `json:"cost_min"`
		CostPrecision  int             `json:"cost_precision"`
		Status         string          `json:"status"`
	} `json:"pairs"`
}

func NewAdapter(symbol string, depth int, clock domain.Clock) (*Adapter, error) {
	if symbol == "" || depth < 10 || !validDepth(depth) || clock == nil {
		return nil, errors.New("Kraken adapter requires a symbol, supported depth, and clock")
	}
	return &Adapter{symbol: symbol, depth: depth, clock: clock}, nil
}

func validDepth(depth int) bool {
	switch depth {
	case 10, 25, 100, 500, 1000:
		return true
	}
	return false
}

func (adapter *Adapter) Current() (domain.Revision, bool) {
	adapter.mu.RLock()
	defer adapter.mu.RUnlock()
	if adapter.book == nil {
		return domain.Revision{}, false
	}
	return adapter.book.Current(), true
}

func (adapter *Adapter) Instrument() (domain.Instrument, bool) {
	adapter.mu.RLock()
	defer adapter.mu.RUnlock()
	if adapter.instrument == nil {
		return domain.Instrument{}, false
	}
	return *adapter.instrument, true
}

func (adapter *Adapter) Invalidate(reason string) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.book != nil {
		adapter.book.Invalidate(reason)
	}
}

func (adapter *Adapter) CheckLiveness(timeout time.Duration) bool {
	adapter.mu.RLock()
	defer adapter.mu.RUnlock()
	if adapter.book == nil {
		return false
	}
	return adapter.book.CheckLiveness(timeout)
}

func (adapter *Adapter) Handle(frame []byte) (HandleResult, error) {
	if len(frame) == 0 {
		return HandleResult{}, errors.New("empty WebSocket frame")
	}
	var message wireMessage
	if err := json.Unmarshal(frame, &message); err != nil {
		adapter.Invalidate("malformed_frame")
		return HandleResult{}, fmt.Errorf("decode Kraken frame: %w", err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if message.Success != nil && !*message.Success {
		if adapter.book != nil {
			adapter.book.Invalidate("subscription_failed")
		}
		return HandleResult{}, fmt.Errorf("Kraken subscription failed: %s", message.Error)
	}
	if adapter.book != nil {
		adapter.book.ObserveFeed()
	}
	switch message.Channel {
	case "instrument":
		return adapter.handleInstrument(message)
	case "book":
		return adapter.handleBook(message)
	case "heartbeat", "status":
		return HandleResult{}, nil
	default:
		if message.Method == "subscribe" || message.Method == "unsubscribe" {
			return HandleResult{}, nil
		}
		return HandleResult{}, fmt.Errorf("unexpected Kraken channel %q", message.Channel)
	}
}

func (adapter *Adapter) handleInstrument(message wireMessage) (HandleResult, error) {
	if message.Type != "snapshot" && message.Type != "update" {
		return HandleResult{}, errors.New("unsupported instrument message type")
	}
	var data wireInstrument
	if err := json.Unmarshal(message.Data, &data); err != nil {
		return HandleResult{}, fmt.Errorf("decode instrument: %w", err)
	}
	for _, pair := range data.Pairs {
		if pair.Symbol != adapter.symbol {
			continue
		}
		price, _, err := parseDecimal(pair.PriceIncrement)
		if err != nil {
			return HandleResult{}, fmt.Errorf("price increment: %w", err)
		}
		quantity, _, err := parseDecimal(pair.QtyIncrement)
		if err != nil {
			return HandleResult{}, fmt.Errorf("quantity increment: %w", err)
		}
		minimumQuantity, _, err := parseDecimal(pair.QtyMin)
		if err != nil {
			return HandleResult{}, fmt.Errorf("minimum quantity: %w", err)
		}
		minimumCost, _, err := parseDecimal(pair.CostMin)
		if err != nil {
			return HandleResult{}, fmt.Errorf("minimum cost: %w", err)
		}
		instrument := domain.Instrument{
			Symbol: pair.Symbol, BaseCurrency: pair.Base, QuoteCurrency: pair.Quote,
			PriceIncrement: price, QuantityIncrement: quantity,
			MinimumQuantity: minimumQuantity, MinimumCost: minimumCost,
			QuoteScale: pair.CostPrecision, Status: pair.Status,
		}
		if err := instrument.Validate(); err != nil {
			if adapter.book != nil {
				adapter.book.Invalidate("unsupported_instrument")
			}
			return HandleResult{}, err
		}
		if adapter.instrument != nil && equalInstrument(*adapter.instrument, instrument) {
			return HandleResult{InstrumentReady: true}, nil
		}
		changed := adapter.instrument != nil
		if adapter.book != nil {
			adapter.book.Invalidate("instrument_changed")
		}
		book, err := domain.NewBook(instrument, adapter.depth, domain.Live, adapter.clock)
		if err != nil {
			return HandleResult{}, err
		}
		adapter.instrument = &instrument
		adapter.book = book
		adapter.asks, adapter.bids = nil, nil
		return HandleResult{InstrumentReady: true, MetadataChanged: changed}, nil
	}
	if message.Type == "snapshot" && adapter.instrument == nil {
		return HandleResult{}, fmt.Errorf("symbol %s is absent from instrument snapshot", adapter.symbol)
	}
	return HandleResult{}, nil
}

func equalInstrument(a, b domain.Instrument) bool {
	return a.Symbol == b.Symbol && a.BaseCurrency == b.BaseCurrency && a.QuoteCurrency == b.QuoteCurrency &&
		a.PriceIncrement.Compare(b.PriceIncrement) == 0 && a.QuantityIncrement.Compare(b.QuantityIncrement) == 0 &&
		a.MinimumQuantity.Compare(b.MinimumQuantity) == 0 && a.MinimumCost.Compare(b.MinimumCost) == 0 &&
		a.QuoteScale == b.QuoteScale && a.Status == b.Status
}

func (adapter *Adapter) handleBook(message wireMessage) (HandleResult, error) {
	if adapter.book == nil || adapter.instrument == nil {
		return HandleResult{}, errors.New("book frame arrived before instrument metadata")
	}
	if message.Type != "snapshot" && message.Type != "update" {
		adapter.book.Invalidate("malformed_book")
		return HandleResult{}, errors.New("unsupported book message type")
	}
	var entries []wireBook
	if err := json.Unmarshal(message.Data, &entries); err != nil || len(entries) == 0 {
		adapter.book.Invalidate("malformed_book")
		return HandleResult{}, errors.New("book data must be a nonempty array")
	}
	for _, entry := range entries {
		if entry.Symbol != adapter.symbol {
			adapter.book.Invalidate("wrong_symbol")
			return HandleResult{}, fmt.Errorf("book symbol %q does not match %q", entry.Symbol, adapter.symbol)
		}
		if err := adapter.applyEntry(message.Type, entry); err != nil {
			adapter.book.Invalidate("book_checksum_or_decode_failed")
			return HandleResult{}, err
		}
	}
	return HandleResult{BookChanged: true}, nil
}

func (adapter *Adapter) applyEntry(kind string, entry wireBook) error {
	if len(entry.Checksum) == 0 {
		return errors.New("book checksum is required")
	}
	expected, err := strconv.ParseUint(string(entry.Checksum), 10, 32)
	if err != nil {
		return fmt.Errorf("invalid book checksum: %w", err)
	}
	asks, bids := cloneLevels(adapter.asks), cloneLevels(adapter.bids)
	if kind == "snapshot" {
		asks, bids = map[string]rawLevel{}, map[string]rawLevel{}
	} else if adapter.book.Current().CheckEligible(domain.Live) != nil {
		return domain.ErrNotReady
	}
	changes := make([]domain.Change, 0, len(entry.Asks)+len(entry.Bids))
	for _, side := range []struct {
		wire []wireLevel
		out  map[string]rawLevel
		side domain.Side
	}{{entry.Asks, asks, domain.Ask}, {entry.Bids, bids, domain.Bid}} {
		for _, wire := range side.wire {
			price, priceText, err := parseDecimal(wire.Price)
			if err != nil {
				return err
			}
			quantity, quantityText, err := parseDecimal(wire.Qty)
			if err != nil {
				return err
			}
			key, err := price.ExactDecimal()
			if err != nil {
				return err
			}
			if kind == "snapshot" {
				if _, duplicate := side.out[key]; duplicate {
					return errors.New("duplicate snapshot price")
				}
				if quantity.Sign() == 0 {
					return errors.New("zero snapshot quantity")
				}
			}
			if quantity.Sign() == 0 {
				delete(side.out, key)
			} else {
				side.out[key] = rawLevel{price, quantity, priceText, quantityText}
			}
			changes = append(changes, domain.Change{Side: side.side, Price: price, Quantity: quantity})
		}
	}
	truncate(asks, domain.Ask, adapter.depth)
	truncate(bids, domain.Bid, adapter.depth)
	actual := checksum(asks, bids)
	if uint64(actual) != expected {
		return fmt.Errorf("book checksum mismatch: received %d, calculated %d", expected, actual)
	}
	if kind == "snapshot" {
		if err := adapter.book.ApplySnapshot(toLevels(asks, domain.Ask), toLevels(bids, domain.Bid), true); err != nil {
			return err
		}
	} else if err := adapter.book.ApplyUpdate(changes, true); err != nil {
		return err
	}
	adapter.asks, adapter.bids = asks, bids
	return nil
}

func cloneLevels(source map[string]rawLevel) map[string]rawLevel {
	copy := make(map[string]rawLevel, len(source))
	for key, level := range source {
		copy[key] = level
	}
	return copy
}

func ordered(levels map[string]rawLevel, side domain.Side) []rawLevel {
	result := make([]rawLevel, 0, len(levels))
	for _, level := range levels {
		result = append(result, level)
	}
	sort.Slice(result, func(i, j int) bool {
		compare := result[i].price.Compare(result[j].price)
		if side == domain.Ask {
			return compare < 0
		}
		return compare > 0
	})
	return result
}

func truncate(levels map[string]rawLevel, side domain.Side, depth int) {
	for index, level := range ordered(levels, side) {
		if index >= depth {
			key, _ := level.price.ExactDecimal()
			delete(levels, key)
		}
	}
}

func toLevels(levels map[string]rawLevel, side domain.Side) []domain.Level {
	orderedLevels := ordered(levels, side)
	result := make([]domain.Level, 0, len(orderedLevels))
	for _, level := range orderedLevels {
		result = append(result, domain.Level{Price: level.price, Quantity: level.quantity})
	}
	return result
}

func checksum(asks, bids map[string]rawLevel) uint32 {
	var builder strings.Builder
	for _, side := range []struct {
		levels map[string]rawLevel
		kind   domain.Side
	}{{asks, domain.Ask}, {bids, domain.Bid}} {
		for index, level := range ordered(side.levels, side.kind) {
			if index == 10 {
				break
			}
			builder.WriteString(checksumPart(level.priceText))
			builder.WriteString(checksumPart(level.quantityText))
		}
	}
	return crc32.ChecksumIEEE([]byte(builder.String()))
}

func checksumPart(decimal string) string {
	withoutPoint := strings.ReplaceAll(decimal, ".", "")
	trimmed := strings.TrimLeft(withoutPoint, "0")
	if trimmed == "" {
		return "0"
	}
	return trimmed
}

func parseDecimal(raw json.RawMessage) (value.Value, string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return value.Value{}, "", errors.New("missing decimal field")
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return value.Value{}, "", err
		}
		parsed, err := value.Parse(text)
		return parsed, text, err
	}
	text := string(raw)
	parsed, err := value.ParseJSONNumber(text)
	if err != nil {
		return value.Value{}, "", err
	}
	if strings.ContainsAny(text, "eE") {
		text, err = expandExponent(text)
	}
	return parsed, text, err
}

func expandExponent(input string) (string, error) {
	index := strings.IndexAny(input, "eE")
	mantissa := input[:index]
	exponent, err := strconv.Atoi(input[index+1:])
	if err != nil || exponent < -128 || exponent > 128 {
		return "", errors.New("invalid decimal exponent")
	}
	point := strings.IndexByte(mantissa, '.')
	if point < 0 {
		point = len(mantissa)
	}
	digits := strings.ReplaceAll(mantissa, ".", "")
	position := point + exponent
	switch {
	case position <= 0:
		return "0." + strings.Repeat("0", -position) + digits, nil
	case position >= len(digits):
		return digits + strings.Repeat("0", position-len(digits)), nil
	default:
		return digits[:position] + "." + digits[position:], nil
	}
}
