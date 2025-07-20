package broadcast

import (
	"context"
	"sync"
	"time"
)

type Subscriber struct {
	ID string
	Ch chan []byte
}

type BroadcastService struct {
	input       chan []byte
	Output      chan []byte // Make this public
	subscribers map[string]*Subscriber
	mu          sync.RWMutex
	bufferSize  int
	metrics     *Metrics
	ctx         context.Context
	cancel      context.CancelFunc
}

func NewBroadcastService(bufferSize int) *BroadcastService {
	ctx, cancel := context.WithCancel(context.Background())
	return &BroadcastService{
		input:       make(chan []byte, bufferSize),
		Output:      make(chan []byte, bufferSize), // Initialize the output channel
		subscribers: make(map[string]*Subscriber),
		bufferSize:  bufferSize,
		metrics:     &Metrics{},
		ctx:         ctx,
		cancel:      cancel,
	}
}

func (b *BroadcastService) GetOutput() <-chan []byte {
	return b.Output
}

type Metrics struct {
	SubscriberCount   int
	MessagesSent      int64
	BytesSent         int64
	DroppedMessages   int64
	LastBroadcastTime time.Time
	mu                sync.RWMutex
}

func (b *BroadcastService) Start() {
	go b.broadcastLoop()
}

func (b *BroadcastService) Stop() {
	b.cancel()
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, sub := range b.subscribers {
		close(sub.Ch)
	}
	b.subscribers = make(map[string]*Subscriber)
}

func (b *BroadcastService) Subscribe() (string, <-chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := generateSubscriberID()
	ch := make(chan []byte, b.bufferSize)
	sub := &Subscriber{
		ID: id,
		Ch: ch,
	}
	b.subscribers[id] = sub

	b.metrics.mu.Lock()
	b.metrics.SubscriberCount++
	b.metrics.mu.Unlock()

	return id, ch
}

func (b *BroadcastService) Unsubscribe(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if sub, exists := b.subscribers[id]; exists {
		close(sub.Ch)
		delete(b.subscribers, id)

		b.metrics.mu.Lock()
		b.metrics.SubscriberCount--
		b.metrics.mu.Unlock()
	}
}

func (b *BroadcastService) GetInput() chan<- []byte {
	return b.input
}

func (b *BroadcastService) broadcastLoop() {
	for {
		select {
		case <-b.ctx.Done():
			return
		case msg := <-b.input:
			b.broadcast(msg)
		}
	}
}

func (b *BroadcastService) broadcast(msg []byte) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	b.metrics.mu.Lock()
	b.metrics.LastBroadcastTime = time.Now()
	msgSize := int64(len(msg))
	b.metrics.BytesSent += msgSize
	b.metrics.mu.Unlock()

	for _, sub := range b.subscribers {
		select {
		case sub.Ch <- msg:
			b.metrics.mu.Lock()
			b.metrics.MessagesSent++
			b.metrics.mu.Unlock()
		default:
			// Channel buffer is full, drop message for this subscriber
			b.metrics.mu.Lock()
			b.metrics.DroppedMessages++
			b.metrics.mu.Unlock()
		}
	}
}

func (b *BroadcastService) GetMetrics() Metrics {
	b.metrics.mu.RLock()
	defer b.metrics.mu.RUnlock()
	return *b.metrics
}

// generateSubscriberID generates a unique subscriber ID
func generateSubscriberID() string {
	return time.Now().Format("20060102150405.000000000")
}
