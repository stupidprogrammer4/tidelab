package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/stupidprogrammer4/tidelab/internal/modules/market/domain"
	"github.com/stupidprogrammer4/tidelab/internal/modules/market/infra/kraken"
	"github.com/stupidprogrammer4/tidelab/internal/modules/market/infra/recording"
)

const DefaultKrakenURL = "wss://ws.kraken.com/v2"

var ErrQueueOverflow = errors.New("capture queue exceeded its count or byte limit")

type CaptureConfig struct {
	DataRoot        string
	URL             string
	Symbol          string
	Depth           int
	QueueCount      int
	QueueBytes      int64
	LivenessTimeout time.Duration
	MaxReconnects   int // Zero means unlimited until context cancellation.
}

func (config *CaptureConfig) defaults() {
	if config.URL == "" {
		config.URL = DefaultKrakenURL
	}
	if config.Depth == 0 {
		config.Depth = 10
	}
	if config.QueueCount == 0 {
		config.QueueCount = 4096
	}
	if config.QueueBytes == 0 {
		config.QueueBytes = 32 << 20
	}
	if config.LivenessTimeout == 0 {
		config.LivenessTimeout = 15 * time.Second
	}
}

type Clock interface{ Now() time.Time }

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type CaptureService struct {
	Dialer kraken.Dialer
	Clock  Clock
	Wait   func(context.Context, time.Duration) error
	Jitter func(time.Duration) time.Duration
}

type receivedFrame struct {
	payload []byte
	at      time.Time
}

type frameQueue struct {
	frames chan receivedFrame
	limit  int64
	mu     sync.Mutex
	bytes  int64
}

func newFrameQueue(count int, byteLimit int64) *frameQueue {
	return &frameQueue{frames: make(chan receivedFrame, count), limit: byteLimit}
}

func (queue *frameQueue) Offer(frame receivedFrame) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.bytes+int64(len(frame.payload)) > queue.limit {
		return ErrQueueOverflow
	}
	select {
	case queue.frames <- frame:
		queue.bytes += int64(len(frame.payload))
		return nil
	default:
		return ErrQueueOverflow
	}
}

func (queue *frameQueue) Released(frame receivedFrame) {
	queue.mu.Lock()
	queue.bytes -= int64(len(frame.payload))
	queue.mu.Unlock()
}

func (service CaptureService) Run(ctx context.Context, config CaptureConfig) (final recording.Manifest, runErr error) {
	config.defaults()
	if config.DataRoot == "" || config.Symbol == "" || config.QueueCount < 1 || config.QueueCount > 4096 ||
		config.QueueBytes < recording.MaxFrameBytes || config.QueueBytes > 32<<20 ||
		config.LivenessTimeout < time.Second || config.MaxReconnects < 0 {
		return recording.Manifest{}, errors.New("invalid capture configuration")
	}
	if service.Dialer == nil {
		service.Dialer = kraken.WebSocketDialer{}
	}
	if service.Clock == nil {
		service.Clock = wallClock{}
	}
	if service.Wait == nil {
		service.Wait = waitContext
	}
	if service.Jitter == nil {
		service.Jitter = func(max time.Duration) time.Duration { return time.Duration(rand.Int63n(int64(max) + 1)) }
	}
	adapter, err := kraken.NewAdapter(config.Symbol, config.Depth, service.Clock)
	if err != nil {
		return recording.Manifest{}, err
	}
	startedAt := service.Clock.Now()
	session, err := recording.Create(config.DataRoot, config.Symbol, config.Depth, startedAt)
	if err != nil {
		return recording.Manifest{}, err
	}
	completed := false
	defer func() {
		if !completed {
			manifest, err := session.Abort("capture_terminated", service.Clock.Now())
			final = manifest
			if err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
	}()
	backoff := time.Second
	reconnects := 0
	hadEligibleBook := false
	var lastConnectionError error
	for ctx.Err() == nil {
		socket, err := service.Dialer.Dial(ctx, config.URL)
		if err == nil {
			backoff = time.Second
			at := service.Clock.Now()
			if err = session.StartSegment(at, at.Sub(startedAt)); err != nil {
				socket.Close()
				adapter.Invalidate("recording_failure")
				return session.Snapshot(), err
			}
			if err = writeSubscription(ctx, socket, "heartbeat", "", 0); err == nil {
				err = writeSubscription(ctx, socket, "instrument", "", 0)
			}
			if err == nil {
				var observed bool
				observed, err = service.captureConnection(ctx, socket, adapter, session, config, startedAt)
				hadEligibleBook = hadEligibleBook || observed
			}
			lastConnectionError = err
			socket.Close()
			reason := "disconnect"
			if err != nil {
				reason = classifyCaptureError(err)
			}
			adapter.Invalidate(reason)
			at = service.Clock.Now()
			if endErr := session.EndSegment(reason, at, at.Sub(startedAt)); endErr != nil {
				return session.Snapshot(), endErr
			}
			if syncErr := session.Sync(at); syncErr != nil {
				return session.Snapshot(), syncErr
			}
			if errors.Is(err, ErrQueueOverflow) || errors.Is(err, kraken.ErrFrameLimit) || errors.Is(err, errRecording) {
				return session.Snapshot(), err
			}
			if ctx.Err() != nil {
				break
			}
		} else {
			lastConnectionError = err
		}
		reconnects++
		if config.MaxReconnects > 0 && reconnects > config.MaxReconnects {
			return session.Snapshot(), fmt.Errorf("capture reconnect limit reached: %w", err)
		}
		wait := backoff + service.Jitter(backoff/10)
		if waitErr := service.Wait(ctx, wait); waitErr != nil {
			break
		}
		if backoff < 60*time.Second {
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
		}
	}
	if !hadEligibleBook {
		if lastConnectionError != nil {
			return session.Snapshot(), fmt.Errorf("capture ended without a validated book snapshot: %w", lastConnectionError)
		}
		return session.Snapshot(), errors.New("capture ended without a validated book snapshot")
	}
	at := service.Clock.Now()
	manifest, err := session.Close(at, at.Sub(startedAt))
	if err != nil {
		return manifest, err
	}
	completed = true
	return manifest, nil
}

var errRecording = errors.New("recording failure")

func classifyCaptureError(err error) string {
	switch {
	case errors.Is(err, ErrQueueOverflow):
		return "queue_overflow"
	case errors.Is(err, kraken.ErrFrameLimit):
		return "frame_limit"
	case errors.Is(err, errRecording):
		return "recording_failure"
	case errors.Is(err, context.DeadlineExceeded):
		return "liveness_timeout"
	default:
		return "protocol_or_connection_failure"
	}
}

func (service CaptureService) captureConnection(ctx context.Context, socket kraken.Socket, adapter *kraken.Adapter,
	session *recording.Session, config CaptureConfig, startedAt time.Time) (bool, error) {
	queue := newFrameQueue(config.QueueCount, config.QueueBytes)
	readError := make(chan error, 1)
	readerCtx, stopReader := context.WithCancel(ctx)
	defer stopReader()
	go func() {
		defer close(queue.frames)
		for {
			readCtx, cancel := context.WithTimeout(readerCtx, config.LivenessTimeout)
			payload, err := socket.Read(readCtx)
			cancel()
			if err != nil {
				readError <- err
				return
			}
			if len(payload) > recording.MaxFrameBytes {
				readError <- ErrQueueOverflow
				return
			}
			at := service.Clock.Now()
			if err := queue.Offer(receivedFrame{payload: payload, at: at}); err != nil {
				readError <- err
				return
			}
		}
	}()
	ticker := time.NewTicker(recording.SyncInterval)
	defer ticker.Stop()
	bookSubscribed := false
	hadEligibleBook := false
	done := ctx.Done()
	for {
		select {
		case received, ok := <-queue.frames:
			if !ok {
				return hadEligibleBook, <-readError
			}
			queue.Released(received)
			if err := session.AppendFrame(received.payload, received.at, received.at.Sub(startedAt)); err != nil {
				return hadEligibleBook, fmt.Errorf("%w: %v", errRecording, err)
			}
			result, err := adapter.Handle(received.payload)
			if err != nil {
				return hadEligibleBook, err
			}
			if result.InstrumentReady && !bookSubscribed {
				instrument, _ := adapter.Instrument()
				if err := session.Sync(service.Clock.Now()); err != nil {
					return hadEligibleBook, fmt.Errorf("%w: %v", errRecording, err)
				}
				if err := session.SetInstrument(instrument); err != nil {
					return hadEligibleBook, fmt.Errorf("%w: %v", errRecording, err)
				}
				if err := writeSubscription(ctx, socket, "book", config.Symbol, config.Depth); err != nil {
					return hadEligibleBook, err
				}
				bookSubscribed = true
			}
			if result.MetadataChanged {
				return hadEligibleBook, errors.New("instrument metadata changed; fresh connection required")
			}
			if result.BookChanged {
				revision, ok := adapter.Current()
				hadEligibleBook = hadEligibleBook || (ok && revision.CheckEligible(domain.Live) == nil)
			}
			if err := session.SyncIfDue(service.Clock.Now()); err != nil {
				return hadEligibleBook, fmt.Errorf("%w: %v", errRecording, err)
			}
		case <-ticker.C:
			if err := session.Sync(service.Clock.Now()); err != nil {
				return hadEligibleBook, fmt.Errorf("%w: %v", errRecording, err)
			}
		case <-done:
			done = nil
			stopReader()
			socket.Close()
		}
	}
}

func writeSubscription(ctx context.Context, socket kraken.Socket, channel, symbol string, depth int) error {
	params := map[string]any{"channel": channel}
	if symbol != "" {
		params["symbol"] = []string{symbol}
		params["depth"] = depth
	}
	payload, err := json.Marshal(struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}{Method: "subscribe", Params: params})
	if err != nil {
		return err
	}
	return socket.Write(ctx, payload)
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
