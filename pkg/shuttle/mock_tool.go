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
	"sync"
)

// MockTool is a mock implementation of the Tool interface for testing.
// It allows tests to control all tool behavior and verify interactions.
// Thread-safe for concurrent testing.
type MockTool struct {
	mu              sync.Mutex
	MockName        string
	MockDescription string
	MockSchema      *JSONSchema
	MockBackend     string
	MockExecute     func(ctx context.Context, params map[string]interface{}) (*Result, error)
	ExecuteCount    int
	LastParams      map[string]interface{}
}

// Name returns the mock tool name.
func (m *MockTool) Name() string {
	if m.MockName == "" {
		return "mock_tool"
	}
	return m.MockName
}

// Description returns the mock tool description.
func (m *MockTool) Description() string {
	if m.MockDescription == "" {
		return "Mock tool for testing"
	}
	return m.MockDescription
}

// InputSchema returns the mock input schema.
func (m *MockTool) InputSchema() *JSONSchema {
	if m.MockSchema == nil {
		return NewObjectSchema("Mock schema", map[string]*JSONSchema{
			"input": NewStringSchema("Test input"),
		}, []string{})
	}
	return m.MockSchema
}

// Execute runs the mock execution function.
func (m *MockTool) Execute(ctx context.Context, params map[string]interface{}) (*Result, error) {
	m.mu.Lock()
	m.ExecuteCount++
	m.LastParams = params
	m.mu.Unlock()

	if m.MockExecute != nil {
		return m.MockExecute(ctx, params)
	}

	// Default success result
	return &Result{
		Success: true,
		Data:    "mock result",
		Metadata: map[string]interface{}{
			"mock": true,
		},
	}, nil
}

// Backend returns the mock backend type.
func (m *MockTool) Backend() string {
	return m.MockBackend
}

// Ensure MockTool implements Tool interface
var _ Tool = (*MockTool)(nil)
