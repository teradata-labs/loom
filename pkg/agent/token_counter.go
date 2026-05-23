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
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/pkoukk/tiktoken-go"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

const (
	// EncoderPoolSizeEnvVar lets operators override the tiktoken encoder pool
	// size without a recompile. When set to a positive integer, the singleton
	// TokenCounter uses that value; anything else (unset, non-numeric, <=0)
	// falls back to the GOMAXPROCS-driven default.
	//
	// The value is parsed with strconv.Atoi and NOT whitespace-trimmed, so
	// shell-quoting accidents like LOOM_TOKEN_ENCODER_POOL_SIZE="16 " (trailing
	// space) will silently fall through to the default. If an override you set
	// does not appear to take effect, re-export without surrounding whitespace.
	EncoderPoolSizeEnvVar = "LOOM_TOKEN_ENCODER_POOL_SIZE"

	// encoderPoolSizeFloor is the minimum pool size. Kept at 16 so dev machines
	// and small containers (GOMAXPROCS 4-8) behave exactly as they did before
	// this resolver was introduced.
	encoderPoolSizeFloor = 16
)

// encoderPoolSize is resolved once at package init. It is a var, not a const,
// so the value depends on GOMAXPROCS and env at startup rather than being baked
// in at compile time. See resolveEncoderPoolSize for the full precedence order.
//
// Sizing rationale, from an AKS microbench on a 32 vCPU Intel Xeon 8473C
// (Platinum, 2026-04-23):
//
//	goroutines  pool=8   pool=16  pool=32  pool=64
//	16          63k      381k     374k     384k ops/s
//	32          37k      99k      381k     354k
//	64          30k      60k      111k     369k
//
// Once concurrent callers exceed pool size, throughput collapses because every
// call blocks on <-tc.encoders. At g=32 the hardcoded 16 gave 99k ops/s versus
// 381k at pool=32 — a 3.8x loss of throughput from a 1 MB memory savings.
// Memory per extra encoder is ~45 KB (the cl100k_base MergeableRanks map is
// shared via tiktoken's internal encodingMap cache; only the per-instance
// decoder map, sortedTokenBytes, and compiled regex are paid per encoder).
var encoderPoolSize = resolveEncoderPoolSize()

// resolveEncoderPoolSize picks the pool size with this precedence:
//  1. LOOM_TOKEN_ENCODER_POOL_SIZE env var, if a positive integer
//  2. runtime.GOMAXPROCS(0), floored at encoderPoolSizeFloor
//
// Invalid env values (non-numeric, <=0) silently fall through to the default so
// a typo or a misconfigured container doesn't take the process down. The
// chosen value is logged once at GetTokenCounter initialization for ops
// visibility.
func resolveEncoderPoolSize() int {
	if raw := os.Getenv(EncoderPoolSizeEnvVar); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	procs := runtime.GOMAXPROCS(0)
	if procs < encoderPoolSizeFloor {
		return encoderPoolSizeFloor
	}
	return procs
}

// EncoderPoolSize returns the resolved size of the singleton TokenCounter's
// encoder pool. Exposed for tests and startup diagnostics; not a reconfiguration
// hook — the channel is built once in GetTokenCounter.
func EncoderPoolSize() int { return encoderPoolSize }

// TokenCounter provides accurate token counting for LLM context management.
// Uses tiktoken with cl100k_base encoding (Claude-compatible approximation).
//
// Internally uses a fixed-size channel pool of tiktoken encoders so that
// concurrent goroutines can count tokens without contending on a single mutex.
// Unlike sync.Pool, the channel pool is not subject to GC draining.
type TokenCounter struct {
	encoders chan *tiktoken.Tiktoken // Fixed-size pool of encoders
	encoder  *tiktoken.Tiktoken      // Kept for API compatibility (tests that inspect .encoder)
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

		encoders := make(chan *tiktoken.Tiktoken, encoderPoolSize)
		var first *tiktoken.Tiktoken

		for range encoderPoolSize {
			tkm, err := tiktoken.GetEncoding(encoding)
			if err != nil {
				// If we can't create any encoder, fall back
				if first == nil {
					globalTokenCounter = &TokenCounter{encoder: nil, encoders: nil}
					return
				}
				break
			}
			if first == nil {
				first = tkm
			}
			encoders <- tkm
		}

		globalTokenCounter = &TokenCounter{
			encoder:  first, // kept for test introspection (non-nil means encoder available)
			encoders: encoders,
		}

		// Log the resolved pool size exactly once per process. Useful for
		// confirming a LOOM_TOKEN_ENCODER_POOL_SIZE override took effect or for
		// spotting GOMAXPROCS-vs-container-quota mismatches in k8s deploys.
		// Logged at Info because it fires once per process lifetime -- the
		// ops-visibility goal outweighs the Debug-level default, and volume is
		// not a concern.
		zap.L().Info("token encoder pool initialized",
			zap.Int("pool_size", encoderPoolSize),
			zap.Int("gomaxprocs", runtime.GOMAXPROCS(0)),
			zap.Int("num_cpu", runtime.NumCPU()),
			zap.String("env_override", os.Getenv(EncoderPoolSizeEnvVar)),
		)
	})
	return globalTokenCounter
}

// CountTokens returns the accurate token count for a given text.
// Borrows an encoder from the fixed-size channel pool so concurrent
// callers don't contend on a single mutex. If all encoders are in use,
// the caller blocks until one is returned — no allocation, no GC pressure.
func (tc *TokenCounter) CountTokens(text string) int {
	if tc.encoders == nil {
		// Fallback to char-based estimation if encoder not available
		return len(text) / 4
	}

	tkm := <-tc.encoders // borrow
	tokens := tkm.Encode(text, nil, nil)
	tc.encoders <- tkm // return
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
