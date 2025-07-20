package websocket

import (
	"context"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

type Hub struct {
	Clients    map[*Client]bool
	Broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	logger     zerolog.Logger
}

func NewHub(maxConnections int, bufferSize int, maxMessageSize int64) *Hub {
	return &Hub{
		Broadcast:  make(chan []byte, bufferSize),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		Clients:    make(map[*Client]bool),
		logger:     log.With().Str("component", "hub").Logger(),
	}
}

func (h *Hub) Run(ctx context.Context) {
	h.logger.Info().Msg("Starting WebSocket hub")

	for {
		select {
		case <-ctx.Done():
			h.logger.Info().Msg("Shutting down WebSocket hub")
			return

		case client := <-h.register:
			h.Clients[client] = true
			h.logger.Info().
				Int("total_clients", len(h.Clients)).
				Msg("Client registered")

		case client := <-h.unregister:
			if _, ok := h.Clients[client]; ok {
				delete(h.Clients, client)
				close(client.send)
				h.logger.Info().
					Int("total_clients", len(h.Clients)).
					Msg("Client unregistered")
			}

		case message := <-h.Broadcast:
			for client := range h.Clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.Clients, client)
					h.logger.Warn().
						Int("total_clients", len(h.Clients)).
						Msg("Removed slow client")
				}
			}
		}
	}
}

func (h *Hub) ClientCount() int {
	return len(h.Clients)
}

func (h *Hub) SendBroadcast(msg []byte) {
	h.Broadcast <- msg
}
