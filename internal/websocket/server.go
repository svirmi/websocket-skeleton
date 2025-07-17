package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/svirmi/websocket-skeleton/internal/config"
	"github.com/svirmi/websocket-skeleton/internal/processor"
)

type Server struct {
	port      string
	host      string
	hub       *Hub
	upgrader  websocket.Upgrader
	processor *processor.MessageProcessor
	startTime time.Time
	config    *config.Config

	// Protected server state
	mu           sync.RWMutex
	server       *http.Server
	shuttingDown atomic.Int32
}

func NewServer(cfg *config.Config, hub *Hub) *Server {
	s := &Server{
		port: cfg.WSPort,
		host: cfg.Host,
		hub:  hub,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				// TODO: Implement proper origin check in production
				return true
			},
		},
		processor: processor.NewMessageProcessor(cfg.MaxMessageSize),
		startTime: time.Now(),
		config:    cfg,
	}
	return s
}

func (s *Server) Run(ctx context.Context) error {
	// Create mux and setup routes
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)

	addr := fmt.Sprintf("%s%s", s.host, s.port)

	// Create new server instance
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadTimeout:       s.config.ReadTimeout,
		WriteTimeout:      s.config.WriteTimeout,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Safely store server instance
	s.mu.Lock()
	s.server = srv
	s.mu.Unlock()

	// Channel for server errors
	serverErr := make(chan error, 1)

	// Start server in a goroutine
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- fmt.Errorf("server error: %w", err)
		}
	}()

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
		return s.Shutdown(context.Background())
	case err := <-serverErr:
		return err
	}
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if s.shuttingDown.Load() == 1 {
		http.Error(w, "Server is shutting down", http.StatusServiceUnavailable)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "Could not upgrade connection", http.StatusInternalServerError)
		return
	}

	client := NewClient(
		s.hub,
		conn,
		s.config.MaxMessageSize,
		s.processor,
		s.config.BufferSize,
	)

	s.hub.register <- client

	go client.WritePump(
		s.config.WriteTimeout,
		s.config.PingInterval,
	)
	go client.ReadPump(s.config.PongWait)
}

func (s *Server) Shutdown(ctx context.Context) error {
	// Mark server as shutting down first
	s.StartDraining()

	// Get current server instance safely
	s.mu.RLock()
	srv := s.server
	s.mu.RUnlock()

	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

func (s *Server) StartDraining() {
	// Use atomic operation for shutting down flag
	if !s.shuttingDown.CompareAndSwap(0, 1) {
		return // Already shutting down
	}

	s.hub.SetDraining(true)

	// Get current server instance safely
	s.mu.RLock()
	srv := s.server
	s.mu.RUnlock()

	if srv != nil {
		srv.SetKeepAlivesEnabled(false)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	metrics := s.hub.GetMetrics()

	status := "healthy"
	if s.shuttingDown.Load() == 1 {
		status = "shutting_down"
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	health := struct {
		Status       string    `json:"status"`
		Uptime       string    `json:"uptime"`
		StartTime    time.Time `json:"start_time"`
		Connections  int64     `json:"connections"`
		MessageCount int64     `json:"message_count"`
		Environment  string    `json:"environment"`
	}{
		Status:       status,
		Uptime:       time.Since(s.startTime).String(),
		StartTime:    s.startTime,
		Connections:  metrics.ActiveConnections,
		MessageCount: metrics.MessagesProcessed,
		Environment:  s.config.Environment,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	metrics := s.hub.GetMetrics()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}
