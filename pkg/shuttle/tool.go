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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Tool defines the interface for executable tools (shuttles) in the agent framework.
// Tools are the primary mechanism for agents to interact with backends and perform
// domain-specific operations. Each tool encapsulates a single capability.
//
// Why "shuttle"? Tools "shuttle" data and execution between the LLM and the backend,
// like a shuttle in weaving carries thread back and forth across the loom.
type Tool interface {
	// Name returns the tool's unique identifier
	Name() string

	// Description returns a human-readable description for LLM context
	Description() string

	// InputSchema returns the JSON Schema for tool parameters
	InputSchema() *JSONSchema

	// Execute runs the tool with given parameters
	Execute(ctx context.Context, params map[string]interface{}) (*Result, error)

	// Backend returns the backend type this tool requires (e.g., "teradata", "postgres", "api")
	// Empty string means the tool is backend-agnostic
	Backend() string
}

// Result represents the outcome of tool execution.
type Result struct {
	// Success indicates if the tool executed successfully
	Success bool

	// Data contains the result data (format varies by tool)
	// For small results, data is stored here directly
	// For large results, use DataReference instead
	Data interface{}

	// Error contains error information if execution failed
	Error *Error

	// Metadata contains tool-specific metadata
	Metadata map[string]interface{}

	// ExecutionTime in milliseconds
	ExecutionTimeMs int64

	// CacheHit indicates if this result came from cache
	CacheHit bool

	// DataReference points to large result data in shared memory
	// When set, Data field should contain only a brief summary
	DataReference *loomv1.DataReference
}

// Error represents a tool execution error with structured information.
type Error struct {
	// Code is a machine-readable error code
	Code string

	// Message is a human-readable error message
	Message string

	// Details provides additional error context
	Details map[string]interface{}

	// Retryable indicates if the operation can be retried
	Retryable bool

	// Suggestion provides a suggestion for fixing the error
	Suggestion string
}

// JSONSchema represents a JSON Schema for tool parameters.
// This follows the JSON Schema spec for type definitions.
type JSONSchema struct {
	Type        string                 `json:"type"`
	Description string                 `json:"description,omitempty"`
	Properties  map[string]*JSONSchema `json:"properties,omitempty"`
	Required    []string               `json:"required,omitempty"`
	Items       *JSONSchema            `json:"items,omitempty"`
	Enum        []interface{}          `json:"enum,omitempty"`
	Default     interface{}            `json:"default,omitempty"`
	Format      string                 `json:"format,omitempty"`
	Pattern     string                 `json:"pattern,omitempty"`
	Minimum     *float64               `json:"minimum,omitempty"`
	Maximum     *float64               `json:"maximum,omitempty"`
	MinLength   *int                   `json:"minLength,omitempty"`
	MaxLength   *int                   `json:"maxLength,omitempty"`
}

// ToJSON converts the schema to JSON bytes.
func (s *JSONSchema) ToJSON() ([]byte, error) {
	return json.Marshal(s)
}

// FromJSON creates a JSONSchema from JSON bytes.
func FromJSON(data []byte) (*JSONSchema, error) {
	var schema JSONSchema
	err := json.Unmarshal(data, &schema)
	if err != nil {
		return nil, err
	}
	return &schema, nil
}

// NewObjectSchema creates a new object schema with the given properties.
func NewObjectSchema(description string, properties map[string]*JSONSchema, required []string) *JSONSchema {
	return &JSONSchema{
		Type:        "object",
		Description: description,
		Properties:  properties,
		Required:    required,
	}
}

// NewStringSchema creates a new string schema.
func NewStringSchema(description string) *JSONSchema {
	return &JSONSchema{
		Type:        "string",
		Description: description,
	}
}

// NewNumberSchema creates a new number schema.
func NewNumberSchema(description string) *JSONSchema {
	return &JSONSchema{
		Type:        "number",
		Description: description,
	}
}

// NewBooleanSchema creates a new boolean schema.
func NewBooleanSchema(description string) *JSONSchema {
	return &JSONSchema{
		Type:        "boolean",
		Description: description,
	}
}

// NewArraySchema creates a new array schema.
func NewArraySchema(description string, items *JSONSchema) *JSONSchema {
	return &JSONSchema{
		Type:        "array",
		Description: description,
		Items:       items,
	}
}

// WithEnum adds enum values to the schema.
func (s *JSONSchema) WithEnum(values ...interface{}) *JSONSchema {
	s.Enum = values
	return s
}

// WithDefault adds a default value to the schema.
func (s *JSONSchema) WithDefault(value interface{}) *JSONSchema {
	s.Default = value
	return s
}

// WithFormat adds a format constraint to the schema.
func (s *JSONSchema) WithFormat(format string) *JSONSchema {
	s.Format = format
	return s
}

// WithPattern adds a pattern constraint to the schema.
func (s *JSONSchema) WithPattern(pattern string) *JSONSchema {
	s.Pattern = pattern
	return s
}

// WithRange adds min/max constraints to the schema.
func (s *JSONSchema) WithRange(min, max *float64) *JSONSchema {
	s.Minimum = min
	s.Maximum = max
	return s
}

// WithLength adds length constraints to the schema.
func (s *JSONSchema) WithLength(minLen, maxLen *int) *JSONSchema {
	s.MinLength = minLen
	s.MaxLength = maxLen
	return s
}
