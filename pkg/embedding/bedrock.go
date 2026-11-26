// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package embedding

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/teradata-labs/loom/pkg/memory"
)

// DefaultTitanModelID is Amazon Titan Text Embeddings V2, which produces
// 1024-dimension vectors by default (also supports 512 and 256). The Postgres
// HNSW index is pinned to vector(1024), so 1024 is the matching default.
const DefaultTitanModelID = "amazon.titan-embed-text-v2:0"

// DefaultTitanDimensions matches the vector(1024) column used by the Postgres
// graph_memories HNSW index (migration 000025).
const DefaultTitanDimensions = 1024

// BedrockConfig configures the Amazon Bedrock embeddings client.
type BedrockConfig struct {
	// Region is the AWS region (e.g. us-west-2). Required.
	Region string

	// ModelID is the Bedrock embedding model. Default: amazon.titan-embed-text-v2:0.
	ModelID string

	// Dimensions is the output vector size. Titan v2 supports 256, 512, 1024.
	// Default: 1024 (matches the Postgres HNSW vector(1024) column).
	Dimensions int

	// Normalize requests unit-normalized vectors from Titan v2. Recommended for
	// cosine similarity (the operator used by VectorRecall). Default: true.
	Normalize bool

	// Credentials (all optional). Resolution mirrors the Bedrock LLM client:
	// bearer token > static keys > profile > default AWS credential chain.
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Profile         string
	BearerToken     string
}

// BedrockEmbedder implements memory.Embedder using Amazon Bedrock (Titan v2).
type BedrockEmbedder struct {
	client     *bedrockruntime.Client
	modelID    string
	dimensions int
	normalize  bool
}

// Compile-time interface check.
var _ memory.Embedder = (*BedrockEmbedder)(nil)

// NewBedrockEmbedder creates a Bedrock embeddings client. The AWS config is
// loaded eagerly so credential/region errors surface at startup, not first use.
func NewBedrockEmbedder(ctx context.Context, cfg BedrockConfig) (*BedrockEmbedder, error) {
	if cfg.Region == "" {
		return nil, fmt.Errorf("bedrock embedder requires a region")
	}
	if cfg.ModelID == "" {
		cfg.ModelID = DefaultTitanModelID
	}
	if cfg.Dimensions == 0 {
		cfg.Dimensions = DefaultTitanDimensions
	}

	awsCfg, err := loadBedrockAWSConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("load AWS config for bedrock embedder: %w", err)
	}

	return &BedrockEmbedder{
		client:     bedrockruntime.NewFromConfig(awsCfg),
		modelID:    cfg.ModelID,
		dimensions: cfg.Dimensions,
		normalize:  cfg.Normalize,
	}, nil
}

// loadBedrockAWSConfig mirrors the credential precedence of the Bedrock LLM
// client (pkg/llm/bedrock): explicit keys > profile > default chain.
func loadBedrockAWSConfig(ctx context.Context, cfg BedrockConfig) (aws.Config, error) {
	switch {
	case cfg.AccessKeyID != "" && cfg.SecretAccessKey != "":
		return awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(cfg.Region),
			awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
				cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken)),
		)
	case cfg.Profile != "":
		return awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(cfg.Region),
			awsconfig.WithSharedConfigProfile(cfg.Profile),
		)
	default:
		return awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	}
}

// titanRequest is the Titan Text Embeddings V2 request body.
type titanRequest struct {
	InputText  string `json:"inputText"`
	Dimensions int    `json:"dimensions,omitempty"`
	Normalize  bool   `json:"normalize"`
}

// titanResponse is the Titan Text Embeddings V2 response body.
type titanResponse struct {
	Embedding           []float32 `json:"embedding"`
	InputTextTokenCount int       `json:"inputTextTokenCount"`
}

// Embed returns a vector embedding for a single text.
func (e *BedrockEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	reqBody, err := json.Marshal(titanRequest{
		InputText:  text,
		Dimensions: e.dimensions,
		Normalize:  e.normalize,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal titan request: %w", err)
	}

	out, err := e.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(e.modelID),
		Body:        reqBody,
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock embed invocation failed: %w", err)
	}

	var resp titanResponse
	if err := json.Unmarshal(out.Body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal titan response: %w", err)
	}
	if len(resp.Embedding) == 0 {
		return nil, fmt.Errorf("bedrock returned an empty embedding")
	}
	return resp.Embedding, nil
}

// EmbedBatch embeds multiple texts. Titan's InvokeModel accepts a single
// inputText per call, so this issues one request per text.
func (e *BedrockEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		vec, err := e.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out = append(out, vec)
	}
	return out, nil
}

// Dimensions returns the embedding dimensionality.
func (e *BedrockEmbedder) Dimensions() int { return e.dimensions }

// Model returns the model identifier stored with each embedding.
func (e *BedrockEmbedder) Model() string { return e.modelID }
