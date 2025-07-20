package processor

import (
	"context"
	"sync"
	"time"
)

type Stage interface {
	Process(ctx context.Context, data []byte) ([]byte, error)
	Name() string
}

type Pipeline struct {
	stages       []Stage
	input        chan []byte
	output       chan []byte
	errorHandler func(error)
	metrics      *PipelineMetrics
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

type PipelineMetrics struct {
	ProcessedCount int64
	ErrorCount     int64
	LastError      string
	LastProcessed  time.Time
	ProcessingTime time.Duration
	mu             sync.RWMutex
}

func NewPipeline(bufferSize int, errorHandler func(error)) *Pipeline {
	ctx, cancel := context.WithCancel(context.Background())
	return &Pipeline{
		stages:       make([]Stage, 0),
		input:        make(chan []byte, bufferSize),
		output:       make(chan []byte, bufferSize),
		errorHandler: errorHandler,
		metrics:      &PipelineMetrics{},
		ctx:          ctx,
		cancel:       cancel,
	}
}

func (p *Pipeline) AddStage(stage Stage) {
	p.stages = append(p.stages, stage)
}

func (p *Pipeline) Start(workerCount int) {
	for i := 0; i < workerCount; i++ {
		p.wg.Add(1)
		go p.processWorker()
	}
}

func (p *Pipeline) Stop() {
	p.cancel()
	p.wg.Wait()
	close(p.output)
}

func (p *Pipeline) GetInput() chan<- []byte {
	return p.input
}

func (p *Pipeline) GetOutput() <-chan []byte {
	return p.output
}

func (p *Pipeline) processWorker() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		case data, ok := <-p.input:
			if !ok {
				return
			}

			startTime := time.Now()
			processed := data

			for _, stage := range p.stages {
				var err error
				processed, err = stage.Process(p.ctx, processed)
				if err != nil {
					p.metrics.mu.Lock()
					p.metrics.ErrorCount++
					p.metrics.LastError = err.Error()
					p.metrics.mu.Unlock()

					if p.errorHandler != nil {
						p.errorHandler(err)
					}
					continue
				}
			}

			p.metrics.mu.Lock()
			p.metrics.ProcessedCount++
			p.metrics.LastProcessed = time.Now()
			p.metrics.ProcessingTime = time.Since(startTime)
			p.metrics.mu.Unlock()

			p.output <- processed
		}
	}
}

func (p *Pipeline) GetMetrics() PipelineMetrics {
	p.metrics.mu.RLock()
	defer p.metrics.mu.RUnlock()
	return *p.metrics
}
