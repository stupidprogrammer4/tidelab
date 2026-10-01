package kraken

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stupidprogrammer4/tidelab/internal/modules/market/domain"
	"github.com/stupidprogrammer4/tidelab/internal/value"
)

type testClock struct{ now time.Time }

func (clock *testClock) Now() time.Time { return clock.now }

func instrumentFrame() []byte {
	return []byte(`{"channel":"instrument","type":"snapshot","data":{"pairs":[{"symbol":"BTC/USD","base":"BTC","quote":"USD","price_increment":0.1,"qty_increment":0.00000001,"qty_min":0.00000001,"cost_min":0.1,"cost_precision":8,"status":"online"}]}}`)
}

// Numeric data and expected CRC32 come from Kraken's WebSocket v2 book checksum guide:
// https://docs.kraken.com/exchange/guides/websockets/book-checksum-v2
func TestOfficialChecksumVectorAndExactTokens(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	adapter, err := NewAdapter("BTC/USD", 10, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Handle(instrumentFrame()); err != nil {
		t.Fatal(err)
	}
	frame := []byte(`{"channel":"book","type":"snapshot","data":[{"symbol":"BTC/USD","bids":[{"price":"45283.5","qty":"0.10000000"},{"price":"45283.4","qty":"1.54582015"},{"price":"45282.1","qty":"0.10000000"},{"price":"45281.0","qty":"0.10000000"},{"price":"45280.3","qty":"1.54592586"},{"price":"45279.0","qty":"0.07990000"},{"price":"45277.6","qty":"0.03310103"},{"price":"45277.5","qty":"0.30000000"},{"price":"45277.3","qty":"1.54602737"},{"price":"45276.6","qty":"0.15445238"}],"asks":[{"price":"45285.2","qty":"0.00100000"},{"price":"45286.4","qty":"1.54571953"},{"price":"45286.6","qty":"1.54571109"},{"price":"45289.6","qty":"1.54560911"},{"price":"45290.2","qty":"0.15890660"},{"price":"45291.8","qty":"1.54553491"},{"price":"45294.7","qty":"0.04454749"},{"price":"45296.1","qty":"0.35380000"},{"price":"45297.5","qty":"0.09945542"},{"price":"45299.5","qty":"0.18772827"}],"checksum":3310070434}]}`)
	result, err := adapter.Handle(frame)
	if err != nil || !result.BookChanged {
		t.Fatalf("official checksum frame: %+v, %v", result, err)
	}
	revision, ok := adapter.Current()
	if !ok || revision.CheckEligible(domain.Live) != nil {
		t.Fatal("official snapshot was not eligible")
	}
	if got, _ := revision.Asks()[0].Quantity.ExactDecimal(); got != "0.001" {
		t.Fatalf("exact quantity = %s", got)
	}
	bad := []byte(strings.Replace(string(frame), "3310070434", "3310070435", 1))
	if _, err := adapter.Handle(bad); err == nil {
		t.Fatal("wrong checksum was accepted")
	}
	revision, _ = adapter.Current()
	if revision.CheckEligible(domain.Live) == nil {
		t.Fatal("checksum mismatch left the book eligible")
	}
	if _, err := adapter.Handle([]byte(`{"channel":"book","type":"update","data":[{"symbol":"BTC/USD","asks":[],"bids":[],"checksum":3310070434}]}`)); err == nil {
		t.Fatal("update without a fresh snapshot was accepted")
	}
}

func TestOrderedUpdatesAndExponentNumbers(t *testing.T) {
	clock := &testClock{now: time.Now()}
	adapter, err := NewAdapter("BTC/USD", 10, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Handle(instrumentFrame()); err != nil {
		t.Fatal(err)
	}
	first := rawLevel{priceText: "100.0", quantityText: "1.000", price: mustNumber(t, "100"), quantity: mustNumber(t, "1")}
	checksumSnapshot := checksum(map[string]rawLevel{"100": first}, nil)
	snapshot := fmt.Sprintf(`{"channel":"book","type":"snapshot","data":[{"symbol":"BTC/USD","asks":[{"price":100.0,"qty":1.000}],"bids":[],"checksum":%d}]}`, checksumSnapshot)
	if _, err := adapter.Handle([]byte(snapshot)); err != nil {
		t.Fatal(err)
	}
	last := rawLevel{priceText: "100.0", quantityText: "3.000", price: mustNumber(t, "100"), quantity: mustNumber(t, "3")}
	checksumUpdate := checksum(map[string]rawLevel{"100": last}, nil)
	update := fmt.Sprintf(`{"channel":"book","type":"update","data":[{"symbol":"BTC/USD","asks":[{"price":100.0,"qty":2.000},{"price":100.0,"qty":3.000}],"bids":[],"checksum":%d}]}`, checksumUpdate)
	if _, err := adapter.Handle([]byte(update)); err != nil {
		t.Fatal(err)
	}
	revision, _ := adapter.Current()
	if got, _ := revision.Asks()[0].Quantity.ExactDecimal(); got != "3" {
		t.Fatalf("last update quantity = %s", got)
	}
	parsed, text, err := parseDecimal([]byte("1.2300e-3"))
	if err != nil || text != "0.0012300" {
		t.Fatalf("exponent expansion = %q, %v", text, err)
	}
	if got, _ := parsed.ExactDecimal(); got != "0.00123" {
		t.Fatalf("exponent exact value = %s", got)
	}
}

func TestDisconnectAndHeartbeatLivenessRequireNewSnapshot(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	adapter, err := NewAdapter("BTC/USD", 10, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Handle(instrumentFrame()); err != nil {
		t.Fatal(err)
	}
	asks := rawLevel{priceText: "100", quantityText: "1", price: mustNumber(t, "100"), quantity: mustNumber(t, "1")}
	value := checksum(map[string]rawLevel{"100": asks}, nil)
	snapshot := []byte(fmt.Sprintf(`{"channel":"book","type":"snapshot","data":[{"symbol":"BTC/USD","asks":[{"price":100,"qty":1}],"bids":[],"checksum":%d}]}`, value))
	if _, err := adapter.Handle(snapshot); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(10 * time.Second)
	if _, err := adapter.Handle([]byte(`{"channel":"heartbeat"}`)); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(10 * time.Second)
	if adapter.CheckLiveness(15 * time.Second) {
		t.Fatal("heartbeat did not keep connection alive")
	}
	clock.now = clock.now.Add(16 * time.Second)
	if !adapter.CheckLiveness(15 * time.Second) {
		t.Fatal("silent connection did not time out")
	}
	if _, err := adapter.Handle([]byte(`{"channel":"book","type":"update","data":[{"symbol":"BTC/USD","asks":[],"bids":[],"checksum":0}]}`)); err == nil {
		t.Fatal("update after timeout was accepted")
	}
	if _, err := adapter.Handle(snapshot); err != nil {
		t.Fatal(err)
	}
	adapter.Invalidate("disconnect")
	if _, err := adapter.Handle([]byte(`{"channel":"book","type":"update","data":[{"symbol":"BTC/USD","asks":[],"bids":[],"checksum":0}]}`)); err == nil {
		t.Fatal("update after disconnect was accepted")
	}
	if _, err := adapter.Handle(snapshot); err != nil {
		t.Fatal(err)
	}
	revision, _ := adapter.Current()
	if err := revision.CheckEligible(domain.Live); err != nil {
		t.Fatalf("fresh snapshot did not restore eligibility: %v", err)
	}
}

func TestInstrumentChangeInvalidatesOldBook(t *testing.T) {
	adapter, err := NewAdapter("BTC/USD", 10, &testClock{now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Handle(instrumentFrame()); err != nil {
		t.Fatal(err)
	}
	changed := []byte(strings.Replace(string(instrumentFrame()), `"cost_precision":8`, `"cost_precision":9`, 1))
	result, err := adapter.Handle(changed)
	if err != nil || !result.MetadataChanged {
		t.Fatalf("metadata change = %+v, %v", result, err)
	}
	revision, ok := adapter.Current()
	if !ok || revision.CheckEligible(domain.Live) == nil {
		t.Fatal("changed instrument left an eligible old book")
	}
	if revision.Instrument().QuoteScale != 9 {
		t.Fatal("new instrument rules were not installed")
	}
}

func mustNumber(t *testing.T, text string) value.Value {
	t.Helper()
	parsed, err := value.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
