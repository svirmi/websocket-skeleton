package processor

import (
	"encoding/json"
	"fmt"
	"time"
)

type MessageProcessor struct {
	maxMessageSize int64
	stats          *ProcessorStats
}

type ProcessorStats struct {
	ProcessedCount int64
	ErrorCount     int64
	LastError      string
	LastErrorTime  time.Time
	StartTime      time.Time
}

type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	Time    time.Time       `json:"time"`
}

func NewMessageProcessor(maxMessageSize int64) *MessageProcessor {
	return &MessageProcessor{
		maxMessageSize: maxMessageSize,
		stats: &ProcessorStats{
			StartTime: time.Now(),
		},
	}
}

func (p *MessageProcessor) ProcessMessage(data []byte) (*Message, error) {
	// Check message size
	if int64(len(data)) > p.maxMessageSize {
		return nil, fmt.Errorf("message size exceeds limit: %d > %d", len(data), p.maxMessageSize)
	}

	// Parse message
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		p.stats.ErrorCount++
		p.stats.LastError = fmt.Sprintf("invalid message format: %v", err)
		p.stats.LastErrorTime = time.Now()
		return nil, fmt.Errorf("invalid message format: %w", err)
	}

	// Set message time if not set
	if msg.Time.IsZero() {
		msg.Time = time.Now()
	}

	// Validate message type
	if msg.Type == "" {
		p.stats.ErrorCount++
		p.stats.LastError = "message type is required"
		p.stats.LastErrorTime = time.Now()
		return nil, fmt.Errorf("message type is required")
	}

	p.stats.ProcessedCount++
	return &msg, nil
}

func (p *MessageProcessor) GetStats() *ProcessorStats {
	return p.stats
}
