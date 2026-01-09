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
	"strings"
	"sync"
	"testing"
)

func TestGetTokenCounter(t *testing.T) {
	tc := GetTokenCounter()
	if tc == nil {
		t.Fatal("Expected token counter, got nil")
	}

	// Calling again should return same instance (singleton)
	tc2 := GetTokenCounter()
	if tc != tc2 {
		t.Error("Expected singleton instance, got different instances")
	}
}

func TestTokenCounter_CountTokens(t *testing.T) {
	tc := GetTokenCounter()

	tests := []struct {
		name          string
		text          string
		expectNonZero bool
	}{
		{
			name:          "empty string",
			text:          "",
			expectNonZero: false,
		},
		{
			name:          "simple text",
			text:          "Hello, world!",
			expectNonZero: true,
		},
		{
			name:          "longer text",
			text:          "This is a longer piece of text that should be counted accurately by tiktoken.",
			expectNonZero: true,
		},
		{
			name:          "SQL query",
			text:          "SELECT * FROM users WHERE age > 18 AND status = 'active' ORDER BY created_at DESC LIMIT 100;",
			expectNonZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := tc.CountTokens(tt.text)
			if tt.expectNonZero && count == 0 {
				t.Errorf("Expected non-zero token count for %q", tt.text)
			}
			if !tt.expectNonZero && count != 0 {
				t.Errorf("Expected zero tokens for empty string, got %d", count)
			}
		})
	}
}

func TestTokenCounter_CountTokensMultiple(t *testing.T) {
	tc := GetTokenCounter()

	texts := []string{
		"First piece of text.",
		"Second piece of text.",
		"Third piece of text.",
	}

	// Count individually
	expectedTotal := 0
	for _, text := range texts {
		expectedTotal += tc.CountTokens(text)
	}

	// Count all at once
	actualTotal := tc.CountTokensMultiple(texts...)

	if actualTotal != expectedTotal {
		t.Errorf("Expected total %d, got %d", expectedTotal, actualTotal)
	}
}

func TestTokenCounter_EstimateMessagesTokens(t *testing.T) {
	tc := GetTokenCounter()

	messages := []Message{
		{
			Role:    "user",
			Content: "What is the capital of France?",
		},
		{
			Role:    "assistant",
			Content: "The capital of France is Paris.",
		},
	}

	count := tc.EstimateMessagesTokens(messages)
	if count == 0 {
		t.Error("Expected non-zero token count for messages")
	}

	// Should be more than just content due to formatting overhead
	contentOnly := tc.CountTokens(messages[0].Content) + tc.CountTokens(messages[1].Content)
	if count <= contentOnly {
		t.Errorf("Expected message overhead, got %d <= %d", count, contentOnly)
	}
}

func TestTokenCounter_ConcurrentAccess(t *testing.T) {
	tc := GetTokenCounter()

	var wg sync.WaitGroup
	concurrency := 10
	iterations := 100

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			text := "This is a test string for concurrent access testing."
			for j := 0; j < iterations; j++ {
				count := tc.CountTokens(text)
				if count == 0 {
					t.Errorf("Goroutine %d iteration %d: Expected non-zero count", id, j)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestNewTokenBudget(t *testing.T) {
	maxTokens := 200000
	reserved := 20000

	tb := NewTokenBudget(maxTokens, reserved)

	if tb == nil {
		t.Fatal("Expected token budget, got nil")
	}
	if tb.MaxTokens != maxTokens {
		t.Errorf("Expected max tokens %d, got %d", maxTokens, tb.MaxTokens)
	}
	if tb.ReservedTokens != reserved {
		t.Errorf("Expected reserved tokens %d, got %d", reserved, tb.ReservedTokens)
	}
	if tb.UsedTokens != 0 {
		t.Errorf("Expected used tokens 0, got %d", tb.UsedTokens)
	}
}

func TestTokenBudget_AvailableTokens(t *testing.T) {
	tb := NewTokenBudget(200000, 20000)

	// Initially: 200K - 20K = 180K available
	expected := 180000
	actual := tb.AvailableTokens()
	if actual != expected {
		t.Errorf("Expected %d available tokens, got %d", expected, actual)
	}

	// After using some
	tb.Use(50000)
	expected = 130000
	actual = tb.AvailableTokens()
	if actual != expected {
		t.Errorf("After using 50K, expected %d available, got %d", expected, actual)
	}
}

func TestTokenBudget_Use(t *testing.T) {
	tb := NewTokenBudget(200000, 20000)

	// Use tokens within budget
	if !tb.Use(100000) {
		t.Error("Expected Use(100000) to succeed")
	}

	used, available, total := tb.GetUsage()
	if used != 100000 {
		t.Errorf("Expected 100000 used tokens, got %d", used)
	}
	if available != 80000 {
		t.Errorf("Expected 80000 available tokens, got %d", available)
	}
	if total != 200000 {
		t.Errorf("Expected 200000 total tokens, got %d", total)
	}

	// Try to use more than available
	if tb.Use(100000) {
		t.Error("Expected Use(100000) to fail when only 80K available")
	}
}

func TestTokenBudget_Free(t *testing.T) {
	tb := NewTokenBudget(200000, 20000)

	tb.Use(100000)

	// Free some tokens
	tb.Free(30000)

	used, _, _ := tb.GetUsage()
	if used != 70000 {
		t.Errorf("Expected 70000 used tokens after freeing 30K, got %d", used)
	}

	// Free more than used (should not go negative)
	tb.Free(100000)
	used, _, _ = tb.GetUsage()
	if used != 0 {
		t.Errorf("Expected 0 used tokens after freeing more than used, got %d", used)
	}
}

func TestTokenBudget_Reset(t *testing.T) {
	tb := NewTokenBudget(200000, 20000)

	tb.Use(100000)
	tb.Reset()

	used, _, _ := tb.GetUsage()
	if used != 0 {
		t.Errorf("Expected 0 used tokens after reset, got %d", used)
	}
}

func TestTokenBudget_UsagePercentage(t *testing.T) {
	tb := NewTokenBudget(200000, 20000)

	// No usage
	pct := tb.UsagePercentage()
	if pct != 0 {
		t.Errorf("Expected 0%% usage, got %.2f%%", pct)
	}

	// 50% usage (90K out of 180K)
	tb.Use(90000)
	pct = tb.UsagePercentage()
	if pct < 49 || pct > 51 {
		t.Errorf("Expected ~50%% usage, got %.2f%%", pct)
	}

	// 100% usage
	tb.Use(90000) // Total 180K
	pct = tb.UsagePercentage()
	if pct < 99 || pct > 101 {
		t.Errorf("Expected ~100%% usage, got %.2f%%", pct)
	}
}

func TestTokenBudget_CanFit(t *testing.T) {
	tb := NewTokenBudget(200000, 20000)

	if !tb.CanFit(100000) {
		t.Error("Expected 100K tokens to fit in fresh budget")
	}

	tb.Use(100000)

	if !tb.CanFit(80000) {
		t.Error("Expected 80K tokens to fit after using 100K")
	}

	if tb.CanFit(90000) {
		t.Error("Expected 90K tokens NOT to fit (only 80K available)")
	}
}

func TestTokenBudget_IsNearLimit(t *testing.T) {
	tb := NewTokenBudget(200000, 20000)

	if tb.IsNearLimit(70.0) {
		t.Error("Fresh budget should not be near 70% limit")
	}

	// Use 70% (126K out of 180K)
	tb.Use(126000)
	if !tb.IsNearLimit(70.0) {
		t.Error("Should be near 70% limit after using 126K")
	}

	// Use 86% (154.8K out of 180K)
	tb.Reset()
	tb.Use(154800)
	if !tb.IsNearLimit(85.0) {
		t.Error("Should be near 85% limit after using 154.8K")
	}
}

func TestTokenBudget_IsCritical(t *testing.T) {
	tb := NewTokenBudget(200000, 20000)

	if tb.IsCritical() {
		t.Error("Fresh budget should not be critical")
	}

	// Use 86% (154.8K out of 180K)
	tb.Use(154800)
	if !tb.IsCritical() {
		t.Error("Should be critical after using 86%")
	}
}

func TestTokenBudget_NeedsWarning(t *testing.T) {
	tb := NewTokenBudget(200000, 20000)

	if tb.NeedsWarning() {
		t.Error("Fresh budget should not need warning")
	}

	// Use 71% (127.8K out of 180K)
	tb.Use(127800)
	if !tb.NeedsWarning() {
		t.Error("Should need warning after using 71%")
	}
}

func TestTokenBudget_ConcurrentAccess(t *testing.T) {
	tb := NewTokenBudget(200000, 20000)

	var wg sync.WaitGroup
	concurrency := 10
	tokensPerGoroutine := 1000

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				tb.Use(tokensPerGoroutine)
				tb.Free(tokensPerGoroutine)
			}
		}()
	}

	wg.Wait()

	// After all concurrent use/free, should be back to 0
	used, _, _ := tb.GetUsage()
	if used != 0 {
		t.Errorf("Expected 0 used tokens after concurrent operations, got %d", used)
	}
}

// Benchmark tests
func BenchmarkTokenCounter_CountTokens(b *testing.B) {
	tc := GetTokenCounter()
	text := "This is a sample text to benchmark token counting performance."

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tc.CountTokens(text)
	}
}

func BenchmarkTokenCounter_CountTokensLarge(b *testing.B) {
	tc := GetTokenCounter()
	// Generate a large text (~1000 tokens)
	text := strings.Repeat("This is a sample sentence for benchmarking. ", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tc.CountTokens(text)
	}
}

func BenchmarkTokenBudget_Use(b *testing.B) {
	tb := NewTokenBudget(200000, 20000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tb.Use(1000)
		tb.Free(1000)
	}
}
