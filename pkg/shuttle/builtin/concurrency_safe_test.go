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
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// stubRegistry is a non-nil PromptRegistry; the wrapper never calls it here.
type stubRegistry struct{ prompts.PromptRegistry }

// TestBuiltinConcurrencySafe_Allowlist pins the exact set of built-ins that
// declare themselves concurrency-safe. Any new mark anywhere turns it red, so
// marking a tool is a deliberate, reviewed change. Everything else fails
// closed. parse_document also serves the enhanced CSV analysis path (same
// type), so it covers both.
func TestBuiltinConcurrencySafe_Allowlist(t *testing.T) {
	want := []string{"analyze_image", "file_read", "parse_document", "web_search"}

	// All() plus the constructors it does not include.
	tools := append(All(nil),
		NewEditFilesTool(""),
		NewExecuteQueryTool(nil),
		NewSendMessageTool(nil, "agent"),
		NewPublishTool(nil, "agent"),
		NewPublishRestartTool(nil, "agent"),
		NewWorkspaceTool(nil),
		NewSharedMemoryWriteTool(nil, "agent"),
		NewSharedMemoryReadTool(nil, "agent"),
		NewTopNQueryTool(nil, "agent"),
		NewGroupByQueryTool(nil, "agent"),
		NewWorkflowVisualizationTool(),
		NewManageEphemeralAgentsTool(nil, "session", "agent"),
	)

	safeNames := func(ts []shuttle.Tool) []string {
		var got []string
		for _, tool := range ts {
			if shuttle.IsConcurrencySafe(tool) {
				got = append(got, tool.Name())
			}
		}
		sort.Strings(got)
		return got
	}

	// Wrapped the way the agent registers them when prompts are configured.
	wrapped := make([]shuttle.Tool, len(tools))
	for i, tool := range tools {
		wrapped[i] = shuttle.NewPromptAwareTool(tool, stubRegistry{}, "tools."+tool.Name())
	}

	assert.Equal(t, want, safeNames(tools), "bare")
	assert.Equal(t, want, safeNames(wrapped), "wrapped in PromptAwareTool")
}
