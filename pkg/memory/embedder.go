// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package memory

import "context"

// Embedder produces vector embeddings from text. Implementations may use
// OpenAI, local models, or any provider that returns fixed-dimension float32 vectors.
type Embedder interface {
	// Embed returns a vector embedding for the given text.
	Embed(ctx context.Context, text string) ([]float32, error)

	// EmbedBatch returns embeddings for multiple texts. Implementations
	// should batch API calls where possible for efficiency.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)

	// Dimensions returns the dimensionality of embeddings produced.
	Dimensions() int

	// Model returns the model identifier. Stored with each embedding so that
	// model changes don't corrupt recall (incompatible vector spaces).
	Model() string
}

// VectorRecallOpts configures a vector similarity search.
type VectorRecallOpts struct {
	AgentID   string
	Embedding []float32 // query vector
	Model     string    // only match embeddings from this model (required)
	Limit     int       // max results (default: 20)
	Threshold float32   // minimum cosine similarity (default: 0.0)
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns a value in [-1, 1] where 1 means identical direction.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (sqrt32(normA) * sqrt32(normB))
}

// sqrt32 is float32 square root using Newton's method (avoids float64 conversion).
func sqrt32(x float32) float32 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 10; i++ {
		z = (z + x/z) / 2
	}
	return z
}
