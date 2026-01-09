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
package patterns

// Pattern represents a comprehensive execution pattern definition.
// Patterns encapsulate domain knowledge for complex operations (SQL, REST APIs, document processing, etc.).
// This is backend-agnostic - SQL patterns, REST API patterns, document patterns all use this structure.
type Pattern struct {
	// Metadata
	Name        string `yaml:"name" json:"name"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description" json:"description"`
	Category    string `yaml:"category" json:"category"`         // e.g., "analytics", "etl", "ml", "rest_api"
	Difficulty  string `yaml:"difficulty" json:"difficulty"`     // "beginner", "intermediate", "advanced"
	BackendType string `yaml:"backend_type" json:"backend_type"` // "sql", "rest", "document", etc.

	// Use cases and related patterns
	UseCases        []string `yaml:"use_cases" json:"use_cases"`
	RelatedPatterns []string `yaml:"related_patterns,omitempty" json:"related_patterns,omitempty"`

	// Pattern definition
	Parameters    []Parameter         `yaml:"parameters" json:"parameters"`
	Templates     map[string]Template `yaml:"templates" json:"templates"`
	Examples      []Example           `yaml:"examples" json:"examples"`
	CommonErrors  []CommonError       `yaml:"common_errors,omitempty" json:"common_errors,omitempty"`
	BestPractices string              `yaml:"best_practices,omitempty" json:"best_practices,omitempty"`

	// Backend-specific syntax documentation (optional)
	Syntax *Syntax `yaml:"syntax,omitempty" json:"syntax,omitempty"`

	// Backend-specific function name (e.g., Teradata nPath, Postgres jsonb_path_query)
	BackendFunction string `yaml:"backend_function,omitempty" json:"backend_function,omitempty"`
}

// Parameter defines a single parameter used in pattern templates.
type Parameter struct {
	Name         string `yaml:"name" json:"name"`
	Type         string `yaml:"type" json:"type"` // "string", "number", "array", "object"
	Required     bool   `yaml:"required" json:"required"`
	Description  string `yaml:"description" json:"description"`
	Example      string `yaml:"example" json:"example"`
	DefaultValue string `yaml:"default,omitempty" json:"default,omitempty"`
}

// Template represents a parameterized execution template with placeholders.
// For SQL: SQL string with {{param}} placeholders
// For REST: request body template
// For Documents: query template
type Template struct {
	Description        string   `yaml:"description,omitempty" json:"description,omitempty"`
	Content            string   `yaml:"content,omitempty" json:"content,omitempty"` // Template content (SQL, JSON, etc.)
	SQL                string   `yaml:"sql,omitempty" json:"sql,omitempty"`         // Alternative field for "content"
	RequiredParameters []string `yaml:"required_parameters,omitempty" json:"required_parameters,omitempty"`
	OutputFormat       string   `yaml:"output_format,omitempty" json:"output_format,omitempty"` // "table", "json", "text"
}

// UnmarshalYAML handles both simple string templates and rich template objects
func (t *Template) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Try simple string first (for Postgres patterns)
	var str string
	if err := unmarshal(&str); err == nil {
		t.Content = str
		return nil
	}

	// Try as struct
	type rawTemplate Template
	var raw rawTemplate
	if err := unmarshal(&raw); err != nil {
		return err
	}

	*t = Template(raw)
	// If SQL field is present, copy to Content
	if t.SQL != "" && t.Content == "" {
		t.Content = t.SQL
	}

	return nil
}

// GetSQL returns the SQL/content regardless of format
func (t *Template) GetSQL() string {
	if t.Content != "" {
		return t.Content
	}
	return t.SQL
}

// Example provides a complete worked example with parameters and expected results.
type Example struct {
	Name           string                 `yaml:"name" json:"name"`
	Description    string                 `yaml:"description" json:"description"`
	Parameters     map[string]interface{} `yaml:"parameters" json:"parameters"`
	ExpectedResult string                 `yaml:"expected_result" json:"expected_result"`
	Notes          string                 `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// CommonError documents frequently encountered errors and solutions.
type CommonError struct {
	Error    string `yaml:"error" json:"error"`
	Cause    string `yaml:"cause" json:"cause"`
	Solution string `yaml:"solution" json:"solution"`
}

// Syntax documents pattern-specific syntax rules (e.g., nPath pattern operators, JSONPath syntax).
type Syntax struct {
	Description string           `yaml:"description" json:"description"`
	Operators   []SyntaxOperator `yaml:"operators" json:"operators"`
}

// SyntaxOperator describes a single pattern syntax operator.
type SyntaxOperator struct {
	Symbol  string `yaml:"symbol" json:"symbol"`
	Meaning string `yaml:"meaning" json:"meaning"`
	Example string `yaml:"example" json:"example"`
}

// PatternSummary provides lightweight metadata for catalog listing.
type PatternSummary struct {
	Name            string   `json:"name"`
	Title           string   `json:"title"`
	Description     string   `json:"description"` // Truncated for listing
	Category        string   `json:"category"`
	Difficulty      string   `json:"difficulty"`
	BackendType     string   `json:"backend_type"`
	UseCases        []string `json:"use_cases"`
	BackendFunction string   `json:"backend_function,omitempty"`
}

// IntentCategory represents the classified intent of a user request.
// This is used by the orchestrator for routing and pattern selection.
type IntentCategory string

const (
	// Generic intent categories (backend-agnostic)
	IntentSchemaDiscovery   IntentCategory = "schema_discovery"
	IntentDataQuality       IntentCategory = "data_quality"
	IntentDataTransform     IntentCategory = "data_transform"
	IntentAnalytics         IntentCategory = "analytics"
	IntentRelationshipQuery IntentCategory = "relationship_query"
	IntentQueryGeneration   IntentCategory = "query_generation"
	IntentDocumentSearch    IntentCategory = "document_search"
	IntentAPICall           IntentCategory = "api_call"
	IntentUnknown           IntentCategory = "unknown"
)

// ExecutionPlan represents a planned sequence of operations.
// The orchestrator creates this plan based on classified intent.
type ExecutionPlan struct {
	Intent      IntentCategory `json:"intent"`
	Description string         `json:"description"`
	Steps       []PlannedStep  `json:"steps"`
	Reasoning   string         `json:"reasoning"`
	PatternName string         `json:"pattern_name,omitempty"` // Recommended pattern to use
}

// PlannedStep represents a single step in an execution plan.
type PlannedStep struct {
	ToolName    string            `json:"tool_name"`
	Params      map[string]string `json:"params"`
	Description string            `json:"description"`
	PatternHint string            `json:"pattern_hint,omitempty"` // Suggested pattern to apply
}

// IntentClassifierFunc is a pluggable function for intent classification.
// Backends can provide custom classifiers for domain-specific intent detection.
type IntentClassifierFunc func(userMessage string, context map[string]interface{}) (IntentCategory, float64)

// ExecutionPlannerFunc is a pluggable function for execution planning.
// Backends can provide custom planners for domain-specific execution strategies.
type ExecutionPlannerFunc func(intent IntentCategory, userMessage string, context map[string]interface{}) (*ExecutionPlan, error)
