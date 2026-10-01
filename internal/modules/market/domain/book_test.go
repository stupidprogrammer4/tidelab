package domain

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stupidprogrammer4/tidelab/internal/value"
)

type testClock struct{ nanos atomic.Int64 }

func newTestClock() *testClock {
	clock := &testClock{}
	clock.nanos.Store(time.Date(2026, 9, 30, 6, 0, 0, 0, time.UTC).UnixNano())
	return clock
}

func (clock *testClock) Now() time.Time                 { return time.Unix(0, clock.nanos.Load()).UTC() }
func (clock *testClock) Advance(duration time.Duration) { clock.nanos.Add(int64(duration)) }

func decimal(t *testing.T, input string) value.Value {
	t.Helper()
	parsed, err := value.Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func instrument(t *testing.T) Instrument {
	t.Helper()
	return Instrument{
		Symbol: "TEST/USD", BaseCurrency: "TEST", QuoteCurrency: "USD",
		PriceIncrement: decimal(t, "0.01"), QuantityIncrement: decimal(t, "0.0001"),
		MinimumQuantity: decimal(t, "0.0001"), MinimumCost: decimal(t, "0.0001"),
		QuoteScale: 4, Status: "online",
	}
}

func level(t *testing.T, price, quantity string) Level {
	t.Helper()
	return Level{Price: decimal(t, price), Quantity: decimal(t, quantity)}
}

func change(t *testing.T, side Side, price, quantity string) Change {
	t.Helper()
	return Change{Side: side, Price: decimal(t, price), Quantity: decimal(t, quantity)}
}

func newBook(t *testing.T, mode SourceMode) (*Book, *testClock) {
	t.Helper()
	clock := newTestClock()
	book, err := NewBook(instrument(t), 2, mode, clock)
	if err != nil {
		t.Fatal(err)
	}
	return book, clock
}

func snapshot(t *testing.T, book *Book) {
	t.Helper()
	if err := book.ApplySnapshot(
		[]Level{level(t, "100", "1"), level(t, "101", "2")},
		[]Level{level(t, "99", "1"), level(t, "98", "2")}, false,
	); err != nil {
		t.Fatal(err)
	}
}

func TestBookAppliesOrderedChangesAndDepth(t *testing.T) {
	book, clock := newBook(t, Offline)
	if err := book.Current().CheckEligible(Offline); !errors.Is(err, ErrNotReady) {
		t.Fatalf("initial eligibility = %v", err)
	}
	if err := book.ApplyUpdate([]Change{change(t, Ask, "100", "2")}, false); !errors.Is(err, ErrNotReady) {
		t.Fatalf("pre-snapshot update = %v", err)
	}
	snapshot(t, book)
	clock.Advance(time.Second)
	changes := []Change{
		change(t, Ask, "100", "0.5"),
		change(t, Ask, "100", "0.8"),
		change(t, Ask, "101", "0"),
		change(t, Ask, "102", "2"),
		change(t, Ask, "103", "9"),
		change(t, Bid, "99", "0"),
		change(t, Bid, "97", "3"),
		change(t, Bid, "96", "4"),
	}
	if err := book.ApplyUpdate(changes, false); err != nil {
		t.Fatal(err)
	}
	revision := book.Current()
	if revision.State() != Valid || revision.Number() != 2 {
		t.Fatalf("state = %s, revision = %d", revision.State(), revision.Number())
	}
	if got := revision.Digest(); got != "4f3927a97e98139c95807ab81c89dde2b0519654ddf0ac3dee9c789b7091c657" {
		t.Fatalf("digest = %s", got)
	}
	if got := revision.Asks(); len(got) != 2 || exact(t, got[0].Price) != "100" || exact(t, got[0].Quantity) != "0.8" || exact(t, got[1].Price) != "102" {
		t.Fatalf("asks = %+v", got)
	}
	if got := revision.Bids(); len(got) != 2 || exact(t, got[0].Price) != "98" || exact(t, got[0].Quantity) != "2" || exact(t, got[1].Price) != "97" {
		t.Fatalf("bids = %+v", got)
	}
	if !revision.AskDepthFull() || !revision.BidDepthFull() {
		t.Fatal("subscribed depth should be full on both sides")
	}
	if err := revision.CheckEligible(Offline); err != nil {
		t.Fatal(err)
	}
	if err := revision.CheckEligible(Live); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("synthetic book was eligible as live: %v", err)
	}
}

func TestSnapshotAggregatesDuplicatePricesAndReplacesBook(t *testing.T) {
	book, _ := newBook(t, Offline)
	if err := book.ApplySnapshot(
		[]Level{level(t, "100", "0.2"), level(t, "100.00", "0.3"), level(t, "101", "1")},
		[]Level{level(t, "99", "1")}, false,
	); err != nil {
		t.Fatal(err)
	}
	first := book.Current()
	if got := exact(t, first.Asks()[0].Quantity); got != "0.5" {
		t.Fatalf("aggregated quantity = %s", got)
	}
	if err := book.ApplySnapshot([]Level{level(t, "105", "2")}, nil, false); err != nil {
		t.Fatal(err)
	}
	second := book.Current()
	if len(second.Bids()) != 0 || exact(t, second.Asks()[0].Price) != "105" {
		t.Fatalf("replacement snapshot = %+v", second)
	}
	if len(first.Bids()) != 1 || exact(t, first.Asks()[0].Price) != "100" {
		t.Fatal("previous revision changed after snapshot replacement")
	}
}

func TestInvalidSnapshotNeedsFreshResynchronization(t *testing.T) {
	book, _ := newBook(t, Offline)
	if err := book.ApplySnapshot([]Level{level(t, "99", "1")}, []Level{level(t, "99", "1")}, false); err == nil {
		t.Fatal("locked snapshot was accepted")
	}
	if book.Current().State() != Invalid || book.Current().Reason() != "crossed_book" {
		t.Fatalf("invalid snapshot state = %s, reason = %s", book.Current().State(), book.Current().Reason())
	}
	if err := book.Current().CheckEligible(Offline); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid snapshot was eligible: %v", err)
	}
	if err := book.ApplySnapshot(nil, nil, false); err != nil {
		t.Fatalf("empty but valid resynchronization failed: %v", err)
	}
	revision := book.Current()
	if revision.State() != Valid {
		t.Fatalf("state after resynchronization = %s", revision.State())
	}
	if _, exists := revision.BestAsk(); exists {
		t.Fatal("empty book invented an ask")
	}
	if _, exists := revision.BestBid(); exists {
		t.Fatal("empty book invented a bid")
	}
}

func TestNonTerminatingRationalsCannotEnterMarketData(t *testing.T) {
	third, err := decimal(t, "1").Divide(decimal(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	metadata := instrument(t)
	metadata.PriceIncrement = third
	if _, err := NewBook(metadata, 2, Offline, newTestClock()); err == nil {
		t.Fatal("repeating price increment was accepted")
	}
	book, _ := newBook(t, Offline)
	if err := book.ApplySnapshot([]Level{{Price: third, Quantity: decimal(t, "1")}}, nil, false); err == nil {
		t.Fatal("repeating level price was accepted")
	}
	if book.Current().State() != Invalid {
		t.Fatal("invalid snapshot did not invalidate the book")
	}
}

func TestMalformedUpdateInvalidatesWithoutPartialPublication(t *testing.T) {
	book, _ := newBook(t, Offline)
	snapshot(t, book)
	before := book.Current()
	err := book.ApplyUpdate([]Change{
		change(t, Ask, "100", "0.5"),
		change(t, Ask, "100.001", "1"),
	}, false)
	if err == nil {
		t.Fatal("misaligned price was accepted")
	}
	after := book.Current()
	if after.State() != Invalid || after.Reason() != "malformed_update" {
		t.Fatalf("state = %s, reason = %s", after.State(), after.Reason())
	}
	if after.Digest() != before.Digest() || exact(t, after.Asks()[0].Quantity) != "1" {
		t.Fatal("partial update was published")
	}
	if err := after.CheckEligible(Offline); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid book was eligible: %v", err)
	}
	if err := book.ApplyUpdate([]Change{change(t, Ask, "100", "0.5")}, false); !errors.Is(err, ErrNotReady) {
		t.Fatalf("update after invalidation = %v", err)
	}
	snapshot(t, book)
	if err := book.Current().CheckEligible(Offline); err != nil {
		t.Fatalf("fresh snapshot did not restore eligibility: %v", err)
	}
}

func TestEmptyUpdateChangesFeedTimeOnly(t *testing.T) {
	book, clock := newBook(t, Offline)
	snapshot(t, book)
	before := book.Current()
	clock.Advance(time.Second)
	if err := book.ApplyUpdate(nil, false); err != nil {
		t.Fatal(err)
	}
	after := book.Current()
	if after.Number() != before.Number() || after.Digest() != before.Digest() || !after.LastBookUpdateAt().Equal(before.LastBookUpdateAt()) {
		t.Fatal("empty update changed book revision")
	}
	if !after.LastFeedMessageAt().After(before.LastFeedMessageAt()) {
		t.Fatal("empty update did not advance feed time")
	}
}

func TestCrossedBookDisconnectAndLiveness(t *testing.T) {
	book, clock := newBook(t, Live)
	if err := book.ApplySnapshot([]Level{level(t, "100", "1")}, []Level{level(t, "99", "1")}, true); err != nil {
		t.Fatal(err)
	}
	if err := book.Current().CheckEligible(Offline); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("live book was eligible through offline mode: %v", err)
	}
	clock.Advance(10 * time.Second)
	book.ObserveFeed()
	clock.Advance(10 * time.Second)
	if book.CheckLiveness(15 * time.Second) {
		t.Fatal("quiet book with recent feed was marked stale")
	}
	clock.Advance(6 * time.Second)
	if !book.CheckLiveness(15 * time.Second) {
		t.Fatal("feed timeout did not invalidate book")
	}
	if err := book.Current().CheckEligible(Live); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stale live book was eligible: %v", err)
	}
	if err := book.ApplySnapshot([]Level{level(t, "100", "1")}, []Level{level(t, "99", "1")}, true); err != nil {
		t.Fatal(err)
	}
	if err := book.ApplyUpdate([]Change{change(t, Bid, "100", "1")}, true); err == nil {
		t.Fatal("crossed update was accepted")
	}
	if book.Current().Reason() != "crossed_book" {
		t.Fatalf("reason = %s", book.Current().Reason())
	}
	book.Disconnect("network_lost")
	if book.Current().FeedLive() || book.Current().State() != Invalid {
		t.Fatal("disconnect did not invalidate live book")
	}
	book.Close()
	if book.Current().State() != Closed {
		t.Fatal("close did not publish closed state")
	}
	if err := book.ApplySnapshot(nil, nil, true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed book accepted snapshot: %v", err)
	}
}

func TestPublishedRevisionCannotBeMutatedByCallers(t *testing.T) {
	book, _ := newBook(t, Offline)
	asks := []Level{level(t, "100", "1")}
	bids := []Level{level(t, "99", "1")}
	if err := book.ApplySnapshot(asks, bids, false); err != nil {
		t.Fatal(err)
	}
	revision := book.Current()
	asks[0] = level(t, "200", "9")
	bids[0] = level(t, "50", "9")
	returned := revision.Asks()
	returned[0] = level(t, "300", "9")
	if got := exact(t, book.Current().Asks()[0].Price); got != "100" {
		t.Fatalf("published ask mutated to %s", got)
	}
	if got := exact(t, book.Current().Bids()[0].Price); got != "99" {
		t.Fatalf("published bid mutated to %s", got)
	}
}

func TestConcurrentReadersSeeCompleteRevisions(t *testing.T) {
	book, _ := newBook(t, Offline)
	snapshot(t, book)
	var group sync.WaitGroup
	var broken atomic.Bool
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 200 {
				revision := book.Current()
				asks := revision.Asks()
				bids := revision.Bids()
				if revision.State() != Valid || len(asks) != 2 || len(bids) != 2 || asks[0].Price.Compare(bids[0].Price) <= 0 {
					broken.Store(true)
					return
				}
			}
		}()
	}
	for range 100 {
		if err := book.ApplyUpdate([]Change{change(t, Ask, "100", "0.5"), change(t, Ask, "100", "1")}, false); err != nil {
			t.Fatal(err)
		}
	}
	group.Wait()
	if broken.Load() {
		t.Fatal("reader observed an incomplete revision")
	}
}

func exact(t *testing.T, number value.Value) string {
	t.Helper()
	result, err := number.ExactDecimal()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func FuzzBookUpdateNeverPublishesCrossedLevels(f *testing.F) {
	f.Add("100", "0.5")
	f.Add("99", "1")
	f.Add("100.001", "1")
	f.Add("", "1")
	f.Fuzz(func(t *testing.T, priceText, quantityText string) {
		price, priceErr := value.Parse(priceText)
		quantity, quantityErr := value.Parse(quantityText)
		if priceErr != nil || quantityErr != nil {
			return
		}
		book, _ := newBook(t, Offline)
		snapshot(t, book)
		_ = book.ApplyUpdate([]Change{{Side: Ask, Price: price, Quantity: quantity}}, false)
		revision := book.Current()
		if revision.State() != Valid {
			return
		}
		ask, hasAsk := revision.BestAsk()
		bid, hasBid := revision.BestBid()
		if hasAsk && hasBid && bid.Price.Compare(ask.Price) >= 0 {
			t.Fatal("published crossed book")
		}
		if revision.Digest() == "" {
			t.Fatal("valid book lacks a digest")
		}
	})
}
