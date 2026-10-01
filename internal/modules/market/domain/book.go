package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type State string

const (
	Initializing State = "initializing"
	Valid        State = "valid"
	Invalid      State = "invalid"
	Closed       State = "closed"
)

type SourceMode string

const (
	Offline SourceMode = "offline"
	Live    SourceMode = "live"
)

var (
	ErrNotReady    = errors.New("market_not_ready: waiting for a validated snapshot")
	ErrUnavailable = errors.New("market_unavailable: book is invalid, closed, or disconnected")
)

type Clock interface {
	Now() time.Time
}

// Revision is immutable after publication. Slice getters return independent copies.
type Revision struct {
	number            uint64
	state             State
	mode              SourceMode
	reason            string
	depth             int
	instrument        Instrument
	asks              []Level
	bids              []Level
	digest            string
	feedLive          bool
	checksumValid     bool
	lastBookUpdateAt  time.Time
	lastFeedMessageAt time.Time
}

func (revision Revision) Number() uint64               { return revision.number }
func (revision Revision) State() State                 { return revision.state }
func (revision Revision) Mode() SourceMode             { return revision.mode }
func (revision Revision) Reason() string               { return revision.reason }
func (revision Revision) Depth() int                   { return revision.depth }
func (revision Revision) Instrument() Instrument       { return revision.instrument }
func (revision Revision) Digest() string               { return revision.digest }
func (revision Revision) FeedLive() bool               { return revision.feedLive }
func (revision Revision) ChecksumValid() bool          { return revision.checksumValid }
func (revision Revision) LastBookUpdateAt() time.Time  { return revision.lastBookUpdateAt }
func (revision Revision) LastFeedMessageAt() time.Time { return revision.lastFeedMessageAt }
func (revision Revision) Asks() []Level                { return append([]Level(nil), revision.asks...) }
func (revision Revision) Bids() []Level                { return append([]Level(nil), revision.bids...) }
func (revision Revision) AskDepthFull() bool           { return len(revision.asks) == revision.depth }
func (revision Revision) BidDepthFull() bool           { return len(revision.bids) == revision.depth }

func (revision Revision) BestAsk() (Level, bool) {
	if len(revision.asks) == 0 {
		return Level{}, false
	}
	return revision.asks[0], true
}

func (revision Revision) BestBid() (Level, bool) {
	if len(revision.bids) == 0 {
		return Level{}, false
	}
	return revision.bids[0], true
}

func (revision Revision) CheckEligible(mode SourceMode) error {
	if revision.state == Initializing {
		return ErrNotReady
	}
	if revision.state != Valid {
		return ErrUnavailable
	}
	if mode != revision.mode {
		return ErrUnavailable
	}
	if mode == Live && (!revision.feedLive || !revision.checksumValid) {
		return ErrUnavailable
	}
	if mode != Live && mode != Offline {
		return errors.New("invalid source mode")
	}
	return nil
}

// Book serializes state transitions and publishes immutable revisions to readers.
type Book struct {
	mu         sync.Mutex
	current    atomic.Pointer[Revision]
	instrument Instrument
	depth      int
	mode       SourceMode
	clock      Clock
}

func NewBook(instrument Instrument, depth int, mode SourceMode, clock Clock) (*Book, error) {
	if err := instrument.Validate(); err != nil {
		return nil, err
	}
	if depth < 1 || depth > 1000 {
		return nil, errors.New("subscribed depth must be from 1 to 1000")
	}
	if mode != Offline && mode != Live {
		return nil, errors.New("invalid source mode")
	}
	if clock == nil {
		return nil, errors.New("clock is required")
	}
	book := &Book{instrument: instrument, depth: depth, mode: mode, clock: clock}
	book.current.Store(&Revision{state: Initializing, mode: mode, instrument: instrument, depth: depth})
	return book, nil
}

func (book *Book) Current() Revision { return *book.current.Load() }

// ApplySnapshot replaces the old levels only after full validation.
func (book *Book) ApplySnapshot(asks, bids []Level, checksumValid bool) error {
	book.mu.Lock()
	defer book.mu.Unlock()
	old := book.current.Load()
	if old.state == Closed {
		return ErrUnavailable
	}
	normalizedAsks, err := book.normalizeSnapshot(Ask, asks)
	if err != nil {
		book.invalidateLocked("malformed_snapshot")
		return err
	}
	normalizedBids, err := book.normalizeSnapshot(Bid, bids)
	if err != nil {
		book.invalidateLocked("malformed_snapshot")
		return err
	}
	if err := validateSpread(normalizedAsks, normalizedBids); err != nil {
		book.invalidateLocked("crossed_book")
		return err
	}
	digest, err := bookDigest(book.instrument.Symbol, book.depth, normalizedAsks, normalizedBids)
	if err != nil {
		book.invalidateLocked("malformed_snapshot")
		return err
	}
	now := book.clock.Now()
	book.current.Store(&Revision{
		number:            old.number + 1,
		state:             Valid,
		mode:              book.mode,
		depth:             book.depth,
		instrument:        book.instrument,
		asks:              normalizedAsks,
		bids:              normalizedBids,
		digest:            digest,
		feedLive:          book.mode == Live,
		checksumValid:     checksumValid,
		lastBookUpdateAt:  now,
		lastFeedMessageAt: now,
	})
	return nil
}

// ApplyUpdate processes repeated changes to one price in their original order.
func (book *Book) ApplyUpdate(changes []Change, checksumValid bool) error {
	book.mu.Lock()
	defer book.mu.Unlock()
	old := book.current.Load()
	if old.state != Valid {
		return ErrNotReady
	}
	if len(changes) == 0 {
		next := *old
		next.feedLive = book.mode == Live
		next.checksumValid = checksumValid
		next.lastFeedMessageAt = book.clock.Now()
		book.current.Store(&next)
		return nil
	}
	asks, err := levelsToMap(old.asks)
	if err != nil {
		book.invalidateLocked("corrupt_book_state")
		return err
	}
	bids, err := levelsToMap(old.bids)
	if err != nil {
		book.invalidateLocked("corrupt_book_state")
		return err
	}
	for _, change := range changes {
		if err := book.validateChange(change); err != nil {
			book.invalidateLocked("malformed_update")
			return err
		}
		price, err := change.Price.ExactDecimal()
		if err != nil {
			book.invalidateLocked("malformed_update")
			return err
		}
		levels := asks
		if change.Side == Bid {
			levels = bids
		}
		if change.Quantity.Sign() == 0 {
			delete(levels, price)
		} else {
			levels[price] = Level{Price: change.Price, Quantity: change.Quantity}
		}
	}
	normalizedAsks := sortedLevels(asks, Ask, book.depth)
	normalizedBids := sortedLevels(bids, Bid, book.depth)
	if err := validateSpread(normalizedAsks, normalizedBids); err != nil {
		book.invalidateLocked("crossed_book")
		return err
	}
	digest, err := bookDigest(book.instrument.Symbol, book.depth, normalizedAsks, normalizedBids)
	if err != nil {
		book.invalidateLocked("malformed_update")
		return err
	}
	now := book.clock.Now()
	book.current.Store(&Revision{
		number:            old.number + 1,
		state:             Valid,
		mode:              book.mode,
		depth:             book.depth,
		instrument:        book.instrument,
		asks:              normalizedAsks,
		bids:              normalizedBids,
		digest:            digest,
		feedLive:          book.mode == Live,
		checksumValid:     checksumValid,
		lastBookUpdateAt:  now,
		lastFeedMessageAt: now,
	})
	return nil
}

// ObserveFeed records liveness without implying a book change.
func (book *Book) ObserveFeed() {
	book.mu.Lock()
	defer book.mu.Unlock()
	old := book.current.Load()
	if old.state == Closed {
		return
	}
	next := *old
	next.feedLive = book.mode == Live
	next.lastFeedMessageAt = book.clock.Now()
	book.current.Store(&next)
}

func (book *Book) CheckLiveness(timeout time.Duration) bool {
	book.mu.Lock()
	defer book.mu.Unlock()
	old := book.current.Load()
	if book.mode != Live || old.state != Valid || !old.feedLive || timeout <= 0 {
		return false
	}
	if book.clock.Now().Sub(old.lastFeedMessageAt) <= timeout {
		return false
	}
	book.invalidateLocked("feed_timeout")
	return true
}

func (book *Book) Invalidate(reason string) {
	book.mu.Lock()
	defer book.mu.Unlock()
	if book.current.Load().state != Closed {
		book.invalidateLocked(reason)
	}
}

func (book *Book) Disconnect(reason string) { book.Invalidate(reason) }

func (book *Book) Close() {
	book.mu.Lock()
	defer book.mu.Unlock()
	old := book.current.Load()
	if old.state == Closed {
		return
	}
	next := *old
	next.number++
	next.state = Closed
	next.reason = "closed"
	next.feedLive = false
	book.current.Store(&next)
}

func (book *Book) invalidateLocked(reason string) {
	old := book.current.Load()
	next := *old
	next.number++
	next.state = Invalid
	next.reason = reason
	next.feedLive = false
	next.checksumValid = false
	book.current.Store(&next)
}

func (book *Book) normalizeSnapshot(side Side, input []Level) ([]Level, error) {
	levels := make(map[string]Level, len(input))
	for _, level := range input {
		if err := book.validateLevel(level, false); err != nil {
			return nil, err
		}
		price, err := level.Price.ExactDecimal()
		if err != nil {
			return nil, err
		}
		if previous, exists := levels[price]; exists {
			level.Quantity = previous.Quantity.Add(level.Quantity)
		}
		levels[price] = level
	}
	return sortedLevels(levels, side, book.depth), nil
}

func (book *Book) validateChange(change Change) error {
	if change.Side != Ask && change.Side != Bid {
		return errors.New("change side must be ask or bid")
	}
	return book.validateLevel(Level{Price: change.Price, Quantity: change.Quantity}, true)
}

func (book *Book) validateLevel(level Level, allowZeroQuantity bool) error {
	if level.Price.Sign() <= 0 || level.Quantity.Sign() < 0 || (!allowZeroQuantity && level.Quantity.Sign() == 0) {
		return errors.New("level price and quantity must be positive, except a zero deletion quantity")
	}
	if err := validateFiniteDecimal(level.Price); err != nil {
		return fmt.Errorf("level price: %w", err)
	}
	if err := validateFiniteDecimal(level.Quantity); err != nil {
		return fmt.Errorf("level quantity: %w", err)
	}
	aligned, err := level.Price.FloorToIncrement(book.instrument.PriceIncrement)
	if err != nil || aligned.Compare(level.Price) != 0 {
		return errors.New("level price is not aligned to the instrument increment")
	}
	return nil
}

func levelsToMap(input []Level) (map[string]Level, error) {
	levels := make(map[string]Level, len(input))
	for _, level := range input {
		price, err := level.Price.ExactDecimal()
		if err != nil {
			return nil, err
		}
		levels[price] = level
	}
	return levels, nil
}

func sortedLevels(input map[string]Level, side Side, depth int) []Level {
	levels := make([]Level, 0, len(input))
	for _, level := range input {
		levels = append(levels, level)
	}
	sort.Slice(levels, func(i, j int) bool {
		comparison := levels[i].Price.Compare(levels[j].Price)
		if side == Ask {
			return comparison < 0
		}
		return comparison > 0
	})
	if len(levels) > depth {
		levels = levels[:depth]
	}
	return levels
}

func validateSpread(asks, bids []Level) error {
	if len(asks) != 0 && len(bids) != 0 && bids[0].Price.Compare(asks[0].Price) >= 0 {
		return errors.New("book is crossed or locked")
	}
	return nil
}

func bookDigest(symbol string, depth int, asks, bids []Level) (string, error) {
	var canonical strings.Builder
	canonical.WriteString("tidelab-book-v1\n")
	canonical.WriteString("symbol=")
	canonical.WriteString(symbol)
	canonical.WriteByte('\n')
	canonical.WriteString("depth=")
	canonical.WriteString(strconv.Itoa(depth))
	canonical.WriteString("\nasks\n")
	if err := writeLevels(&canonical, asks); err != nil {
		return "", err
	}
	canonical.WriteString("bids\n")
	if err := writeLevels(&canonical, bids); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(digest[:]), nil
}

func writeLevels(builder *strings.Builder, levels []Level) error {
	for _, level := range levels {
		price, err := level.Price.ExactDecimal()
		if err != nil {
			return err
		}
		quantity, err := level.Quantity.ExactDecimal()
		if err != nil {
			return err
		}
		builder.WriteString(price)
		builder.WriteByte(':')
		builder.WriteString(quantity)
		builder.WriteByte('\n')
	}
	return nil
}
