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
	"encoding/json"
	"testing"
)

// mockTool is a simple tool for testing
type mockTool struct {
	name        string
	description string
	backend     string
}

func (m *mockTool) Name() string             { return m.name }
func (m *mockTool) Description() string      { return m.description }
func (m *mockTool) Backend() string          { return m.backend }
func (m *mockTool) InputSchema() *JSONSchema { return NewObjectSchema("test", nil, nil) }
func (m *mockTool) Execute(ctx context.Context, params map[string]interface{}) (*Result, error) {
	return &Result{Success: true, Data: params}, nil
}

func TestNewObjectSchema(t *testing.T) {
	schema := NewObjectSchema("test object", map[string]*JSONSchema{
		"name": NewStringSchema("name field"),
		"age":  NewNumberSchema("age field"),
	}, []string{"name"})

	if schema.Type != "object" {
		t.Errorf("Expected type 'object', got %s", schema.Type)
	}

	if schema.Description != "test object" {
		t.Errorf("Expected description 'test object', got %s", schema.Description)
	}

	if len(schema.Properties) != 2 {
		t.Errorf("Expected 2 properties, got %d", len(schema.Properties))
	}

	if len(schema.Required) != 1 {
		t.Errorf("Expected 1 required field, got %d", len(schema.Required))
	}
}

func TestNewStringSchema(t *testing.T) {
	schema := NewStringSchema("test string")

	if schema.Type != "string" {
		t.Errorf("Expected type 'string', got %s", schema.Type)
	}

	if schema.Description != "test string" {
		t.Errorf("Expected description 'test string', got %s", schema.Description)
	}
}

func TestNewNumberSchema(t *testing.T) {
	schema := NewNumberSchema("test number")

	if schema.Type != "number" {
		t.Errorf("Expected type 'number', got %s", schema.Type)
	}
}

func TestNewBooleanSchema(t *testing.T) {
	schema := NewBooleanSchema("test boolean")

	if schema.Type != "boolean" {
		t.Errorf("Expected type 'boolean', got %s", schema.Type)
	}
}

func TestNewArraySchema(t *testing.T) {
	itemSchema := NewStringSchema("array item")
	schema := NewArraySchema("test array", itemSchema)

	if schema.Type != "array" {
		t.Errorf("Expected type 'array', got %s", schema.Type)
	}

	if schema.Items == nil {
		t.Error("Expected items schema to be set")
	}

	if schema.Items.Type != "string" {
		t.Errorf("Expected items type 'string', got %s", schema.Items.Type)
	}
}

func TestJSONSchema_WithEnum(t *testing.T) {
	schema := NewStringSchema("test").WithEnum("a", "b", "c")

	if len(schema.Enum) != 3 {
		t.Errorf("Expected 3 enum values, got %d", len(schema.Enum))
	}
}

func TestJSONSchema_WithDefault(t *testing.T) {
	schema := NewStringSchema("test").WithDefault("default value")

	if schema.Default != "default value" {
		t.Errorf("Expected default 'default value', got %v", schema.Default)
	}
}

func TestJSONSchema_WithFormat(t *testing.T) {
	schema := NewStringSchema("test").WithFormat("email")

	if schema.Format != "email" {
		t.Errorf("Expected format 'email', got %s", schema.Format)
	}
}

func TestJSONSchema_WithPattern(t *testing.T) {
	schema := NewStringSchema("test").WithPattern("^[a-z]+$")

	if schema.Pattern != "^[a-z]+$" {
		t.Errorf("Expected pattern '^[a-z]+$', got %s", schema.Pattern)
	}
}

func TestJSONSchema_WithRange(t *testing.T) {
	min := 0.0
	max := 100.0
	schema := NewNumberSchema("test").WithRange(&min, &max)

	if schema.Minimum == nil || *schema.Minimum != 0.0 {
		t.Error("Expected minimum to be 0.0")
	}

	if schema.Maximum == nil || *schema.Maximum != 100.0 {
		t.Error("Expected maximum to be 100.0")
	}
}

func TestJSONSchema_WithLength(t *testing.T) {
	minLen := 1
	maxLen := 10
	schema := NewStringSchema("test").WithLength(&minLen, &maxLen)

	if schema.MinLength == nil || *schema.MinLength != 1 {
		t.Error("Expected minLength to be 1")
	}

	if schema.MaxLength == nil || *schema.MaxLength != 10 {
		t.Error("Expected maxLength to be 10")
	}
}

func TestJSONSchema_ToJSON(t *testing.T) {
	schema := NewObjectSchema("test", map[string]*JSONSchema{
		"name": NewStringSchema("name").WithPattern("^[a-z]+$"),
	}, []string{"name"})

	data, err := schema.ToJSON()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Verify it's valid JSON
	var result map[string]interface{}
	err = json.Unmarshal(data, &result)
	if err != nil {
		t.Fatalf("Expected valid JSON, got error: %v", err)
	}

	if result["type"] != "object" {
		t.Error("Expected type 'object' in JSON")
	}
}

func TestJSONSchema_FromJSON(t *testing.T) {
	jsonData := []byte(`{
		"type": "object",
		"description": "test object",
		"properties": {
			"name": {
				"type": "string",
				"description": "name field"
			}
		},
		"required": ["name"]
	}`)

	schema, err := FromJSON(jsonData)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if schema.Type != "object" {
		t.Errorf("Expected type 'object', got %s", schema.Type)
	}

	if schema.Description != "test object" {
		t.Errorf("Expected description 'test object', got %s", schema.Description)
	}

	if len(schema.Properties) != 1 {
		t.Errorf("Expected 1 property, got %d", len(schema.Properties))
	}

	if len(schema.Required) != 1 {
		t.Errorf("Expected 1 required field, got %d", len(schema.Required))
	}
}

func TestJSONSchema_ComplexSchema(t *testing.T) {
	min := 0.0
	max := 120.0

	schema := NewObjectSchema("Person", map[string]*JSONSchema{
		"name":   NewStringSchema("Full name").WithPattern("^[A-Za-z ]+$"),
		"age":    NewNumberSchema("Age in years").WithRange(&min, &max),
		"email":  NewStringSchema("Email address").WithFormat("email"),
		"active": NewBooleanSchema("Whether the person is active"),
		"tags":   NewArraySchema("Tags", NewStringSchema("tag")),
		"role":   NewStringSchema("User role").WithEnum("admin", "user", "guest").WithDefault("user"),
	}, []string{"name", "email"})

	// Verify schema structure
	if len(schema.Properties) != 6 {
		t.Errorf("Expected 6 properties, got %d", len(schema.Properties))
	}

	if len(schema.Required) != 2 {
		t.Errorf("Expected 2 required fields, got %d", len(schema.Required))
	}

	// Verify it can be serialized
	data, err := schema.ToJSON()
	if err != nil {
		t.Fatalf("Expected no error serializing, got %v", err)
	}

	// Verify it can be deserialized
	parsed, err := FromJSON(data)
	if err != nil {
		t.Fatalf("Expected no error deserializing, got %v", err)
	}

	if parsed.Type != "object" {
		t.Error("Expected type 'object' after round-trip")
	}
}
