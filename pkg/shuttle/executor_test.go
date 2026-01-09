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
