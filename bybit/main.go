package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// -------------------- CONFIG (Updated for Pipelining & Metrics) --------------------
const (
	BybitWSLinear      = "wss://stream.bybit.com/v5/public/linear"
	MaxSymbolsPerConn  = 40
	BatchSize          = 25
	BatchPause         = 300 * time.Millisecond
	RawBuffer          = 85000
	QueueWarnEvery     = 3 * time.Second
	ReadDeadline       = 60 * time.Second
	PingInterval       = 3 * time.Minute
	ReconnectBaseDelay = 2 * time.Second
	ReconnectMaxDelay  = 30 * time.Second

	// Redis Configuration
	RedisAddr     = "localhost:6379"
	RedisPassword = "" // Assuming no password for local setup
	RedisDB       = 0
	RedisChannel  = "exchange_raw:bybit" // Main raw data channel

	// Pipelining and Worker Settings
	RedisMaxRetries      = 3
	RedisPoolSize        = 10
	RedisPipelineSize    = 500                   // Max number of messages to batch before sending
	RedisPipelineTimeout = 50 * time.Millisecond // Max time to wait before flushing batch

	// Metrics Stream
	RedisMetricsStream = "monitor:telemetry:bybit"
)

// counters
var (
	dropCounter    int64
	publishCounter int64
	redisErrCount  int64 // Added error counter
)

// WaitGroups
var (
	readWG sync.WaitGroup
	// redisWG is now handled implicitly by the startWorkers caller
)

// -------------------- SafeWebSocket (hardened) --------------------
// thread-safe small wrapper around *websocket.Conn
type SafeWebSocket struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	closed atomic.Bool
}

func (s *SafeWebSocket) WriteJSON(v interface{}) error {
	if s == nil || s.closed.Load() {
		return fmt.Errorf("conn closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return fmt.Errorf("no conn")
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.conn.WriteJSON(v)
}

func (s *SafeWebSocket) WriteControl(messageType int, data []byte, deadline time.Time) error {
	if s == nil || s.closed.Load() {
		return fmt.Errorf("conn closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return fmt.Errorf("no conn")
	}
	return s.conn.WriteControl(messageType, data, deadline)
}

func (s *SafeWebSocket) Close() error {
	if s == nil {
		return nil
	}
	s.closed.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

func (s *SafeWebSocket) SetWriteDeadline(t time.Time) error {
	if s == nil || s.closed.Load() {
		return fmt.Errorf("conn closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return fmt.Errorf("no conn")
	}
	return s.conn.SetWriteDeadline(t)
}

func (s *SafeWebSocket) SetReadDeadline(t time.Time) error {
	if s == nil || s.closed.Load() {
		return fmt.Errorf("conn closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return fmt.Errorf("no conn")
	}
	return s.conn.SetReadDeadline(t)
}

func (s *SafeWebSocket) ReadMessage() (int, []byte, error) {
	if s == nil || s.closed.Load() {
		return 0, nil, fmt.Errorf("conn closed")
	}
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c == nil {
		return 0, nil, fmt.Errorf("no conn")
	}
	return c.ReadMessage()
}

// -------------------- Messages and Publisher --------------------
type RawWSMessage struct{ Data []byte }

type RedisPublisher struct {
	client  *redis.Client
	channel string
}

func NewRedisPublisher(addr, password, channel string, db int) (*RedisPublisher, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		MaxRetries:   RedisMaxRetries,
		PoolSize:     RedisPoolSize,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connection failed: %w", err)
	}

	log.Printf("Redis connected successfully to %s", addr)
	return &RedisPublisher{
		client:  client,
		channel: channel,
	}, nil
}

// PublishMetrics sends internal telemetry to a Redis Stream (MaxLen keeps size manageable)
func (rp *RedisPublisher) PublishMetrics(ctx context.Context, stream string, values map[string]interface{}) error {
	return rp.client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: values,
		MaxLen: 3600,
	}).Err()
}

func (rp *RedisPublisher) Close() error {
	return rp.client.Close()
}

// flushPipeline executes the Redis pipeline and handles success/error counts.
func flushPipeline(ctx context.Context, pipe redis.Pipeliner, batchSize int) error {
	if batchSize == 0 {
		return nil
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		atomic.AddInt64(&redisErrCount, int64(batchSize))
		return fmt.Errorf("redis pipeline execution failed for batch size %d: %w", batchSize, err)
	}

	atomic.AddInt64(&publishCounter, int64(batchSize))
	return nil
}

// -------------------- MAIN --------------------
func main() {
	log.Println("Bybit DUMB RAW reader starting (PIPELINED MODE)")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize Redis Publisher
	publisher, err := NewRedisPublisher(RedisAddr, RedisPassword, RedisChannel, RedisDB)
	if err != nil {
		log.Fatalf("failed to initialize redis: %v", err)
	}
	defer publisher.Close()

	// fetch symbols
	symbols, err := symbols()
	if err != nil {
		log.Fatal("Failed to fetch symbols:", err)
	}
	log.Printf("Loaded %d symbols", len(symbols))

	// jitter startup
	rand.Shuffle(len(symbols), func(i, j int) { symbols[i], symbols[j] = symbols[j], symbols[i] })

	rawChan := make(chan RawWSMessage, RawBuffer)

	var redisWG sync.WaitGroup

	// Start Pipelined Workers
	startWorkers(ctx, rawChan, publisher, &redisWG)

	// monitoring
	go monitorQueue(rawChan)
	go printStats()
	go streamMetricsToRedis(ctx, rawChan, publisher) // Start metrics streamer

	var shardWg sync.WaitGroup

	// manage shards
	for i := 0; i < len(symbols); i += MaxSymbolsPerConn {
		end := i + MaxSymbolsPerConn
		if end > len(symbols) {
			end = len(symbols)
		}
		chunk := symbols[i:end]
		shardID := i / MaxSymbolsPerConn

		// jittered startup (200-800ms)
		sleepWithContext(ctx, time.Duration(rand.Intn(600)+200)*time.Millisecond)

		shardWg.Add(1)
		go func(chunk []string, shardID int) {
			defer shardWg.Done()
			manageConnection(ctx, rawChan, chunk, shardID)
		}(chunk, shardID)
	}

	// signal handling
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// wait for signal
	<-sig
	log.Println("Shutting down...")

	// 1) cancel to instruct managers/readers to stop
	cancel()

	// 2) wait for all shard managers to exit (they spawn read loops)
	shardWg.Wait()

	// 3) wait for all readLoop goroutines to exit before closing rawChan
	readWG.Wait()

	// 4) now safe to close rawChan
	close(rawChan)

	// 5) wait for redis workers to drain
	redisWG.Wait()

	log.Printf("FINAL: published=%d dropped=%d redis_errors=%d",
		atomic.LoadInt64(&publishCounter),
		atomic.LoadInt64(&dropCounter),
		atomic.LoadInt64(&redisErrCount))
	log.Println("Bybit reader stopped cleanly")
}

// -------------------- Pipelined Worker Implementation --------------------

// startWorkers sets up goroutines to read from rawChan and publish in optimized Redis pipelines.
func startWorkers(ctx context.Context, rawChan <-chan RawWSMessage, publisher *RedisPublisher, wg *sync.WaitGroup) {
	workerCount := runtime.NumCPU() * 2
	wg.Add(workerCount)

	for i := 0; i < workerCount; i++ {
		go func(id int) {
			defer wg.Done()

			// Worker-local buffer and timer
			var bufferSize int
			pipe := publisher.client.Pipeline()
			ticker := time.NewTicker(RedisPipelineTimeout)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					// On shutdown, flush any remaining messages
					flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = flushPipeline(flushCtx, pipe, bufferSize)
					return

				case <-ticker.C:
					// Flush if timeout reached
					if bufferSize > 0 {
						_ = flushPipeline(ctx, pipe, bufferSize)
						bufferSize = 0 // Reset buffer size after flush
						pipe.Discard() // Re-initialize pipeline
						pipe = publisher.client.Pipeline()
					}

				case raw, ok := <-rawChan:
					if !ok {
						// Channel closed, flush and exit
						flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						_ = flushPipeline(flushCtx, pipe, bufferSize)
						return
					}

					// Add the raw publish command to the pipeline directly
					pipe.Publish(ctx, publisher.channel, raw.Data)
					bufferSize++

					// Flush if size limit reached
					if bufferSize >= RedisPipelineSize {
						_ = flushPipeline(ctx, pipe, bufferSize)
						bufferSize = 0 // Reset buffer size after flush
						pipe.Discard() // Re-initialize pipeline
						pipe = publisher.client.Pipeline()
						ticker.Reset(RedisPipelineTimeout) // Reset timer after a size-based flush
					}
				}
			}
		}(i)
	}
}

// -------------------- WS Manager & Helpers --------------------
func manageConnection(ctx context.Context, rawChan chan<- RawWSMessage, symbols []string, shardID int) {
	prefix := fmt.Sprintf("[Shard%d]", shardID)
	delay := ReconnectBaseDelay

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := dialBybit()
		if err != nil {
			log.Printf("%s dial attempt failed (transient): %v", prefix, err)
			delay = jitteredDelay(delay)
			sleepWithContext(ctx, delay)
			continue
		}

		log.Printf("%s connected — subscribing %d symbols", prefix, len(symbols))
		delay = ReconnectBaseDelay

		if err := fetchAndSubscribeBybit(conn, symbols, prefix); err != nil {
			_ = conn.Close()
			log.Printf("%s subscribe failed: %v", prefix, err)
			delay = jitteredDelay(delay)
			sleepWithContext(ctx, delay)
			continue
		}

		// read loop: send errors to channel; readLoop itself is accounted in readWG
		readErr := make(chan error, 1)
		readWG.Add(1)
		go func() {
			defer readWG.Done()
			readErr <- readLoop(ctx, conn, rawChan, prefix)
		}()

		pingTicker := time.NewTicker(PingInterval)

	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			case <-pingTicker.C:
				payload := []byte(strconv.FormatInt(time.Now().UnixMilli(), 10))
				if err := conn.WriteControl(websocket.PingMessage, payload, time.Now().Add(5*time.Second)); err != nil {
					log.Printf("%s ping failed: %v", prefix, err)
					break loop
				}
			case err := <-readErr:
				if err != nil {
					log.Printf("%s read error: %v", prefix, err)
				} else {
					log.Printf("%s read loop ended cleanly", prefix)
				}
				break loop
			}
		}

		pingTicker.Stop()
		_ = conn.Close()

		select {
		case <-ctx.Done():
			return
		default:
		}

		delay = jitteredDelay(delay)
		sleepWithContext(ctx, delay)
	}
}

// dialBybit returns a SafeWebSocket with handlers configured
func dialBybit() (*SafeWebSocket, error) {
	d := websocket.Dialer{HandshakeTimeout: 20 * time.Second}
	conn, _, err := d.Dial(BybitWSLinear, nil)
	if err != nil {
		return nil, err
	}

	s := &SafeWebSocket{conn: conn}
	_ = s.SetReadDeadline(time.Now().Add(ReadDeadline))

	conn.SetPongHandler(func(appData string) error {
		_ = s.SetReadDeadline(time.Now().Add(ReadDeadline))
		return nil
	})

	conn.SetPingHandler(func(msg string) error {
		return s.WriteControl(
			websocket.PongMessage,
			[]byte(msg),
			time.Now().Add(5*time.Second),
		)
	})

	return s, nil
}

func fetchAndSubscribeBybit(conn *SafeWebSocket, symbols []string, prefix string) error {
	log.Printf("%s subscribing to %d symbols...", prefix, len(symbols))

	params := make([]string, len(symbols))
	for i, s := range symbols {
		params[i] = "orderbook.1." + s
	}

	for i := 0; i < len(params); i += BatchSize {
		end := i + BatchSize
		if end > len(params) {
			end = len(params)
		}
		batch := params[i:end]

		msg := map[string]any{"op": "subscribe", "args": batch}

		if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		if err := conn.WriteJSON(msg); err != nil {
			return err
		}

		time.Sleep(BatchPause)
	}

	return nil
}

// readLoop now uses non-blocking send logic (select/default)
// to ensure the high-speed reader does not block, even if the queue is full.
func readLoop(ctx context.Context, conn *SafeWebSocket, ch chan<- RawWSMessage, prefix string) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(ReadDeadline))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		if looksLikeControlMessage(data) {
			continue
		}

		// NON-BLOCKING SEND: The worker (publisher) should be fast enough.
		// If the channel is full, we drop the message and count it.
		// This ensures the WebSocket reader never blocks and misses data.
		select {
		case <-ctx.Done():
			return nil
		case ch <- RawWSMessage{Data: data}:
			// Message sent successfully
		default:
			// Channel full, message dropped.
			atomic.AddInt64(&dropCounter, 1)
			// NOTE: The previous version's blocking sleep logic was removed here.
		}
	}
}

// streamMetricsToRedis collects and sends app health data to Redis every second
func streamMetricsToRedis(ctx context.Context, rawChan chan RawWSMessage, publisher *RedisPublisher) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var lastPub, lastDrop int64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			currPub := atomic.LoadInt64(&publishCounter)
			currDrop := atomic.LoadInt64(&dropCounter)
			currErr := atomic.LoadInt64(&redisErrCount)

			// Calculate rates (per second)
			pubRate := currPub - lastPub
			dropRate := currDrop - lastDrop

			lastPub = currPub
			lastDrop = currDrop

			metrics := map[string]interface{}{
				"rate_published": pubRate,
				"rate_dropped":   dropRate,
				"total_errors":   currErr,
				"buffer_len":     len(rawChan),
				"goroutines":     runtime.NumGoroutine(),
				"ts":             time.Now().UnixMilli(),
			}

			if err := publisher.PublishMetrics(ctx, RedisMetricsStream, metrics); err != nil {
				log.Printf("METRICS ERROR: failed to send to redis: %v", err)
			}
		}
	}
}

// -------------------- utils --------------------
func jitteredDelay(d time.Duration) time.Duration {
	jitter := time.Duration(rand.Intn(1000)-500) * time.Millisecond
	n := d + jitter
	if n < ReconnectBaseDelay {
		n = ReconnectBaseDelay
	}
	if n > ReconnectMaxDelay {
		n = ReconnectMaxDelay
	}
	return n
}

func sleepWithContext(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func looksLikeControlMessage(b []byte) bool {
	return bytes.Contains(b, []byte("\"op\"")) || bytes.Contains(b, []byte("\"success\""))
}

func printStats() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	last := int64(0)
	for range t.C {
		p := atomic.LoadInt64(&publishCounter)
		d := atomic.LoadInt64(&dropCounter)
		e := atomic.LoadInt64(&redisErrCount)
		rate := float64(p-last) / 10
		log.Printf("BYBIT RAW: published=%d (%.1f/s) dropped=%d redis_errors=%d", p, rate, d, e)
		last = p
	}
}

func monitorQueue(ch chan RawWSMessage) {
	t := time.NewTicker(QueueWarnEvery)
	defer t.Stop()
	for range t.C {
		if n := len(ch); n > RawBuffer/2 {
			log.Printf("WARNING: queue high %d/%d", n, RawBuffer)
		}
	}
}

// -------------------- symbols fetch --------------------
func symbols() ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://152.70.55.44:8080/intersection/futures")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)

	var apiResp struct {
		Symbols []struct {
			Symbol string `json:"symbol"`
		} `json:"symbols"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, err
	}

	excluded := map[string]struct{}{
		"ETHUSDT": {}, "SOLUSDT": {}, "BTCUSDT": {}, "XRPUSDT": {}, "BNBUSDT": {},
		"DOGEUSDT": {}, "LTCUSDT": {}, "ADAUSDT": {}, "ATOMUSDT": {}, "SUIUSDT": {},
		"HYPEUSDT": {}, "LINKUSDT": {},
	}

	var list []string
	for _, s := range apiResp.Symbols {
		if _, ex := excluded[s.Symbol]; !ex {
			list = append(list, s.Symbol)
		}
	}
	return list, nil
}
