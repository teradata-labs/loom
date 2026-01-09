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
	"testing"
	"time"
)

func TestStatusCodeString(t *testing.T) {
	tests := []struct {
		code StatusCode
		want string
	}{
		{StatusUnset, "unset"},
		{StatusOK, "ok"},
		{StatusError, "error"},
		{StatusCode(999), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.code.String(); got != tt.want {
			t.Errorf("StatusCode(%d).String() = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestSpanSetAttribute(t *testing.T) {
	span := &Span{}

	span.SetAttribute("key1", "value1")
	span.SetAttribute("key2", 42)

	if span.Attributes["key1"] != "value1" {
		t.Errorf("Expected key1=value1, got %v", span.Attributes["key1"])
	}
	if span.Attributes["key2"] != 42 {
		t.Errorf("Expected key2=42, got %v", span.Attributes["key2"])
	}
}

func TestSpanAddEvent(t *testing.T) {
	span := &Span{}

	before := time.Now()
	span.AddEvent("test_event", map[string]interface{}{
		"detail": "something happened",
	})
	after := time.Now()

	if len(span.Events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(span.Events))
	}

	event := span.Events[0]
	if event.Name != "test_event" {
		t.Errorf("Expected event name 'test_event', got %q", event.Name)
	}
	if event.Attributes["detail"] != "something happened" {
		t.Errorf("Expected detail attribute, got %v", event.Attributes["detail"])
	}
	if event.Timestamp.Before(before) || event.Timestamp.After(after) {
		t.Errorf("Event timestamp %v not in expected range [%v, %v]", event.Timestamp, before, after)
	}
}

func TestSpanOptions(t *testing.T) {
	span := &Span{Attributes: make(map[string]interface{})}

	// Test WithAttribute
	opt := WithAttribute("test_key", "test_value")
	opt(span)
	if span.Attributes["test_key"] != "test_value" {
		t.Errorf("WithAttribute failed: got %v", span.Attributes["test_key"])
	}

	// Test WithSpanKind
	opt = WithSpanKind("test_kind")
	opt(span)
	if span.Attributes["span.kind"] != "test_kind" {
		t.Errorf("WithSpanKind failed: got %v", span.Attributes["span.kind"])
	}

	// Test WithParentSpanID
	opt = WithParentSpanID("parent-123")
	opt(span)
	if span.ParentID != "parent-123" {
		t.Errorf("WithParentSpanID failed: got %v", span.ParentID)
	}
}
