package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/svirmi/websocket-skeleton/internal/broadcast"
	"github.com/svirmi/websocket-skeleton/internal/config"
	"github.com/svirmi/websocket-skeleton/internal/ingestion"
	"github.com/svirmi/websocket-skeleton/internal/logger"
	"github.com/svirmi/websocket-skeleton/internal/models"
	"github.com/svirmi/websocket-skeleton/internal/processor"
	"github.com/svirmi/websocket-skeleton/internal/websocket"
)

const (
	AppVersion = "1.0.0"
	StartTime  = "2025-07-20 10:15:15" // UTC
	Author     = "svirmi"
)

func main() {
	startupMsg := fmt.Sprintf("Starting application v%s at %s by %s", AppVersion, StartTime, Author)

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load config")
	}

	// Initialize logger
	logger.Init(cfg.Environment)

	log.Info().
		Str("version", AppVersion).
		Str("environment", cfg.Environment).
		Msg(startupMsg)

	// Create root context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize services
	ingestionService := ingestion.NewDataIngestionService(cfg.BufferSize)

	// Create Bybit source
	bybitSource := ingestion.NewBybitSource(
		"bybit_options",
		cfg.BybitSymbols,
		cfg.BufferSize,
		cfg.MaxMessageSize,
	)

	// Add source to ingestion service
	if err := ingestionService.AddSource(bybitSource); err != nil {
		log.Fatal().Err(err).Msg("Failed to add Bybit source")
	}

	log.Info().
		Strs("symbols", cfg.BybitSymbols).
		Int("buffer_size", cfg.BufferSize).
		Int64("max_message_size", cfg.MaxMessageSize).
		Msg("Configured Bybit source")

	// Initialize processing pipeline
	pipeline := processor.NewPipeline(cfg.BufferSize, func(err error) {
		log.Error().Err(err).Msg("Pipeline error")
	})

	// Add processing stages
	pipeline.AddStage(&processor.ValidationStage{})
	pipeline.AddStage(&processor.EnrichmentStage{})
	pipeline.AddStage(&processor.TransformationStage{})

	log.Info().
		Int("workers", cfg.ProcessingWorkers).
		Msg("Configured processing pipeline")

	// Initialize broadcast service
	broadcastService := broadcast.NewBroadcastService(cfg.BufferSize)

	// Initialize WebSocket hub and server
	hub := websocket.NewHub(cfg.MaxConnections, cfg.BufferSize, cfg.MaxMessageSize)
	wsServer := websocket.NewServer(cfg, hub)

	// Connect components
	go func() {
		log.Info().Msg("Starting message monitoring")
		for msg := range ingestionService.GetOutput() {
			var trade models.ProcessedTrade
			if err := json.Unmarshal(msg, &trade); err != nil {
				log.Error().Err(err).Msg("Failed to parse trade message")
				continue
			}
			log.Debug().
				Str("symbol", trade.Symbol).
				Float64("price", trade.Price).
				Float64("size", trade.Size).
				Str("side", trade.Side).
				Time("trade_time", trade.TradeTime).
				Msg("Received trade")
		}
	}()

	go func() {
		log.Info().Msg("Starting message forwarding from pipeline to broadcast")
		for msg := range pipeline.GetOutput() {
			select {
			case broadcastService.GetInput() <- msg:
				log.Debug().
					Int("size", len(msg)).
					Msg("Forwarded message to broadcast")
			case <-ctx.Done():
				return
			}
		}
	}()

	// Connect broadcast service to hub
	go func() {
		log.Info().Msg("Starting message forwarding from broadcast to WebSocket hub")
		for msg := range broadcastService.GetOutput() {
			select {
			case hub.Broadcast <- msg: // Direct channel send
				log.Debug().
					Int("size", len(msg)).
					Int("clients", hub.ClientCount()).
					Msg("Broadcasted message to WebSocket clients")
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start services
	if err := ingestionService.Start(); err != nil {
		log.Fatal().Err(err).Msg("Failed to start ingestion service")
	}
	log.Info().Msg("Started ingestion service")

	// Monitor source status
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				status, err := ingestionService.GetSourceStatus("bybit_options")
				if err != nil {
					log.Error().Err(err).Msg("Failed to get source status")
					continue
				}
				log.Info().
					Bool("connected", status.Connected).
					Time("last_message", status.LastMessage).
					Int64("messages", status.MessagesCount).
					Int64("bytes", status.BytesReceived).
					Int64("errors", status.Errors).
					Str("last_error", status.LastError).
					Msg("Bybit source status")
			}
		}
	}()

	pipeline.Start(cfg.ProcessingWorkers)
	log.Info().Msg("Started processing pipeline")

	broadcastService.Start()
	log.Info().Msg("Started broadcast service")

	// Start WebSocket server
	go func() {
		log.Info().Str("port", cfg.WSPort).Msg("Starting WebSocket server")
		if err := wsServer.Run(ctx); err != nil {
			log.Error().Err(err).Msg("WebSocket server error")
		}
	}()

	// Print connection information
	log.Info().
		Str("ws_url", fmt.Sprintf("ws://localhost%s/ws", cfg.WSPort)).
		Str("health_url", fmt.Sprintf("http://localhost%s/health", cfg.WSPort)).
		Msg("Server endpoints available")

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	log.Info().
		Str("signal", sig.String()).
		Msg("Received shutdown signal")

	// Stop all components in reverse order
	log.Info().Msg("Initiating graceful shutdown")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	// Shutdown sequence
	wsServer.Shutdown(shutdownCtx)
	log.Info().Msg("Stopped WebSocket server")

	broadcastService.Stop()
	log.Info().Msg("Stopped broadcast service")

	pipeline.Stop()
	log.Info().Msg("Stopped processing pipeline")

	ingestionService.Stop()
	log.Info().Msg("Stopped ingestion service")

	select {
	case <-shutdownCtx.Done():
		log.Warn().Msg("Shutdown timeout exceeded")
	default:
		log.Info().Msg("Graceful shutdown completed")
	}
}
