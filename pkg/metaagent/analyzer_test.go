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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// MockLLMProvider is a mock LLM provider for testing
type MockLLMProvider struct {
	response string
	err      error
}

func (m *MockLLMProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &llmtypes.LLMResponse{
		Content: m.response,
	}, nil
}

func (m *MockLLMProvider) Name() string {
	return "anthropic"
}

func (m *MockLLMProvider) Model() string {
	return "mock-model"
}

func TestAnalyzer_Analyze_SQLDomain(t *testing.T) {
	mockLLM := &MockLLMProvider{
		response: `{
			"domain": "sql",
			"capabilities": [
				{"name": "sql_performance_analysis", "description": "Analyze slow queries", "category": "performance", "priority": 1},
				{"name": "index_optimization", "description": "Suggest indexes", "category": "optimization", "priority": 2}
			],
			"data_sources": [
				{"type": "postgres", "connection_hint": "postgresql://localhost:5432/db"}
			],
			"complexity": "medium"
		}`,
	}

	analyzer := NewAnalyzer(mockLLM, observability.NewNoOpTracer())
	analysis, err := analyzer.Analyze(context.Background(), "Analyze PostgreSQL slow queries and suggest indexes")

	require.NoError(t, err)
	assert.Equal(t, DomainSQL, analysis.Domain)
	assert.Equal(t, ComplexityMedium, analysis.Complexity)
	assert.Len(t, analysis.Capabilities, 2)
	assert.Equal(t, "sql_performance_analysis", analysis.Capabilities[0].Name)
	assert.Equal(t, "index_optimization", analysis.Capabilities[1].Name)
	assert.Len(t, analysis.DataSources, 1)
	assert.Equal(t, "postgres", analysis.DataSources[0].Type)
}

func TestAnalyzer_Analyze_RESTDomain(t *testing.T) {
	mockLLM := &MockLLMProvider{
		response: `{
			"domain": "rest",
			"capabilities": [
				{"name": "http_requests", "description": "Make HTTP requests", "category": "execution", "priority": 1},
				{"name": "rate_limit_tracking", "description": "Track rate limits", "category": "monitoring", "priority": 2}
			],
			"data_sources": [
				{"type": "rest_api", "connection_hint": "https://api.github.com"}
			],
			"complexity": "low"
		}`,
	}

	analyzer := NewAnalyzer(mockLLM, observability.NewNoOpTracer())
	analysis, err := analyzer.Analyze(context.Background(), "Monitor GitHub API for rate limits")

	require.NoError(t, err)
	assert.Equal(t, DomainREST, analysis.Domain)
	assert.Equal(t, ComplexityLow, analysis.Complexity)
	assert.Len(t, analysis.Capabilities, 2)
}

func TestAnalyzer_Analyze_HybridDomain(t *testing.T) {
	mockLLM := &MockLLMProvider{
		response: `{
			"domain": "hybrid",
			"capabilities": [
				{"name": "sql_query_execution", "description": "Execute SQL", "category": "execution", "priority": 1},
				{"name": "http_requests", "description": "Make HTTP requests", "category": "execution", "priority": 1}
			],
			"data_sources": [
				{"type": "postgres", "connection_hint": "postgresql://localhost:5432/db"},
				{"type": "rest_api", "connection_hint": "https://api.example.com"}
			],
			"complexity": "high"
		}`,
	}

	analyzer := NewAnalyzer(mockLLM, observability.NewNoOpTracer())
	analysis, err := analyzer.Analyze(context.Background(), "Extract from PostgreSQL, load to REST API")

	require.NoError(t, err)
	assert.Equal(t, DomainHybrid, analysis.Domain)
	assert.Equal(t, ComplexityHigh, analysis.Complexity)
	assert.Len(t, analysis.Capabilities, 2)
	assert.Len(t, analysis.DataSources, 2)
}

func TestAnalyzer_Analyze_ComplexityLevels(t *testing.T) {
	tests := []struct {
		name               string
		response           string
		expectedComplexity ComplexityLevel
	}{
		{
			name: "low complexity",
			response: `{
				"domain": "file",
				"capabilities": [{"name": "file_operations", "description": "Read/write files", "category": "io", "priority": 1}],
				"data_sources": [{"type": "file", "connection_hint": "/data"}],
				"complexity": "low"
			}`,
			expectedComplexity: ComplexityLow,
		},
		{
			name: "medium complexity",
			response: `{
				"domain": "sql",
				"capabilities": [{"name": "sql_analysis", "description": "Analyze queries", "category": "analysis", "priority": 1}],
				"data_sources": [{"type": "postgres", "connection_hint": ""}],
				"complexity": "medium"
			}`,
			expectedComplexity: ComplexityMedium,
		},
		{
			name: "high complexity",
			response: `{
				"domain": "hybrid",
				"capabilities": [
					{"name": "etl", "description": "Complex ETL", "category": "pipeline", "priority": 1},
					{"name": "orchestration", "description": "Workflow orchestration", "category": "workflow", "priority": 1}
				],
				"data_sources": [{"type": "multiple", "connection_hint": ""}],
				"complexity": "high"
			}`,
			expectedComplexity: ComplexityHigh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockLLM := &MockLLMProvider{response: tt.response}
			analyzer := NewAnalyzer(mockLLM, observability.NewNoOpTracer())
			analysis, err := analyzer.Analyze(context.Background(), "test requirements")

			require.NoError(t, err)
			assert.Equal(t, tt.expectedComplexity, analysis.Complexity)
		})
	}
}
