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
