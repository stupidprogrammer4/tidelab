package services

import (
	"context"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stupidprogrammer4/tidelab/internal/modules/market/infra/recording"
)

func TestFrameQueueRejectsCountAndByteOverflow(t *testing.T) {
	queue := newFrameQueue(1, 10)
	first := receivedFrame{payload: make([]byte, 6)}
	if err := queue.Offer(first); err != nil {
		t.Fatal(err)
	}
	if err := queue.Offer(receivedFrame{payload: make([]byte, 5)}); err != ErrQueueOverflow {
		t.Fatalf("byte overflow = %v", err)
	}
	if err := queue.Offer(receivedFrame{payload: make([]byte, 1)}); err != ErrQueueOverflow {
		t.Fatalf("count overflow = %v", err)
	}
	queue.Released(<-queue.frames)
	if err := queue.Offer(receivedFrame{payload: make([]byte, 5)}); err != nil {
		t.Fatalf("queue did not release capacity: %v", err)
	}
}

func TestCaptureWithLocalWebSocketReconnectsIntoNewSegment(t *testing.T) {
	var connections atomic.Int32
	secondSnapshot := make(chan struct{})
	checksum := crc32.ChecksumIEEE([]byte("1001991"))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "")
		attempt := connections.Add(1)
		for range 2 {
			if _, _, err := connection.Read(context.Background()); err != nil {
				return
			}
		}
		instrument := []byte(`{"channel":"instrument","type":"snapshot","data":{"pairs":[{"symbol":"TEST/USD","base":"TEST","quote":"USD","price_increment":1,"qty_increment":0.0001,"qty_min":0.0001,"cost_min":0.0001,"cost_precision":4,"status":"online"}]}}`)
		if err := connection.Write(context.Background(), websocket.MessageText, instrument); err != nil {
			return
		}
		if _, _, err := connection.Read(context.Background()); err != nil {
			return
		}
		book := []byte(fmt.Sprintf(`{"channel":"book","type":"snapshot","data":[{"symbol":"TEST/USD","asks":[{"price":100,"qty":1}],"bids":[{"price":99,"qty":1}],"checksum":%d}]}`, checksum))
		if err := connection.Write(context.Background(), websocket.MessageText, book); err != nil {
			return
		}
		if attempt == 1 {
			return // The next connection must receive and validate a new snapshot.
		}
		if attempt == 2 {
			close(secondSnapshot)
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := connection.Write(ctx, websocket.MessageText, []byte(`{"channel":"heartbeat"}`))
			cancel()
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		select {
		case <-secondSnapshot:
			time.Sleep(100 * time.Millisecond)
			cancel()
		case <-ctx.Done():
		}
	}()
	service := CaptureService{
		Wait:   func(context.Context, time.Duration) error { return nil },
		Jitter: func(time.Duration) time.Duration { return 0 },
	}
	root := t.TempDir()
	manifest, err := service.Run(ctx, CaptureConfig{
		DataRoot: root, URL: server.URL, Symbol: "TEST/USD", Depth: 10,
		LivenessTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != recording.Complete || len(manifest.Segments) < 2 || manifest.DurableSeq != manifest.WrittenSeq || len(manifest.InstrumentHistory) < 2 {
		t.Fatalf("capture manifest = %+v", manifest)
	}
	if connections.Load() < 2 {
		t.Fatalf("only %d WebSocket connection(s)", connections.Load())
	}
	if _, err := recording.Verify(root, manifest.SessionID); err != nil {
		t.Fatal(err)
	}
}
