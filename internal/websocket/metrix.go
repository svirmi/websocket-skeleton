package websocket

import (
	"time"
)

// Metrics represents the server's runtime metrics
type Metrics struct {
	ActiveConnections int64     `json:"active_connections"`
	MessagesProcessed int64     `json:"messages_processed"`
	MessagesFailed    int64     `json:"messages_failed"`
	BytesProcessed    int64     `json:"bytes_processed"`
	StartTime         time.Time `json:"start_time"`
	LastShutdownTime  time.Time `json:"last_shutdown_time,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
	LastErrorTime     time.Time `json:"last_error_time,omitempty"`
	MaxConnections    int       `json:"max_connections"`
	MaxMessageSize    int64     `json:"max_message_size"`
	Uptime            string    `json:"uptime"`
}
