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

package agent

import (
	"context"

	"github.com/teradata-labs/loom/pkg/patterns"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// LoadPatternTool retrieves a pattern's content on demand and returns it as an
// ordinary tool-result. The content lands in L1 as tool-result data, so it is
// subject to the same size-threshold and compaction rules as any other data —
// it is never a system-slot injection. The reference the model passes here is
// the one surfaced from a skill's PatternRefs by manage_skills(load).
type LoadPatternTool struct {
	orchestrator *patterns.Orchestrator
}

// NewLoadPatternTool creates a load_pattern tool backed by the pattern orchestrator.
func NewLoadPatternTool(orchestrator *patterns.Orchestrator) *LoadPatternTool {
	return &LoadPatternTool{orchestrator: orchestrator}
}

func (t *LoadPatternTool) Name() string    { return "load_pattern" }
func (t *LoadPatternTool) Backend() string { return "" }

func (t *LoadPatternTool) Description() string {
	return `Load a domain pattern's full content by reference.

The pattern content is returned as a tool result and appended to the conversation,
where it is subject to the standard size and compaction handling. Pass the reference
string surfaced by manage_skills(load) from a skill's pattern references. An unknown
reference returns an error and adds no pattern content to the conversation.`
}

func (t *LoadPatternTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"reference": {
				Type:        "string",
				Description: "Pattern reference to load (as surfaced by manage_skills(load) from a skill's pattern references)",
			},
		},
		Required: []string{"reference"},
	}
}

// Execute loads the referenced pattern and returns its LLM rendering as string
// tool-result data. An unknown reference yields an error result so no pattern
// content enters the conversation.
func (t *LoadPatternTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	reference, _ := params["reference"].(string)
	if reference == "" {
		return errorResult("INVALID_PARAMETER", "reference is required"), nil
	}

	if t.orchestrator == nil || t.orchestrator.GetLibrary() == nil {
		return errorResult("PATTERN_LIBRARY_UNAVAILABLE", "no pattern library is configured"), nil
	}

	pattern, err := t.orchestrator.GetLibrary().Load(reference)
	if err != nil {
		return errorResult("PATTERN_NOT_FOUND", "unknown pattern reference: "+reference), nil
	}

	return &shuttle.Result{
		Success: true,
		Data:    pattern.FormatForLLM(),
	}, nil
}

// Compile-time check.
var _ shuttle.Tool = (*LoadPatternTool)(nil)
