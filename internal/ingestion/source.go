package ingestion

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type DataSource interface {
	Start(ctx context.Context) error
	Stop() error
	GetChannel() <-chan []byte
	ID() string
	Status() SourceStatus
}

type SourceStatus struct {
	Connected     bool
	LastMessage   time.Time
	MessagesCount int64
	BytesReceived int64
	Errors        int64
	LastError     string
}

type WebSocketSource struct {
	id                string
	url               string
	conn              *websocket.Conn
	output            chan []byte
	mu                sync.RWMutex
	reconnectInterval time.Duration
	maxMessageSize    int64

	// Atomic counters
	messagesCount atomic.Int64
	bytesReceived atomic.Int64
	errorCount    atomic.Int64

	// Protected fields
	statusMu    sync.RWMutex
	connected   atomic.Bool
	lastMessage atomic.Value // stores time.Time
	lastError   atomic.Value // stores string
}

func NewWebSocketSource(id, url string, bufferSize int, maxMessageSize int64) *WebSocketSource {
	ws := &WebSocketSource{
		id:                id,
		url:               url,
		output:            make(chan []byte, bufferSize),
		reconnectInterval: time.Second * 5,
		maxMessageSize:    maxMessageSize,
	}
	ws.lastMessage.Store(time.Now())
	ws.lastError.Store("")
	return ws
}

func (ws *WebSocketSource) Start(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				ws.disconnect()
				return
			default:
				if err := ws.connect(&dialer); err != nil {
					ws.setError(err.Error())
					time.Sleep(ws.reconnectInterval)
					continue
				}

				ws.readPump(ctx)
			}
		}
	}()

	return nil
}

func (ws *WebSocketSource) connect(dialer *websocket.Dialer) error {
	conn, _, err := dialer.Dial(ws.url, nil)
	if err != nil {
		return err
	}

	ws.mu.Lock()
	ws.conn = conn
	ws.mu.Unlock()

	ws.connected.Store(true)
	return nil
}

func (ws *WebSocketSource) readPump(ctx context.Context) {
	ws.mu.RLock()
	conn := ws.conn
	ws.mu.RUnlock()

	if conn == nil {
		return
	}

	conn.SetReadLimit(ws.maxMessageSize)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, message, err := conn.ReadMessage()
			if err != nil {
				ws.setError(err.Error())
				return
			}

			ws.lastMessage.Store(time.Now())
			ws.messagesCount.Add(1)
			ws.bytesReceived.Add(int64(len(message)))

			ws.output <- message
		}
	}
}

func (ws *WebSocketSource) disconnect() {
	ws.mu.Lock()
	if ws.conn != nil {
		ws.conn.Close()
		ws.conn = nil
	}
	ws.mu.Unlock()

	ws.connected.Store(false)
	ws.setError("disconnected")
}

func (ws *WebSocketSource) Stop() error {
	ws.disconnect()
	close(ws.output)
	return nil
}

func (ws *WebSocketSource) GetChannel() <-chan []byte {
	return ws.output
}

func (ws *WebSocketSource) ID() string {
	return ws.id
}

func (ws *WebSocketSource) setError(err string) {
	ws.lastError.Store(err)
	ws.errorCount.Add(1)
}

func (ws *WebSocketSource) Status() SourceStatus {
	return SourceStatus{
		Connected:     ws.connected.Load(),
		LastMessage:   ws.lastMessage.Load().(time.Time),
		MessagesCount: ws.messagesCount.Load(),
		BytesReceived: ws.bytesReceived.Load(),
		Errors:        ws.errorCount.Load(),
		LastError:     ws.lastError.Load().(string),
	}
}
