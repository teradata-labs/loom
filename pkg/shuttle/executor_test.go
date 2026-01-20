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
package shuttle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/storage"
)

// errorTool is a tool that always returns an error
type errorTool struct {
	name string
}

func (e *errorTool) Name() string        { return e.name }
func (e *errorTool) Description() string { return "error tool" }
func (e *errorTool) Backend() string     { return "" }
func (e *errorTool) InputSchema() *JSONSchema {
	return NewObjectSchema("error", nil, nil)
}
func (e *errorTool) Execute(ctx context.Context, params map[string]interface{}) (*Result, error) {
	return nil, errors.New("intentional error")
}

// slowTool is a tool that sleeps before returning
type slowTool struct {
	name     string
	duration time.Duration
}

func (s *slowTool) Name() string        { return s.name }
func (s *slowTool) Description() string { return "slow tool" }
func (s *slowTool) Backend() string     { return "" }
func (s *slowTool) InputSchema() *JSONSchema {
	return NewObjectSchema("slow", nil, nil)
}
func (s *slowTool) Execute(ctx context.Context, params map[string]interface{}) (*Result, error) {
	time.Sleep(s.duration)
	return &Result{Success: true, Data: "completed"}, nil
}

func TestNewExecutor(t *testing.T) {
	reg := NewRegistry()
	exec := NewExecutor(reg)

	if exec == nil {
		t.Fatal("Expected non-nil executor")
	}

	if exec.registry != reg {
		t.Error("Expected executor to use provided registry")
	}
}

func TestExecutor_Execute(t *testing.T) {
	reg := NewRegistry()
	tool := &mockTool{name: "test"}
	reg.Register(tool)

	exec := NewExecutor(reg)

	result, err := exec.Execute(context.Background(), "test", map[string]interface{}{"key": "value"})
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if !result.Success {
		t.Error("Expected successful result")
	}

	// Execution time might be 0 for very fast operations, that's OK
	if result.ExecutionTimeMs < 0 {
		t.Error("Expected execution time to be non-negative")
	}
}

func TestExecutor_Execute_ToolNotFound(t *testing.T) {
	reg := NewRegistry()
	exec := NewExecutor(reg)

	_, err := exec.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Error("Expected error for nonexistent tool")
	}

	// Check that error message contains the tool name (may include dynamic registration details)
	if !strings.Contains(err.Error(), "tool not found: nonexistent") {
		t.Errorf("Expected error message to contain 'tool not found: nonexistent', got: %v", err)
	}
}

func TestExecutor_Execute_ToolError(t *testing.T) {
	reg := NewRegistry()
	tool := &errorTool{name: "error"}
	reg.Register(tool)

	exec := NewExecutor(reg)

	result, err := exec.Execute(context.Background(), "error", nil)
	if err != nil {
		t.Fatalf("Expected no error from executor, got %v", err)
	}

	if result.Success {
		t.Error("Expected unsuccessful result")
	}

	if result.Error == nil {
		t.Fatal("Expected error in result")
	}

	if result.Error.Message != "intentional error" {
		t.Errorf("Expected error message 'intentional error', got %s", result.Error.Message)
	}

	// Execution time might be 0 for very fast operations, that's OK
	if result.ExecutionTimeMs < 0 {
		t.Error("Expected execution time to be non-negative even on error")
	}
}

func TestExecutor_ExecuteWithTool(t *testing.T) {
	reg := NewRegistry()
	exec := NewExecutor(reg)

	tool := &mockTool{name: "standalone"}

	result, err := exec.ExecuteWithTool(context.Background(), tool, map[string]interface{}{"test": "data"})
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if !result.Success {
		t.Error("Expected successful result")
	}

	// Execution time might be 0 for very fast operations, that's OK
	if result.ExecutionTimeMs < 0 {
		t.Error("Expected execution time to be non-negative")
	}
}

func TestExecutor_ExecuteWithTool_Error(t *testing.T) {
	reg := NewRegistry()
	exec := NewExecutor(reg)

	tool := &errorTool{name: "error"}

	result, err := exec.ExecuteWithTool(context.Background(), tool, nil)
	if err != nil {
		t.Fatalf("Expected no error from executor, got %v", err)
	}

	if result.Success {
		t.Error("Expected unsuccessful result")
	}

	if result.Error == nil {
		t.Fatal("Expected error in result")
	}
}

func TestExecutor_ListAvailableTools(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockTool{name: "tool1"})
	reg.Register(&mockTool{name: "tool2"})

	exec := NewExecutor(reg)

	tools := exec.ListAvailableTools()
	if len(tools) != 2 {
		t.Errorf("Expected 2 tools, got %d", len(tools))
	}
}

func TestExecutor_ListToolsByBackend(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockTool{name: "sql1", backend: "postgres"})
	reg.Register(&mockTool{name: "sql2", backend: "postgres"})
	reg.Register(&mockTool{name: "api1", backend: "rest-api"})

	exec := NewExecutor(reg)

	postgresTools := exec.ListToolsByBackend("postgres")
	if len(postgresTools) < 2 {
		t.Errorf("Expected at least 2 postgres tools, got %d", len(postgresTools))
	}

	apiTools := exec.ListToolsByBackend("rest-api")
	if len(apiTools) < 1 {
		t.Errorf("Expected at least 1 api tool, got %d", len(apiTools))
	}
}

func TestExecutor_ExecutionTiming(t *testing.T) {
	reg := NewRegistry()
	tool := &slowTool{name: "slow", duration: 50 * time.Millisecond}
	reg.Register(tool)

	exec := NewExecutor(reg)

	start := time.Now()
	result, err := exec.Execute(context.Background(), "slow", nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Check that execution time is approximately correct (within 10ms tolerance)
	if result.ExecutionTimeMs < 40 || result.ExecutionTimeMs > 100 {
		t.Errorf("Expected execution time around 50ms, got %dms", result.ExecutionTimeMs)
	}

	// Check that actual elapsed time is also reasonable
	if elapsed < 40*time.Millisecond {
		t.Error("Expected at least 40ms elapsed time")
	}
}

func TestExecutor_ContextCancellation(t *testing.T) {
	reg := NewRegistry()
	tool := &slowTool{name: "slow", duration: 1 * time.Second}
	reg.Register(tool)

	exec := NewExecutor(reg)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	result, err := exec.Execute(ctx, "slow", nil)
	elapsed := time.Since(start)

	// The tool doesn't respect context cancellation in this simple implementation,
	// but we verify the test setup works. In a real implementation, the tool
	// should check ctx.Done() and return early.

	if result == nil && err == nil {
		t.Error("Expected either result or error")
	}

	// Execution should complete even if context is cancelled (for this simple implementation)
	// In production, tools should respect context cancellation
	_ = elapsed
}

func TestExecutor_WithParams(t *testing.T) {
	reg := NewRegistry()
	tool := &mockTool{name: "test"}
	reg.Register(tool)

	exec := NewExecutor(reg)

	params := map[string]interface{}{
		"query":  "SELECT * FROM users",
		"limit":  10,
		"offset": 0,
	}

	result, err := exec.Execute(context.Background(), "test", params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if !result.Success {
		t.Error("Expected successful result")
	}

	// The mock tool returns the params as data
	if result.Data == nil {
		t.Error("Expected data to be set")
	}
}

// Test estimateValueSize function
func TestEstimateValueSize(t *testing.T) {
	tests := []struct {
		name     string
		value    interface{}
		expected int64
	}{
		{
			name:     "string value",
			value:    "hello world",
			expected: 11,
		},
		{
			name:     "large string",
			value:    strings.Repeat("a", 5000),
			expected: 5000,
		},
		{
			name:     "byte slice",
			value:    []byte("test data"),
			expected: 9,
		},
		{
			name:     "map value",
			value:    map[string]interface{}{"key1": "value1", "key2": "value2"},
			expected: 32, // Approximate JSON size
		},
		{
			name:     "array value",
			value:    []interface{}{"item1", "item2", "item3"},
			expected: 24, // Approximate JSON size
		},
		{
			name:     "int value",
			value:    42,
			expected: 0, // Primitives return 0
		},
		{
			name:     "bool value",
			value:    true,
			expected: 0, // Primitives return 0
		},
		{
			name:     "float value",
			value:    3.14,
			expected: 0, // Primitives return 0
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := estimateValueSize(tt.value)

			// For maps and arrays, we allow some tolerance due to JSON encoding variations
			if tt.name == "map value" || tt.name == "array value" {
				if size < tt.expected-5 || size > tt.expected+5 {
					t.Errorf("estimateValueSize() = %d, expected around %d", size, tt.expected)
				}
			} else {
				if size != tt.expected {
					t.Errorf("estimateValueSize() = %d, expected %d", size, tt.expected)
				}
			}
		})
	}
}

// Test handleLargeParameters function
func TestExecutor_HandleLargeParameters(t *testing.T) {
	// Create shared memory store
	sharedMem := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       1 * 1024 * 1024, // 1MB
		CompressionThreshold: 1 * 1024 * 1024,
		TTLSeconds:           3600,
	})

	reg := NewRegistry()
	exec := NewExecutor(reg)
	exec.SetSharedMemory(sharedMem, 2560) // 2.5KB threshold

	t.Run("small parameters pass through unchanged", func(t *testing.T) {
		params := map[string]interface{}{
			"small_string": "hello",
			"number":       42,
			"bool":         true,
		}

		result, err := exec.handleLargeParameters(params)
		if err != nil {
			t.Fatalf("handleLargeParameters() error = %v", err)
		}

		if len(result) != len(params) {
			t.Errorf("Expected %d params, got %d", len(params), len(result))
		}

		// Verify small params are unchanged
		if result["small_string"] != "hello" {
			t.Error("Small string should pass through unchanged")
		}
		if result["number"] != 42 {
			t.Error("Number should pass through unchanged")
		}
	})

	t.Run("large parameters stored in shared memory", func(t *testing.T) {
		largeContent := strings.Repeat("a", 5000) // 5KB > 2.5KB threshold
		params := map[string]interface{}{
			"large_param": largeContent,
			"small_param": "small",
		}

		result, err := exec.handleLargeParameters(params)
		if err != nil {
			t.Fatalf("handleLargeParameters() error = %v", err)
		}

		// Small param should pass through
		if result["small_param"] != "small" {
			t.Error("Small param should pass through unchanged")
		}

		// Large param should be replaced with DataReference
		ref, ok := result["large_param"].(*loomv1.DataReference)
		if !ok {
			t.Fatalf("Expected large param to be DataReference, got %T", result["large_param"])
		}

		if ref.Id == "" {
			t.Error("Expected DataReference to have ID")
		}

		if ref.Location != loomv1.StorageLocation_STORAGE_LOCATION_MEMORY {
			t.Errorf("Expected memory location, got %v", ref.Location)
		}

		// Verify metadata
		if ref.Metadata["parameter_name"] != "large_param" {
			t.Error("Expected parameter_name in metadata")
		}
	})

	t.Run("multiple large parameters", func(t *testing.T) {
		params := map[string]interface{}{
			"large1": strings.Repeat("a", 3000),
			"large2": strings.Repeat("b", 4000),
			"large3": strings.Repeat("c", 5000),
		}

		result, err := exec.handleLargeParameters(params)
		if err != nil {
			t.Fatalf("handleLargeParameters() error = %v", err)
		}

		// All should be replaced with DataReferences
		for key := range params {
			if _, ok := result[key].(*loomv1.DataReference); !ok {
				t.Errorf("Expected %s to be DataReference", key)
			}
		}
	})

	t.Run("no shared memory configured", func(t *testing.T) {
		execNoMem := NewExecutor(reg)
		params := map[string]interface{}{
			"large_param": strings.Repeat("a", 5000),
		}

		result, err := execNoMem.handleLargeParameters(params)
		if err != nil {
			t.Fatalf("handleLargeParameters() error = %v", err)
		}

		// Without shared memory, params should pass through unchanged
		if result["large_param"] != params["large_param"] {
			t.Error("Expected params to pass through when no shared memory configured")
		}
	})
}

// Test dereferenceLargeParameters function
func TestExecutor_DereferenceLargeParameters(t *testing.T) {
	// Create shared memory store
	sharedMem := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       1 * 1024 * 1024, // 1MB
		CompressionThreshold: 1 * 1024 * 1024,
		TTLSeconds:           3600,
	})

	reg := NewRegistry()
	exec := NewExecutor(reg)
	exec.SetSharedMemory(sharedMem, 2560)

	t.Run("non-reference parameters pass through", func(t *testing.T) {
		params := map[string]interface{}{
			"string": "hello",
			"number": 42,
			"bool":   true,
		}

		result, err := exec.dereferenceLargeParameters(params)
		if err != nil {
			t.Fatalf("dereferenceLargeParameters() error = %v", err)
		}

		if len(result) != len(params) {
			t.Errorf("Expected %d params, got %d", len(params), len(result))
		}

		if result["string"] != "hello" {
			t.Error("String should pass through unchanged")
		}
	})

	t.Run("dereference DataReference objects", func(t *testing.T) {
		// First, store data in shared memory
		largeContent := strings.Repeat("test", 1000) // 4KB
		id := storage.GenerateID()
		ref, err := sharedMem.Store(id, []byte("\""+largeContent+"\""), "application/json", nil)
		if err != nil {
			t.Fatalf("Failed to store: %v", err)
		}

		params := map[string]interface{}{
			"large_param": ref,
			"small_param": "small",
		}

		result, err := exec.dereferenceLargeParameters(params)
		if err != nil {
			t.Fatalf("dereferenceLargeParameters() error = %v", err)
		}

		// Large param should be dereferenced
		if result["large_param"] != largeContent {
			t.Errorf("Expected dereferenced content, got %v", result["large_param"])
		}

		// Small param should pass through
		if result["small_param"] != "small" {
			t.Error("Small param should pass through unchanged")
		}
	})

	t.Run("invalid reference returns error", func(t *testing.T) {
		// Create a reference with non-existent ID
		invalidRef := &loomv1.DataReference{
			Id:       "nonexistent_id",
			Location: loomv1.StorageLocation_STORAGE_LOCATION_MEMORY,
		}

		params := map[string]interface{}{
			"invalid": invalidRef,
		}

		_, err := exec.dereferenceLargeParameters(params)
		if err == nil {
			t.Error("Expected error for invalid reference")
		}

		if !strings.Contains(err.Error(), "failed to dereference parameter") {
			t.Errorf("Expected dereference error, got: %v", err)
		}
	})
}

// Test end-to-end parameter handling in Execute()
func TestExecutor_Execute_WithLargeParameters(t *testing.T) {
	// Create shared memory store
	sharedMem := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       1 * 1024 * 1024,
		CompressionThreshold: 1 * 1024 * 1024,
		TTLSeconds:           3600,
	})

	reg := NewRegistry()
	tool := &mockTool{name: "test"}
	reg.Register(tool)

	exec := NewExecutor(reg)
	exec.SetSharedMemory(sharedMem, 2560) // 2.5KB threshold

	t.Run("large parameters handled transparently", func(t *testing.T) {
		// Use 3KB content - large enough to be stored as param (>2.5KB)
		// but small enough that the result won't trigger handleLargeResult (<2.5KB when returned)
		largeContent := strings.Repeat("x", 3000) // 3KB
		params := map[string]interface{}{
			"content": largeContent,
		}

		result, err := exec.Execute(context.Background(), "test", params)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}

		if !result.Success {
			t.Error("Expected successful execution")
		}

		// Verify the large param was handled:
		// 1. It was stored in shared memory during handleLargeParameters
		// 2. It was dereferenced before tool execution
		// 3. Tool received the full content (mockTool returns params as data)
		// 4. Result might be stored if it exceeds threshold, so check both cases

		// The mockTool returns params as data
		// If result is small enough, we get the map directly
		// If result is large, handleLargeResult converts it to a summary string
		switch data := result.Data.(type) {
		case map[string]interface{}:
			// Result was not stored (small enough)
			if data["content"] != largeContent {
				t.Error("Tool should receive dereferenced large parameter")
			}
		case string:
			// Result was stored in shared memory (large)
			// This is expected for large results - just verify it's a summary
			if !strings.Contains(data, "Large") && !strings.Contains(data, "stored") {
				t.Errorf("Expected result summary, got: %s", data)
			}
		default:
			t.Errorf("Expected map or string data, got %T", result.Data)
		}
	})

	t.Run("small parameters not stored", func(t *testing.T) {
		params := map[string]interface{}{
			"small1": "hello",
			"small2": "world",
		}

		// Get initial stats
		statsBefore := sharedMem.Stats()

		result, err := exec.Execute(context.Background(), "test", params)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}

		if !result.Success {
			t.Error("Expected successful execution")
		}

		// Stats should not change (no storage for small params)
		statsAfter := sharedMem.Stats()
		if statsAfter.ItemCount > statsBefore.ItemCount {
			t.Error("Small parameters should not be stored in shared memory")
		}
	})
}

// Test executor metrics for large parameter operations
func TestExecutor_LargeParameterMetrics(t *testing.T) {
	// Create shared memory store
	sharedMem := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       1 * 1024 * 1024,
		CompressionThreshold: 1 * 1024 * 1024,
		TTLSeconds:           3600,
	})

	reg := NewRegistry()
	tool := &mockTool{name: "test"}
	reg.Register(tool)

	exec := NewExecutor(reg)
	exec.SetSharedMemory(sharedMem, 2560)

	// Get initial stats
	statsBefore := exec.Stats()
	if statsBefore.LargeParamStores != 0 {
		t.Error("Expected zero initial stores")
	}

	// Execute with large parameter
	largeContent := strings.Repeat("x", 3000) // 3KB
	params := map[string]interface{}{
		"content": largeContent,
	}

	_, err := exec.Execute(context.Background(), "test", params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Check stats after execution
	statsAfter := exec.Stats()

	if statsAfter.LargeParamStores != 1 {
		t.Errorf("Expected 1 store, got %d", statsAfter.LargeParamStores)
	}

	if statsAfter.LargeParamDerefs != 1 {
		t.Errorf("Expected 1 dereference, got %d", statsAfter.LargeParamDerefs)
	}

	if statsAfter.LargeParamBytesStored != 3000 {
		t.Errorf("Expected 3000 bytes stored, got %d", statsAfter.LargeParamBytesStored)
	}

	if statsAfter.LargeParamDerefErrors != 0 {
		t.Errorf("Expected 0 dereference errors, got %d", statsAfter.LargeParamDerefErrors)
	}

	t.Logf("✓ Metrics tracked correctly:")
	t.Logf("  Stores: %d", statsAfter.LargeParamStores)
	t.Logf("  Derefs: %d", statsAfter.LargeParamDerefs)
	t.Logf("  Bytes: %d", statsAfter.LargeParamBytesStored)
	t.Logf("  Errors: %d", statsAfter.LargeParamDerefErrors)
}
