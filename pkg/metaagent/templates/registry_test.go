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
package templates

import (
	"strings"
	"sync"
	"testing"
)

func TestNewRegistry(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	if registry == nil {
		t.Fatal("registry should not be nil")
	}

	if len(registry.templates) == 0 {
		t.Fatal("registry should have loaded templates")
	}

	t.Logf("Loaded %d templates", len(registry.templates))
}

func TestRegistry_Get(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	tests := []struct {
		name      string
		tmplName  string
		shouldErr bool
	}{
		{
			name:      "get sql_postgres_analyst",
			tmplName:  "sql_postgres_analyst",
			shouldErr: false,
		},
		{
			name:      "get api_monitor",
			tmplName:  "api_monitor",
			shouldErr: false,
		},
		{
			name:      "get etl_processor",
			tmplName:  "etl_processor",
			shouldErr: false,
		},
		{
			name:      "get file_analyzer",
			tmplName:  "file_analyzer",
			shouldErr: false,
		},
		{
			name:      "get nonexistent",
			tmplName:  "nonexistent_template",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := registry.Get(tt.tmplName)
			if tt.shouldErr {
				if err == nil {
					t.Error("expected error for nonexistent template")
				}
				return
			}

			if err != nil {
				t.Fatalf("Get failed: %v", err)
			}

			if tmpl == nil {
				t.Fatal("template should not be nil")
			}

			if tmpl.Name != tt.tmplName {
				t.Errorf("template name should be %s, got: %s", tt.tmplName, tmpl.Name)
			}

			t.Logf("Got template: %s, domain: %s, capabilities: %d",
				tmpl.Name, tmpl.Domain, len(tmpl.Capabilities))
		})
	}
}

func TestRegistry_List(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	names := registry.List()
	if len(names) == 0 {
		t.Fatal("List should return template names")
	}

	// Check for expected templates
	expectedTemplates := []string{"sql_postgres_analyst", "api_monitor", "etl_processor", "file_analyzer"}
	for _, expected := range expectedTemplates {
		found := false
		for _, name := range names {
			if name == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected template %s not found in list", expected)
		}
	}

	t.Logf("Templates: %v", names)
}

func TestRegistry_ListByDomain(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	tests := []struct {
		name          string
		domain        string
		expectedCount int
		expectEmpty   bool
	}{
		{
			name:          "postgres domain",
			domain:        "postgres",
			expectedCount: 3, // analyst, expert, helper
			expectEmpty:   false,
		},
		{
			name:          "teradata domain",
			domain:        "teradata",
			expectedCount: 3, // analyst, expert, helper
			expectEmpty:   false,
		},
		{
			name:          "rest domain",
			domain:        "rest",
			expectedCount: 1,
			expectEmpty:   false,
		},
		{
			name:          "file domain",
			domain:        "file",
			expectedCount: 1,
			expectEmpty:   false,
		},
		{
			name:        "unknown domain",
			domain:      "unknown",
			expectEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			templates := registry.ListByDomain(tt.domain)

			if tt.expectEmpty && len(templates) > 0 {
				t.Errorf("expected empty result for domain %s, got %d templates", tt.domain, len(templates))
			}

			if !tt.expectEmpty && len(templates) == 0 {
				t.Errorf("expected templates for domain %s, got none", tt.domain)
			}

			if tt.expectedCount > 0 && len(templates) != tt.expectedCount {
				t.Logf("note: expected %d templates for domain %s, got %d", tt.expectedCount, tt.domain, len(templates))
			}

			for _, tmpl := range templates {
				t.Logf("Domain %s: %s", tt.domain, tmpl.Name)
			}
		})
	}
}

func TestRegistry_GetAll(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	templates := registry.GetAll()
	if len(templates) == 0 {
		t.Fatal("GetAll should return templates")
	}

	// Verify each template has required fields
	for _, tmpl := range templates {
		if tmpl.Name == "" {
			t.Error("template should have name")
		}
		if tmpl.Content == "" {
			t.Error("template should have content")
		}
		if tmpl.Domain == "" {
			t.Error("template should have domain")
		}

		t.Logf("Template: %s, domain: %s, capabilities: %d, patterns: %d, variables: %d",
			tmpl.Name, tmpl.Domain, len(tmpl.Capabilities), len(tmpl.Patterns), len(tmpl.Variables))
	}
}

func TestTemplate_Metadata(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	tmpl, err := registry.Get("sql_postgres_analyst")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Check metadata extraction
	if tmpl.Domain != "postgres" {
		t.Errorf("domain should be 'postgres', got: %s", tmpl.Domain)
	}

	if len(tmpl.Capabilities) == 0 {
		t.Error("template should have capabilities")
	}

	if len(tmpl.Patterns) == 0 {
		t.Error("template should have patterns")
	}

	t.Logf("SQL Analyst - capabilities: %v", tmpl.Capabilities)
	t.Logf("SQL Analyst - patterns: %v", tmpl.Patterns)
}

func TestTemplate_Variables(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	tmpl, err := registry.Get("sql_postgres_analyst")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Check that variables were extracted
	if len(tmpl.Variables) == 0 {
		t.Error("template should have variables")
	}

	// Check for expected variables
	expectedVars := []string{"agent_name", "description", "llm_provider", "llm_model"}
	for _, expected := range expectedVars {
		found := false
		for _, v := range tmpl.Variables {
			if v == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected variable %s not found", expected)
		}
	}

	t.Logf("Variables: %v", tmpl.Variables)
}

func TestTemplate_Content(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	tests := []struct {
		name     string
		tmplName string
		contains []string
	}{
		{
			name:     "sql_postgres_analyst content",
			tmplName: "sql_postgres_analyst",
			contains: []string{"SQL", "performance", "query", "{{agent_name}}"},
		},
		{
			name:     "api_monitor content",
			tmplName: "api_monitor",
			contains: []string{"REST", "API", "monitor", "{{agent_name}}"},
		},
		{
			name:     "etl_processor content",
			tmplName: "etl_processor",
			contains: []string{"ETL", "extract", "transform", "load"},
		},
		{
			name:     "file_analyzer content",
			tmplName: "file_analyzer",
			contains: []string{"file", "analyze", "{{agent_name}}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := registry.Get(tt.tmplName)
			if err != nil {
				t.Fatalf("Get failed: %v", err)
			}

			for _, needle := range tt.contains {
				if !strings.Contains(tmpl.Content, needle) {
					t.Errorf("template content should contain '%s'", needle)
				}
			}

			t.Logf("Content length: %d bytes", len(tmpl.Content))
		})
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	// Test concurrent reads
	var wg sync.WaitGroup
	templateNames := []string{"sql_postgres_analyst", "api_monitor", "etl_processor", "file_analyzer"}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			tmplName := templateNames[index%len(templateNames)]
			tmpl, err := registry.Get(tmplName)
			if err != nil {
				t.Errorf("concurrent Get failed: %v", err)
				return
			}
			if tmpl == nil {
				t.Error("concurrent Get returned nil template")
			}
		}(i)
	}

	wg.Wait()
	t.Log("Concurrent access test completed successfully")
}

func TestExtractVariables(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected []string
	}{
		{
			name:     "single variable",
			content:  "name: {{agent_name}}",
			expected: []string{"agent_name"},
		},
		{
			name:     "multiple variables",
			content:  "name: {{agent_name}}, provider: {{llm_provider}}",
			expected: []string{"agent_name", "llm_provider"},
		},
		{
			name:     "duplicate variables",
			content:  "{{var1}} {{var1}} {{var2}}",
			expected: []string{"var1", "var2"},
		},
		{
			name:     "no variables",
			content:  "no variables here",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := extractVariables(tt.content)

			if len(vars) != len(tt.expected) {
				t.Errorf("expected %d variables, got %d", len(tt.expected), len(vars))
			}

			// Check that all expected variables are present
			for _, expected := range tt.expected {
				found := false
				for _, v := range vars {
					if v == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected variable %s not found", expected)
				}
			}

			t.Logf("Extracted variables: %v", vars)
		})
	}
}
