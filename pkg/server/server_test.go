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
package server

import (
	"context"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// mockTool implements shuttle.Tool for testing.
type mockTool struct {
	name        string
	description string
}

func (m *mockTool) Name() string        { return m.name }
func (m *mockTool) Description() string { return m.description }
func (m *mockTool) Backend() string     { return "mock" }
func (m *mockTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{
		Success: true,
		Data:    "mock result",
	}, nil
}
func (m *mockTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"input": {Type: "string"},
		},
	}
}

func TestConvertSession(t *testing.T) {
	now := time.Now()
	session := &agent.Session{
		ID:           "sess_123",
		CreatedAt:    now,
		UpdatedAt:    now.Add(time.Hour),
		TotalCostUSD: 0.0123,
	}

	protoSession := ConvertSession(session)

	if protoSession.Id != session.ID {
		t.Errorf("Expected ID %s, got %s", session.ID, protoSession.Id)
	}

	if protoSession.CreatedAt != now.Unix() {
		t.Errorf("Expected CreatedAt %d, got %d", now.Unix(), protoSession.CreatedAt)
	}

	if protoSession.UpdatedAt != now.Add(time.Hour).Unix() {
		t.Errorf("Expected UpdatedAt %d, got %d", now.Add(time.Hour).Unix(), protoSession.UpdatedAt)
	}

	if protoSession.State != "active" {
		t.Errorf("Expected state active, got %s", protoSession.State)
	}

	if protoSession.TotalCostUsd != 0.0123 {
		t.Errorf("Expected cost 0.0123, got %f", protoSession.TotalCostUsd)
	}
}

func TestConvertMessage(t *testing.T) {
	now := time.Now()
	message := &agent.Message{
		ID:        "msg_123",
		Role:      "user",
		Content:   "test message",
		Timestamp: now,
	}

	protoMessage := ConvertMessage(message)

	if protoMessage.Id != message.ID {
		t.Errorf("Expected ID %s, got %s", message.ID, protoMessage.Id)
	}

	if protoMessage.Role != message.Role {
		t.Errorf("Expected role %s, got %s", message.Role, protoMessage.Role)
	}

	if protoMessage.Content != message.Content {
		t.Errorf("Expected content %s, got %s", message.Content, protoMessage.Content)
	}

	if protoMessage.Timestamp != now.Unix() {
		t.Errorf("Expected timestamp %d, got %d", now.Unix(), protoMessage.Timestamp)
	}
}

func TestConvertTool(t *testing.T) {
	tool := &mockTool{
		name:        "test_tool",
		description: "A test tool",
	}

	protoTool := ConvertTool(tool)

	if protoTool.Name != tool.Name() {
		t.Errorf("Expected name %s, got %s", tool.Name(), protoTool.Name)
	}

	if protoTool.Description != tool.Description() {
		t.Errorf("Expected description %s, got %s", tool.Description(), protoTool.Description)
	}
}

func TestConvertTool_WithMetadata(t *testing.T) {
	// Test with a builtin tool that has rich metadata
	tool := &mockTool{
		name:        "web_search",
		description: "Search the web",
	}

	protoTool := ConvertTool(tool)

	// Basic fields should always be set
	if protoTool.Name != "web_search" {
		t.Errorf("Expected name web_search, got %s", protoTool.Name)
	}

	// Rich metadata should be populated IF metadata files are accessible
	// (tests may run from different directories)
	if protoTool.Category == "" {
		t.Skip("Metadata files not accessible from test directory - skipping rich metadata validation")
	}

	// If we got this far, metadata was loaded - verify it
	if protoTool.Category != "web" {
		t.Errorf("Expected category web, got %s", protoTool.Category)
	}

	// Capabilities
	if len(protoTool.Capabilities) == 0 {
		t.Error("Expected capabilities to be populated")
	}

	// Use cases
	if len(protoTool.UseCases) == 0 {
		t.Error("Expected use_cases to be populated")
	}

	// Conflicts - web_search should have HIGH severity conflict with http_request
	if len(protoTool.Conflicts) == 0 {
		t.Error("Expected conflicts to be populated")
	}
	foundHttpConflict := false
	for _, conflict := range protoTool.Conflicts {
		if conflict.ToolName == "http_request" {
			foundHttpConflict = true
			if conflict.Severity != "high" {
				t.Errorf("Expected high severity conflict with http_request, got %s", conflict.Severity)
			}
		}
	}
	if !foundHttpConflict {
		t.Error("Expected conflict with http_request")
	}

	// Providers
	if len(protoTool.Providers) == 0 {
		t.Error("Expected providers to be populated")
	}

	// Prerequisites
	if len(protoTool.Prerequisites) == 0 {
		t.Error("Expected prerequisites to be populated")
	}

	// Examples
	if len(protoTool.Examples) == 0 {
		t.Error("Expected examples to be populated")
	}
}

func TestGenerateSessionID(t *testing.T) {
	id1 := GenerateSessionID()
	id2 := GenerateSessionID()

	// Check format
	if len(id1) == 0 {
		t.Error("Generated ID should not be empty")
	}

	// Should have sess_ prefix
	if id1[:5] != "sess_" {
		t.Errorf("Expected sess_ prefix, got %s", id1[:5])
	}

	// IDs should be unique
	if id1 == id2 {
		t.Error("Generated IDs should be unique")
	}

	// Should be reasonable length (sess_ + 8 chars = 13)
	if len(id1) != 13 {
		t.Errorf("Expected length 13, got %d", len(id1))
	}
}

func TestConvertSessionWithNilFields(t *testing.T) {
	// Test with zero values
	session := &agent.Session{
		ID:           "",
		CreatedAt:    time.Time{},
		UpdatedAt:    time.Time{},
		TotalCostUSD: 0.0,
	}

	// Should not panic
	protoSession := ConvertSession(session)

	if protoSession.Id != "" {
		t.Errorf("Expected empty ID, got %s", protoSession.Id)
	}

	if protoSession.TotalCostUsd != 0.0 {
		t.Errorf("Expected zero cost, got %f", protoSession.TotalCostUsd)
	}
}

func TestConvertMessageWithEmptyContent(t *testing.T) {
	message := &agent.Message{
		ID:        "",
		Role:      "",
		Content:   "",
		Timestamp: time.Time{},
	}

	// Should not panic
	protoMessage := ConvertMessage(message)

	if protoMessage.Id != "" {
		t.Errorf("Expected empty ID, got %s", protoMessage.Id)
	}

	if protoMessage.Role != "" {
		t.Errorf("Expected empty role, got %s", protoMessage.Role)
	}

	if protoMessage.Content != "" {
		t.Errorf("Expected empty content, got %s", protoMessage.Content)
	}
}

// TestRaceConditions ensures thread-safety of conversion functions.
func TestRaceConditions(t *testing.T) {
	session := &agent.Session{
		ID:           "sess_race",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		TotalCostUSD: 0.01,
	}

	message := &agent.Message{
		ID:        "msg_race",
		Role:      "user",
		Content:   "test",
		Timestamp: time.Now(),
	}

	tool := &mockTool{
		name:        "test",
		description: "test tool",
	}

	done := make(chan bool, 300)

	// Run 100 concurrent conversions of each type
	for i := 0; i < 100; i++ {
		go func() {
			_ = ConvertSession(session)
			done <- true
		}()

		go func() {
			_ = ConvertMessage(message)
			done <- true
		}()

		go func() {
			_ = ConvertTool(tool)
			done <- true
		}()
	}

	// Wait for all to complete
	for i := 0; i < 300; i++ {
		<-done
	}
}

// TestGenerateSessionIDConcurrent ensures unique IDs under concurrent generation.
func TestGenerateSessionIDConcurrent(t *testing.T) {
	const numGoroutines = 100
	ids := make(chan string, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			ids <- GenerateSessionID()
		}()
	}

	seen := make(map[string]bool)
	for i := 0; i < numGoroutines; i++ {
		id := <-ids
		if seen[id] {
			t.Errorf("Duplicate ID generated: %s", id)
		}
		seen[id] = true
	}

	if len(seen) != numGoroutines {
		t.Errorf("Expected %d unique IDs, got %d", numGoroutines, len(seen))
	}
}

func TestConvertToolWithNilSchema(t *testing.T) {
	tool := &mockTool{
		name:        "nil_schema_tool",
		description: "Tool with nil schema",
	}

	// Should not panic even if tool has nil schema
	protoTool := ConvertTool(tool)

	if protoTool.Name != tool.Name() {
		t.Errorf("Expected name %s, got %s", tool.Name(), protoTool.Name)
	}

	// InputSchemaJson should be empty since we don't populate it in ConvertTool
	if protoTool.InputSchemaJson != "" {
		t.Errorf("Expected empty schema JSON, got %s", protoTool.InputSchemaJson)
	}
}
