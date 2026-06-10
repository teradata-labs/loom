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

//go:build integration

package factory

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/types"
)

// TestBedrock_AnthropicRoutesToStreamingSDK is a live dogfood test against real
// Bedrock. It proves the fix: the AWS Converse client (NewClient) leaves
// ChatStream stubbed so the token callback never fires, whereas the Anthropic
// SDK client (NewSDKClient) — which the factory now selects for Claude models —
// streams tokens incrementally.
//
// Gated behind LOOM_BEDROCK_LIVE=1 and the `integration` build tag so it never
// runs in unit CI. Uses the ambient AWS credential chain (AWS_PROFILE/SSO).
//
//	LOOM_BEDROCK_LIVE=1 go test -tags 'fts5 integration' -run TestBedrock_AnthropicRoutesToStreamingSDK \
//	    ./pkg/llm/factory -v -count=1
func TestBedrock_AnthropicRoutesToStreamingSDK(t *testing.T) {
	if os.Getenv("LOOM_BEDROCK_LIVE") != "1" {
		t.Skip("set LOOM_BEDROCK_LIVE=1 to run the live Bedrock streaming test")
	}

	model := getenvDefault("LOOM_LLM_BEDROCK_MODEL_ID", "global.anthropic.claude-opus-4-6-v1")
	region := getenvDefault("AWS_REGION", "us-west-2")

	cfg := bedrock.Config{Region: region, ModelID: model, MaxTokens: 512, Temperature: 0.0}
	prompt := []llmtypes.Message{{Role: "user", Content: "Count from 1 to 20, one number per line."}}

	// 1) The factory must route this Anthropic model to the streaming SDK client.
	f := NewProviderFactory(FactoryConfig{BedrockRegion: region, BedrockModelID: model})
	provider, err := f.CreateProvider("bedrock", model)
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if _, ok := provider.(*bedrock.SDKClient); !ok {
		t.Fatalf("factory routed %q to %T, want *bedrock.SDKClient", model, provider)
	}

	// 2) OLD path (NewClient): ChatStream is stubbed -> callback must never fire.
	oldClient, err := bedrock.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	oldCount, oldFirst, oldTotal := measureStream(t, oldClient, prompt)
	t.Logf("OLD NewClient (Converse): callbacks=%d, firstToken=%v, total=%v", oldCount, oldFirst, oldTotal)

	// 3) NEW path (NewSDKClient): real streaming -> many callbacks, early first token.
	newClient, err := bedrock.NewSDKClient(cfg)
	if err != nil {
		t.Fatalf("NewSDKClient: %v", err)
	}
	newCount, newFirst, newTotal := measureStream(t, newClient, prompt)
	t.Logf("NEW NewSDKClient (Anthropic SDK): callbacks=%d, firstToken=%v, total=%v", newCount, newFirst, newTotal)

	if oldCount != 0 {
		t.Errorf("OLD path fired %d token callbacks, want 0 (stub returns whole response)", oldCount)
	}
	if newCount < 2 {
		t.Errorf("NEW path fired %d token callbacks, want >=2 (incremental streaming)", newCount)
	}
	// Time-to-first-token on the streaming path should be a fraction of the
	// total generation time — that is the latency users feel improve.
	if newFirst >= newTotal {
		t.Errorf("NEW path first token at %v >= total %v; not streaming incrementally", newFirst, newTotal)
	}
}

// measureStream runs ChatStream and returns (callbackCount, timeToFirstToken, total).
func measureStream(t *testing.T, p any, msgs []llmtypes.Message) (int, time.Duration, time.Duration) {
	t.Helper()
	sp, ok := p.(types.StreamingLLMProvider)
	if !ok {
		t.Fatalf("%T does not implement StreamingLLMProvider", p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var mu sync.Mutex
	var count int
	var firstAt time.Duration
	start := time.Now()
	cb := func(string) {
		mu.Lock()
		defer mu.Unlock()
		count++
		if count == 1 {
			firstAt = time.Since(start)
		}
	}
	if _, err := sp.ChatStream(ctx, msgs, nil, cb); err != nil {
		t.Fatalf("ChatStream(%T): %v", p, err)
	}
	return count, firstAt, time.Since(start)
}

func getenvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
