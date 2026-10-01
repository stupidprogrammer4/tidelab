package services

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stupidprogrammer4/tidelab/internal/modules/market/domain"
	"github.com/stupidprogrammer4/tidelab/internal/modules/market/infra"
)

func TestInspectSyntheticFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "testdata", "synthetic-market.json")
	report, err := (InspectOfflineBook{Loader: infra.FileFixtureLoader{}}).Run(path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Source != "synthetic_fixture" || report.State != domain.Valid || report.Revision != 2 || report.Symbol != "TEST/USD" {
		t.Fatalf("report identity = %+v", report)
	}
	if report.Digest != "4f3927a97e98139c95807ab81c89dde2b0519654ddf0ac3dee9c789b7091c657" {
		t.Fatalf("digest = %s", report.Digest)
	}
	if report.BestAsk == nil || report.BestAsk.Price != "100" || report.BestAsk.Quantity != "0.8" {
		t.Fatalf("best ask = %+v", report.BestAsk)
	}
	if report.BestBid == nil || report.BestBid.Price != "98" || report.BestBid.Quantity != "2" {
		t.Fatalf("best bid = %+v", report.BestBid)
	}
	if report.FeedLive || report.ChecksumValid {
		t.Fatal("synthetic fixture was presented as verified live data")
	}
	if got := report.LastBookUpdateAt.Format("2006-01-02T15:04:05Z"); got != "2026-09-30T06:00:01Z" {
		t.Fatalf("last update time = %s", got)
	}
}

func TestInspectRejectsInvalidatedFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "testdata", "invalid-book.json")
	_, err := (InspectOfflineBook{Loader: infra.FileFixtureLoader{}}).Run(path)
	if err == nil || !strings.Contains(err.Error(), "book is crossed or locked") {
		t.Fatalf("invalid fixture error = %v", err)
	}
}
