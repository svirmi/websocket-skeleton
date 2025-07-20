package models

import (
	"encoding/json"
	"time"
)

// BybitMessage represents the wrapper message structure from Bybit
type BybitMessage struct {
	Topic       string          `json:"topic"`
	MessageType string          `json:"type"`
	Data        json.RawMessage `json:"data"`
	Timestamp   int64           `json:"ts"`
	ServerTime  int64           `json:"serverTime,omitempty"`
}

// SubscribeMessage represents the subscription request to Bybit WebSocket
type SubscribeMessage struct {
	ReqID string   `json:"req_id"`
	Op    string   `json:"op"`
	Args  []string `json:"args"`
}

// OptionTradeData represents the trade data for options
type OptionTradeData struct {
	Symbol        string  `json:"symbol"`
	TickDirection string  `json:"tickDirection"`
	Price         float64 `json:"price,string"`
	Size          float64 `json:"size,string"`
	TradeTime     int64   `json:"tradeTime"`
	Side          string  `json:"side"`
	BlockTrade    bool    `json:"blockTrade"`
	ImpliedVol    float64 `json:"iv,string"`
}

// ProcessedTrade represents our enriched trade data
type ProcessedTrade struct {
	Symbol        string    `json:"symbol"`
	Price         float64   `json:"price"`
	Size          float64   `json:"size"`
	Side          string    `json:"side"`
	ImpliedVol    float64   `json:"implied_vol"`
	TradeTime     time.Time `json:"trade_time"`
	ProcessedTime time.Time `json:"processed_time"`
	Source        string    `json:"source"`
}
