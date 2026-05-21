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
package sidebar

import (
	"strings"
	"testing"
)

func TestCurrentModelBlockPrefersCurrentAgentModelInfoOverActiveProvider(t *testing.T) {
	s := &sidebarCmp{
		currentAgent:   "weaver",
		activeProvider: "Claude Sonnet 4",
		agents: []AgentInfo{
			{
				ID:        "weaver",
				Name:      "weaver",
				ModelInfo: "anthropic/claude-opus-4-5",
			},
		},
	}

	block := s.currentModelBlock()

	if !strings.Contains(block, "anthropic/claude-opus-4-5") {
		t.Fatalf("currentModelBlock() = %q, want current agent model info", block)
	}
	if strings.Contains(block, "Claude Sonnet 4") {
		t.Fatalf("currentModelBlock() = %q, should not prefer active provider over current agent model", block)
	}
}
