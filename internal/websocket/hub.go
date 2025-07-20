package websocket

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Hub maintains the set of active clients and broadcasts messages to the clients.
type Hub struct {
	// Registered clients.
	clients map[*Client]bool

	// Channel sizes (initialized from config)
	broadcastBufferSize int
	clientBufferSize    int

	// Inbound messages from the clients.
	broadcast chan []byte

	// Register requests from the clients.
	register chan *Client

	// Unregister requests from clients.
	unregister chan *Client

	// Mutex for thread-safe operations
	mu sync.RWMutex

	// Metrics and status information
	metrics struct {
		activeConnections int64
		messagesProcessed int64
		messagesFailed    int64
		bytesProcessed    int64
		shutdownRequested time.Time
		startTime         time.Time
		lastError         string
		lastErrorTime     time.Time
	}

	// Configuration
	maxConnections int
	maxMessageSize int64

	// Context for shutdown coordination
	ctx    context.Context
	cancel context.CancelFunc
}

// NewHub creates a new Hub instance with the specified configuration
func NewHub(maxConn, bufferSize int, maxMessageSize int64) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		broadcast:           make(chan []byte, bufferSize),
		register:            make(chan *Client),
		unregister:          make(chan *Client),
		clients:             make(map[*Client]bool),
		maxConnections:      maxConn,
		maxMessageSize:      maxMessageSize,
		broadcastBufferSize: bufferSize,
		clientBufferSize:    bufferSize / 4, // Client buffer is smaller than hub buffer
		ctx:                 ctx,
		cancel:              cancel,
		metrics: struct {
			activeConnections int64
			messagesProcessed int64
			messagesFailed    int64
			bytesProcessed    int64
			shutdownRequested time.Time
			startTime         time.Time
			lastError         string
			lastErrorTime     time.Time
		}{
			startTime: time.Now(),
		},
	}
}

// Run starts the hub's main event loop
func (h *Hub) Run(ctx context.Context) {
	defer h.cancel()

	for {
		select {
		case <-ctx.Done():
			h.shutdown()
			return
		case <-h.ctx.Done():
			h.shutdown()
			return
		case client := <-h.register:
			h.handleRegister(client)
		case client := <-h.unregister:
			h.handleUnregister(client)
		case message := <-h.broadcast:
			h.handleBroadcast(message)
		}
	}
}

// handleRegister processes new client registrations
func (h *Hub) handleRegister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if int64(len(h.clients)) < int64(h.maxConnections) {
		h.clients[client] = true
		atomic.AddInt64(&h.metrics.activeConnections, 1)
	} else {
		// Connection limit reached
		h.updateError("connection limit reached", time.Now())
		client.Close()
	}
}

// handleUnregister processes client disconnections
func (h *Hub) handleUnregister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		client.Close()
		atomic.AddInt64(&h.metrics.activeConnections, -1)
	}
}

// handleBroadcast sends a message to all connected clients
func (h *Hub) handleBroadcast(message []byte) {
	if len(message) > int(h.maxMessageSize) {
		h.updateError("message size exceeds limit", time.Now())
		atomic.AddInt64(&h.metrics.messagesFailed, 1)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	messageSize := int64(len(message))
	atomic.AddInt64(&h.metrics.bytesProcessed, messageSize)

	for client := range h.clients {
		select {
		case client.send <- message:
			atomic.AddInt64(&h.metrics.messagesProcessed, 1)
		default:
			// Client send buffer is full
			atomic.AddInt64(&h.metrics.messagesFailed, 1)
			go h.handleUnregister(client)
		}
	}
}

// Close shuts down the hub and all client connections
func (h *Hub) Close() error {
	h.cancel()
	h.shutdown()
	return nil
}

// shutdown performs the actual shutdown operations
func (h *Hub) shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Close all client connections
	for client := range h.clients {
		client.Close()
		delete(h.clients, client)
	}

	// Update metrics
	atomic.StoreInt64(&h.metrics.activeConnections, 0)
}

// SetDraining marks the hub as draining for graceful shutdown
func (h *Hub) SetDraining(draining bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if draining {
		h.metrics.shutdownRequested = time.Now()
	}
}

// GetMetrics returns current hub metrics
func (h *Hub) GetMetrics() *Metrics {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return &Metrics{
		ActiveConnections: atomic.LoadInt64(&h.metrics.activeConnections),
		MessagesProcessed: atomic.LoadInt64(&h.metrics.messagesProcessed),
		MessagesFailed:    atomic.LoadInt64(&h.metrics.messagesFailed),
		BytesProcessed:    atomic.LoadInt64(&h.metrics.bytesProcessed),
		StartTime:         h.metrics.startTime,
		LastShutdownTime:  h.metrics.shutdownRequested,
		LastError:         h.metrics.lastError,
		LastErrorTime:     h.metrics.lastErrorTime,
		MaxConnections:    h.maxConnections,
		MaxMessageSize:    h.maxMessageSize,
		Uptime:            time.Since(h.metrics.startTime).String(),
	}
}

// updateError updates the last error information
func (h *Hub) updateError(err string, timestamp time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.metrics.lastError = err
	h.metrics.lastErrorTime = timestamp
}
