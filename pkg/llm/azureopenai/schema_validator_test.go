// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package azureopenai

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/llm/openai"
)

func TestSanitizeToolSchemas_RemovesEmptyArrays(t *testing.T) {
	tools := []openai.Tool{
		{
			Type: "function",
			Function: openai.FunctionDef{
				Name:        "test_tool",
				Description: "Test tool with empty arrays",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"param1": map[string]interface{}{
							"type":        "string",
							"description": "A parameter",
							"enum":        []interface{}{}, // Empty enum - should be removed
						},
						"param2": map[string]interface{}{
							"type":        "object",
							"description": "An object",
							"properties":  map[string]interface{}{},
							"required":    []string{}, // Empty required - should be removed
						},
					},
					"required": []string{}, // Empty required - should be removed
				},
			},
		},
	}

	sanitized := SanitizeToolSchemas(tools)

	require.Len(t, sanitized, 1)
	params := sanitized[0].Function.Parameters

	// Check that empty arrays were removed
	_, hasRequired := params["required"]
	assert.False(t, hasRequired, "empty required array should be removed")

	props := params["properties"].(map[string]interface{})

	// Check param1 - enum should be removed
	param1 := props["param1"].(map[string]interface{})
	_, hasEnum := param1["enum"]
	assert.False(t, hasEnum, "empty enum array should be removed")

	// Check param2 - required should be removed
	param2 := props["param2"].(map[string]interface{})
	_, hasRequired2 := param2["required"]
	assert.False(t, hasRequired2, "empty required array should be removed")
}

func TestSanitizeToolSchemas_RemovesEmptyDefaults(t *testing.T) {
	tools := []openai.Tool{
		{
			Type: "function",
			Function: openai.FunctionDef{
				Name:        "test_tool",
				Description: "Test tool with empty defaults",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"param1": map[string]interface{}{
							"type":        "string",
							"description": "A parameter",
							"default":     "", // Empty string default - should be removed
						},
						"param2": map[string]interface{}{
							"type":        "boolean",
							"description": "A boolean",
							"default":     false, // False default - should be kept
						},
						"param3": map[string]interface{}{
							"type":        "number",
							"description": "A number",
							"default":     0, // Zero default - should be kept
						},
					},
				},
			},
		},
	}

	sanitized := SanitizeToolSchemas(tools)

	require.Len(t, sanitized, 1)
	params := sanitized[0].Function.Parameters
	props := params["properties"].(map[string]interface{})

	// Check param1 - empty string default should be removed
	param1 := props["param1"].(map[string]interface{})
	_, hasDefault1 := param1["default"]
	assert.False(t, hasDefault1, "empty string default should be removed")

	// Check param2 - false default should be kept
	param2 := props["param2"].(map[string]interface{})
	default2, hasDefault2 := param2["default"]
	assert.True(t, hasDefault2, "false default should be kept")
	assert.False(t, default2.(bool), "default should be false")

	// Check param3 - zero default should be kept
	param3 := props["param3"].(map[string]interface{})
	default3, hasDefault3 := param3["default"]
	assert.True(t, hasDefault3, "zero default should be kept")
	assert.Equal(t, 0, default3.(int), "default should be 0")
}

func TestSanitizeToolSchemas_Composites(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf", "allOf", "not"} {
		for _, typed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/typed=%t", keyword, typed), func(t *testing.T) {
				branch := map[string]interface{}{
					"default": "",
					"properties": map[string]interface{}{
						"empty": map[string]interface{}{"default": ""},
						"zero":  map[string]interface{}{"default": 0},
						"false": map[string]interface{}{"default": false},
					},
					"items": map[string]interface{}{"default": ""},
				}
				var value interface{} = []interface{}{branch}
				if typed {
					value = []map[string]interface{}{branch}
				}
				if keyword == "not" {
					value = branch
				}
				params := map[string]interface{}{"properties": map[string]interface{}{
					"choice": map[string]interface{}{keyword: value},
				}}
				result := sanitizeParameters(params)
				choice := result["properties"].(map[string]interface{})["choice"].(map[string]interface{})
				var sanitized map[string]interface{}
				switch branches := choice[keyword].(type) {
				case map[string]interface{}:
					sanitized = branches
				case []interface{}:
					sanitized = branches[0].(map[string]interface{})
				case []map[string]interface{}:
					sanitized = branches[0]
				}
				require.NotNil(t, sanitized)
				assert.NotContains(t, sanitized, "default")
				properties := sanitized["properties"].(map[string]interface{})
				assert.NotContains(t, properties["empty"], "default")
				assert.Equal(t, 0, properties["zero"].(map[string]interface{})["default"])
				assert.Equal(t, false, properties["false"].(map[string]interface{})["default"])
				assert.NotContains(t, sanitized["items"], "default")
				assert.Equal(t, "", branch["default"])
			})
		}
	}
}

func TestSanitizeToolSchemas_PreservesValidSchema(t *testing.T) {
	tools := []openai.Tool{
		{
			Type: "function",
			Function: openai.FunctionDef{
				Name:        "test_tool",
				Description: "Test tool with valid schema",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"param1": map[string]interface{}{
							"type":        "string",
							"description": "A parameter",
							"enum":        []interface{}{"a", "b", "c"},
						},
						"param2": map[string]interface{}{
							"type":        "object",
							"description": "An object",
							"properties": map[string]interface{}{
								"nested": map[string]interface{}{
									"type": "string",
								},
							},
							"required": []string{"nested"},
						},
					},
					"required": []string{"param1"},
				},
			},
		},
	}

	sanitized := SanitizeToolSchemas(tools)

	require.Len(t, sanitized, 1)
	params := sanitized[0].Function.Parameters

	// Check that valid fields are preserved
	required, hasRequired := params["required"].([]string)
	assert.True(t, hasRequired, "required array should be preserved")
	assert.Equal(t, []string{"param1"}, required)

	props := params["properties"].(map[string]interface{})

	// Check param1 - enum should be preserved
	param1 := props["param1"].(map[string]interface{})
	enum, hasEnum := param1["enum"].([]interface{})
	assert.True(t, hasEnum, "enum should be preserved")
	assert.Len(t, enum, 3, "enum should have 3 values")

	// Check param2 - nested required should be preserved
	param2 := props["param2"].(map[string]interface{})
	required2, hasRequired2 := param2["required"].([]string)
	assert.True(t, hasRequired2, "nested required should be preserved")
	assert.Equal(t, []string{"nested"}, required2)
}
