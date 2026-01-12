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
package patterns

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// mockLLMProvider is a mock LLM provider for testing
type mockLLMProvider struct {
	defaultResponse string // Default response to return
	callCount       int
	lastCall        []types.Message
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	m.callCount++
	m.lastCall = messages

	// Return predefined response or default
	response := m.defaultResponse
	if response == "" {
		response = `{
			"intent": "unknown",
			"confidence": 0.5,
			"reasoning": "Could not classify"
		}`
	}

	return &types.LLMResponse{
		Content: response,
		Usage: types.Usage{
			InputTokens:  100,
			OutputTokens: 50,
		},
	}, nil
}

func (m *mockLLMProvider) Name() string {
	return "mock"
}

func (m *mockLLMProvider) Model() string {
	return "mock-model"
}

func TestNewLLMIntentClassifier(t *testing.T) {
	mock := &mockLLMProvider{}

	config := DefaultLLMClassifierConfig(mock)
	classifier := NewLLMIntentClassifier(config)

	assert.NotNil(t, classifier, "Classifier should be created")
}

func TestLLMClassifier_Analytics(t *testing.T) {
	mock := &mockLLMProvider{
		defaultResponse: `{
			"intent": "analytics",
			"confidence": 0.9,
			"reasoning": "User wants to analyze sales trends"
		}`,
	}

	config := &LLMClassifierConfig{
		LLMProvider: mock,
		EnableCache: false, // Disable cache for testing
		CacheTTL:    15 * time.Minute,
	}

	classifier := NewLLMIntentClassifier(config)

	intent, confidence := classifier("analyze sales trends", map[string]any{})

	assert.Equal(t, IntentAnalytics, intent)
	assert.Equal(t, 0.9, confidence)
	assert.Equal(t, 1, mock.callCount, "LLM should be called once")
}

func TestLLMClassifier_SchemaDiscovery(t *testing.T) {
	mock := &mockLLMProvider{
		defaultResponse: `{
			"intent": "schema_discovery",
			"confidence": 0.95,
			"reasoning": "User wants to list tables"
		}`,
	}

	config := &LLMClassifierConfig{
		LLMProvider: mock,
		EnableCache: false,
		CacheTTL:    15 * time.Minute,
	}

	classifier := NewLLMIntentClassifier(config)

	intent, confidence := classifier("show me all tables", map[string]any{})

	assert.Equal(t, IntentSchemaDiscovery, intent)
	assert.Equal(t, 0.95, confidence)
	assert.Equal(t, 1, mock.callCount)
}

func TestLLMClassifier_CacheHit(t *testing.T) {
	mock := &mockLLMProvider{
		defaultResponse: `{
			"intent": "data_quality",
			"confidence": 0.85,
			"reasoning": "User wants to find duplicates"
		}`,
	}

	config := &LLMClassifierConfig{
		LLMProvider: mock,
		EnableCache: true,
		CacheTTL:    15 * time.Minute,
	}

	classifier := NewLLMIntentClassifier(config)

	// First call - should hit LLM
	intent1, conf1 := classifier("find duplicate records", map[string]any{})
	assert.Equal(t, IntentDataQuality, intent1)
	assert.Equal(t, 0.85, conf1)
	assert.Equal(t, 1, mock.callCount, "First call should hit LLM")

	// Second call with same message - should hit cache
	intent2, conf2 := classifier("find duplicate records", map[string]any{})
	assert.Equal(t, IntentDataQuality, intent2)
	assert.Equal(t, 0.85, conf2)
	assert.Equal(t, 1, mock.callCount, "Second call should hit cache, not LLM")

	// Different message - should hit LLM again
	mock.defaultResponse = `{
		"intent": "analytics",
		"confidence": 0.8,
		"reasoning": "User wants aggregation"
	}`
	intent3, conf3 := classifier("calculate average revenue", map[string]any{})
	assert.Equal(t, IntentAnalytics, intent3)
	assert.Equal(t, 0.8, conf3)
	assert.Equal(t, 2, mock.callCount, "Different message should hit LLM")
}

func TestLLMClassifier_FallbackOnError(t *testing.T) {
	// Mock that always returns error
	errorMock := &errorLLMProvider{}

	config := &LLMClassifierConfig{
		LLMProvider: errorMock,
		EnableCache: false,
		CacheTTL:    15 * time.Minute,
	}

	classifier := NewLLMIntentClassifier(config)

	// Should fallback to keyword classifier
	intent, confidence := classifier("show tables in database", map[string]any{})

	// Keyword classifier should return schema_discovery with 0.90 confidence
	assert.Equal(t, IntentSchemaDiscovery, intent)
	assert.Equal(t, 0.90, confidence)
}

func TestLLMClassifier_FallbackOnInvalidJSON(t *testing.T) {
	mock := &mockLLMProvider{
		defaultResponse: "This is not valid JSON",
	}

	config := &LLMClassifierConfig{
		LLMProvider: mock,
		EnableCache: false,
		CacheTTL:    15 * time.Minute,
	}

	classifier := NewLLMIntentClassifier(config)

	// Should fallback to keyword classifier
	intent, confidence := classifier("analyze sales", map[string]any{})

	// Keyword classifier should match "analyze"
	assert.Equal(t, IntentAnalytics, intent)
	assert.Equal(t, 0.80, confidence)
}

func TestParseClassificationResponse(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantIntent IntentCategory
		wantConf   float64
		wantNil    bool
	}{
		{
			name: "valid JSON",
			input: `{
				"intent": "analytics",
				"confidence": 0.9,
				"reasoning": "test"
			}`,
			wantIntent: IntentAnalytics,
			wantConf:   0.9,
			wantNil:    false,
		},
		{
			name: "JSON with markdown wrapper",
			input: "```json\n" + `{
				"intent": "schema_discovery",
				"confidence": 0.95,
				"reasoning": "test"
			}` + "\n```",
			wantIntent: IntentSchemaDiscovery,
			wantConf:   0.95,
			wantNil:    false,
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantNil: true,
		},
		{
			name: "invalid intent category",
			input: `{
				"intent": "invalid_category",
				"confidence": 0.9,
				"reasoning": "test"
			}`,
			wantNil: true,
		},
		{
			name: "confidence clamping - too high",
			input: `{
				"intent": "analytics",
				"confidence": 1.5,
				"reasoning": "test"
			}`,
			wantIntent: IntentAnalytics,
			wantConf:   1.0,
			wantNil:    false,
		},
		{
			name: "confidence clamping - too low",
			input: `{
				"intent": "data_quality",
				"confidence": -0.5,
				"reasoning": "test"
			}`,
			wantIntent: IntentDataQuality,
			wantConf:   0.0,
			wantNil:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseClassificationResponse(tt.input)

			if tt.wantNil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, tt.wantIntent, result.Intent)
				assert.Equal(t, tt.wantConf, result.Confidence)
			}
		})
	}
}

func TestClassificationCache(t *testing.T) {
	cache := newClassificationCache(100, 5*time.Minute)

	// Test Set and Get
	cache.Set("message1", IntentAnalytics, 0.9)

	entry := cache.Get("message1")
	require.NotNil(t, entry)
	assert.Equal(t, IntentAnalytics, entry.Intent)
	assert.Equal(t, 0.9, entry.Confidence)

	// Test cache miss
	entry = cache.Get("nonexistent")
	assert.Nil(t, entry)
}

func TestClassificationCache_TTL(t *testing.T) {
	cache := newClassificationCache(100, 100*time.Millisecond)

	cache.Set("message1", IntentAnalytics, 0.9)

	// Should be available immediately
	entry := cache.Get("message1")
	require.NotNil(t, entry)

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Should be expired
	entry = cache.Get("message1")
	assert.Nil(t, entry)
}

func TestClassificationCache_Eviction(t *testing.T) {
	cache := newClassificationCache(10, 5*time.Minute)

	// Fill cache beyond capacity
	for i := 0; i < 15; i++ {
		cache.Set(fmt.Sprintf("message%d", i), IntentAnalytics, 0.9)
	}

	// Cache should not exceed max size (eviction should occur)
	assert.LessOrEqual(t, len(cache.entries), 10, "Cache should not exceed max size")
}

func TestBuildClassificationPrompt(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		contextData map[string]any
		wantContain []string
	}{
		{
			name:    "basic message",
			message: "show me tables",
			contextData: map[string]any{
				"backend_type": "teradata",
			},
			wantContain: []string{
				"show me tables",
				"teradata",
				"schema_discovery",
				"analytics",
				"JSON",
			},
		},
		{
			name:        "unknown backend",
			message:     "find duplicates",
			contextData: map[string]any{},
			wantContain: []string{
				"find duplicates",
				"unknown backend",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := buildClassificationPrompt(tt.message, tt.contextData)

			for _, substr := range tt.wantContain {
				assert.Contains(t, prompt, substr)
			}
		})
	}
}

// errorLLMProvider always returns errors
type errorLLMProvider struct{}

func (e *errorLLMProvider) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	return nil, fmt.Errorf("mock error")
}

func (e *errorLLMProvider) Name() string {
	return "error-mock"
}

func (e *errorLLMProvider) Model() string {
	return "error-model"
}
