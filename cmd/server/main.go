package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/svirmi/websocket-skeleton/internal/config"
	"github.com/svirmi/websocket-skeleton/internal/storage"
	"github.com/svirmi/websocket-skeleton/internal/websocket"
)

type ShutdownStatus struct {
	ServerClosed bool
	HubClosed    bool
	DBClosed     bool
}

func main() {
	// Setup logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting WebSocket server...")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	// Create root context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	status := &ShutdownStatus{}

	// Initialize components
	hub := websocket.NewHub(
		cfg.MaxConnections,
		cfg.BufferSize,
		cfg.MaxMessageSize,
	)

	server := websocket.NewServer(
		cfg,
		hub,
	)

	store, err := storage.NewSQLiteStore(cfg.DatabasePath)
	if err != nil {
		log.Fatal("Failed to initialize database:", err)
	}

	// Start components
	go func() {
		log.Printf("Starting hub...")
		hub.Run(ctx)
	}()

	go func() {
		log.Printf("Starting server on %s:%s...", cfg.Host, cfg.WSPort)
		if err := server.Run(ctx); err != nil {
			log.Printf("Server error: %v", err)
			cancel() // Cancel context on server error
		}
	}()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	log.Printf("Received shutdown signal: %v", sig)

	// Start graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		cfg.ShutdownTimeout,
	)
	defer shutdownCancel()

	// Enable connection draining
	log.Println("Starting graceful shutdown...")
	server.StartDraining()

	// Shutdown components in order
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	} else {
		status.ServerClosed = true
		log.Println("Server shutdown completed")
	}

	if err := hub.Close(); err != nil {
		log.Printf("Hub closure error: %v", err)
	} else {
		status.HubClosed = true
		log.Println("Hub shutdown completed")
	}

	if err := store.Close(); err != nil {
		log.Printf("Database closure error: %v", err)
	} else {
		status.DBClosed = true
		log.Println("Database shutdown completed")
	}

	log.Printf("Final shutdown status: %+v", status)
}
