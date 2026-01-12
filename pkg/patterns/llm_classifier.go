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

	return func(userMessage string, contextData map[string]any) (IntentCategory, float64) {
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
func classifyWithLLM(config *LLMClassifierConfig, userMessage string, contextData map[string]any) (IntentCategory, float64) {
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

// buildClassificationPrompt constructs the LLM prompt
func buildClassificationPrompt(userMessage string, contextData map[string]any) string {
	// Extract backend type if available
	backendType := "unknown"
	if bt, ok := contextData["backend_type"].(string); ok {
		backendType = bt
	}

	prompt := fmt.Sprintf(`Classify the user's intent for a %s backend system.

Available intent categories:
1. schema_discovery - User wants to explore database structure, tables, columns, metadata
   Examples: "show me all tables", "what columns are in orders table", "describe the schema"

2. data_quality - User wants to validate data, find duplicates, check completeness, integrity
   Examples: "find duplicate records", "check for null values", "validate data quality"

3. data_transform - User wants to move, copy, transform, or migrate data (ETL operations)
   Examples: "move data from A to B", "transform customer records", "migrate the database"

4. analytics - User wants aggregations, metrics, reports, statistical analysis
   Examples: "analyze sales trends", "calculate average revenue", "show top 10 customers"

5. relationship_query - User wants to understand foreign keys, relationships, joins between tables
   Examples: "how are these tables related", "find foreign keys", "what connects orders to customers"

6. query_generation - User wants to generate or write queries
   Examples: "write a query to find X", "generate SQL for Y", "select all customers where Z"

7. document_search - User wants to search documents or perform text searches
   Examples: "search for documents containing X", "find text matching Y", "full-text search"

8. api_call - User wants to make HTTP/REST API calls
   Examples: "call the user API", "make a GET request to X", "post data to the endpoint"

9. unknown - Intent doesn't clearly match any category

User message: "%s"

Classify this intent. Respond ONLY with valid JSON (no markdown, no code blocks):
{
  "intent": "<category_name>",
  "confidence": <0.0-1.0>,
  "reasoning": "<brief explanation>"
}

Guidelines:
- Be conservative with confidence scores. Only use >0.9 for very clear, unambiguous intents.
- Use 0.7-0.9 for probable intents with some ambiguity.
- Use <0.7 for uncertain or multi-intent queries.
- If the message is greeting/chitchat/off-topic, use "unknown" with low confidence.`, backendType, userMessage)

	return prompt
}

// classificationResult holds parsed LLM response
type classificationResult struct {
	Intent     IntentCategory `json:"intent"`
	Confidence float64        `json:"confidence"`
	Reasoning  string         `json:"reasoning"`
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

	// Validate intent category
	validIntents := map[IntentCategory]bool{
		IntentSchemaDiscovery:   true,
		IntentDataQuality:       true,
		IntentDataTransform:     true,
		IntentAnalytics:         true,
		IntentRelationshipQuery: true,
		IntentQueryGeneration:   true,
		IntentDocumentSearch:    true,
		IntentAPICall:           true,
		IntentUnknown:           true,
	}

	if !validIntents[result.Intent] {
		return nil
	}

	// Clamp confidence to [0.0, 1.0]
	if result.Confidence < 0.0 {
		result.Confidence = 0.0
	}
	if result.Confidence > 1.0 {
		result.Confidence = 1.0
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
	Intent     IntentCategory
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

func (c *classificationCache) Set(message string, intent IntentCategory, confidence float64) {
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
