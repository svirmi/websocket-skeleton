package ingestion

import (
	"context"
	"fmt"
	"sync"
)

// DataIngestionService manages multiple data sources
type DataIngestionService struct {
	sources map[string]DataSource
	output  chan []byte
	mu      sync.RWMutex
	ctx     context.Context
	cancel  context.CancelFunc
}

func NewDataIngestionService(bufferSize int) *DataIngestionService {
	ctx, cancel := context.WithCancel(context.Background())
	return &DataIngestionService{
		sources: make(map[string]DataSource),
		output:  make(chan []byte, bufferSize),
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (s *DataIngestionService) AddSource(source DataSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sources[source.ID()]; exists {
		return fmt.Errorf("source with ID %s already exists", source.ID())
	}

	s.sources[source.ID()] = source
	return nil
}

func (s *DataIngestionService) RemoveSource(sourceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	source, exists := s.sources[sourceID]
	if !exists {
		return fmt.Errorf("source with ID %s not found", sourceID)
	}

	if err := source.Stop(); err != nil {
		return fmt.Errorf("failed to stop source %s: %v", sourceID, err)
	}

	delete(s.sources, sourceID)
	return nil
}

func (s *DataIngestionService) Start() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, source := range s.sources {
		if err := source.Start(s.ctx); err != nil {
			return fmt.Errorf("failed to start source %s: %v", source.ID(), err)
		}

		// Start goroutine to forward messages from source to output channel
		go s.forwardMessages(source)
	}

	return nil
}

func (s *DataIngestionService) Stop() error {
	s.cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	var errs []error
	for _, source := range s.sources {
		if err := source.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop source %s: %v", source.ID(), err))
		}
	}

	close(s.output)

	if len(errs) > 0 {
		return fmt.Errorf("errors stopping sources: %v", errs)
	}
	return nil
}

func (s *DataIngestionService) GetOutput() <-chan []byte {
	return s.output
}

func (s *DataIngestionService) forwardMessages(source DataSource) {
	sourceChan := source.GetChannel()
	for {
		select {
		case <-s.ctx.Done():
			return
		case msg, ok := <-sourceChan:
			if !ok {
				return
			}
			s.output <- msg
		}
	}
}

func (s *DataIngestionService) GetSourceStatus(sourceID string) (SourceStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	source, exists := s.sources[sourceID]
	if !exists {
		return SourceStatus{}, fmt.Errorf("source with ID %s not found", sourceID)
	}

	return source.Status(), nil
}

func (s *DataIngestionService) GetAllSourceStatuses() map[string]SourceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	statuses := make(map[string]SourceStatus)
	for id, source := range s.sources {
		statuses[id] = source.Status()
	}
	return statuses
}
