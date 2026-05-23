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
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/types"
)

// LLMClassifierConfig configures the LLM intent classifier
type LLMClassifierConfig struct {
	// LLM provider to use for classification (should be configured with fast model like Haiku)
	LLMProvider types.LLMProvider

	// Enable caching of classifications (recommended)
	EnableCache bool

	// Cache TTL (default: 15 minutes)
	CacheTTL time.Duration
}

// DefaultLLMClassifierConfig returns sensible defaults.
// Note: The LLM provider should be pre-configured with a fast model (e.g., claude-haiku-3-5)
// for low-latency classification.
func DefaultLLMClassifierConfig(llm types.LLMProvider) *LLMClassifierConfig {
	return &LLMClassifierConfig{
		LLMProvider: llm,
		EnableCache: true,
		CacheTTL:    15 * time.Minute,
	}
}

// NewLLMIntentClassifier creates an LLM-based intent classifier.
// Returns an IntentClassifierFunc that can be plugged into the orchestrator.
//
// Example usage:
//
//	llmClassifier := NewLLMIntentClassifier(config)
//	orchestrator.SetIntentClassifier(llmClassifier)
func NewLLMIntentClassifier(config *LLMClassifierConfig) IntentClassifierFunc {
	var cache *classificationCache
	if config.EnableCache {
		cache = newClassificationCache(5000, config.CacheTTL)
	}

	return func(userMessage string, contextData map[string]any) (string, float64) {
		// Check cache first
		if cache != nil {
			if result := cache.Get(userMessage); result != nil {
				return result.Intent, result.Confidence
			}
		}

		// Call LLM for classification
		intent, confidence := classifyWithLLM(config, userMessage, contextData)

		// Cache result
		if cache != nil {
			cache.Set(userMessage, intent, confidence)
		}

		return intent, confidence
	}
}

// classifyWithLLM performs the actual LLM classification
func classifyWithLLM(config *LLMClassifierConfig, userMessage string, contextData map[string]any) (string, float64) {
	prompt := buildClassificationPrompt(userMessage, contextData)

	ctx := context.Background()

	// Build LLM request
	messages := []types.Message{
		{
			Role:    "user",
			Content: prompt,
		},
	}

	// Call LLM (no tools, just classification)
	resp, err := config.LLMProvider.Chat(ctx, messages, nil)

	if err != nil {
		// Fallback to keyword classifier on error
		return defaultIntentClassifier(userMessage, contextData)
	}

	// Parse JSON response
	result := parseClassificationResponse(resp.Content)
	if result == nil {
		// Fallback on parse error
		return defaultIntentClassifier(userMessage, contextData)
	}

	return result.Intent, result.Confidence
}

// buildClassificationPrompt constructs the LLM prompt for freeform intent classification.
func buildClassificationPrompt(userMessage string, contextData map[string]any) string {
	backendType := "unknown"
	if bt, ok := contextData["backend_type"].(string); ok {
		backendType = bt
	}

	prompt := fmt.Sprintf(`Classify the user's intent for a %s backend system.

Return a short, descriptive intent label (1-3 words, snake_case) that captures the user's primary goal.

Common intents include (but you may return any label that best describes the intent):
- schema_discovery: explore database structure, tables, columns, metadata
- data_quality: validate data, find duplicates, check completeness
- data_transform: move, copy, transform, or migrate data (ETL)
- analytics: aggregations, metrics, reports, statistical analysis
- relationship_query: foreign keys, relationships, joins between tables
- query_generation: generate or write queries
- document_search: search documents, full-text search
- api_call: HTTP/REST API calls

User message: "%s"

Respond ONLY with valid JSON (no markdown, no code blocks):
{
  "intent": "<intent_label>",
  "confidence": <0.0-1.0>,
  "reasoning": "<brief explanation>"
}

Guidelines:
- Use snake_case for the intent label.
- Be conservative with confidence scores. Only use >0.9 for very clear, unambiguous intents.
- Use 0.7-0.9 for probable intents with some ambiguity.
- Use <0.7 for uncertain or multi-intent queries.
- If the message is greeting/chitchat/off-topic, return "" with 0.0 confidence.`, backendType, userMessage)

	return prompt
}

// classificationResult holds parsed LLM response
type classificationResult struct {
	Intent     string  `json:"intent"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning"`
}

// parseClassificationResponse parses the LLM JSON response
func parseClassificationResponse(content string) *classificationResult {
	// Clean up potential markdown code blocks
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var result classificationResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil
	}

	// Normalize "unknown" to empty string
	if result.Intent == "unknown" {
		result.Intent = ""
		result.Confidence = 0.0
	}

	// Clamp confidence to [0.0, 1.0]
	if result.Confidence < 0.0 {
		result.Confidence = 0.0
	}
	if result.Confidence > 1.0 {
		result.Confidence = 1.0
	}

	// Empty intent always has 0.0 confidence
	if result.Intent == "" {
		result.Confidence = 0.0
	}

	return &result
}

// classificationCache provides LRU caching for classifications
type classificationCache struct {
	mu      sync.RWMutex
	maxSize int
	ttl     time.Duration
	entries map[string]*cacheEntry
}

type cacheEntry struct {
	Intent     string
	Confidence float64
	Timestamp  time.Time
}

func newClassificationCache(maxSize int, ttl time.Duration) *classificationCache {
	return &classificationCache{
		maxSize: maxSize,
		ttl:     ttl,
		entries: make(map[string]*cacheEntry),
	}
}

func (c *classificationCache) Get(message string) *cacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[message]
	if !ok {
		return nil
	}

	// Check TTL
	if time.Since(entry.Timestamp) > c.ttl {
		return nil
	}

	return entry
}

func (c *classificationCache) Set(message string, intent string, confidence float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Simple eviction: if at max size, clear 20% oldest entries
	if len(c.entries) >= c.maxSize {
		c.evictOldest(c.maxSize / 5)
	}

	c.entries[message] = &cacheEntry{
		Intent:     intent,
		Confidence: confidence,
		Timestamp:  time.Now(),
	}
}

func (c *classificationCache) evictOldest(count int) {
	// Simple eviction: remove first N entries (could be optimized with heap)
	// Note: This is called while holding the lock
	removed := 0
	for key := range c.entries {
		if removed >= count {
			break
		}
		delete(c.entries, key)
		removed++
	}
}
