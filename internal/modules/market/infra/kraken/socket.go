package kraken

import (
	"context"
	"errors"
	"fmt"

	"github.com/coder/websocket"
)

type Socket interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close() error
}

type Dialer interface {
	Dial(context.Context, string) (Socket, error)
}

type WebSocketDialer struct{}

var ErrFrameLimit = errors.New("WebSocket frame exceeds 1 MiB")

func (WebSocketDialer) Dial(ctx context.Context, address string) (Socket, error) {
	connection, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		return nil, err
	}
	connection.SetReadLimit(1 << 20)
	return &webSocket{connection: connection}, nil
}

type webSocket struct{ connection *websocket.Conn }

func (socket *webSocket) Read(ctx context.Context) ([]byte, error) {
	kind, payload, err := socket.connection.Read(ctx)
	if err != nil {
		if errors.Is(err, websocket.ErrMessageTooBig) {
			return nil, ErrFrameLimit
		}
		return nil, err
	}
	if kind != websocket.MessageText {
		return nil, errors.New("Kraken sent a non-text WebSocket message")
	}
	if len(payload) > 1<<20 {
		return nil, fmt.Errorf("%w: %d", ErrFrameLimit, len(payload))
	}
	return payload, nil
}

func (socket *webSocket) Write(ctx context.Context, payload []byte) error {
	return socket.connection.Write(ctx, websocket.MessageText, payload)
}

func (socket *webSocket) Close() error {
	return socket.connection.Close(websocket.StatusNormalClosure, "")
}
