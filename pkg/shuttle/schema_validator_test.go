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
	"encoding/json"
	"testing"
)

func TestNormalizeSchema_NilProperties(t *testing.T) {
	// Test case: object with nil properties (violates JSON Schema 2020-12)
	schema := &JSONSchema{
		Type:       "object",
		Properties: nil,
	}

	normalized := NormalizeSchema(schema)

	if normalized.Properties == nil {
		t.Error("Expected properties to be non-nil after normalization")
	}
	if len(normalized.Properties) != 0 {
		t.Errorf("Expected empty properties map, got %d properties", len(normalized.Properties))
	}
}

func TestNormalizeSchema_NestedObjects(t *testing.T) {
	// Test case: nested object with nil properties
	schema := &JSONSchema{
		Type: "object",
		Properties: map[string]*JSONSchema{
			"metadata": {
				Type:       "object",
				Properties: nil, // This should be normalized
			},
			"config": {
				Type: "object",
				Properties: map[string]*JSONSchema{
					"nested": {
						Type:       "object",
						Properties: nil, // This should also be normalized
					},
				},
			},
		},
	}

	normalized := NormalizeSchema(schema)

	// Check top-level
	if normalized.Properties["metadata"].Properties == nil {
		t.Error("Expected metadata.properties to be non-nil")
	}

	// Check nested
	if normalized.Properties["config"].Properties["nested"].Properties == nil {
		t.Error("Expected config.nested.properties to be non-nil")
	}
}

func TestNormalizeSchema_MissingType(t *testing.T) {
	// Test case: schema with properties but no type (should infer "object")
	schema := &JSONSchema{
		Properties: map[string]*JSONSchema{
			"name": {
				Type:        "string",
				Description: "Name field",
			},
		},
	}

	normalized := NormalizeSchema(schema)

	if normalized.Type != "object" {
		t.Errorf("Expected type to be inferred as 'object', got '%s'", normalized.Type)
	}
}

func TestNormalizeSchema_ArrayItems(t *testing.T) {
	// Test case: array with items that need normalization
	schema := &JSONSchema{
		Type: "array",
		Items: &JSONSchema{
			Type:       "object",
			Properties: nil,
		},
	}

	normalized := NormalizeSchema(schema)

	if normalized.Items.Properties == nil {
		t.Error("Expected array items properties to be non-nil")
	}
}

func TestNormalizeSchema_JSONMarshaling(t *testing.T) {
	// Test case: ensure normalized schema can be marshaled to valid JSON
	// This is what gets sent to Bedrock
	schema := &JSONSchema{
		Type: "object",
		Properties: map[string]*JSONSchema{
			"metadata": {
				Type:        "object",
				Description: "Optional metadata",
				Properties:  nil,
			},
		},
		Required: []string{"metadata"},
	}

	normalized := NormalizeSchema(schema)

	// Marshal to JSON
	jsonBytes, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("Failed to marshal normalized schema: %v", err)
	}

	// Unmarshal back and verify structure
	var unmarshaled map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	// Check that properties field exists and is an object (not null)
	props, ok := unmarshaled["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected properties to be an object")
	}

	metadata, ok := props["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected metadata to be an object")
	}

	metadataProps, exists := metadata["properties"]
	if !exists {
		t.Fatal("Expected metadata.properties to exist")
	}

	// Critical: properties must be {} not null for Bedrock compliance
	if metadataProps == nil {
		t.Error("metadata.properties is null - this violates JSON Schema 2020-12")
	}
}

func TestNormalizeSchema_AnyOfPreserved(t *testing.T) {
	// Test case: MCP servers use anyOf for nullable types: anyOf: [{type: "string"}, {type: "null"}]
	// This must survive round-trip through JSONSchema marshal/unmarshal
	schema := &JSONSchema{
		Type: "object",
		Properties: map[string]*JSONSchema{
			"query": {
				Type:        "string",
				Description: "SQL query to execute",
			},
			"database_name": {
				Description: "Optional database name",
				AnyOf: []*JSONSchema{
					{Type: "string"},
					{Type: "null"},
				},
			},
		},
		Required: []string{"query"},
	}

	normalized := NormalizeSchema(schema)

	// anyOf must survive normalization
	dbName := normalized.Properties["database_name"]
	if len(dbName.AnyOf) != 2 {
		t.Fatalf("Expected 2 anyOf entries, got %d", len(dbName.AnyOf))
	}
	if dbName.AnyOf[0].Type != "string" {
		t.Errorf("Expected anyOf[0].type = 'string', got '%s'", dbName.AnyOf[0].Type)
	}
	if dbName.AnyOf[1].Type != "null" {
		t.Errorf("Expected anyOf[1].type = 'null', got '%s'", dbName.AnyOf[1].Type)
	}

	// anyOf must survive JSON round-trip
	jsonBytes, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &raw); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	props := raw["properties"].(map[string]interface{})
	dbNameRaw := props["database_name"].(map[string]interface{})
	anyOf, ok := dbNameRaw["anyOf"].([]interface{})
	if !ok {
		t.Fatalf("anyOf missing from serialized JSON, got keys: %v", dbNameRaw)
	}
	if len(anyOf) != 2 {
		t.Errorf("Expected 2 anyOf entries in JSON, got %d", len(anyOf))
	}
}

func TestNormalizeSchema_OneOfAllOfNotPreserved(t *testing.T) {
	schema := &JSONSchema{
		Type: "object",
		Properties: map[string]*JSONSchema{
			"with_one_of": {
				OneOf: []*JSONSchema{
					{Type: "string"},
					{Type: "integer"},
				},
			},
			"with_all_of": {
				AllOf: []*JSONSchema{
					{Type: "object", Properties: map[string]*JSONSchema{
						"a": {Type: "string"},
					}},
				},
			},
			"with_not": {
				Not: &JSONSchema{Type: "null"},
			},
		},
	}

	normalized := NormalizeSchema(schema)

	if len(normalized.Properties["with_one_of"].OneOf) != 2 {
		t.Errorf("Expected 2 oneOf entries, got %d", len(normalized.Properties["with_one_of"].OneOf))
	}
	if len(normalized.Properties["with_all_of"].AllOf) != 1 {
		t.Errorf("Expected 1 allOf entry, got %d", len(normalized.Properties["with_all_of"].AllOf))
	}
	// allOf[0] is an object — its properties should be normalized
	allOfObj := normalized.Properties["with_all_of"].AllOf[0]
	if allOfObj.Properties == nil {
		t.Error("Expected allOf[0].properties to be non-nil after normalization")
	}
	if normalized.Properties["with_not"].Not == nil {
		t.Error("Expected not to be preserved")
	}
}

func TestNormalizeSchema_MCPNullableRoundTrip(t *testing.T) {
	// Simulate exact schema from teradata-mcp-server's base_readQuery tool
	// This is the real-world schema that was being corrupted
	inputJSON := `{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "SQL query to execute"
			},
			"databaseName": {
				"anyOf": [{"type": "string"}, {"type": "null"}],
				"description": "Optional database context",
				"default": null
			}
		},
		"required": ["query"]
	}`

	var schema JSONSchema
	if err := json.Unmarshal([]byte(inputJSON), &schema); err != nil {
		t.Fatalf("Failed to unmarshal MCP schema: %v", err)
	}

	normalized := NormalizeSchema(&schema)

	// Re-serialize
	outputJSON, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("Failed to marshal normalized schema: %v", err)
	}

	// Verify anyOf survived
	var result map[string]interface{}
	if err := json.Unmarshal(outputJSON, &result); err != nil {
		t.Fatalf("Failed to unmarshal output: %v", err)
	}

	props := result["properties"].(map[string]interface{})
	dbName := props["databaseName"].(map[string]interface{})

	anyOf, ok := dbName["anyOf"].([]interface{})
	if !ok {
		t.Fatalf("anyOf was dropped during normalization! Keys present: %v", dbName)
	}
	if len(anyOf) != 2 {
		t.Errorf("Expected 2 anyOf entries, got %d", len(anyOf))
	}

	// Verify the types
	first := anyOf[0].(map[string]interface{})
	second := anyOf[1].(map[string]interface{})
	if first["type"] != "string" || second["type"] != "null" {
		t.Errorf("anyOf types corrupted: got [%v, %v]", first["type"], second["type"])
	}
}

func TestNormalizeSchema_BedrockCompliance(t *testing.T) {
	// Test case: Simulate the exact issue that caused Bedrock validation error
	// This is what happened with the working memory feature
	schema := &JSONSchema{
		Type: "object",
		Properties: map[string]*JSONSchema{
			"key": {
				Type:        "string",
				Description: "Key name",
			},
			"metadata": {
				Type:        "object",
				Description: "Optional metadata",
				Properties:  nil, // This caused: "tools.X.custom.input_schema: JSON schema is invalid"
			},
		},
		Required: []string{"key"},
	}

	normalized := NormalizeSchema(schema)

	// Marshal and check JSON
	jsonBytes, _ := json.Marshal(normalized)
	var result map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	// Navigate to nested properties
	props := result["properties"].(map[string]interface{})
	metadata := props["metadata"].(map[string]interface{})
	metadataProps := metadata["properties"]

	// This must be an empty object {}, not null
	propsMap, ok := metadataProps.(map[string]interface{})
	if !ok {
		t.Errorf("Expected properties to be a map, got %T", metadataProps)
	}

	if propsMap == nil {
		t.Error("Properties map is nil - Bedrock will reject this")
	}

	if len(propsMap) != 0 {
		t.Errorf("Expected empty properties map, got %d entries", len(propsMap))
	}
}
