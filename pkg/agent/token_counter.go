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
package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/pkoukk/tiktoken-go"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TokenCounter provides accurate token counting for LLM context management.
// Uses tiktoken with cl100k_base encoding (Claude-compatible approximation).
type TokenCounter struct {
	encoder *tiktoken.Tiktoken
	mu      sync.Mutex
}

var (
	globalTokenCounter *TokenCounter
	counterInitOnce    sync.Once
)

// GetTokenCounter returns a singleton token counter instance.
func GetTokenCounter() *TokenCounter {
	counterInitOnce.Do(func() {
		// Use cl100k_base encoding (GPT-4/Claude compatible)
		// This is a good approximation for Claude models
		encoding := "cl100k_base"
		tkm, err := tiktoken.GetEncoding(encoding)
		if err != nil {
			// Fallback: use approximate counting if tiktoken fails
			globalTokenCounter = &TokenCounter{encoder: nil}
			return
		}
		globalTokenCounter = &TokenCounter{encoder: tkm}
	})
	return globalTokenCounter
}

// CountTokens returns the accurate token count for a given text.
func (tc *TokenCounter) CountTokens(text string) int {
	if tc.encoder == nil {
		// Fallback to char-based estimation if encoder not available
		return len(text) / 4
	}

	tc.mu.Lock()
	defer tc.mu.Unlock()

	tokens := tc.encoder.Encode(text, nil, nil)
	return len(tokens)
}

// CountTokensMultiple counts tokens across multiple text segments.
func (tc *TokenCounter) CountTokensMultiple(texts ...string) int {
	total := 0
	for _, text := range texts {
		total += tc.CountTokens(text)
	}
	return total
}

// EstimateMessagesTokens estimates token count for a slice of messages.
// Includes formatting overhead for message structure.
func (tc *TokenCounter) EstimateMessagesTokens(messages []Message) int {
	total := 0
	for _, msg := range messages {
		// Message overhead: role + formatting (~10 tokens per message)
		total += 10
		// Content tokens
		total += tc.CountTokens(msg.Content)
		// Tool call tokens (if present)
		if len(msg.ToolCalls) > 0 {
			total += tc.CountTokens(fmt.Sprintf("%v", msg.ToolCalls))
		}
		// Tool result tokens (if present)
		if msg.ToolResult != nil {
			total += tc.CountTokens(fmt.Sprintf("%v", *msg.ToolResult))
		}
	}
	return total
}

// CachedToolResult represents a recent tool execution stored in memory.
type CachedToolResult struct {
	ToolName      string
	Args          map[string]interface{}
	Result        string // Brief summary of result (for small results)
	Timestamp     time.Time
	DataReference *loomv1.DataReference // For large results stored in shared memory
}

// EstimateToolResultTokens estimates token count for cached tool results.
func (tc *TokenCounter) EstimateToolResultTokens(results []CachedToolResult) int {
	total := 0
	for _, result := range results {
		// Tool result overhead: name + args formatting (~20 tokens)
		total += 20
		total += tc.CountTokens(result.ToolName)
		total += tc.CountTokens(fmt.Sprintf("%v", result.Args))

		// If result has a DataReference, only count the reference metadata (~50 tokens)
		// The actual data is in shared memory and not part of the context
		if result.DataReference != nil {
			total += 50 // Fixed cost for reference metadata
		} else {
			// Small result stored inline
			total += tc.CountTokens(result.Result)
		}
	}
	return total
}

// TokenBudget represents a token budget with usage tracking.
type TokenBudget struct {
	MaxTokens      int
	UsedTokens     int
	ReservedTokens int // Reserved for output (e.g., 20000)
	mu             sync.RWMutex
}

// NewTokenBudget creates a new token budget.
// For Claude Sonnet 4.5: 200K total, reserve 20K for output = 180K available for input.
func NewTokenBudget(maxTokens, reservedForOutput int) *TokenBudget {
	return &TokenBudget{
		MaxTokens:      maxTokens,
		ReservedTokens: reservedForOutput,
		UsedTokens:     0,
	}
}

// AvailableTokens returns the number of tokens available for new content.
func (tb *TokenBudget) AvailableTokens() int {
	tb.mu.RLock()
	defer tb.mu.RUnlock()
	return tb.MaxTokens - tb.ReservedTokens - tb.UsedTokens
}

// CanFit checks if a given number of tokens can fit in the budget.
func (tb *TokenBudget) CanFit(tokens int) bool {
	return tb.AvailableTokens() >= tokens
}

// Use marks tokens as used. Returns false if budget exceeded.
func (tb *TokenBudget) Use(tokens int) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	if tokens > (tb.MaxTokens - tb.ReservedTokens - tb.UsedTokens) {
		return false
	}

	tb.UsedTokens += tokens
	return true
}

// Free returns tokens to the budget.
func (tb *TokenBudget) Free(tokens int) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.UsedTokens -= tokens
	if tb.UsedTokens < 0 {
		tb.UsedTokens = 0
	}
}

// Reset resets the used token count.
func (tb *TokenBudget) Reset() {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.UsedTokens = 0
}

// GetUsage returns current usage statistics.
func (tb *TokenBudget) GetUsage() (used, available, total int) {
	tb.mu.RLock()
	defer tb.mu.RUnlock()
	return tb.UsedTokens, tb.MaxTokens - tb.ReservedTokens - tb.UsedTokens, tb.MaxTokens
}

// UsagePercentage returns the percentage of budget used.
func (tb *TokenBudget) UsagePercentage() float64 {
	tb.mu.RLock()
	defer tb.mu.RUnlock()
	maxAvailable := tb.MaxTokens - tb.ReservedTokens
	if maxAvailable == 0 {
		return 0
	}
	return float64(tb.UsedTokens) / float64(maxAvailable) * 100
}

// IsNearLimit checks if usage is approaching budget limits.
// Returns true if usage is above the given percentage threshold.
func (tb *TokenBudget) IsNearLimit(thresholdPct float64) bool {
	return tb.UsagePercentage() >= thresholdPct
}

// IsCritical checks if usage is at critical levels (>85%).
func (tb *TokenBudget) IsCritical() bool {
	return tb.IsNearLimit(85.0)
}

// NeedsWarning checks if usage warrants a warning (>70%).
func (tb *TokenBudget) NeedsWarning() bool {
	return tb.IsNearLimit(70.0)
}
