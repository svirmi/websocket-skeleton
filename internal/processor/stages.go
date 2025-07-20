package processor

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Common errors
var (
	ErrInvalidJSON = errors.New("invalid JSON data")
)

// ValidationStage performs data validation
type ValidationStage struct{}

func (s *ValidationStage) Process(ctx context.Context, data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return nil, ErrInvalidJSON
	}
	return data, nil
}

func (s *ValidationStage) Name() string {
	return "validation"
}

// EnrichmentStage adds metadata to the message
type EnrichmentStage struct{}

func (s *EnrichmentStage) Process(ctx context.Context, data []byte) ([]byte, error) {
	var msg map[string]interface{}
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}

	// Add enrichment data
	msg["processed_at"] = time.Now().UTC()
	msg["pipeline_version"] = "1.0"

	enriched, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	return enriched, nil
}

func (s *EnrichmentStage) Name() string {
	return "enrichment"
}

// TransformationStage transforms the data format
type TransformationStage struct{}

func (s *TransformationStage) Process(ctx context.Context, data []byte) ([]byte, error) {
	var msg map[string]interface{}
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}

	// Add your transformation logic here
	// For example, standardize field names, convert data types, etc.

	transformed, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	return transformed, nil
}

func (s *TransformationStage) Name() string {
	return "transformation"
}
