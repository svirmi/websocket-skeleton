package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
)

// ----------------------
// Interfaces
// ----------------------
type MessageProcessor interface {
	Process(msg []byte) ([]byte, string, error)
}

type ConnectionManager interface {
	AddConnection(connID string, conn *websocket.Conn)
	RemoveConnection(connID string)
	GetConnection(connID string) (*websocket.Conn, bool)
}

// ----------------------
// Data Structures
// ----------------------
type RawMessage struct {
	ID     string
	Data   []byte
	ConnID string
	RecvAt time.Time
}

// ----------------------
// WebSocket Receiver
// ----------------------
type WSReceiver struct {
	processingChan chan<- RawMessage
	storageChan    chan<- RawMessage
	connManager    ConnectionManager
	upgrader       websocket.Upgrader
}

func NewWSReceiver(processingChan, storageChan chan<- RawMessage, connManager ConnectionManager) *WSReceiver {
	return &WSReceiver{
		processingChan: processingChan,
		storageChan:    storageChan,
		connManager:    connManager,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
	}
}

func (r *WSReceiver) HandleWebSocket(w http.ResponseWriter, req *http.Request) {
	conn, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Println("WebSocket upgrade failed:", err)
		return
	}

	connID := uuid.New().String()
	r.connManager.AddConnection(connID, conn)
	defer r.connManager.RemoveConnection(connID)
	defer conn.Close()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Connection %s closed: %v", connID, err)
			}
			break
		}

		msg := RawMessage{
			ID:     uuid.New().String(),
			Data:   message,
			ConnID: connID,
			RecvAt: time.Now().UTC(),
		}

		// Send to processing pipeline
		select {
		case r.processingChan <- msg:
		default:
			log.Println("Processing queue full - dropping message")
		}

		// Send to storage pipeline
		select {
		case r.storageChan <- msg:
		default:
			log.Println("Storage queue full - dropping raw message")
		}
	}
}

// ----------------------
// SQLite Storage Worker
// ----------------------
type StorageConfig struct {
	BatchSize    int
	BatchTimeout time.Duration
	DBPath       string
}

type SQLiteStorage struct {
	db          *sql.DB
	inputChan   <-chan RawMessage
	batchBuffer []RawMessage
	config      StorageConfig
	shutdown    chan struct{}
	wg          sync.WaitGroup
	mu          sync.Mutex
}

func NewSQLiteStorage(config StorageConfig) (*SQLiteStorage, error) {
	db, err := sql.Open("sqlite3", config.DBPath+"?_journal=WAL&_sync=NORMAL")
	if err != nil {
		return nil, err
	}

	// Create messages table if not exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS raw_messages (
			id TEXT PRIMARY KEY,
			conn_id TEXT NOT NULL,
			received_at DATETIME NOT NULL,
			data BLOB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_received_at ON raw_messages(received_at);
	`)
	if err != nil {
		return nil, err
	}

	return &SQLiteStorage{
		db:       db,
		config:   config,
		shutdown: make(chan struct{}),
	}, nil
}

func (s *SQLiteStorage) Start(inputChan <-chan RawMessage) {
	s.inputChan = inputChan
	s.wg.Add(1)
	go s.run()
}

func (s *SQLiteStorage) run() {
	defer s.wg.Done()
	defer s.db.Close()

	batchTimer := time.NewTimer(s.config.BatchTimeout)
	defer batchTimer.Stop()

	for {
		select {
		case msg, ok := <-s.inputChan:
			if !ok {
				s.flushBatch()
				return
			}

			s.mu.Lock()
			s.batchBuffer = append(s.batchBuffer, msg)
			batchFull := len(s.batchBuffer) >= s.config.BatchSize
			s.mu.Unlock()

			if batchFull {
				s.flushBatch()
				if !batchTimer.Stop() {
					<-batchTimer.C
				}
				batchTimer.Reset(s.config.BatchTimeout)
			}

		case <-batchTimer.C:
			s.flushBatch()
			batchTimer.Reset(s.config.BatchTimeout)

		case <-s.shutdown:
			s.flushBatch()
			return
		}
	}
}

func (s *SQLiteStorage) flushBatch() {
	s.mu.Lock()
	if len(s.batchBuffer) == 0 {
		s.mu.Unlock()
		return
	}

	// Copy batch and clear buffer
	batch := make([]RawMessage, len(s.batchBuffer))
	copy(batch, s.batchBuffer)
	s.batchBuffer = s.batchBuffer[:0]
	s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("Storage: failed to begin transaction: %v", err)
		return
	}

	stmt, err := tx.Prepare(`
		INSERT INTO raw_messages(id, conn_id, received_at, data)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`)
	if err != nil {
		log.Printf("Storage: prepare failed: %v", err)
		tx.Rollback()
		return
	}

	for _, msg := range batch {
		_, err := stmt.Exec(msg.ID, msg.ConnID, msg.RecvAt, msg.Data)
		if err != nil {
			log.Printf("Storage: insert failed for %s: %v", msg.ID, err)
		}
	}

	if err := stmt.Close(); err != nil {
		log.Printf("Storage: statement close failed: %v", err)
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Storage: commit failed: %v", err)
	}
}

func (s *SQLiteStorage) Stop() {
	close(s.shutdown)
	s.wg.Wait()
}

// ----------------------
// Processor Worker Pool
// ----------------------
type ProcessorPool struct {
	workers   int
	inChan    <-chan RawMessage
	outRouter *OutputRouter
	processor MessageProcessor
}

func NewProcessorPool(workers int, inChan <-chan RawMessage, outRouter *OutputRouter, processor MessageProcessor) *ProcessorPool {
	return &ProcessorPool{
		workers:   workers,
		inChan:    inChan,
		outRouter: outRouter,
		processor: processor,
	}
}

func (p *ProcessorPool) Start() {
	for i := 0; i < p.workers; i++ {
		go p.worker()
	}
}

func (p *ProcessorPool) worker() {
	for msg := range p.inChan {
		processed, dest, err := p.processor.Process(msg.Data)
		if err != nil {
			log.Printf("Processing failed for %s: %v", msg.ID, err)
			continue
		}
		p.outRouter.Route(processed, dest)
	}
}

// ----------------------
// Output Router
// ----------------------
type OutputRouter struct {
	outChans map[string]chan []byte
	mu       sync.RWMutex
}

func NewOutputRouter() *OutputRouter {
	return &OutputRouter{
		outChans: make(map[string]chan []byte),
	}
}

func (r *OutputRouter) AddDestination(dest string, ch chan []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outChans[dest] = ch
}

func (r *OutputRouter) RemoveDestination(dest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.outChans[dest]; ok {
		close(ch)
		delete(r.outChans, dest)
	}
}

func (r *OutputRouter) Route(msg []byte, dest string) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if ch, ok := r.outChans[dest]; ok {
		select {
		case ch <- msg:
		default:
			log.Printf("Output channel full for %s - dropping message", dest)
		}
	} else {
		log.Printf("No destination found for %s", dest)
	}
}

// ----------------------
// WebSocket Sender
// ----------------------
type WSSender struct {
	outChan     <-chan []byte
	connManager ConnectionManager
	dest        string
}

func NewWSSender(outChan <-chan []byte, connManager ConnectionManager, dest string) *WSSender {
	return &WSSender{
		outChan:     outChan,
		connManager: connManager,
		dest:        dest,
	}
}

func (s *WSSender) Start() {
	for msg := range s.outChan {
		conn, ok := s.connManager.GetConnection(s.dest)
		if !ok {
			log.Printf("No connection for %s", s.dest)
			continue
		}

		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("Failed to send to %s: %v", s.dest, err)
			s.connManager.RemoveConnection(s.dest)
		}
	}
}

// ----------------------
// Connection Manager
// ----------------------
type ConnectionStore struct {
	connections map[string]*websocket.Conn
	mu          sync.RWMutex
}

func NewConnectionStore() *ConnectionStore {
	return &ConnectionStore{
		connections: make(map[string]*websocket.Conn),
	}
}

func (s *ConnectionStore) AddConnection(connID string, conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connections[connID] = conn
}

func (s *ConnectionStore) RemoveConnection(connID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if conn, ok := s.connections[connID]; ok {
		conn.Close()
		delete(s.connections, connID)
	}
}

func (s *ConnectionStore) GetConnection(connID string) (*websocket.Conn, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conn, ok := s.connections[connID]
	return conn, ok
}

// ----------------------
// Main Application
// ----------------------
type App struct {
	receiver       *WSReceiver
	processor      *ProcessorPool
	router         *OutputRouter
	storage        *SQLiteStorage
	connManager    *ConnectionStore
	shutdown       chan struct{}
	processingChan chan RawMessage
	storageChan    chan RawMessage
}

func NewApp() *App {
	// Create channels
	processingChan := make(chan RawMessage, 1000)
	storageChan := make(chan RawMessage, 5000)

	// Create components
	connManager := NewConnectionStore()
	router := NewOutputRouter()

	// Storage configuration
	storageCfg := StorageConfig{
		BatchSize:    100,
		BatchTimeout: 1 * time.Second,
		DBPath:       "messages.db",
	}

	storage, err := NewSQLiteStorage(storageCfg)
	if err != nil {
		log.Fatal("Storage init failed:", err)
	}

	// Create receiver
	receiver := NewWSReceiver(processingChan, storageChan, connManager)

	return &App{
		receiver:       receiver,
		processor:      NewProcessorPool(4, processingChan, router, &SampleProcessor{}),
		router:         router,
		storage:        storage,
		connManager:    connManager,
		shutdown:       make(chan struct{}),
		processingChan: processingChan,
		storageChan:    storageChan,
	}
}

func (a *App) Start() {
	// Start storage first to prevent message loss
	a.storage.Start(a.storageChan)

	// Start processor pool
	a.processor.Start()

	// Start HTTP server
	go a.startHTTPServer()

	// Start downstream senders (in a real app you'd manage these dynamically)
	go a.startDownstreamSenders()

	// Wait for shutdown signal
	<-a.shutdown
}

func (a *App) startHTTPServer() {
	http.HandleFunc("/ws", a.receiver.HandleWebSocket)
	log.Println("Starting server on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal("HTTP server failed:", err)
	}
}

func (a *App) startDownstreamSenders() {
	// In real implementation, this would be dynamic
	destinations := []string{"serviceA", "serviceB"}

	for _, dest := range destinations {
		ch := make(chan []byte, 100)
		a.router.AddDestination(dest, ch)

		// Create downstream connection
		dialer := websocket.Dialer{}
		conn, _, err := dialer.Dial("ws://"+dest+"/in", nil)
		if err != nil {
			log.Printf("Failed to connect to %s: %v", dest, err)
			continue
		}

		a.connManager.AddConnection(dest, conn)

		sender := NewWSSender(ch, a.connManager, dest)
		go sender.Start()
	}
}

func (a *App) Shutdown() {
	close(a.shutdown)

	// Stop storage first
	a.storage.Stop()

	// Close processing channels
	close(a.processingChan)
	close(a.storageChan)

	// Close all connections
	a.connManager.mu.Lock()
	for id, conn := range a.connManager.connections {
		conn.Close()
		delete(a.connManager.connections, id)
	}
	a.connManager.mu.Unlock()
}

// ----------------------
// Sample Processor
// ----------------------
type SampleProcessor struct{}

func (p *SampleProcessor) Process(msg []byte) ([]byte, string, error) {
	// Simple echo processor with destination routing
	return msg, "serviceA", nil
}

// ----------------------
// Main Function
// ----------------------
func main() {
	app := NewApp()

	// Handle graceful shutdown
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("Shutdown signal received")
		app.Shutdown()
	}()

	log.Println("Starting application")
	app.Start()
	log.Println("Application stopped")
}
