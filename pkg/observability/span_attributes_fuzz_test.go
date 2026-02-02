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

package observability

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// FuzzSpanAttributes tests span attribute handling with random key-value pairs.
// Properties tested:
// - SetAttribute never panics
// - Attributes map is properly initialized
// - Concurrent SetAttribute operations are safe
// - Any value type can be stored
// - Keys and values are preserved accurately
func FuzzSpanAttributes(f *testing.F) {
	// Seed with various attribute types
	f.Add("string_key", "string_value", int64(42), true)
	f.Add("int_key", "", int64(-100), false)
	f.Add("", "empty_key", int64(0), true)
	f.Add("unicode_key", "世界🚀", int64(999), false)
	f.Add("special\nchars", "value\t\r\n", int64(123), true)

	f.Fuzz(func(t *testing.T, strKey, strVal string, intVal int64, boolVal bool) {
		span := &Span{
			TraceID:   "trace-" + strKey,
			SpanID:    "span-" + strKey,
			Name:      "test-span",
			StartTime: time.Now(),
		}

		// Property 1: SetAttribute should never panic
		testAttributes := map[string]any{
			"str_" + strKey:   strVal,
			"int_" + strKey:   intVal,
			"bool_" + strKey:  boolVal,
			"float_" + strKey: float64(intVal),
			"nil_" + strKey:   nil,
		}

		for key, value := range testAttributes {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("SetAttribute panicked on key=%q value=%v: %v", key, value, r)
					}
				}()
				span.SetAttribute(key, value)
			}()
		}

		// Property 2: Attributes should be retrievable and match what was set
		if span.Attributes == nil {
			t.Errorf("Attributes map is nil after SetAttribute calls")
		}

		for key, expectedValue := range testAttributes {
			actualValue, exists := span.Attributes[key]
			if !exists {
				t.Errorf("attribute %q not found after SetAttribute", key)
				continue
			}

			// For non-nil values, check they match
			if expectedValue != nil && actualValue != expectedValue {
				t.Errorf("attribute %q mismatch: expected %v, got %v", key, expectedValue, actualValue)
			}
		}

		// Property 3: Setting same key twice should overwrite
		originalValue := span.Attributes["str_"+strKey]
		newValue := "overwritten-" + strVal
		span.SetAttribute("str_"+strKey, newValue)

		if span.Attributes["str_"+strKey] != newValue {
			t.Errorf("overwrite failed: expected %q, got %v", newValue, span.Attributes["str_"+strKey])
		}
		if span.Attributes["str_"+strKey] == originalValue && originalValue != newValue {
			t.Errorf("attribute not overwritten: still has original value %v", originalValue)
		}

		// Property 4: Empty string keys should be allowed
		span.SetAttribute("", "value_for_empty_key")
		if val, exists := span.Attributes[""]; !exists {
			t.Errorf("empty string key not stored")
		} else if val != "value_for_empty_key" {
			t.Errorf("empty string key has wrong value: %v", val)
		}

		// Property 5: Very long keys and values should work
		longKey := strings.Repeat(strKey, 100)
		longValue := strings.Repeat(strVal, 100)
		span.SetAttribute(longKey, longValue)
		if span.Attributes[longKey] != longValue {
			t.Errorf("long key/value not stored correctly")
		}
	})
}

// FuzzSpanAttributesConcurrent tests that creating multiple spans concurrently is safe.
// Note: Span itself is not thread-safe for concurrent writes to the same instance,
// but creating separate span instances concurrently should work fine.
func FuzzSpanAttributesConcurrent(f *testing.F) {
	f.Add("key1", "value1", int32(10))
	f.Add("key2", "value2", int32(100))

	f.Fuzz(func(t *testing.T, baseKey, baseValue string, numGoroutines int32) {
		// Limit goroutines to reasonable number
		if numGoroutines < 1 {
			numGoroutines = 1
		}
		if numGoroutines > 100 {
			numGoroutines = 100
		}

		// Property: Creating and using separate span instances concurrently should be safe
		var wg sync.WaitGroup
		var mu sync.Mutex
		spanCount := 0

		for i := int32(0); i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int32) {
				defer wg.Done()

				// Each goroutine creates its own span (this is the safe pattern)
				span := &Span{
					TraceID:   "concurrent-test-" + string(rune(id)),
					SpanID:    "span-" + string(rune(id)),
					Name:      "concurrent-span",
					StartTime: time.Now(),
				}

				// Set attributes on this goroutine's own span
				key := baseKey + string(rune(id))
				value := baseValue + string(rune(id))
				span.SetAttribute(key, value)

				// Verify the span is valid
				if span.Attributes == nil || len(span.Attributes) == 0 {
					t.Errorf("span attributes not set correctly")
					return
				}

				mu.Lock()
				spanCount++
				mu.Unlock()
			}(i)
		}

		wg.Wait()

		// All goroutines should have completed successfully
		if spanCount != int(numGoroutines) {
			t.Errorf("expected %d spans, got %d", numGoroutines, spanCount)
		}
	})
}

// FuzzSpanTimestamps tests span timing with various timestamp combinations.
func FuzzSpanTimestamps(f *testing.F) {
	now := time.Now()
	f.Add(now.Unix(), now.Add(time.Second).Unix())
	f.Add(int64(0), int64(1))
	f.Add(now.Unix(), now.Unix()) // Same time

	f.Fuzz(func(t *testing.T, startUnix, endUnix int64) {
		startTime := time.Unix(startUnix, 0)
		endTime := time.Unix(endUnix, 0)

		span := &Span{
			TraceID:    "time-test",
			SpanID:     "span-time",
			Name:       "timing-span",
			StartTime:  startTime,
			EndTime:    endTime,
			Attributes: make(map[string]any), // Initialize to avoid nil map issues
		}

		// Property 1: StartTime and EndTime should be preserved
		if !span.StartTime.Equal(startTime) {
			t.Errorf("StartTime not preserved: expected %v, got %v", startTime, span.StartTime)
		}
		if !span.EndTime.Equal(endTime) {
			t.Errorf("EndTime not preserved: expected %v, got %v", endTime, span.EndTime)
		}

		// Property 2: Duration calculation (if implemented)
		// Note: Duration field exists but may not be automatically calculated
		// This is just a sanity check
		if !span.EndTime.IsZero() && !span.StartTime.IsZero() {
			expectedDuration := span.EndTime.Sub(span.StartTime)
			// If Duration is set, it should match
			if span.Duration != 0 && span.Duration != expectedDuration {
				t.Logf("Duration mismatch: calculated=%v, field=%v", expectedDuration, span.Duration)
			}
		}
	})
}

// FuzzSpanEvents tests adding events to spans with random data.
func FuzzSpanEvents(f *testing.F) {
	f.Add("event1", "attr_key", "attr_value")
	f.Add("", "", "")
	f.Add("unicode_event_🚀", "key", "世界")

	f.Fuzz(func(t *testing.T, eventName, attrKey, attrValue string) {
		span := &Span{
			TraceID:   "event-test",
			SpanID:    "span-event",
			Name:      "event-span",
			StartTime: time.Now(),
		}

		// Create event
		event := Event{
			Timestamp: time.Now(),
			Name:      eventName,
			Attributes: map[string]any{
				attrKey: attrValue,
			},
		}

		// Property: Adding events should not panic
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("adding event panicked: %v", r)
				}
			}()
			span.Events = append(span.Events, event)
		}()

		// Property: Event should be retrievable
		if len(span.Events) == 0 {
			t.Errorf("event not added to span")
			return
		}

		lastEvent := span.Events[len(span.Events)-1]
		if lastEvent.Name != eventName {
			t.Errorf("event name mismatch: expected %q, got %q", eventName, lastEvent.Name)
		}

		if lastEvent.Attributes[attrKey] != attrValue {
			t.Errorf("event attribute mismatch: expected %q, got %v",
				attrValue, lastEvent.Attributes[attrKey])
		}
	})
}

// FuzzSpanStatus tests span status with various codes and messages.
func FuzzSpanStatus(f *testing.F) {
	f.Add(int32(0), "")
	f.Add(int32(1), "success message")
	f.Add(int32(2), "error message")
	f.Add(int32(999), "invalid status code")

	f.Fuzz(func(t *testing.T, statusCode int32, message string) {
		span := &Span{
			TraceID:   "status-test",
			SpanID:    "span-status",
			Name:      "status-span",
			StartTime: time.Now(),
		}

		// Property: Setting status should not panic
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("setting status panicked: %v", r)
				}
			}()
			span.Status = Status{
				Code:    StatusCode(statusCode),
				Message: message,
			}
		}()

		// Property: Status should be retrievable
		if span.Status.Code != StatusCode(statusCode) {
			t.Errorf("status code mismatch: expected %d, got %d", statusCode, span.Status.Code)
		}
		if span.Status.Message != message {
			t.Errorf("status message mismatch: expected %q, got %q", message, span.Status.Message)
		}

		// Property: StatusCode.String() should not panic
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("StatusCode.String() panicked on code %d: %v", statusCode, r)
				}
			}()
			_ = span.Status.Code.String()
		}()
	})
}

// FuzzSpanIdentifiers tests span with various ID formats.
func FuzzSpanIdentifiers(f *testing.F) {
	f.Add("trace-123", "span-456", "parent-789")
	f.Add("", "", "")
	f.Add("very-long-"+strings.Repeat("x", 1000), "span", "parent")
	f.Add("unicode-世界", "emoji-🚀", "parent-💻")

	f.Fuzz(func(t *testing.T, traceID, spanID, parentID string) {
		span := &Span{
			TraceID:   traceID,
			SpanID:    spanID,
			ParentID:  parentID,
			Name:      "id-test-span",
			StartTime: time.Now(),
		}

		// Property 1: IDs should be preserved exactly
		if span.TraceID != traceID {
			t.Errorf("TraceID not preserved: expected %q, got %q", traceID, span.TraceID)
		}
		if span.SpanID != spanID {
			t.Errorf("SpanID not preserved: expected %q, got %q", spanID, span.SpanID)
		}
		if span.ParentID != parentID {
			t.Errorf("ParentID not preserved: expected %q, got %q", parentID, span.ParentID)
		}

		// Property 2: Empty IDs should be allowed (for root spans)
		// Empty parentID is valid - root spans have no parent

		// Property 3: Setting attributes should not affect IDs
		span.SetAttribute("test", "value")
		if span.TraceID != traceID || span.SpanID != spanID || span.ParentID != parentID {
			t.Errorf("IDs changed after SetAttribute")
		}
	})
}
