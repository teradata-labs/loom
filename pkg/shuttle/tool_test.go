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
	"reflect"
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

func TestJSONSchema_ToMap(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "nested keywords",
			input: `{"type":"object","description":"Task input","required":["tasks"],"properties":{
				"tasks":{"type":"array","description":"Tasks to create","items":{
					"type":"object","description":"A task","required":["idx","subject","details"],"properties":{
						"idx":{"type":"integer","description":"1-based task number","minimum":0,"maximum":100},
						"subject":{"type":"string","description":"Short task title","minLength":0,"maxLength":80,"pattern":"^[A-Z]","format":"text","enum":["Task"],"default":"Task"},
						"details":{"type":"object","required":["active"],"properties":{"active":{"type":"boolean","default":false}}},
						"empty":{"type":"object","properties":{}},
						"matrix":{"type":"array","items":{"type":"array","items":{"type":"integer"}}},
						"nullable":{"anyOf":[{"type":"integer"},{"type":"null"}]},
						"choice":{"oneOf":[{"type":"string"},{"type":"number"}]},
						"combined":{"allOf":[{"type":"string","minLength":1},{"not":{"type":"null"}}]},
						"excluded":{"not":{"type":"string"}},
						"explicit":{"type":"integer","anyOf":[{"type":"integer","minimum":1}]}
					}
				}}
			}}`,
		},
		{name: "empty object", input: `{"type":"object"}`, want: `{"type":"object","properties":{}}`},
		{name: "infer object", input: `{"properties":{"name":{"description":"Name"}}}`, want: `{"type":"object","properties":{"name":{"type":"string","description":"Name"}}}`},
		{name: "infer empty object", input: `{"properties":{}}`, want: `{"type":"object","properties":{}}`},
		{name: "infer array", input: `{"items":{"items":{"type":"integer"}}}`, want: `{"type":"array","items":{"type":"array","items":{"type":"integer"}}}`},
		{name: "infer string", input: `{}`, want: `{"type":"string"}`},
		{name: "constraint-only composite", input: `{"type":"integer","allOf":[{"minimum":1}]}`},
		{name: "unconstrained alternative", input: `{"anyOf":[{},{"type":"integer"}]}`},
		{name: "constraint-only negation", input: `{"type":"integer","not":{"minimum":1}}`},
		{name: "constraint-only property in composite", input: `{"type":"object","allOf":[{"properties":{"count":{"minimum":1}}}]}`, want: `{"type":"object","properties":{},"allOf":[{"properties":{"count":{"minimum":1}}}]}`},
		{name: "composite with properties", input: `{"anyOf":[{"type":"null"}],"properties":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema, err := FromJSON([]byte(test.input))
			if err != nil {
				t.Fatal(err)
			}
			if test.want == "" {
				test.want = test.input
			}
			var want interface{}
			if err := json.Unmarshal([]byte(test.want), &want); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(schema.ToMap())
			if err != nil {
				t.Fatal(err)
			}
			var got interface{}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("want %s, got %s", test.want, data)
			}
			parsed, err := FromJSON(data)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(schema.ToMap(), parsed.ToMap()) {
				t.Error("schema changed after round-trip")
			}
		})
	}
	var schema *JSONSchema
	if schema.ToMap() != nil {
		t.Error("nil schema must serialize to nil")
	}
}

func TestJSONSchema_MarshalJSONPreservesTypes(t *testing.T) {
	tests := []struct {
		name   string
		schema *JSONSchema
		want   string
	}{
		{name: "empty", schema: &JSONSchema{}, want: `{"type":""}`},
		{name: "description only", schema: &JSONSchema{Description: "Any value"}, want: `{"type":"","description":"Any value"}`},
		{name: "empty object", schema: &JSONSchema{Type: "object"}, want: `{"type":"object","properties":{}}`},
		{
			name: "untyped nested property",
			schema: &JSONSchema{
				Type: "object", Properties: map[string]*JSONSchema{"value": {}},
			},
			want: `{"type":"object","properties":{"value":{"type":""}}}`,
		},
		{
			name: "untyped array item",
			schema: &JSONSchema{
				Type: "array", Items: &JSONSchema{},
			},
			want: `{"type":"array","items":{"type":""}}`,
		},
		{
			name: "nullable composite",
			schema: &JSONSchema{
				AnyOf: []*JSONSchema{{Type: "integer"}, {Type: "null"}},
			},
			want: `{"type":"","anyOf":[{"type":"integer"},{"type":"null"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.schema)
			if err != nil {
				t.Fatal(err)
			}
			var got, want interface{}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(test.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("want %s, got %s", test.want, data)
			}
		})
	}
}

func TestJSONSchema_ToMapDoesNotAliasSlices(t *testing.T) {
	schema := &JSONSchema{Type: "object", Required: []string{"name"}, Enum: []interface{}{"value"}}
	result := schema.ToMap()
	result["required"].([]string)[0] = "changed"
	result["enum"].([]interface{})[0] = "changed"
	if schema.Required[0] != "name" || schema.Enum[0] != "value" {
		t.Error("changing the returned slices mutated the schema")
	}
}

func TestJSONSchema_ToToolMap(t *testing.T) {
	for _, schema := range []*JSONSchema{
		{},
		{Description: "No arguments"},
		{Properties: map[string]*JSONSchema{}},
		{AnyOf: []*JSONSchema{{Type: "object"}}},
		{OneOf: []*JSONSchema{{Type: "object"}}},
		{AllOf: []*JSONSchema{{Type: "object"}}},
		{Not: &JSONSchema{Type: "null"}},
	} {
		got := schema.ToToolMap()
		if got["type"] != "object" {
			t.Errorf("tool root must default to object, got %v", got)
		}
		properties, ok := got["properties"].(map[string]interface{})
		if !ok || properties == nil || len(properties) != 0 {
			t.Errorf("empty object must have empty properties, got %v", got)
		}
		if schema.Type != "" {
			t.Error("serialization mutated the root schema type")
		}
		if schema.Description != "" && got["description"] != schema.Description {
			t.Error("serialization dropped the root description")
		}
	}
	var schema *JSONSchema
	if schema.ToToolMap() != nil {
		t.Error("nil tool schema must serialize to nil")
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
