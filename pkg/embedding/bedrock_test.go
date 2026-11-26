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
	"testing"

	"github.com/teradata-labs/loom/pkg/memory"
)

// Interface conformance is asserted at compile time in bedrock.go, but re-check
// here so the test package fails loudly if the contract drifts.
var _ memory.Embedder = (*BedrockEmbedder)(nil)

func TestNewBedrockEmbedder_RequiresRegion(t *testing.T) {
	if _, err := NewBedrockEmbedder(context.Background(), BedrockConfig{}); err == nil {
		t.Fatal("expected error when region is empty")
	}
}

func TestNewBedrockEmbedder_Defaults(t *testing.T) {
	e, err := NewBedrockEmbedder(context.Background(), BedrockConfig{Region: "us-west-2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := e.Model(); got != DefaultTitanModelID {
		t.Errorf("Model() = %q, want %q", got, DefaultTitanModelID)
	}
	if got := e.Dimensions(); got != DefaultTitanDimensions {
		t.Errorf("Dimensions() = %d, want %d", got, DefaultTitanDimensions)
	}
}

func TestNewBedrockEmbedder_OverrideModelAndDimensions(t *testing.T) {
	e, err := NewBedrockEmbedder(context.Background(), BedrockConfig{
		Region:     "us-east-1",
		ModelID:    "amazon.titan-embed-text-v2:0",
		Dimensions: 512,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := e.Dimensions(); got != 512 {
		t.Errorf("Dimensions() = %d, want 512", got)
	}
}

func TestTitanRequest_Marshal(t *testing.T) {
	body, err := json.Marshal(titanRequest{InputText: "hello", Dimensions: 1024, Normalize: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(body, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round["inputText"] != "hello" {
		t.Errorf("inputText = %v, want hello", round["inputText"])
	}
	if round["dimensions"].(float64) != 1024 {
		t.Errorf("dimensions = %v, want 1024", round["dimensions"])
	}
	if round["normalize"] != true {
		t.Errorf("normalize = %v, want true", round["normalize"])
	}
}

func TestTitanResponse_Unmarshal(t *testing.T) {
	raw := []byte(`{"embedding":[0.1,0.2,0.3],"inputTextTokenCount":4}`)
	var resp titanResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Embedding) != 3 {
		t.Fatalf("embedding len = %d, want 3", len(resp.Embedding))
	}
	if resp.InputTextTokenCount != 4 {
		t.Errorf("token count = %d, want 4", resp.InputTextTokenCount)
	}
}
