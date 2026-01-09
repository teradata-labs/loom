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

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"
)

// Backend-specific ROM files (embedded at compile time)
//
//go:embed roms/TD.rom
var teradataROM string

// ROMBuilder builds Read-Only Memory (ROM) content for agents
// ROM is static documentation that never changes during agent session
type ROMBuilder struct {
	version string
	tracer  observability.Tracer
}

// NewROMBuilder creates a new ROM builder
func NewROMBuilder(tracer observability.Tracer) *ROMBuilder {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &ROMBuilder{
		version: "0.2.0",
		tracer:  tracer,
	}
}

// ROM represents the structured ROM content
type ROM struct {
	Identity      string                 `yaml:"identity"`
	Version       string                 `yaml:"version"`
	Purpose       string                 `yaml:"purpose"`
	Domain        string                 `yaml:"domain,omitempty"`
	Capabilities  []string               `yaml:"capabilities,omitempty"`
	Patterns      []string               `yaml:"patterns,omitempty"`
	Guidelines    map[string][]string    `yaml:"guidelines,omitempty"`
	AntiPatterns  []AntiPattern          `yaml:"anti_patterns,omitempty"`
	BestPractices []string               `yaml:"best_practices,omitempty"`
	Metadata      map[string]string      `yaml:"metadata,omitempty"`
	KnowledgeBase map[string]interface{} `yaml:"knowledge_base,omitempty"`
}

// AntiPattern represents a pattern to avoid
type AntiPattern struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Detection   string `yaml:"detection"`
	Example     string `yaml:"example,omitempty"`
}

// BuildMetaAgentROM builds ROM for the meta-agent itself
// This ROM contains knowledge about how to generate other agents
func (rb *ROMBuilder) BuildMetaAgentROM() (string, error) {
	// Start span for observability
	ctx := context.Background()
	_, span := rb.tracer.StartSpan(ctx, "metaagent.rom_builder.build_meta_agent_rom")
	defer rb.tracer.EndSpan(span)

	rom := ROM{
		Identity: "Meta-Agent Factory",
		Version:  rb.version,
		Purpose:  "Generate complete Loom agents from natural language requirements",
		Guidelines: map[string][]string{
			"requirement_analysis": {
				"Extract domain, capabilities, data sources, and complexity from requirements",
				"Use task-oriented prompts (NO role-prompting like 'You are a...' or 'As a...')",
				"Handle mixed LLM responses (JSON + explanatory text)",
				"Classify complexity as low/medium/high based on requirements",
				"Identify all data sources and connection hints",
			},
			"template_selection": {
				"Score templates: Domain 40%, Capabilities 50%, Complexity 10%",
				"Apply domain-specific thresholds: SQL 0.70, REST 0.60, File 0.45",
				"Validate all template variables before substitution",
				"Ensure template matches domain and capabilities",
				"Use highest-scoring template that passes threshold",
			},
			"tool_selection": {
				"Select builtin tools based on capabilities and domain (context-efficient)",
				"http_request: Keywords [http, api, rest, web, endpoint] or domains [rest, hybrid]",
				"file_write: Keywords [file, write, save, storage]",
				"grpc_call: Keywords [grpc, rpc, microservice]",
				"Only include tools that agent will actually use",
				"Apple-style: Tools should 'just work' with sensible defaults",
			},
			"backend_selection": {
				"Map domain and data sources to backend configuration files",
				"Teradata SQL (TD-SQL is unique and requires syntactical care): vantage-mcp.yaml (batteries-included MCP backend using vantage-mcp)",
				"Other SQL domains: postgres.yaml, sqlite.yaml",
				"REST/API domains: public-api.yaml for HTTP backends",
				"File domains: file.yaml for filesystem operations",
				"Hybrid domains: Check capabilities - API keywords [api, http, rest, web] → public-api.yaml, else file.yaml",
				"MCP domains: mcp-python.yaml for Python subprocess MCP servers, vantage-mcp.yaml for Teradata (vantage-mcp server)",
				"Backend files located in ./examples/backends/ directory",
				"Teradata keyword detection: [teradata, td, vantage, td-sql]",
			},
			"pattern_integration": {
				"Map capabilities to specific patterns using PatternSelector",
				"Resolve pattern dependencies automatically",
				"Include relevant patterns from 59-pattern library",
				"Prioritize domain-specific patterns over generic ones",
				"Score patterns based on capability match and priority",
			},
			"yaml_generation": {
				"Generate valid YAML with all required fields",
				"Use kebab-case for agent names",
				"Include descriptive agent descriptions",
				"Set appropriate LLM parameters (temperature, max_tokens)",
				"Configure memory and behavior settings",
			},
			"validation": {
				"Validate YAML syntax before spawning",
				"Check for anti-patterns (especially role-prompting)",
				"Verify backend configuration completeness",
				"Ensure pattern references exist in library",
				"Run validation before spawning agents",
			},
		},
		AntiPatterns: []AntiPattern{
			{
				Name:        "role_prompting",
				Description: "Using 'You are a...', 'As a...', 'Act as...', 'Pretend to be...', or 'Imagine you are...' in prompts",
				Detection:   "Regex patterns: (?i)you are a, (?i)as a, (?i)act as, (?i)pretend to be, (?i)imagine you are",
				Example:     "BAD: 'You are a SQL expert. Analyze this query.' GOOD: 'Analyze this SQL query for performance issues.'",
			},
			{
				Name:        "hardcoded_credentials",
				Description: "Embedding credentials directly in configurations",
				Detection:   "Scan for password=, api_key=, secret= in YAML",
				Example:     "Use environment variables or secret management instead",
			},
			{
				Name:        "overly_generic_prompts",
				Description: "Prompts that are too vague or generic",
				Detection:   "Prompt length < 50 characters or missing output format specification",
				Example:     "BAD: 'Help me with SQL' GOOD: 'Analyze this SQL query and suggest index optimizations in JSON format'",
			},
		},
		BestPractices: []string{
			"Always test concurrent code with -race detector",
			"Validate generated YAML before spawning agents",
			"Use real LLM for analysis (not mocks in production)",
			"Include specific patterns based on capabilities",
			"Set appropriate token budgets and memory limits",
			"Configure observability (Hawk tracing) for production agents",
			"Use task-oriented prompts without role-playing",
			"Test generated agents before deployment",
			"Document agent purpose and capabilities clearly",
			"Version control agent configurations",
		},
		Metadata: map[string]string{
			"created_by":   "meta-agent-factory",
			"version":      rb.version,
			"generated_at": time.Now().Format(time.RFC3339),
			"loom_version": "v0.2.0",
		},
		KnowledgeBase: map[string]interface{}{
			"template_count":     4,
			"pattern_count":      59,
			"pattern_categories": 8,
			"supported_domains":  []string{"sql", "rest", "file", "document", "graphql", "mcp", "hybrid"},
			"supported_backends": []string{"postgres", "teradata", "mysql", "sqlite", "http", "file"},
			"llm_providers":      []string{"anthropic", "bedrock", "ollama"},
			"template_thresholds": map[string]float64{
				"sql":  0.70,
				"rest": 0.60,
				"file": 0.45,
			},
		},
	}

	yamlBytes, err := yaml.Marshal(rom)
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: fmt.Sprintf("Failed to marshal ROM: %v", err),
		}
		rb.tracer.RecordMetric("metaagent.rom_builder.build_meta_agent_rom.failed", 1.0, nil)
		return "", fmt.Errorf("failed to marshal meta-agent ROM: %w", err)
	}

	// Record success metrics
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "Meta-agent ROM built successfully",
	}
	span.Attributes["rom_size_bytes"] = fmt.Sprintf("%d", len(yamlBytes))
	rb.tracer.RecordMetric("metaagent.rom_builder.build_meta_agent_rom.success", 1.0, nil)

	return string(yamlBytes), nil
}

// BuildAgentROM loads backend-specific ROM content (markdown format)
// Returns the appropriate ROM file for the backend (e.g., TD.rom for Teradata)
// This avoids nested YAML parsing issues by using plain markdown
func (rb *ROMBuilder) BuildAgentROM(analysis *Analysis, selectedPatterns []string, backendPath string) (string, error) {
	// Start span for observability
	ctx := context.Background()
	_, span := rb.tracer.StartSpan(ctx, "metaagent.rom_builder.build_agent_rom")
	defer rb.tracer.EndSpan(span)

	if analysis == nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: "Analysis is nil",
		}
		rb.tracer.RecordMetric("metaagent.rom_builder.build_agent_rom.failed", 1.0, map[string]string{
			"error": "nil_analysis",
		})
		return "", fmt.Errorf("analysis cannot be nil")
	}

	// Add span attributes
	span.Attributes["domain"] = analysis.Domain
	span.Attributes["backend_path"] = backendPath
	span.Attributes["patterns_count"] = fmt.Sprintf("%d", len(selectedPatterns))

	// Load backend-specific ROM (markdown format)
	backendROM := rb.LoadBackendROM(backendPath)

	// Record success metrics
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "Agent ROM built successfully",
	}
	span.Attributes["rom_size_bytes"] = fmt.Sprintf("%d", len(backendROM))
	span.Attributes["has_rom"] = fmt.Sprintf("%t", backendROM != "")
	rb.tracer.RecordMetric("metaagent.rom_builder.build_agent_rom.success", 1.0, map[string]string{
		"domain":  string(analysis.Domain),
		"has_rom": fmt.Sprintf("%t", backendROM != ""),
	})

	// Return backend ROM directly - no structured YAML to avoid parsing issues
	return backendROM, nil
}

// inferIdentity creates agent identity from analysis
func (rb *ROMBuilder) inferIdentity(analysis *Analysis) string {
	caser := cases.Title(language.English)
	if len(analysis.Capabilities) == 0 {
		return fmt.Sprintf("%s Agent", caser.String(string(analysis.Domain)))
	}

	primaryCap := analysis.Capabilities[0]
	for _, cap := range analysis.Capabilities {
		if cap.Priority < primaryCap.Priority {
			primaryCap = cap
		}
	}

	return fmt.Sprintf("%s %s Agent", caser.String(string(analysis.Domain)), caser.String(primaryCap.Name))
}

// inferPurpose creates agent purpose from analysis
func (rb *ROMBuilder) inferPurpose(analysis *Analysis) string {
	if len(analysis.Capabilities) == 0 {
		return fmt.Sprintf("Agent for %s domain operations", analysis.Domain)
	}

	capList := make([]string, len(analysis.Capabilities))
	for i, cap := range analysis.Capabilities {
		capList[i] = cap.Name
	}

	return fmt.Sprintf("Specialized agent for %s domain with capabilities: %s",
		analysis.Domain, strings.Join(capList, ", "))
}

// buildDomainGuidelines creates domain-specific guidelines
func (rb *ROMBuilder) buildDomainGuidelines(domain DomainType) map[string][]string {
	guidelines := map[string][]string{
		"general": {
			"Use task-oriented prompts without role-playing",
			"Always validate inputs before processing",
			"Handle errors gracefully with clear messages",
			"Log all operations for observability",
		},
	}

	switch domain {
	case DomainSQL:
		guidelines["sql_operations"] = []string{
			"Always use parameterized queries to prevent SQL injection",
			"Check table existence before operations",
			"Validate schema compatibility",
			"Use EXPLAIN for query optimization",
			"Handle NULL values explicitly",
			"Respect transaction boundaries",
		}
		guidelines["performance"] = []string{
			"Use indexes for WHERE clauses",
			"Avoid SELECT * in production queries",
			"Limit result sets for large tables",
			"Use sampling for data profiling on large tables",
			"Monitor query execution time",
		}

	case DomainREST:
		guidelines["api_operations"] = []string{
			"Validate allowed domains before requests",
			"Handle HTTP status codes appropriately",
			"Implement retry logic with exponential backoff",
			"Respect rate limits",
			"Validate JSON responses",
			"Use proper authentication headers",
		}
		guidelines["error_handling"] = []string{
			"Check for network timeouts",
			"Handle 4xx errors (client errors)",
			"Handle 5xx errors (server errors)",
			"Log failed requests for debugging",
		}

	case DomainFile:
		guidelines["file_operations"] = []string{
			"Validate file paths before access",
			"Check file permissions",
			"Handle missing files gracefully",
			"Use streaming for large files",
			"Validate file formats",
			"Implement proper cleanup",
		}

	case DomainDocument:
		guidelines["document_operations"] = []string{
			"Parse document structure correctly",
			"Handle encoding issues",
			"Extract metadata accurately",
			"Validate document format",
			"Handle malformed documents",
		}
	}

	return guidelines
}

// buildDomainBestPractices creates domain-specific best practices
func (rb *ROMBuilder) buildDomainBestPractices(domain DomainType) []string {
	common := []string{
		"Test with -race detector for concurrent operations",
		"Use observability tracing for production",
		"Implement circuit breakers for external dependencies",
		"Cache frequently accessed data",
		"Monitor resource usage (memory, tokens)",
	}

	var domainSpecific []string
	switch domain {
	case DomainSQL:
		domainSpecific = []string{
			"Profile queries before optimization",
			"Use prepared statements for repeated queries",
			"Implement connection pooling",
			"Monitor database connection health",
			"Use read replicas for read-heavy workloads",
		}
	case DomainREST:
		domainSpecific = []string{
			"Implement request/response logging",
			"Use connection keep-alive",
			"Compress large payloads",
			"Implement request deduplication",
			"Monitor API endpoint health",
		}
	case DomainFile:
		domainSpecific = []string{
			"Use buffered I/O for large files",
			"Implement file locking for concurrent access",
			"Clean up temporary files",
			"Monitor disk space usage",
			"Validate file integrity with checksums",
		}
	}

	return append(common, domainSpecific...)
}

// LoadBackendROM loads backend-specific ROM content based on backend path
func (rb *ROMBuilder) LoadBackendROM(backendPath string) string {
	// Check if backend is Teradata
	backendLower := strings.ToLower(backendPath)
	if strings.Contains(backendLower, "teradata") {
		return teradataROM
	}

	// Add other backend ROMs here as they're created
	// if strings.Contains(backendLower, "postgres") {
	//     return postgresROM
	// }

	return "" // No backend-specific ROM for this backend
}

// buildDomainKnowledge creates domain-specific knowledge base
func (rb *ROMBuilder) buildDomainKnowledge(analysis *Analysis) map[string]interface{} {
	knowledge := map[string]interface{}{
		"domain":       string(analysis.Domain),
		"complexity":   string(analysis.Complexity),
		"data_sources": len(analysis.DataSources),
		"capabilities": len(analysis.Capabilities),
	}

	switch analysis.Domain {
	case DomainSQL:
		knowledge["sql_dialects"] = []string{"teradata", "postgres", "mysql", "sqlite"}
		knowledge["primary_dialect"] = "teradata"
		knowledge["teradata_notes"] = "TD-SQL has unique syntax - requires pattern library"
		knowledge["common_patterns"] = []string{
			"data_quality", "performance_analysis", "index_optimization",
			"query_validation", "data_profiling",
		}
		knowledge["tools"] = []string{
			"execute_query", "get_schema", "get_tables", "explain_query",
		}

	case DomainREST:
		knowledge["http_methods"] = []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
		knowledge["common_patterns"] = []string{
			"health_check", "rate_limiting", "authentication", "error_handling",
		}
		knowledge["tools"] = []string{
			"http_get", "http_post", "http_put", "http_delete",
		}

	case DomainFile:
		knowledge["file_types"] = []string{"text", "json", "csv", "yaml", "binary"}
		knowledge["common_patterns"] = []string{
			"file_parsing", "content_extraction", "format_validation",
		}
		knowledge["tools"] = []string{
			"read_file", "write_file", "list_files", "delete_file",
		}
	}

	return knowledge
}
