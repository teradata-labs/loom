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
package builtin

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// schemaCase defines the ceiling constraints for a tool's schema and description.
// Ceilings are set to (baseline_after_pruning + 50) to catch future bloat
// without being overly prescriptive.
type schemaCase struct {
	name           string
	tool           shuttle.Tool
	maxSchemaBytes int // ceiling: baseline + 50
	maxDescChars   int // ceiling: baseline + 50
	minDescWords   int // quality floor: ≥20 words
}

// TestToolSchemaSize verifies that tool schemas and descriptions stay within
// the established size budgets after the Phase 3 pruning pass.
// This test is a regression guard — it will fail if future edits re-inflate tokens.
func TestToolSchemaSize(t *testing.T) {
	cases := []schemaCase{
		{
			name:           "http_request",
			tool:           NewHTTPClientTool(),
			maxSchemaBytes: 623, // baseline=573
			maxDescChars:   208, // baseline=158
			minDescWords:   20,
		},
		{
			name:           "web_search",
			tool:           NewWebSearchTool(),
			maxSchemaBytes: 817, // baseline=767
			maxDescChars:   230, // baseline=180
			minDescWords:   20,
		},
		{
			name:           "file_write",
			tool:           NewFileWriteTool(""),
			maxSchemaBytes: 569, // baseline=519
			maxDescChars:   257, // baseline=207
			minDescWords:   20,
		},
		{
			name:           "file_read",
			tool:           NewFileReadTool(""),
			maxSchemaBytes: 589, // baseline=539
			maxDescChars:   240, // baseline=190
			minDescWords:   20,
		},
		{
			name:           "analyze_image",
			tool:           NewVisionTool(""),
			maxSchemaBytes: 372, // baseline=322
			maxDescChars:   245, // baseline=195
			minDescWords:   20,
		},
		{
			name:           "parse_document",
			tool:           NewDocumentParseTool(""),
			maxSchemaBytes: 1613, // baseline=1563
			maxDescChars:   278,  // baseline=228
			minDescWords:   25,
		},
		{
			name:           "grpc_call",
			tool:           NewGRPCClientTool(),
			maxSchemaBytes: 662, // baseline=612
			maxDescChars:   209, // baseline=159
			minDescWords:   20,
		},
		{
			name:           "shell_execute",
			tool:           NewShellExecuteTool(""),
			maxSchemaBytes: 825, // baseline=775
			maxDescChars:   313, // baseline=263
			minDescWords:   25,
		},
		{
			name:           "agent_management",
			tool:           NewAgentManagementTool(),
			maxSchemaBytes: 471, // baseline=421
			maxDescChars:   615, // baseline=565
			minDescWords:   50,
		},
		{
			name:           "contact_human",
			tool:           shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{}),
			maxSchemaBytes: 899, // baseline=849
			maxDescChars:   483, // baseline=433
			minDescWords:   50,
		},
	}

	totalSchemaBytes := 0
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Validate tool has correct name.
			assert.Equal(t, tc.name, tc.tool.Name(), "unexpected tool name")

			// Measure InputSchema serialized size.
			schemaBytes, err := json.Marshal(tc.tool.InputSchema())
			require.NoError(t, err, "InputSchema must be JSON-serializable")
			schemaSize := len(schemaBytes)
			assert.LessOrEqual(t, schemaSize, tc.maxSchemaBytes,
				"InputSchema for %q is %d bytes (limit %d) — schema grew, check for added properties",
				tc.name, schemaSize, tc.maxSchemaBytes)

			// Measure Description length.
			desc := tc.tool.Description()
			descChars := utf8.RuneCountInString(desc)
			assert.LessOrEqual(t, descChars, tc.maxDescChars,
				"Description for %q is %d chars (limit %d) — description grew, trim it",
				tc.name, descChars, tc.maxDescChars)

			// Quality floor: description must be informative enough.
			descWords := len(strings.Fields(desc))
			assert.GreaterOrEqual(t, descWords, tc.minDescWords,
				"Description for %q has only %d words (min %d) — too short to be useful",
				tc.name, descWords, tc.minDescWords)
		})
		totalSchemaBytes += len(func() []byte {
			b, _ := json.Marshal(tc.tool.InputSchema())
			return b
		}())
	}

	// Aggregate guard: total tool schemas must stay under 12K tokens worth of bytes.
	// This prevents gradual schema inflation from going unnoticed.
	assert.Less(t, totalSchemaBytes, 12000,
		"total InputSchema bytes across all core tools is %d — over the 12K budget", totalSchemaBytes)
}
