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

// NormalizeSchema ensures a JSON Schema complies with JSON Schema draft 2020-12.
// This is critical for Bedrock Claude models which strictly validate schemas.
//
// Common issues fixed:
// - Object types with nil properties -> empty map {}
// - Missing type fields -> inferred from structure
// - Nested objects with nil properties -> recursively normalized
func NormalizeSchema(schema *JSONSchema) *JSONSchema {
	if schema == nil {
		return nil
	}

	// Ensure object types have non-nil properties
	if schema.Type == "object" {
		if schema.Properties == nil {
			schema.Properties = make(map[string]*JSONSchema)
		}

		// Recursively normalize nested schemas
		for key, prop := range schema.Properties {
			schema.Properties[key] = NormalizeSchema(prop)
		}
	}

	// Ensure array types have items schema
	if schema.Type == "array" && schema.Items != nil {
		schema.Items = NormalizeSchema(schema.Items)
	}

	// Infer type if missing but structure is clear
	if schema.Type == "" {
		if schema.Properties != nil {
			schema.Type = "object"
			// Ensure properties map is not nil
			if schema.Properties == nil {
				schema.Properties = make(map[string]*JSONSchema)
			}
			// Recursively normalize
			for key, prop := range schema.Properties {
				schema.Properties[key] = NormalizeSchema(prop)
			}
		} else if schema.Items != nil {
			schema.Type = "array"
			schema.Items = NormalizeSchema(schema.Items)
		} else if len(schema.Enum) > 0 {
			// If enum exists, infer type from first enum value
			schema.Type = "string"
		}
	}

	return schema
}
