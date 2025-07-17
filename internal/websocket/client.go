package websocket

import (
	"context"
	"encoding/json"
	"log"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/svirmi/websocket-skeleton/internal/processor"
)

// Client represents a single websocket connection
type Client struct {
	// The websocket connection
	conn *websocket.Conn

	// The hub managing this client
	hub *Hub

	// Buffered channel of outbound messages
	send chan []byte

	// Unique client identifier
	id string

	// Last ping time for monitoring
	lastPing time.Time

	// Context for handling client lifecycle
	ctx    context.Context
	cancel context.CancelFunc

	// Configuration
	maxSize   int64
	processor *processor.MessageProcessor

	// Client metadata
	metadata struct {
		userAgent   string
		ipAddress   string
		location    string
		connectedAt time.Time
	}

	// Client statistics
	stats struct {
		messagesReceived atomic.Int64
		messagesSent     atomic.Int64
		bytesReceived    atomic.Int64
		bytesSent        atomic.Int64
		errors           atomic.Int64
		lastError        string
		lastErrorTime    time.Time
	}
}

// NewClient creates a new client instance
func NewClient(
	hub *Hub,
	conn *websocket.Conn,
	maxMessageSize int64,
	proc *processor.MessageProcessor,
	bufferSize int,
) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	client := &Client{
		hub:       hub,
		conn:      conn,
		send:      make(chan []byte, bufferSize),
		id:        uuid.New().String(),
		lastPing:  time.Now(),
		ctx:       ctx,
		cancel:    cancel,
		maxSize:   maxMessageSize,
		processor: proc,
	}

	// Initialize metadata
	client.metadata.connectedAt = time.Now()
	client.metadata.userAgent = conn.RemoteAddr().String() // Basic implementation
	client.metadata.ipAddress = conn.RemoteAddr().String()

	return client
}

// ReadPump pumps messages from the websocket connection to the hub
func (c *Client) ReadPump(pongWait time.Duration) {
	defer func() {
		c.hub.unregister <- c
		c.cancel()
		c.conn.Close()
	}()

	// Configure connection
	c.conn.SetReadLimit(c.maxSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.lastPing = time.Now()
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			// Read message
			_, message, err := c.conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err,
					websocket.CloseGoingAway,
					websocket.CloseAbnormalClosure) {
					c.logError("read error", err)
				}
				return
			}

			// Update statistics
			c.stats.messagesReceived.Add(1)
			c.stats.bytesReceived.Add(int64(len(message)))

			// Process message
			processedMsg, err := c.processor.ProcessMessage(message)
			if err != nil {
				c.logError("process error", err)
				c.stats.errors.Add(1)
				continue
			}

			// If valid JSON, update message with client ID and timestamp
			if processedMsg != nil {
				enrichedMsg, err := c.enrichMessage(processedMsg)
				if err != nil {
					c.logError("enrich error", err)
					c.stats.errors.Add(1)
					continue
				}
				message = enrichedMsg
			}

			// Broadcast message
			c.hub.broadcast <- message
		}
	}
}

// WritePump pumps messages from the hub to the websocket connection
func (c *Client) WritePump(writeWait, pingPeriod time.Duration) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				c.logError("writer error", err)
				return
			}

			// Write the message
			n, err := w.Write(message)
			if err != nil {
				c.logError("write error", err)
				return
			}

			// Update statistics
			c.stats.messagesSent.Add(1)
			c.stats.bytesSent.Add(int64(n))

			// Add queued chat messages to the current websocket message
			n = len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				msg := <-c.send
				w.Write(msg)
				c.stats.messagesSent.Add(1)
				c.stats.bytesSent.Add(int64(len(msg)))
			}

			if err := w.Close(); err != nil {
				c.logError("close error", err)
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.logError("ping error", err)
				return
			}
		}
	}
}

// Close gracefully closes the client connection
func (c *Client) Close() {
	c.cancel()
	close(c.send)
}

// GetStats returns current client statistics
func (c *Client) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"id":                c.id,
		"connected_at":      c.metadata.connectedAt,
		"messages_received": c.stats.messagesReceived.Load(),
		"messages_sent":     c.stats.messagesSent.Load(),
		"bytes_received":    c.stats.bytesReceived.Load(),
		"bytes_sent":        c.stats.bytesSent.Load(),
		"errors":            c.stats.errors.Load(),
		"last_error":        c.stats.lastError,
		"last_error_time":   c.stats.lastErrorTime,
		"last_ping":         c.lastPing,
		"user_agent":        c.metadata.userAgent,
		"ip_address":        c.metadata.ipAddress,
		"location":          c.metadata.location,
	}
}

// Helper methods

// enrichMessage adds client metadata to the message
func (c *Client) enrichMessage(msg *processor.Message) ([]byte, error) {
	// Create enriched message structure
	enriched := struct {
		*processor.Message
		ClientID   string    `json:"client_id"`
		ClientIP   string    `json:"client_ip"`
		ReceivedAt time.Time `json:"received_at"`
	}{
		Message:    msg,
		ClientID:   c.id,
		ClientIP:   c.metadata.ipAddress,
		ReceivedAt: time.Now(),
	}

	// Marshal enriched message
	return json.Marshal(enriched)
}

// logError logs an error with context
func (c *Client) logError(context string, err error) {
	errMsg := err.Error()
	log.Printf("Client %s: %s: %v", c.id, context, err)

	c.stats.lastError = errMsg
	c.stats.lastErrorTime = time.Now()
}
