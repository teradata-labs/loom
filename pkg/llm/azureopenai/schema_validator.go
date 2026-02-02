// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package azureopenai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/teradata-labs/loom/pkg/llm/openai"
)

// ValidateToolSchemas validates all tool schemas for Azure OpenAI compatibility
// and returns detailed error messages for any validation issues.
func ValidateToolSchemas(tools []openai.Tool) []string {
	var errors []string

	for i, tool := range tools {
		toolErrors := validateToolSchema(tool, i)
		errors = append(errors, toolErrors...)
	}

	return errors
}

// validateToolSchema validates a single tool schema
func validateToolSchema(tool openai.Tool, index int) []string {
	var errors []string
	prefix := fmt.Sprintf("tools[%d] (%s)", index, tool.Function.Name)

	// Validate function name
	if tool.Function.Name == "" {
		errors = append(errors, fmt.Sprintf("%s: function name is empty", prefix))
	}

	// Validate parameters
	if tool.Function.Parameters == nil {
		errors = append(errors, fmt.Sprintf("%s: parameters is nil", prefix))
		return errors
	}

	params := tool.Function.Parameters

	// Check type field
	paramType, hasType := params["type"].(string)
	if !hasType {
		errors = append(errors, fmt.Sprintf("%s.parameters: missing 'type' field", prefix))
	} else if paramType != "object" {
		errors = append(errors, fmt.Sprintf("%s.parameters: type must be 'object', got '%s'", prefix, paramType))
	}

	// For object types, properties must be present (even if empty)
	if paramType == "object" {
		if _, hasProps := params["properties"]; !hasProps {
			errors = append(errors, fmt.Sprintf("%s.parameters: object type missing 'properties' field", prefix))
		} else {
			// Validate properties recursively
			if props, ok := params["properties"].(map[string]interface{}); ok {
				propErrors := validateProperties(props, fmt.Sprintf("%s.parameters.properties", prefix))
				errors = append(errors, propErrors...)
			}
		}
	}

	// Validate required array (if present)
	if required, hasRequired := params["required"]; hasRequired {
		if reqArr, ok := required.([]string); ok {
			if len(reqArr) == 0 {
				// Empty required arrays might be problematic - flag as warning
				errors = append(errors, fmt.Sprintf("%s.parameters: has empty 'required' array (consider removing)", prefix))
			}
		} else {
			errors = append(errors, fmt.Sprintf("%s.parameters: 'required' must be string array", prefix))
		}
	}

	return errors
}

// validateProperties recursively validates property schemas
func validateProperties(props map[string]interface{}, path string) []string {
	var errors []string

	for propName, propValue := range props {
		propPath := fmt.Sprintf("%s.%s", path, propName)
		propMap, ok := propValue.(map[string]interface{})
		if !ok {
			errors = append(errors, fmt.Sprintf("%s: property is not an object", propPath))
			continue
		}

		// Check type field
		propType, hasType := propMap["type"].(string)
		if !hasType {
			errors = append(errors, fmt.Sprintf("%s: missing 'type' field", propPath))
			continue
		}

		// Validate based on type
		switch propType {
		case "object":
			// Objects must have properties field
			if _, hasProps := propMap["properties"]; !hasProps {
				errors = append(errors, fmt.Sprintf("%s: object type missing 'properties' field", propPath))
			} else if nestedProps, ok := propMap["properties"].(map[string]interface{}); ok {
				// Recursively validate nested properties
				nestedErrors := validateProperties(nestedProps, propPath+".properties")
				errors = append(errors, nestedErrors...)
			}

		case "array":
			// Arrays must have items field
			if _, hasItems := propMap["items"]; !hasItems {
				errors = append(errors, fmt.Sprintf("%s: array type missing 'items' field", propPath))
			} else if items, ok := propMap["items"].(map[string]interface{}); ok {
				// Validate items schema
				itemType, hasItemType := items["type"].(string)
				if !hasItemType {
					errors = append(errors, fmt.Sprintf("%s.items: missing 'type' field", propPath))
				}
				// If items is an object, validate recursively
				if itemType == "object" {
					if itemProps, ok := items["properties"].(map[string]interface{}); ok {
						itemErrors := validateProperties(itemProps, propPath+".items.properties")
						errors = append(errors, itemErrors...)
					} else {
						errors = append(errors, fmt.Sprintf("%s.items: object type missing 'properties' field", propPath))
					}
				}
			}

		case "string", "number", "integer", "boolean":
			// These are valid primitive types, no additional validation needed

		default:
			errors = append(errors, fmt.Sprintf("%s: unknown type '%s'", propPath, propType))
		}

		// Check for empty enum arrays (these might cause issues)
		if enum, hasEnum := propMap["enum"]; hasEnum {
			if enumArr, ok := enum.([]interface{}); ok && len(enumArr) == 0 {
				errors = append(errors, fmt.Sprintf("%s: has empty 'enum' array (consider removing)", propPath))
			}
		}

		// Check for empty required arrays
		if required, hasRequired := propMap["required"]; hasRequired {
			if reqArr, ok := required.([]interface{}); ok && len(reqArr) == 0 {
				errors = append(errors, fmt.Sprintf("%s: has empty 'required' array (consider removing)", propPath))
			}
		}
	}

	return errors
}

// DumpToolSchemasJSON returns a pretty-printed JSON representation of all tool schemas
// for debugging purposes.
func DumpToolSchemasJSON(tools []openai.Tool) string {
	var sb strings.Builder

	sb.WriteString("Tool Schemas (JSON):\n")
	sb.WriteString("====================\n\n")

	for i, tool := range tools {
		sb.WriteString(fmt.Sprintf("Tool [%d]: %s\n", i, tool.Function.Name))
		sb.WriteString("---\n")

		// Pretty-print the parameters as JSON
		if tool.Function.Parameters != nil {
			jsonBytes, err := json.MarshalIndent(tool.Function.Parameters, "", "  ")
			if err != nil {
				sb.WriteString(fmt.Sprintf("ERROR marshaling parameters: %v\n", err))
			} else {
				sb.WriteString(string(jsonBytes))
				sb.WriteString("\n")
			}
		} else {
			sb.WriteString("(parameters is nil)\n")
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// SanitizeToolSchemas removes fields that might cause Azure OpenAI validation issues:
// - Empty required arrays
// - Empty enum arrays
// - Null/empty default values
// - additionalProperties (if not explicitly needed)
func SanitizeToolSchemas(tools []openai.Tool) []openai.Tool {
	sanitized := make([]openai.Tool, len(tools))

	for i, tool := range tools {
		sanitized[i] = openai.Tool{
			Type: tool.Type,
			Function: openai.FunctionDef{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  sanitizeParameters(tool.Function.Parameters),
			},
		}
	}

	return sanitized
}

// sanitizeParameters recursively sanitizes parameter schemas
func sanitizeParameters(params map[string]interface{}) map[string]interface{} {
	if params == nil {
		return nil
	}

	result := make(map[string]interface{})

	for key, value := range params {
		// Skip empty arrays
		if arr, ok := value.([]interface{}); ok && len(arr) == 0 {
			continue
		}
		if arr, ok := value.([]string); ok && len(arr) == 0 {
			continue
		}

		// Recursively sanitize nested objects
		if key == "properties" {
			if props, ok := value.(map[string]interface{}); ok {
				result[key] = sanitizeProperties(props)
				continue
			}
		}

		// Recursively sanitize items
		if key == "items" {
			if items, ok := value.(map[string]interface{}); ok {
				result[key] = sanitizeParameters(items)
				continue
			}
		}

		// Skip empty string defaults (but keep false, 0, etc)
		if key == "default" {
			if str, ok := value.(string); ok && str == "" {
				continue
			}
		}

		result[key] = value
	}

	return result
}

// sanitizeProperties recursively sanitizes property schemas
func sanitizeProperties(props map[string]interface{}) map[string]interface{} {
	if props == nil {
		return make(map[string]interface{})
	}

	result := make(map[string]interface{})

	for propName, propValue := range props {
		propMap, ok := propValue.(map[string]interface{})
		if !ok {
			result[propName] = propValue
			continue
		}

		sanitizedProp := make(map[string]interface{})

		for key, value := range propMap {
			// Skip empty arrays
			if arr, ok := value.([]interface{}); ok && len(arr) == 0 {
				continue
			}
			if arr, ok := value.([]string); ok && len(arr) == 0 {
				continue
			}

			// Recursively sanitize nested properties
			if key == "properties" {
				if nestedProps, ok := value.(map[string]interface{}); ok {
					sanitizedProp[key] = sanitizeProperties(nestedProps)
					continue
				}
			}

			// Recursively sanitize items
			if key == "items" {
				if items, ok := value.(map[string]interface{}); ok {
					sanitizedProp[key] = sanitizeParameters(items)
					continue
				}
			}

			// Skip empty string defaults
			if key == "default" {
				if str, ok := value.(string); ok && str == "" {
					continue
				}
			}

			sanitizedProp[key] = value
		}

		result[propName] = sanitizedProp
	}

	return result
}
