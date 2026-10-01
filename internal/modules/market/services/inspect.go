package services

import (
	"fmt"
	"time"

	"github.com/stupidprogrammer4/tidelab/internal/modules/market/domain"
)

type BookSequenceLoader interface {
	Load(path string) (domain.OfflineBookSequence, error)
}

type InspectOfflineBook struct {
	Loader BookSequenceLoader
}

func (service InspectOfflineBook) Run(path string) (domain.BookInspection, error) {
	sequence, err := service.Loader.Load(path)
	if err != nil {
		return domain.BookInspection{}, err
	}
	clock := &sequenceClock{now: sequence.StartAt}
	book, err := domain.NewBook(sequence.Instrument, sequence.Depth, domain.Offline, clock)
	if err != nil {
		return domain.BookInspection{}, err
	}
	if err := book.ApplySnapshot(sequence.SnapshotAsks, sequence.SnapshotBids, false); err != nil {
		return domain.BookInspection{}, fmt.Errorf("apply fixture snapshot: %w", err)
	}
	for index, update := range sequence.Updates {
		clock.now = sequence.StartAt.Add(update.Offset)
		if err := book.ApplyUpdate(update.Changes, false); err != nil {
			return domain.BookInspection{}, fmt.Errorf("apply fixture update %d: %w", index+1, err)
		}
	}
	revision := book.Current()
	if err := revision.CheckEligible(domain.Offline); err != nil {
		return domain.BookInspection{}, err
	}
	asks, err := views(revision.Asks())
	if err != nil {
		return domain.BookInspection{}, err
	}
	bids, err := views(revision.Bids())
	if err != nil {
		return domain.BookInspection{}, err
	}
	result := domain.BookInspection{
		Source: "synthetic_fixture", Provenance: sequence.Provenance,
		Symbol: revision.Instrument().Symbol, State: revision.State(),
		Revision: revision.Number(), Depth: revision.Depth(), Digest: revision.Digest(),
		Asks: asks, Bids: bids, AskDepthFull: revision.AskDepthFull(),
		BidDepthFull: revision.BidDepthFull(), ChecksumValid: revision.ChecksumValid(),
		FeedLive: revision.FeedLive(), LastBookUpdateAt: revision.LastBookUpdateAt(),
		LastFeedMessageAt: revision.LastFeedMessageAt(),
	}
	if len(asks) > 0 {
		result.BestAsk = &asks[0]
	}
	if len(bids) > 0 {
		result.BestBid = &bids[0]
	}
	return result, nil
}

type sequenceClock struct{ now time.Time }

func (clock *sequenceClock) Now() time.Time { return clock.now }

func views(levels []domain.Level) ([]domain.LevelView, error) {
	result := make([]domain.LevelView, 0, len(levels))
	for _, level := range levels {
		price, err := level.Price.ExactDecimal()
		if err != nil {
			return nil, err
		}
		quantity, err := level.Quantity.ExactDecimal()
		if err != nil {
			return nil, err
		}
		result = append(result, domain.LevelView{Price: price, Quantity: quantity})
	}
	return result, nil
}
