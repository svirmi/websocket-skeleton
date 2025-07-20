package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/svirmi/websocket-skeleton/internal/models"
)

type BybitSource struct {
	*WebSocketSource
	subscribedSymbols []string
}

func NewBybitSource(id string, symbols []string, bufferSize int, maxMessageSize int64) *BybitSource {
	return &BybitSource{
		WebSocketSource: NewWebSocketSource(
			id,
			"wss://stream-testnet.bybit.com/v5/trade/option",
			bufferSize,
			maxMessageSize,
		),
		subscribedSymbols: symbols,
	}
}

// Override Start to handle Bybit-specific connection logic
func (bs *BybitSource) Start(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				bs.disconnect()
				return
			default:
				if err := bs.connectAndSubscribe(&dialer); err != nil {
					bs.setError(err.Error())
					time.Sleep(bs.reconnectInterval)
					continue
				}

				bs.readPump(ctx)
			}
		}
	}()

	return nil
}

// New method to handle connection and subscription
func (bs *BybitSource) connectAndSubscribe(dialer *websocket.Dialer) error {
	// First connect using the parent's connect logic
	conn, _, err := dialer.Dial(bs.url, nil)
	if err != nil {
		return err
	}

	bs.mu.Lock()
	bs.conn = conn
	bs.mu.Unlock()

	bs.connected.Store(true)

	// Subscribe to trade topics
	subMsg := models.SubscribeMessage{
		ReqID: fmt.Sprintf("sub_%d", time.Now().Unix()),
		Op:    "subscribe",
		Args:  make([]string, len(bs.subscribedSymbols)),
	}

	for i, symbol := range bs.subscribedSymbols {
		subMsg.Args[i] = fmt.Sprintf("trade.%s", symbol)
	}

	bs.mu.RLock()
	defer bs.mu.RUnlock()

	if err := bs.conn.WriteJSON(subMsg); err != nil {
		bs.disconnect()
		return fmt.Errorf("subscription failed: %w", err)
	}

	return nil
}

// Override readPump to handle Bybit-specific message processing
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
				bs.setError(err.Error())
				return
			}

			// Parse the message
			var bybitMsg models.BybitMessage
			if err := json.Unmarshal(message, &bybitMsg); err != nil {
				bs.setError(fmt.Sprintf("JSON parse error: %v", err))
				continue
			}

			// Handle different message types
			switch bybitMsg.Type {
			case "snapshot", "delta":
				var trades []models.OptionTradeData
				if err := json.Unmarshal(bybitMsg.Data, &trades); err != nil {
					bs.setError(fmt.Sprintf("Trade data parse error: %v", err))
					continue
				}

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

					processedJSON, err := json.Marshal(processed)
					if err != nil {
						bs.setError(fmt.Sprintf("JSON marshal error: %v", err))
						continue
					}

					bs.output <- processedJSON
				}

				bs.lastMessage.Store(time.Now())
				bs.messagesCount.Add(1)
				bs.bytesReceived.Add(int64(len(message)))

			case "error":
				bs.setError(fmt.Sprintf("Bybit error message: %s", string(message)))

			default:
				// Ignore non-trade messages (like pong responses)
				continue
			}
		}
	}
}

func (bs *BybitSource) Status() SourceStatus {
	return SourceStatus{
		Connected:     bs.connected.Load(),
		LastMessage:   bs.lastMessage.Load().(time.Time),
		MessagesCount: bs.messagesCount.Load(),
		BytesReceived: bs.bytesReceived.Load(),
		Errors:        bs.errorCount.Load(),
		LastError:     bs.lastError.Load().(string),
	}
}
