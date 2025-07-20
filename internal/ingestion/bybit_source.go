package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/svirmi/websocket-skeleton/internal/logger"
	"github.com/svirmi/websocket-skeleton/internal/models"
)

// Logger instance for this package
var log zerolog.Logger

func init() {
	log = logger.GetLogger("bybit_source")
}

type BybitSource struct {
	WebSocketSource
	subscribedSymbols []string
	logger            zerolog.Logger
}

func NewBybitSource(id string, symbols []string, bufferSize int, maxMessageSize int64) *BybitSource {
	bs := &BybitSource{
		WebSocketSource: *NewWebSocketSource(
			id,
			"wss://stream-testnet.bybit.com/v5/trade/option",
			bufferSize,
			maxMessageSize,
		),
		subscribedSymbols: symbols,
		logger:            log.With().Str("source_id", id).Logger(),
	}
	return bs
}

func (bs *BybitSource) connect(dialer *websocket.Dialer) error {
	bs.logger.Debug().
		Str("url", bs.url).
		Msg("Connecting to Bybit")

	if err := bs.WebSocketSource.connect(dialer); err != nil {
		bs.logger.Error().
			Err(err).
			Msg("Failed to connect to Bybit")
		return err
	}

	// Subscribe to trade topics
	subMsg := models.SubscribeMessage{
		ReqID: fmt.Sprintf("sub_%d", time.Now().Unix()),
		Op:    "subscribe",
		Args:  make([]string, len(bs.subscribedSymbols)),
	}

	for i, symbol := range bs.subscribedSymbols {
		subMsg.Args[i] = fmt.Sprintf("trade.%s", symbol)
	}

	bs.logger.Debug().
		Interface("subscription", subMsg).
		Msg("Subscribing to topics")

	bs.mu.RLock()
	conn := bs.conn
	bs.mu.RUnlock()

	if err := conn.WriteJSON(subMsg); err != nil {
		bs.logger.Error().
			Err(err).
			Interface("subscription", subMsg).
			Msg("Subscription failed")
		bs.disconnect()
		return fmt.Errorf("subscription failed: %w", err)
	}

	bs.logger.Info().
		Strs("symbols", bs.subscribedSymbols).
		Msg("Successfully connected and subscribed")

	return nil
}

func (bs *BybitSource) readPump(ctx context.Context) {
	bs.logger.Debug().Msg("Starting read pump")

	bs.mu.RLock()
	conn := bs.conn
	bs.mu.RUnlock()

	if conn == nil {
		bs.logger.Error().Msg("Connection is nil")
		return
	}

	conn.SetReadLimit(bs.maxMessageSize)

	for {
		select {
		case <-ctx.Done():
			bs.logger.Debug().Msg("Context cancelled, stopping read pump")
			return
		default:
			_, message, err := conn.ReadMessage()
			if err != nil {
				bs.logger.Error().
					Err(err).
					Msg("Read error")
				bs.setError(err.Error())
				return
			}

			// Parse the message
			var bybitMsg models.BybitMessage
			if err := json.Unmarshal(message, &bybitMsg); err != nil {
				bs.logger.Error().
					Err(err).
					RawJSON("message", message).
					Msg("JSON parse error")
				bs.setError(fmt.Sprintf("JSON parse error: %v", err))
				continue
			}

			bs.logger.Debug().
				Str("topic", bybitMsg.Topic).
				Str("type", bybitMsg.Type).
				Int64("timestamp", bybitMsg.Timestamp).
				Msg("Received message")

			// Handle different message types
			switch bybitMsg.Type {
			case "snapshot", "delta":
				// Process trade data
				var trades []models.OptionTradeData
				if err := json.Unmarshal(bybitMsg.Data, &trades); err != nil {
					bs.logger.Error().
						Err(err).
						RawJSON("data", bybitMsg.Data).
						Msg("Trade data parse error")
					bs.setError(fmt.Sprintf("Trade data parse error: %v", err))
					continue
				}

				bs.logger.Debug().
					Int("trade_count", len(trades)).
					Msg("Processing trades")

				// Process each trade
				for _, trade := range trades {
					processed := models.ProcessedTrade{
						Symbol:        trade.Symbol,
						Price:         trade.Price,
						Size:          trade.Size,
						Side:          trade.Side,
						IV:            trade.IV,
						TradeTime:     time.Unix(0, trade.TradeTime*int64(time.Millisecond)),
						ProcessedTime: time.Now().UTC(),
						Source:        "bybit",
					}

					// Convert to JSON
					processedJSON, err := json.Marshal(processed)
					if err != nil {
						bs.logger.Error().
							Err(err).
							Interface("trade", processed).
							Msg("JSON marshal error")
						bs.setError(fmt.Sprintf("JSON marshal error: %v", err))
						continue
					}

					bs.output <- processedJSON
				}

				bs.lastMessage.Store(time.Now())
				bs.messagesCount.Add(1)
				bs.bytesReceived.Add(int64(len(message)))

			case "error":
				bs.logger.Error().
					RawJSON("message", message).
					Msg("Received error message from Bybit")
				bs.setError(fmt.Sprintf("Bybit error message: %s", string(message)))

			default:
				bs.logger.Warn().
					Str("type", bybitMsg.Type).
					RawJSON("message", message).
					Msg("Unknown message type")
			}
		}
	}
}

func (bs *BybitSource) disconnect() {
	bs.logger.Debug().Msg("Disconnecting from Bybit")
	bs.mu.Lock()
	if bs.conn != nil {
		bs.conn.Close()
		bs.conn = nil
	}
	bs.mu.Unlock()

	bs.connected.Store(false)
	bs.setError("disconnected")
}

func (bs *BybitSource) setError(err string) {
	bs.logger.Error().Str("error", err).Msg("Source error occurred")
	bs.lastError.Store(err)
	bs.errorCount.Add(1)
}
