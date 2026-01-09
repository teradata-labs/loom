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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"gopkg.in/yaml.v3"
)

func TestNewROMBuilder(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	assert.NotNil(t, rb)
	assert.Equal(t, "0.2.0", rb.version)
}

func TestROMBuilder_BuildMetaAgentROM(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	romContent, err := rb.BuildMetaAgentROM()
	require.NoError(t, err)
	assert.NotEmpty(t, romContent)

	// Verify it's valid YAML
	var rom ROM
	err = yaml.Unmarshal([]byte(romContent), &rom)
	require.NoError(t, err, "ROM content should be valid YAML")

	// Verify structure
	assert.Equal(t, "Meta-Agent Factory", rom.Identity)
	assert.Equal(t, "0.2.0", rom.Version)
	assert.NotEmpty(t, rom.Purpose)

	// Verify guidelines exist
	assert.NotEmpty(t, rom.Guidelines)
	assert.Contains(t, rom.Guidelines, "requirement_analysis")
	assert.Contains(t, rom.Guidelines, "template_selection")
	assert.Contains(t, rom.Guidelines, "pattern_integration")
	assert.Contains(t, rom.Guidelines, "yaml_generation")
	assert.Contains(t, rom.Guidelines, "validation")

	// Verify anti-patterns
	assert.NotEmpty(t, rom.AntiPatterns)
	hasRolePrompting := false
	for _, ap := range rom.AntiPatterns {
		if ap.Name == "role_prompting" {
			hasRolePrompting = true
			assert.NotEmpty(t, ap.Description)
			assert.NotEmpty(t, ap.Detection)
		}
	}
	assert.True(t, hasRolePrompting, "Should include role_prompting anti-pattern")

	// Verify best practices
	assert.NotEmpty(t, rom.BestPractices)
	hasRaceTesting := false
	for _, bp := range rom.BestPractices {
		if contains(bp, "race detector") || contains(bp, "-race") {
			hasRaceTesting = true
		}
	}
	assert.True(t, hasRaceTesting, "Should include race detector best practice")

	// Verify metadata
	assert.NotEmpty(t, rom.Metadata)
	assert.Equal(t, "meta-agent-factory", rom.Metadata["created_by"])
	assert.Equal(t, "0.2.0", rom.Metadata["version"])

	// Verify knowledge base
	assert.NotEmpty(t, rom.KnowledgeBase)
	assert.Contains(t, rom.KnowledgeBase, "template_count")
	assert.Contains(t, rom.KnowledgeBase, "pattern_count")
	assert.Contains(t, rom.KnowledgeBase, "pattern_categories")
	assert.Contains(t, rom.KnowledgeBase, "supported_domains")
}

// TestROMBuilder_BuildAgentROM tests that BuildAgentROM returns backend ROM content
// BuildAgentROM now just loads backend ROM files (markdown) instead of generating structured YAML
// This was changed to avoid nested YAML parsing issues
func TestROMBuilder_BuildAgentROM(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	analysis := &Analysis{
		Domain: DomainSQL,
		Capabilities: []Capability{
			{Name: "data_quality", Description: "Data quality analysis", Priority: 1},
		},
		Complexity: ComplexityMedium,
	}

	// Test with empty backend path (should return empty string for non-Teradata)
	romContent, err := rb.BuildAgentROM(analysis, nil, "")
	require.NoError(t, err)
	// For non-Teradata backends, it returns empty string since only TD.rom is embedded
	assert.Empty(t, romContent)

	// Test with Teradata path (would load TD.rom if backend matched)
	_, err = rb.BuildAgentROM(analysis, nil, "teradata")
	require.NoError(t, err)
	// Will be non-empty if TD.rom is embedded
	// (Currently returns empty for non-teradata backends)
}

func TestROMBuilder_BuildAgentROM_NilAnalysis(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	romContent, err := rb.BuildAgentROM(nil, nil, "")
	assert.Error(t, err)
	assert.Empty(t, romContent)
	assert.Contains(t, err.Error(), "nil")
}

func TestROMBuilder_BuildAgentROM_NoCapabilities(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	analysis := &Analysis{
		Domain:       DomainSQL,
		Capabilities: []Capability{}, // Empty
		Complexity:   ComplexityLow,
	}

	selectedPatterns := []string{}

	// BuildAgentROM now just returns backend ROM (currently empty for non-Teradata)
	romContent, err := rb.BuildAgentROM(analysis, selectedPatterns, "")
	require.NoError(t, err)
	// Empty for non-Teradata backends
	assert.Empty(t, romContent)
}

func TestROMBuilder_InferIdentity(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	tests := []struct {
		name         string
		analysis     *Analysis
		wantContains string
	}{
		{
			name: "SQL with capabilities",
			analysis: &Analysis{
				Domain: DomainSQL,
				Capabilities: []Capability{
					{Name: "data_quality", Priority: 1},
				},
			},
			wantContains: "Sql", // strings.Title converts "sql" to "Sql"
		},
		{
			name: "REST with capabilities",
			analysis: &Analysis{
				Domain: DomainREST,
				Capabilities: []Capability{
					{Name: "api_monitoring", Priority: 1},
				},
			},
			wantContains: "Rest",
		},
		{
			name: "No capabilities",
			analysis: &Analysis{
				Domain:       DomainFile,
				Capabilities: []Capability{},
			},
			wantContains: "File Agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := rb.inferIdentity(tt.analysis)
			assert.Contains(t, identity, tt.wantContains)
		})
	}
}

func TestROMBuilder_InferPurpose(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	tests := []struct {
		name         string
		analysis     *Analysis
		wantContains string
	}{
		{
			name: "With capabilities",
			analysis: &Analysis{
				Domain: DomainSQL,
				Capabilities: []Capability{
					{Name: "data_quality", Description: "Quality analysis"},
				},
			},
			wantContains: "data_quality",
		},
		{
			name: "No capabilities",
			analysis: &Analysis{
				Domain:       DomainSQL,
				Capabilities: []Capability{},
			},
			wantContains: "sql domain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			purpose := rb.inferPurpose(tt.analysis)
			assert.Contains(t, purpose, tt.wantContains)
		})
	}
}

func TestROMBuilder_BuildDomainGuidelines(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	tests := []struct {
		domain     DomainType
		expectKeys []string
	}{
		{
			domain:     DomainSQL,
			expectKeys: []string{"general", "sql_operations", "performance"},
		},
		{
			domain:     DomainREST,
			expectKeys: []string{"general", "api_operations", "error_handling"},
		},
		{
			domain:     DomainFile,
			expectKeys: []string{"general", "file_operations"},
		},
		{
			domain:     DomainDocument,
			expectKeys: []string{"general", "document_operations"},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.domain), func(t *testing.T) {
			guidelines := rb.buildDomainGuidelines(tt.domain)
			for _, key := range tt.expectKeys {
				assert.Contains(t, guidelines, key, "Should have %s guidelines", key)
				assert.NotEmpty(t, guidelines[key])
			}
		})
	}
}

func TestROMBuilder_BuildDomainBestPractices(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	domains := []DomainType{DomainSQL, DomainREST, DomainFile, DomainDocument}

	for _, domain := range domains {
		t.Run(string(domain), func(t *testing.T) {
			practices := rb.buildDomainBestPractices(domain)
			assert.NotEmpty(t, practices, "Should have best practices for %s", domain)

			// Should include common practices
			hasCommon := false
			for _, p := range practices {
				if contains(p, "race detector") {
					hasCommon = true
					break
				}
			}
			assert.True(t, hasCommon, "Should include common best practices")
		})
	}
}

func TestROMBuilder_BuildDomainKnowledge(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	tests := []struct {
		analysis   *Analysis
		expectKeys []string
	}{
		{
			analysis: &Analysis{
				Domain:       DomainSQL,
				Complexity:   ComplexityHigh,
				Capabilities: []Capability{{Name: "test"}},
			},
			expectKeys: []string{"domain", "complexity", "sql_dialects", "common_patterns", "tools"},
		},
		{
			analysis: &Analysis{
				Domain:       DomainREST,
				Complexity:   ComplexityMedium,
				Capabilities: []Capability{{Name: "test"}},
			},
			expectKeys: []string{"domain", "complexity", "http_methods", "common_patterns", "tools"},
		},
		{
			analysis: &Analysis{
				Domain:       DomainFile,
				Complexity:   ComplexityLow,
				Capabilities: []Capability{{Name: "test"}},
			},
			expectKeys: []string{"domain", "complexity", "file_types", "common_patterns", "tools"},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.analysis.Domain), func(t *testing.T) {
			knowledge := rb.buildDomainKnowledge(tt.analysis)
			for _, key := range tt.expectKeys {
				assert.Contains(t, knowledge, key, "Should have %s in knowledge base", key)
			}
		})
	}
}

func TestROMBuilder_MetaAgentROM_AntiPatterns(t *testing.T) {
	rb := NewROMBuilder(observability.NewNoOpTracer())

	romContent, err := rb.BuildMetaAgentROM()
	require.NoError(t, err)

	var rom ROM
	err = yaml.Unmarshal([]byte(romContent), &rom)
	require.NoError(t, err)

	// Verify all critical anti-patterns are included
	antiPatternNames := make(map[string]bool)
	for _, ap := range rom.AntiPatterns {
		antiPatternNames[ap.Name] = true
		assert.NotEmpty(t, ap.Description, "Anti-pattern %s should have description", ap.Name)
		assert.NotEmpty(t, ap.Detection, "Anti-pattern %s should have detection method", ap.Name)
	}

	assert.True(t, antiPatternNames["role_prompting"], "Should include role_prompting anti-pattern")
}

// TestROMBuilder_ComplexityInMetadata - DELETED
// This test expected BuildAgentROM to generate structured YAML with metadata.
// BuildAgentROM now just returns backend ROM markdown to avoid nested YAML parsing issues.
// See comment on rom_builder.go line 178.
