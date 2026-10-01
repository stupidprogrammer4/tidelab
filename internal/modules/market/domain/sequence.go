package domain

import "time"

// OfflineBookSequence is a synthetic, ordered book timeline for local inspection.
type OfflineBookSequence struct {
	Provenance   string
	Instrument   Instrument
	Depth        int
	StartAt      time.Time
	SnapshotAsks []Level
	SnapshotBids []Level
	Updates      []TimedUpdate
}

type TimedUpdate struct {
	Offset  time.Duration
	Changes []Change
}

type LevelView struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}

type BookInspection struct {
	Source            string      `json:"source"`
	Provenance        string      `json:"provenance"`
	Symbol            string      `json:"symbol"`
	State             State       `json:"state"`
	Revision          uint64      `json:"revision"`
	Depth             int         `json:"subscribed_depth"`
	Digest            string      `json:"book_digest"`
	BestAsk           *LevelView  `json:"best_ask"`
	BestBid           *LevelView  `json:"best_bid"`
	Asks              []LevelView `json:"asks"`
	Bids              []LevelView `json:"bids"`
	AskDepthFull      bool        `json:"ask_depth_full"`
	BidDepthFull      bool        `json:"bid_depth_full"`
	ChecksumValid     bool        `json:"checksum_valid"`
	FeedLive          bool        `json:"feed_live"`
	LastBookUpdateAt  time.Time   `json:"last_book_update_at"`
	LastFeedMessageAt time.Time   `json:"last_feed_message_at"`
}
