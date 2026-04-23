// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package embedding provides vector embedding implementations for semantic search.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/teradata-labs/loom/pkg/memory"
)

// OpenAIConfig configures the OpenAI embeddings client.
type OpenAIConfig struct {
	APIKey     string
	Model      string        // default: "text-embedding-3-small"
	Dimensions int           // default: 1536 (model-dependent)
	BaseURL    string        // default: "https://api.openai.com/v1"
	Timeout    time.Duration // default: 30s
}

// OpenAIEmbedder implements memory.Embedder using the OpenAI embeddings API.
type OpenAIEmbedder struct {
	config OpenAIConfig
	client *http.Client
}

// Compile-time interface check.
var _ memory.Embedder = (*OpenAIEmbedder)(nil)

// NewOpenAIEmbedder creates an OpenAI embeddings client.
func NewOpenAIEmbedder(cfg OpenAIConfig) *OpenAIEmbedder {
	if cfg.Model == "" {
		cfg.Model = "text-embedding-3-small"
	}
	if cfg.Dimensions == 0 {
		cfg.Dimensions = 1536
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &OpenAIEmbedder{
		config: cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embeddingResponse struct {
	Data  []embeddingData `json:"data"`
	Error *embeddingError `json:"error,omitempty"`
}

type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type embeddingError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	results, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	return results[0], nil
}

func (e *OpenAIEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	reqBody := embeddingRequest{
		Model: e.config.Model,
		Input: texts,
	}
	// Only send dimensions if the model supports it (text-embedding-3-*)
	if e.config.Dimensions > 0 {
		reqBody.Dimensions = e.config.Dimensions
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.config.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.config.APIKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if embResp.Error != nil {
		return nil, fmt.Errorf("API error: %s", embResp.Error.Message)
	}

	// Sort by index to maintain input order.
	results := make([][]float32, len(texts))
	for _, d := range embResp.Data {
		if d.Index < len(results) {
			results[d.Index] = d.Embedding
		}
	}

	return results, nil
}

func (e *OpenAIEmbedder) Dimensions() int {
	return e.config.Dimensions
}

func (e *OpenAIEmbedder) Model() string {
	return e.config.Model
}
