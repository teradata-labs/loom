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

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPattern_UnmarshalYAML_TeradataFunctionAlias(t *testing.T) {
	tests := []struct {
		name                    string
		yaml                    string
		expectedBackendFunction string
	}{
		{
			name: "teradata_function populates BackendFunction",
			yaml: `
name: npath
title: "nPath Sequence Analysis"
category: analytics
teradata_function: NPATH
`,
			expectedBackendFunction: "NPATH",
		},
		{
			name: "backend_function works directly",
			yaml: `
name: npath
title: "nPath Sequence Analysis"
category: analytics
backend_function: NPATH
`,
			expectedBackendFunction: "NPATH",
		},
		{
			name: "backend_function takes precedence over teradata_function",
			yaml: `
name: npath
title: "nPath Sequence Analysis"
category: analytics
backend_function: CustomFunc
teradata_function: NPATH
`,
			expectedBackendFunction: "CustomFunc",
		},
		{
			name: "neither key leaves BackendFunction empty",
			yaml: `
name: npath
title: "nPath Sequence Analysis"
category: analytics
`,
			expectedBackendFunction: "",
		},
		{
			name: "teradata_function with complex pattern YAML",
			yaml: `
name: sessionize
title: "Sessionize"
description: "Session boundary detection"
category: analytics
difficulty: intermediate
teradata_function: Sessionize
use_cases:
  - Session analysis
  - User journey mapping
parameters:
  - name: database
    type: string
    required: true
    description: "Database name"
`,
			expectedBackendFunction: "Sessionize",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p Pattern
			err := yaml.Unmarshal([]byte(tt.yaml), &p)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedBackendFunction, p.BackendFunction)
		})
	}
}

func TestPattern_UnmarshalYAML_PreservesOtherFields(t *testing.T) {
	input := `
name: npath
title: "nPath Sequence Analysis"
description: "Analyze sequences"
category: analytics
difficulty: intermediate
teradata_function: NPATH
use_cases:
  - Funnel analysis
  - Clickstream
related_patterns:
  - sessionize
best_practices: "Always use EXPLAIN first"
`
	var p Pattern
	err := yaml.Unmarshal([]byte(input), &p)
	require.NoError(t, err)

	assert.Equal(t, "npath", p.Name)
	assert.Equal(t, "nPath Sequence Analysis", p.Title)
	assert.Equal(t, "Analyze sequences", p.Description)
	assert.Equal(t, "analytics", p.Category)
	assert.Equal(t, "intermediate", p.Difficulty)
	assert.Equal(t, "NPATH", p.BackendFunction)
	assert.Equal(t, []string{"Funnel analysis", "Clickstream"}, p.UseCases)
	assert.Equal(t, []string{"sessionize"}, p.RelatedPatterns)
	assert.Equal(t, "Always use EXPLAIN first", p.BestPractices)
}

func TestPattern_FormatForLLM(t *testing.T) {
	p := Pattern{
		Title:       "Test Pattern",
		Description: "A test pattern",
		UseCases:    []string{"Testing"},
		Parameters: []Parameter{
			{Name: "db", Type: "string", Required: true, Description: "Database", Example: "mydb"},
		},
		BestPractices: "Test carefully",
		CommonErrors: []CommonError{
			{Error: "test error", Solution: "fix it"},
		},
	}

	result := p.FormatForLLM()
	assert.Contains(t, result, "Test Pattern")
	assert.Contains(t, result, "A test pattern")
	assert.Contains(t, result, "Testing")
	assert.Contains(t, result, "`db`")
	assert.Contains(t, result, "[REQUIRED]")
	assert.Contains(t, result, "Test carefully")
	assert.Contains(t, result, "test error")
	assert.Contains(t, result, "fix it")
}
