package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/svirmi/websocket-skeleton/internal/broadcast"
	"github.com/svirmi/websocket-skeleton/internal/config"
	"github.com/svirmi/websocket-skeleton/internal/ingestion"
	"github.com/svirmi/websocket-skeleton/internal/processor"
	"github.com/svirmi/websocket-skeleton/internal/websocket"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	// Create root context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize services
	ingestionService := ingestion.NewDataIngestionService(cfg.BufferSize)

	// Add WebSocket sources
	sources := []struct {
		id  string
		url string
	}{
		{"source1", "ws://source1.example.com/ws"},
		{"source2", "ws://source2.example.com/ws"},
		// Add more sources as needed
	}

	for _, s := range sources {
		source := ingestion.NewWebSocketSource(s.id, s.url, cfg.BufferSize, cfg.MaxMessageSize)
		if err := ingestionService.AddSource(source); err != nil {
			log.Printf("Failed to add source %s: %v", s.id, err)
			continue
		}
	}

	// Initialize processing pipeline
	pipeline := processor.NewPipeline(cfg.BufferSize, func(err error) {
		log.Printf("Pipeline error: %v", err)
	})

	// Add processing stages
	pipeline.AddStage(&processor.ValidationStage{})
	pipeline.AddStage(&processor.EnrichmentStage{})
	pipeline.AddStage(&processor.TransformationStage{})

	// Initialize broadcast service
	broadcastService := broadcast.NewBroadcastService(cfg.BufferSize)

	// Initialize WebSocket hub and server
	hub := websocket.NewHub(cfg.MaxConnections, cfg.BufferSize, cfg.MaxMessageSize)
	server := websocket.NewServer(cfg, hub)

	// Connect components
	go func() {
		for msg := range ingestionService.GetOutput() {
			pipeline.GetInput() <- msg
		}
	}()

	go func() {
		for msg := range pipeline.GetOutput() {
			broadcastService.GetInput() <- msg
		}
	}()

	// Start services
	if err := ingestionService.Start(); err != nil {
		log.Fatal("Failed to start ingestion service:", err)
	}

	// Start pipeline with configured number of workers
	pipeline.Start(cfg.ProcessingWorkers)
	broadcastService.Start()

	go func() {
		if err := server.Run(ctx); err != nil {
			log.Printf("Server error: %v", err)
		}
	}()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	log.Println("Shutting down...")

	// Stop all components in reverse order
	server.Shutdown(ctx)
	broadcastService.Stop()
	pipeline.Stop()
	ingestionService.Stop()
}
