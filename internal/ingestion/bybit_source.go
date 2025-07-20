package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/svirmi/websocket-skeleton/internal/models"
)

type BybitConfig struct {
	BaseCoins    []string            // Base coins to subscribe (BTC, ETH, SOL)
	Expiry       string              // Expiry date in format like "27JUL25"
	TestNet      bool                // Whether to use testnet
	StrikeRanges map[string][]string // Strike prices for each base coin
}

func DefaultBybitConfig() BybitConfig {
	return BybitConfig{
		BaseCoins: []string{"BTC", "ETH", "SOL"},
		Expiry:    "27JUL25",
		TestNet:   true,
		StrikeRanges: map[string][]string{
			"BTC": {"60000", "65000"},
			"ETH": {"2000", "2500"},
			"SOL": {"100", "120"},
		},
	}
}

type BybitSource struct {
	*WebSocketSource
	config BybitConfig
}

func NewBybitSource(id string, config BybitConfig, bufferSize int, maxMessageSize int64) *BybitSource {
	wsURL := "wss://stream-testnet.bybit.com/v5/trade/option"
	if !config.TestNet {
		wsURL = "wss://stream.bybit.com/v5/trade/option"
	}

	return &BybitSource{
		WebSocketSource: NewWebSocketSource(
			id,
			wsURL,
			bufferSize,
			maxMessageSize,
		),
		config: config,
	}
}

func (bs *BybitSource) Start(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				bs.Close()
				return
			default:
				if err := bs.connectAndSubscribe(&dialer); err != nil {
					bs.updateError(err.Error())
					time.Sleep(bs.reconnectInterval)
					continue
				}

				bs.readPump(ctx)
			}
		}
	}()

	return nil
}

func (bs *BybitSource) connectAndSubscribe(dialer *websocket.Dialer) error {
	// First connect
	conn, _, err := dialer.Dial(bs.url, nil)
	if err != nil {
		return err
	}

	bs.mu.Lock()
	bs.conn = conn
	bs.mu.Unlock()

	bs.connected.Store(true)

	// Generate subscription message
	var subscriptionTopics []string
	for _, baseCoin := range bs.config.BaseCoins {
		if strikes, ok := bs.config.StrikeRanges[baseCoin]; ok {
			for _, strike := range strikes {
				// Subscribe to both calls and puts
				subscriptionTopics = append(subscriptionTopics,
					fmt.Sprintf("trade.%s-%s-%s-C", baseCoin, bs.config.Expiry, strike),
					fmt.Sprintf("trade.%s-%s-%s-P", baseCoin, bs.config.Expiry, strike),
				)
			}
		}
	}

	// Create subscription message
	subMsg := models.SubscribeMessage{
		ReqID: fmt.Sprintf("svirmi_%d", time.Now().Unix()),
		Op:    "subscribe",
		Args:  subscriptionTopics,
	}

	bs.mu.RLock()
	defer bs.mu.RUnlock()

	if err := bs.conn.WriteJSON(subMsg); err != nil {
		bs.Close()
		return fmt.Errorf("subscription failed: %w", err)
	}

	return nil
}

func (bs *BybitSource) readPump(ctx context.Context) {
	bs.mu.RLock()
	conn := bs.conn
	bs.mu.RUnlock()

	if conn == nil {
		return
	}

	conn.SetReadLimit(bs.maxMessageSize)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, message, err := conn.ReadMessage()
			if err != nil {
				bs.updateError(err.Error())
				return
			}

			// Parse the message
			var bybitMsg models.BybitMessage
			if err := json.Unmarshal(message, &bybitMsg); err != nil {
				bs.updateError(fmt.Sprintf("JSON parse error: %v", err))
				continue
			}

			// Handle different message types
			switch bybitMsg.MessageType {
			case "snapshot", "delta":
				var trades []models.OptionTradeData
				if err := json.Unmarshal(bybitMsg.Data, &trades); err != nil {
					bs.updateError(fmt.Sprintf("Trade data parse error: %v", err))
					continue
				}

				for _, trade := range trades {
					processed := models.ProcessedTrade{
						Symbol:        trade.Symbol,
						Price:         trade.Price,
						Size:          trade.Size,
						Side:          trade.Side,
						ImpliedVol:    trade.ImpliedVol,
						TradeTime:     time.Unix(0, trade.TradeTime*int64(time.Millisecond)),
						ProcessedTime: time.Now().UTC(),
						Source:        "bybit",
					}

					processedJSON, err := json.Marshal(processed)
					if err != nil {
						bs.updateError(fmt.Sprintf("JSON marshal error: %v", err))
						continue
					}

					bs.output <- processedJSON
				}

				bs.lastMessage.Store(time.Now())
				bs.messagesCount.Add(1)
				bs.bytesReceived.Add(int64(len(message)))

			case "error":
				bs.updateError(fmt.Sprintf("Bybit error message: %s", string(message)))

			default:
				// Ignore non-trade messages (like pong responses)
				continue
			}
		}
	}
}
