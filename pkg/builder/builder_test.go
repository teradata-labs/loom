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
package builder

import (
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
)

// TestAgentBuilder_WithEmbeddedTracer tests embedded tracer configuration
func TestAgentBuilder_WithEmbeddedTracer(t *testing.T) {
	builder := NewAgentBuilder().WithEmbeddedTracer("memory", "")

	if builder.tracerConfig == nil {
		t.Fatal("Expected tracerConfig to be initialized")
	}
	if builder.tracerConfig.Mode != observability.TracerModeEmbedded {
		t.Errorf("Expected mode %s, got %s", observability.TracerModeEmbedded, builder.tracerConfig.Mode)
	}
	if builder.tracerConfig.EmbeddedStorageType != "memory" {
		t.Errorf("Expected storage type 'memory', got %s", builder.tracerConfig.EmbeddedStorageType)
	}
}

// TestAgentBuilder_WithEmbeddedTracer_SQLite tests SQLite configuration
func TestAgentBuilder_WithEmbeddedTracer_SQLite(t *testing.T) {
	builder := NewAgentBuilder().WithEmbeddedTracer("sqlite", "/tmp/test.db")

	if builder.tracerConfig == nil {
		t.Fatal("Expected tracerConfig to be initialized")
	}
	if builder.tracerConfig.EmbeddedStorageType != "sqlite" {
		t.Errorf("Expected storage type 'sqlite', got %s", builder.tracerConfig.EmbeddedStorageType)
	}
	if builder.tracerConfig.EmbeddedSQLitePath != "/tmp/test.db" {
		t.Errorf("Expected path '/tmp/test.db', got %s", builder.tracerConfig.EmbeddedSQLitePath)
	}
}

// TestAgentBuilder_WithAutoTracer tests auto tracer configuration
func TestAgentBuilder_WithAutoTracer(t *testing.T) {
	builder := NewAgentBuilder().WithAutoTracer()

	if builder.tracerConfig == nil {
		t.Fatal("Expected tracerConfig to be initialized")
	}
	if builder.tracerConfig.Mode != observability.TracerModeAuto {
		t.Errorf("Expected mode %s, got %s", observability.TracerModeAuto, builder.tracerConfig.Mode)
	}
	if !builder.tracerConfig.PreferEmbedded {
		t.Error("Expected PreferEmbedded to be true by default")
	}
}

// TestAgentBuilder_WithHawk tests Hawk service configuration (backward compatible)
func TestAgentBuilder_WithHawk(t *testing.T) {
	builder := NewAgentBuilder().WithHawk("http://localhost:8090")

	if builder.tracerConfig == nil {
		t.Fatal("Expected tracerConfig to be initialized")
	}
	if builder.tracerConfig.Mode != observability.TracerModeService {
		t.Errorf("Expected mode %s, got %s", observability.TracerModeService, builder.tracerConfig.Mode)
	}
	if builder.tracerConfig.HawkURL != "http://localhost:8090" {
		t.Errorf("Expected URL 'http://localhost:8090', got %s", builder.tracerConfig.HawkURL)
	}
}

// TestAgentBuilder_WithHawkAPIKey tests API key configuration
func TestAgentBuilder_WithHawkAPIKey(t *testing.T) {
	builder := NewAgentBuilder().
		WithHawk("http://localhost:8090").
		WithHawkAPIKey("test-key")

	if builder.tracerConfig == nil {
		t.Fatal("Expected tracerConfig to be initialized")
	}
	if builder.tracerConfig.HawkAPIKey != "test-key" {
		t.Errorf("Expected API key 'test-key', got %s", builder.tracerConfig.HawkAPIKey)
	}
}

// TestAgentBuilder_WithTracer tests direct tracer injection
func TestAgentBuilder_WithTracer(t *testing.T) {
	tracer := observability.NewNoOpTracer()
	builder := NewAgentBuilder().WithTracer(tracer)

	if builder.tracer == nil {
		t.Fatal("Expected tracer to be set")
	}
	if builder.tracer != tracer {
		t.Error("Expected tracer to match injected tracer")
	}
}

// TestAgentBuilder_ConfigPriority tests that direct tracer takes priority
func TestAgentBuilder_ConfigPriority(t *testing.T) {
	tracer := observability.NewNoOpTracer()
	builder := NewAgentBuilder().
		WithHawk("http://localhost:8090"). // Config set
		WithTracer(tracer)                 // Direct injection (should take priority)

	if builder.tracer != tracer {
		t.Error("Expected direct tracer to be set")
	}
	if builder.tracerConfig == nil {
		t.Error("Expected tracerConfig to still exist (for documentation)")
	}
}

// TestAgentBuilder_ChainedConfiguration tests method chaining
func TestAgentBuilder_ChainedConfiguration(t *testing.T) {
	builder := NewAgentBuilder().
		WithEmbeddedTracer("memory", "").
		WithPrompts("/tmp/prompts").
		WithGuardrails().
		WithCircuitBreakers()

	if builder.tracerConfig == nil {
		t.Fatal("Expected tracerConfig to be initialized")
	}
	if builder.promptsDir != "/tmp/prompts" {
		t.Errorf("Expected promptsDir '/tmp/prompts', got %s", builder.promptsDir)
	}
	if !builder.guardrails {
		t.Error("Expected guardrails to be enabled")
	}
	if !builder.breakers {
		t.Error("Expected circuit breakers to be enabled")
	}
}
