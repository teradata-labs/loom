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
	"encoding/json"
	"fmt"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Analyzer implements RequirementAnalyzer using an LLM
type Analyzer struct {
	llm    agent.LLMProvider
	tracer observability.Tracer
}

// NewAnalyzer creates a new Analyzer
func NewAnalyzer(llm agent.LLMProvider, tracer observability.Tracer) *Analyzer {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &Analyzer{
		llm:    llm,
		tracer: tracer,
	}
}

// Analyze extracts structured information from natural language requirements
func (a *Analyzer) Analyze(ctx context.Context, requirements string) (*Analysis, error) {
	// Start span for observability
	ctx, span := a.tracer.StartSpan(ctx, "metaagent.analyzer.analyze")
	defer a.tracer.EndSpan(span)

	// Add span attributes
	span.Attributes["requirements_length"] = fmt.Sprintf("%d", len(requirements))

	// Construct analysis prompt (task-oriented, NO role-prompting!)
	prompt := fmt.Sprintf(`Analyze requirements for building a LOOM FRAMEWORK AGENT.

LOOM FRAMEWORK CONTEXT:
- Loom is an LLM agent framework with shuttle tools and pattern libraries
- Loom agents are configured in YAML (NOT standalone code)
- Loom agents use builtin tools (http_request, file_write, grpc_call, etc.) and MCP tools
- Your job: Analyze what KIND of Loom agent to build, NOT write implementation code

Requirements: %s

Extract and provide as JSON:
1. domain: primary domain (sql, rest, graphql, file, document, mcp, hybrid)
2. capabilities: list of specific capabilities needed
3. data_sources: list of data sources mentioned
4. complexity: complexity level (low, medium, high)
5. suggested_name: suggested kebab-case name for the agent based on its purpose

Domain Classification Rules:
- "sql": SQL databases (Teradata, PostgreSQL, MySQL, SQLite)
- "rest": REST APIs, HTTP endpoints
- "graphql": GraphQL APIs
- "file": File system operations
- "document": Document processing
- "mcp": Model Context Protocol servers
- "hybrid": Multiple domain types

Output format:
{
  "domain": "...",
  "capabilities": [{"name": "...", "description": "...", "category": "...", "priority": 1}],
  "data_sources": [{"type": "...", "connection_hint": "..."}],
  "complexity": "...",
  "suggested_name": "..."
}`, requirements)

	// Create message
	messages := []agent.Message{
		{
			Role:    "user",
			Content: prompt,
		},
	}

	// Create minimal agent context with tracer
	agentCtx := newMinimalContextWithTracer(ctx, a.tracer)

	// Call LLM
	response, err := a.llm.Chat(agentCtx, messages, nil)
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: fmt.Sprintf("LLM generation failed: %v", err),
		}
		a.tracer.RecordMetric("metaagent.analyzer.llm_calls.failed", 1.0, map[string]string{
			"error": err.Error(),
		})
		return nil, fmt.Errorf("LLM generation failed: %w", err)
	}

	// Record LLM call metrics
	a.tracer.RecordMetric("metaagent.analyzer.llm_calls.total", 1.0, nil)
	span.Attributes["response_length"] = fmt.Sprintf("%d", len(response.Content))

	// Extract and parse JSON response
	// LLM may include explanatory text before/after JSON, so we need to extract it
	jsonContent := extractJSON(response.Content)
	if jsonContent == "" {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: "No valid JSON found in LLM response",
		}
		a.tracer.RecordMetric("metaagent.analyzer.json_extraction.failed", 1.0, nil)
		return nil, fmt.Errorf("no valid JSON found in LLM response")
	}

	var analysis Analysis
	if err := json.Unmarshal([]byte(jsonContent), &analysis); err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: fmt.Sprintf("Failed to parse analysis: %v", err),
		}
		a.tracer.RecordMetric("metaagent.analyzer.json_parsing.failed", 1.0, nil)
		return nil, fmt.Errorf("failed to parse analysis: %w", err)
	}

	// Record success metrics
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "Analysis completed successfully",
	}
	span.Attributes["domain"] = analysis.Domain
	span.Attributes["complexity"] = analysis.Complexity
	span.Attributes["capabilities_count"] = fmt.Sprintf("%d", len(analysis.Capabilities))
	span.Attributes["data_sources_count"] = fmt.Sprintf("%d", len(analysis.DataSources))

	a.tracer.RecordMetric("metaagent.analyzer.analyze.success", 1.0, map[string]string{
		"domain":     string(analysis.Domain),
		"complexity": string(analysis.Complexity),
	})

	return &analysis, nil
}

// extractJSON attempts to extract JSON from text that may contain additional content
func extractJSON(text string) string {
	// Find first '{' and last '}'
	start := -1
	end := -1

	for i, ch := range text {
		if ch == '{' && start == -1 {
			start = i
		}
		if ch == '}' {
			end = i
		}
	}

	if start == -1 || end == -1 || start >= end {
		return ""
	}

	return text[start : end+1]
}
