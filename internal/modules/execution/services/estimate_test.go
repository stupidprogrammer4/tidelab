package services

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	execution "github.com/stupidprogrammer4/tidelab/internal/modules/execution/domain"
	marketinfra "github.com/stupidprogrammer4/tidelab/internal/modules/market/infra"
	marketservices "github.com/stupidprogrammer4/tidelab/internal/modules/market/services"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "..", "..", "testdata", name)
}

func offlineEstimator() EstimateOffline {
	return EstimateOffline{Source: marketservices.OfflineBookService{Loader: marketinfra.FileFixtureLoader{}}}
}

func TestEstimateOfflineFixtureReport(t *testing.T) {
	estimator := offlineEstimator()
	input := Input{Side: "buy", BaseQuantity: "2.5", FeeBps: "10"}
	first, err := estimator.Run(fixture("execution-book.json"), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := estimator.Run(fixture("execution-book.json"), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.InputFingerprint == "" || first.InputFingerprint != second.InputFingerprint || first.BookDigest != second.BookDigest {
		t.Fatal("fixture estimate fingerprint is not deterministic")
	}
	if first.Source != "synthetic_fixture" || first.Symbol != "TEST/USD" || first.DataQuality.FeedLive || first.DataQuality.ChecksumValid {
		t.Fatalf("incorrect source or data quality: %+v", first)
	}
	if first.Result.Status != execution.Filled || first.Result.FilledBaseQuantity != "2.5" || first.Result.GrossQuote != "251.5" || first.Result.QuoteDebit != "251.7515" || first.Result.VWAP == nil || *first.Result.VWAP != "100.6" {
		t.Fatalf("incorrect estimate: %+v", first.Result)
	}
	if len(first.Result.Fills) != 2 || first.Result.Fills[0].Price != "100" || first.Result.Fills[1].BaseQuantity != "1.5" {
		t.Fatalf("incorrect fill levels: %+v", first.Result.Fills)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"gross_quote":"251.5"`) || !strings.Contains(string(encoded), `"fee_bps":"10"`) {
		t.Fatalf("financial JSON fields are not decimal strings: %s", encoded)
	}
	changed, err := estimator.Run(fixture("execution-book.json"), Input{Side: "buy", BaseQuantity: "2.5", FeeBps: "11"})
	if err != nil {
		t.Fatal(err)
	}
	if first.InputFingerprint == changed.InputFingerprint || first.BookDigest != changed.BookDigest {
		t.Fatal("fee change did not alter input fingerprint independently of book")
	}
	loaded, err := (marketservices.OfflineBookService{Loader: marketinfra.FileFixtureLoader{}}).Load(fixture("execution-book.json"))
	if err != nil {
		t.Fatal(err)
	}
	instrument := loaded.Revision.Instrument()
	instrument.QuoteScale++
	changedMetadata, err := inputFingerprint(first.BookDigest, instrument, first.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	if first.InputFingerprint == changedMetadata {
		t.Fatal("quote scale change did not alter input fingerprint")
	}
}

func TestEstimateOfflineRejectsInvalidRequestsAndBooks(t *testing.T) {
	estimator := offlineEstimator()
	for _, input := range []Input{
		{Side: "buy", BaseQuantity: "1"},
		{Side: "sell", QuoteBudget: "100", FeeBps: "10"},
		{Side: "buy", BaseQuantity: "0.00015", FeeBps: "10"},
	} {
		_, err := estimator.Run(fixture("execution-book.json"), input)
		var validation *execution.ValidationError
		if !errors.As(err, &validation) {
			t.Errorf("input %+v returned %v, want validation error", input, err)
		}
	}
	_, err := estimator.Run(fixture("invalid-book.json"), Input{Side: "buy", BaseQuantity: "1", FeeBps: "10"})
	if err == nil || !strings.Contains(err.Error(), "book is crossed or locked") {
		t.Fatalf("invalid fixture returned %v", err)
	}
}
