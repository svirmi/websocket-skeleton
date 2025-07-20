package websocket

import (
	"context"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/svirmi/websocket-skeleton/internal/config"
	"github.com/svirmi/websocket-skeleton/internal/logger"
)

var log = logger.GetLogger("websocket_server")

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins in development
	},
}

type Server struct {
	cfg    *config.Config
	hub    *Hub
	server *http.Server
}

func NewServer(cfg *config.Config, hub *Hub) *Server {
	return &Server{
		cfg: cfg,
		hub: hub,
	}
}

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	// WebSocket endpoint
	mux.HandleFunc("/ws", s.handleWebSocket)

	// Health check endpoint
	mux.HandleFunc("/health", s.handleHealth)

	s.server = &http.Server{
		Addr:    s.cfg.WSPort,
		Handler: mux,
	}

	log.Info().Str("addr", s.cfg.WSPort).Msg("Starting WebSocket server")

	go s.hub.Run(ctx)

	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("Failed to upgrade connection")
		return
	}

	client := &Client{
		hub:  s.hub,
		conn: conn,
		send: make(chan []byte, s.cfg.BufferSize),
	}

	s.hub.register <- client

	go client.writePump()
	go client.readPump()

	log.Info().
		Str("remote_addr", conn.RemoteAddr().String()).
		Msg("New WebSocket connection established")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (s *Server) Shutdown(ctx context.Context) error {
	log.Info().Msg("Shutting down WebSocket server")
	return s.server.Shutdown(ctx)
}
