// File: binance_dumb_l1_reader.go
// Simple, sharded Binance L1 (bookTicker) reader -> normalized JSON -> Redis PUBLISH
package main

import (
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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// ---------------- CONFIG ----------------
const (
	BaseWSHost         = "wss://fstream.binance.com/stream"
	MaxSymbolsPerConn  = 40
	RawBuffer          = 32768
	ReadDeadline       = 60 * time.Second
	PingInterval       = 3 * time.Minute
	ReconnectBaseDelay = 2 * time.Second
	ReconnectMaxDelay  = 30 * time.Second

	RedisAddr     = "localhost:6379"
	RedisPassword = ""
	RedisDB       = 0
	RedisChannel  = "binance_orderbook"

	RedisMetricsStream = "monitor:telemetry:binance"

	SymbolsFetchURL = "http://152.70.55.44:8080/intersection/futures"
)

// ---------------- Types ----------------
type NormalizedTicker struct {
	Exchange  string  `json:"exchange"`
	Symbol    string  `json:"s"`
	BestBidP  float64 `json:"bp"`
	BestBidQ  float64 `json:"bq"`
	BestAskP  float64 `json:"ap"`
	BestAskQ  float64 `json:"aq"`
	TradeTime int64   `json:"t"` // exchange trade time if provided
	RecvTime  int64   `json:"r"`
	EventType string  `json:"o"`
}

// ---------------- Globals ----------------
var (
	dropCounter    int64
	publishedCount int64
	redisErrCount  int64
	wgReaders      sync.WaitGroup
)

// ---------------- SafeWS (simple wrapper) ----------------
type SafeWS struct {
	mu   sync.Mutex
	conn *websocket.Conn
	// closed flag not strictly required; close sets conn=nil under lock
}

func (s *SafeWS) set(conn *websocket.Conn) {
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
}

func (s *SafeWS) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

func (s *SafeWS) readMessage() (int, []byte, error) {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c == nil {
		return 0, nil, fmt.Errorf("no conn")
	}
	return c.ReadMessage()
}

func (s *SafeWS) writeControl(mt int, data []byte, deadline time.Time) error {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c == nil {
		return fmt.Errorf("no conn")
	}
	return c.WriteControl(mt, data, deadline)
}

func (s *SafeWS) setReadDeadline(t time.Time) error {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c == nil {
		return fmt.Errorf("no conn")
	}
	return c.SetReadDeadline(t)
}

// ---------------- Redis helper ----------------
func newRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     RedisAddr,
		Password: RedisPassword,
		DB:       RedisDB,
	})
}

// ---------------- Utilities ----------------
func jitteredDelay(d time.Duration) time.Duration {
	j := time.Duration(rand.Int63n(int64(1000))*int64(time.Millisecond)) - 500*time.Millisecond
	n := d + j
	if n < ReconnectBaseDelay {
		n = ReconnectBaseDelay
	}
	if n > ReconnectMaxDelay {
		n = ReconnectMaxDelay
	}
	return n
}

func sleepWithCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// ---------------- Symbols fetch ----------------
func symbols() ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(SymbolsFetchURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("symbols API returned %d", resp.StatusCode)
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
	out := make([]string, 0, len(apiResp.Symbols))
	for _, s := range apiResp.Symbols {
		if _, ex := excluded[s.Symbol]; !ex {
			out = append(out, s.Symbol)
		}
	}
	return out, nil
}

// ---------------- Normalization (per-message) ----------------
func parseAndNormalize(data []byte) (*NormalizedTicker, error) {
	// Try multiplex wrapper {"stream":"...","data":{...}}
	var wrapper struct {
		Stream string          `json:"stream"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil && len(wrapper.Data) > 0 {
		return normalizeFromPayload(wrapper.Data)
	}
	// fallback to direct payload
	return normalizeFromPayload(data)
}

func normalizeFromPayload(payload []byte) (*NormalizedTicker, error) {
	// Generic approach: unmarshal into map to check presence of required fields
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, err
	}
	// Need symbol (s), bid price (b), ask price (a). Qty fields B and A or missing allowed.
	var sSym, sBid, sAsk string
	var sBqty, sAqty string
	var tt int64

	if v, ok := m["s"]; ok {
		_ = json.Unmarshal(v, &sSym)
	}
	if v, ok := m["b"]; ok {
		_ = json.Unmarshal(v, &sBid)
	}
	if v, ok := m["a"]; ok {
		_ = json.Unmarshal(v, &sAsk)
	}
	if v, ok := m["B"]; ok {
		_ = json.Unmarshal(v, &sBqty)
	}
	if v, ok := m["A"]; ok {
		_ = json.Unmarshal(v, &sAqty)
	}
	// Trade/event time: prefer "T" then "E"
	if v, ok := m["T"]; ok {
		_ = json.Unmarshal(v, &tt)
	} else if v, ok := m["E"]; ok {
		_ = json.Unmarshal(v, &tt)
	}

	if sSym == "" || sBid == "" || sAsk == "" {
		return nil, fmt.Errorf("missing fields")
	}

	bp, err := strconv.ParseFloat(sBid, 64)
	if err != nil {
		return nil, fmt.Errorf("bad bid: %w", err)
	}
	ap, err := strconv.ParseFloat(sAsk, 64)
	if err != nil {
		return nil, fmt.Errorf("bad ask: %w", err)
	}
	var bq, aq float64
	if sBqty != "" {
		if v, err := strconv.ParseFloat(sBqty, 64); err == nil {
			bq = v
		}
	}
	if sAqty != "" {
		if v, err := strconv.ParseFloat(sAqty, 64); err == nil {
			aq = v
		}
	}

	return &NormalizedTicker{
		Exchange:  "binance",
		Symbol:    sSym,
		BestBidP:  bp,
		BestBidQ:  bq,
		BestAskP:  ap,
		BestAskQ:  aq,
		TradeTime: tt,
		RecvTime:  time.Now().UnixMilli(),
		EventType: "bookTicker",
	}, nil
}

// ---------------- Shard manager (multiplexed streams in URL) ----------------
func manageShard(ctx context.Context, redisClient *redis.Client, symbols []string, shardID int) {
	defer log.Printf("[Shard%d] exiting", shardID)

	prefix := fmt.Sprintf("[Shard%d]", shardID)
	delay := ReconnectBaseDelay

	// Lowercase streams required by Binance
	streams := make([]string, 0, len(symbols))
	for _, s := range symbols {
		streams = append(streams, strings.ToLower(s)+"@bookTicker")
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// build URL without escaping slashes (binance expects / separated streams)
		u := BaseWSHost + "?streams=" + strings.Join(streams, "/")
		d := websocket.Dialer{HandshakeTimeout: 20 * time.Second}
		conn, _, err := d.Dial(u, nil)
		if err != nil {
			log.Printf("%s dial failed: %v", prefix, err)
			delay = jitteredDelay(delay)
			sleepWithCtx(ctx, delay)
			continue
		}

		ws := &SafeWS{}
		ws.set(conn)
		_ = ws.setReadDeadline(time.Now().Add(ReadDeadline))
		conn.SetPongHandler(func(app string) error {
			_ = ws.setReadDeadline(time.Now().Add(ReadDeadline))
			return nil
		})

		log.Printf("%s connected (streams=%d)", prefix, len(streams))
		// attempt a quick initial read to verify data is flowing
		_ = ws.setReadDeadline(time.Now().Add(5 * time.Second))
		_, _, err = ws.readMessage()
		if err != nil {
			log.Printf("%s initial read failed: %v", prefix, err)
			_ = ws.close()
			delay = jitteredDelay(delay)
			sleepWithCtx(ctx, delay)
			continue
		}
		log.Printf("%s initial message ok", prefix)
		_ = ws.setReadDeadline(time.Now().Add(ReadDeadline))

		// start read loop
		readErrCh := make(chan error, 1)
		wgReaders.Add(1)
		go func() {
			defer wgReaders.Done()
			readErrCh <- runReaderLoop(ctx, ws, redisClient, prefix)
		}()

		pingTicker := time.NewTicker(PingInterval)
	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			case <-pingTicker.C:
				if err := ws.writeControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
					log.Printf("%s ping failed: %v", prefix, err)
					_ = ws.close()
					break loop
				}
			case err := <-readErrCh:
				if err != nil {
					log.Printf("%s reader error: %v", prefix, err)
				} else {
					log.Printf("%s reader ended cleanly", prefix)
				}
				_ = ws.close()
				break loop
			}
		}

		pingTicker.Stop()
		// backoff then reconnect
		delay = jitteredDelay(delay)
		sleepWithCtx(ctx, delay)
	}
}

// ---------------- Reader loop ----------------
func runReaderLoop(ctx context.Context, ws *SafeWS, redisClient *redis.Client, prefix string) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		_ = ws.setReadDeadline(time.Now().Add(ReadDeadline))
		_, data, err := ws.readMessage()
		if err != nil {
			return err
		}
		norm, err := parseAndNormalize(data)
		if err != nil {
			// skip non-bookTicker or parse errors
			continue
		}
		out, err := json.Marshal(norm)
		if err != nil {
			continue
		}
		// Direct publish (synchronous). Keeps it simple and observable.
		ctxPub, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		res := redisClient.Publish(ctxPub, RedisChannel, out)
		cancel()
		if res.Err() != nil {
			atomic.AddInt64(&redisErrCount, 1)
			log.Printf("%s redis publish error: %v", prefix, res.Err())
			// continue, don't crash
			continue
		}
		atomic.AddInt64(&publishedCount, 1)
	}
}

// ---------------- Metrics & helpers ----------------
func printStatsLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	var last int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p := atomic.LoadInt64(&publishedCount)
			d := atomic.LoadInt64(&dropCounter)
			e := atomic.LoadInt64(&redisErrCount)
			rate := float64(p-last) / 10.0
			log.Printf("STATS: published=%d (%.1f/s) dropped=%d redis_errors=%d", p, rate, d, e)
			last = p
		}
	}
}

func streamMetrics(ctx context.Context, redisClient *redis.Client, normChanLen func() int) {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	var lastPub, lastDrop int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			currPub := atomic.LoadInt64(&publishedCount)
			currDrop := atomic.LoadInt64(&dropCounter)
			currErr := atomic.LoadInt64(&redisErrCount)
			pubRate := currPub - lastPub
			dropRate := currDrop - lastDrop
			lastPub = currPub
			lastDrop = currDrop
			metrics := map[string]interface{}{
				"rate_published": pubRate,
				"rate_dropped":   dropRate,
				"total_errors":   currErr,
				"buffer_len":     normChanLen(),
				"goroutines":     runtime.NumGoroutine(),
				"ts":             time.Now().UnixMilli(),
			}
			_ = redisClient.XAdd(ctx, &redis.XAddArgs{
				Stream: RedisMetricsStream,
				Values: metrics,
				MaxLen: 3600,
			}).Err()
		}
	}
}

// ---------------- MAIN ----------------
func main() {
	log.Println("Starting dumb Binance L1 reader")

	// context & signals
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// redis
	redisClient := newRedisClient()
	// quick ping
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping failed: %v", err)
	}
	defer redisClient.Close()

	// symbols
	syms, err := symbols()
	if err != nil {
		log.Fatalf("failed to fetch symbols: %v", err)
	}
	if len(syms) == 0 {
		log.Fatalf("no symbols returned")
	}
	log.Printf("Loaded %d symbols", len(syms))

	// shuffle
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(syms), func(i, j int) { syms[i], syms[j] = syms[j], syms[i] })

	// create shards
	var shardGroups [][]string
	for i := 0; i < len(syms); i += MaxSymbolsPerConn {
		end := i + MaxSymbolsPerConn
		if end > len(syms) {
			end = len(syms)
		}
		shardGroups = append(shardGroups, syms[i:end])
	}
	log.Printf("Starting %d shards (max %d symbols/shard)", len(shardGroups), MaxSymbolsPerConn)

	// start shards
	for id, grp := range shardGroups {
		go manageShard(ctx, redisClient, grp, id)
	}

	// metrics and stats
	go printStatsLoop(ctx)
	go streamMetrics(ctx, redisClient, func() int { return 0 })

	// wait signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutdown requested")
	cancel()

	// wait readers
	wgReaders.Wait()

	// allow some time for redis to flush
	sleepWithCtx(context.Background(), 500*time.Millisecond)

	log.Printf("FINAL: published=%d dropped=%d redis_errors=%d",
		atomic.LoadInt64(&publishedCount),
		atomic.LoadInt64(&dropCounter),
		atomic.LoadInt64(&redisErrCount))
	log.Println("exiting")
}
