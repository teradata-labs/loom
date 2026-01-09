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
package metaagent

import "context"

// RequirementAnalyzer extracts structured information from natural language
type RequirementAnalyzer interface {
	Analyze(ctx context.Context, requirements string) (*Analysis, error)
}

// Analysis contains structured requirements
type Analysis struct {
	Domain        DomainType      `json:"domain"`
	Capabilities  []Capability    `json:"capabilities"`
	DataSources   []DataSource    `json:"data_sources"`
	Complexity    ComplexityLevel `json:"complexity"`
	SuggestedName string          `json:"suggested_name"` // Suggested agent name based on requirements
}

// DomainType represents the primary domain of the agent
type DomainType string

const (
	DomainSQL      DomainType = "sql"
	DomainREST     DomainType = "rest"
	DomainGraphQL  DomainType = "graphql"
	DomainFile     DomainType = "file"
	DomainDocument DomainType = "document"
	DomainMCP      DomainType = "mcp"
	DomainHybrid   DomainType = "hybrid"
)

// Capability represents a specific capability needed by the agent
type Capability struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Priority    int    `json:"priority"`
}

// DataSource represents a data source mentioned in requirements
type DataSource struct {
	Type           string `json:"type"`
	ConnectionHint string `json:"connection_hint"`
}

// ComplexityLevel represents the complexity of the agent requirements
type ComplexityLevel string

const (
	ComplexityLow    ComplexityLevel = "low"
	ComplexityMedium ComplexityLevel = "medium"
	ComplexityHigh   ComplexityLevel = "high"
)

// ConfigGenerator generates YAML configurations
type ConfigGenerator interface {
	GenerateAgentConfig(ctx context.Context, analysis *Analysis) (string, error)
	GeneratePatternConfigs(ctx context.Context, patterns []string) (map[string]string, error)
	GenerateWorkflowConfig(ctx context.Context, workflow *WorkflowSpec) (string, error)
}

// WorkflowSpec represents a generated workflow specification.
// It includes the workflow pattern, YAML representation, and metadata for tracking.
type WorkflowSpec struct {
	Type     string            // Workflow type: debate, swarm, pipeline, etc.
	Pattern  interface{}       // Generated workflow pattern proto (can be *loomv1.WorkflowPattern or specific pattern type)
	YAML     string            // Kubernetes-style YAML representation
	Metadata map[string]string // Additional context and metadata
	Stages   []StageSpec       // Pipeline stages (for pipeline workflows)
}

// StageSpec represents a stage in a workflow
type StageSpec struct {
	Name string
	Type string
}

// Validator validates generated configurations
type Validator interface {
	Validate(ctx context.Context, config string) (*ValidationResult, error)
}

// ValidationResult contains validation results
type ValidationResult struct {
	Valid    bool
	Errors   []ValidationError
	Warnings []ValidationWarning
}

// ValidationError represents a validation error
type ValidationError struct {
	Field      string
	Message    string
	Type       string // Type of error: "syntax_error", "required_field", "invalid_value", "anti_pattern", "format_error", "security_risk"
	Line       int    // Line number in the prompt where error occurred (0 if not applicable)
	Suggestion string // Suggestion for fixing the error
}

// ValidationWarning represents a validation warning
type ValidationWarning struct {
	Field      string
	Message    string
	Suggestion string // Suggestion for addressing the warning (optional)
}
