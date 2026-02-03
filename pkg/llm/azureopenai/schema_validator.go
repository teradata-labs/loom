// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package azureopenai

import (
	"github.com/teradata-labs/loom/pkg/llm/openai"
)

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
