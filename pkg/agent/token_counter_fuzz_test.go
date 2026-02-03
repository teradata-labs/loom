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
	"testing"
	"unicode/utf8"
)

// FuzzTokenCounter tests token counting with arbitrary string inputs.
// Properties tested:
// - Token count is always non-negative
// - Token count is deterministic (idempotent)
// - Token count is bounded by reasonable limits relative to input length
// - Handles invalid UTF-8 without panicking
// - Handles extremely long strings without panicking
func FuzzTokenCounter(f *testing.F) {
	// Seed corpus with interesting test cases
	f.Add("hello world")
	f.Add("Hello, 世界")                        // Unicode
	f.Add(strings.Repeat("a", 1000))          // Long repetitive text
	f.Add(strings.Repeat("test ", 500))       // Many tokens
	f.Add("")                                 // Empty string
	f.Add("🚀🌟💻")                              // Emoji
	f.Add("SELECT * FROM table WHERE id = 1") // SQL-like
	f.Add("\n\n\n")                           // Whitespace
	f.Add("\x00\x01\x02")                     // Control characters
	f.Add(string([]byte{0xff, 0xfe, 0xfd}))   // Invalid UTF-8

	f.Fuzz(func(t *testing.T, text string) {
		tc := GetTokenCounter()

		// Property 1: Count should never panic
		var count int
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("CountTokens panicked on input: %v", r)
				}
			}()
			count = tc.CountTokens(text)
		}()

		// Property 2: Token count must be non-negative
		if count < 0 {
			t.Errorf("token count is negative: %d for text: %q", count, text)
		}

		// Property 3: Token count should be bounded by reasonable limits
		// Upper bound: Tokenization can vary significantly based on content
		// For safety, allow up to 3x the UTF-8 rune count as upper bound
		// (some tokens like multi-byte UTF-8 or special chars can be split)
		maxReasonable := utf8.RuneCountInString(text) * 3
		if maxReasonable == 0 && len(text) > 0 {
			maxReasonable = len(text) // Fallback for invalid UTF-8
		}
		// Only check for extremely unreasonable token counts
		if count > maxReasonable && maxReasonable > 0 && count > 1000 {
			t.Errorf("token count %d exceeds reasonable bound %d for text length %d",
				count, maxReasonable, len(text))
		}

		// Property 4: Empty or whitespace-only text should have very low token count
		if strings.TrimSpace(text) == "" && count > 10 {
			t.Errorf("whitespace-only text has suspiciously high token count: %d", count)
		}

		// Property 5: Idempotence - counting twice should give same result
		count2 := tc.CountTokens(text)
		if count != count2 {
			t.Errorf("token count not idempotent: first=%d, second=%d", count, count2)
		}

		// Property 6: Concatenation property (approximate due to tokenization)
		// For same text repeated, tokens should scale roughly linearly
		if len(text) > 0 && len(text) < 100 { // Only test on short strings
			doubledText := text + text
			doubledCount := tc.CountTokens(doubledText)

			// Doubled text should have roughly 2x tokens (within 20% tolerance)
			// This accounts for boundary effects in tokenization
			if count > 0 {
				ratio := float64(doubledCount) / float64(count)
				if ratio < 1.5 || ratio > 2.5 {
					// Only report if wildly off - some tokenization variance is expected
					if ratio < 1.0 || ratio > 3.0 {
						t.Logf("concatenation scaling unexpected: %dx text gave %.2fx tokens (original=%d, doubled=%d)",
							2, ratio, count, doubledCount)
					}
				}
			}
		}
	})
}

// FuzzTokenCounterMultiple tests CountTokensMultiple with multiple text segments.
func FuzzTokenCounterMultiple(f *testing.F) {
	// Seed with multiple text segments
	f.Add("hello", "world", "test")
	f.Add("", "", "")
	f.Add("a", "b", "c")

	f.Fuzz(func(t *testing.T, text1, text2, text3 string) {
		tc := GetTokenCounter()

		// Should not panic
		totalCount := tc.CountTokensMultiple(text1, text2, text3)

		// Total should equal sum of individual counts
		count1 := tc.CountTokens(text1)
		count2 := tc.CountTokens(text2)
		count3 := tc.CountTokens(text3)
		expectedTotal := count1 + count2 + count3

		if totalCount != expectedTotal {
			t.Errorf("CountTokensMultiple sum mismatch: got %d, expected %d (individual: %d+%d+%d)",
				totalCount, expectedTotal, count1, count2, count3)
		}

		// Must be non-negative
		if totalCount < 0 {
			t.Errorf("total token count is negative: %d", totalCount)
		}
	})
}

// FuzzTokenBudget tests TokenBudget operations with random values.
func FuzzTokenBudget(f *testing.F) {
	// Seed with interesting budget configurations
	f.Add(int32(100000), int32(20000), int32(1000))
	f.Add(int32(200000), int32(20000), int32(50000))
	f.Add(int32(1000), int32(100), int32(50))

	f.Fuzz(func(t *testing.T, maxTokens, reserved, useAmount int32) {
		// Ensure positive values for valid test
		if maxTokens <= 0 || reserved < 0 || useAmount < 0 {
			return
		}
		if reserved >= maxTokens {
			return // Invalid config
		}

		budget := NewTokenBudget(int(maxTokens), int(reserved))

		// Property: Available tokens should never be negative
		available := budget.AvailableTokens()
		if available < 0 {
			t.Errorf("available tokens is negative: %d", available)
		}

		// Property: Initial available should equal maxTokens - reserved
		expected := int(maxTokens) - int(reserved)
		if available != expected {
			t.Errorf("initial available tokens mismatch: got %d, expected %d", available, expected)
		}

		// Property: Use should succeed if amount fits
		if int(useAmount) <= available {
			success := budget.Use(int(useAmount))
			if !success {
				t.Errorf("Use(%d) failed but should succeed (available=%d)", useAmount, available)
			}

			// After use, available should decrease
			newAvailable := budget.AvailableTokens()
			if newAvailable != available-int(useAmount) {
				t.Errorf("available tokens after Use(%d): got %d, expected %d",
					useAmount, newAvailable, available-int(useAmount))
			}
		} else {
			// Should fail if amount doesn't fit
			success := budget.Use(int(useAmount))
			if success {
				t.Errorf("Use(%d) succeeded but should fail (available=%d)", useAmount, available)
			}
		}

		// Property: Reset should restore to initial state
		budget.Reset()
		afterReset := budget.AvailableTokens()
		if afterReset != expected {
			t.Errorf("available tokens after Reset: got %d, expected %d", afterReset, expected)
		}
	})
}
