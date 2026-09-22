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
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestRegistry_GraphMemoryEnabledFor pins the rule the server uses when wiring
// the graph-memory subsystem (cmd_serve: on unless explicitly disabled) so the
// task manager's completion-memory gate cannot drift from it.
func TestRegistry_GraphMemoryEnabledFor(t *testing.T) {
	r := &Registry{
		configs: map[string]*loomv1.AgentConfig{
			"explicit-off": {Name: "explicit-off", Memory: &loomv1.MemoryConfig{
				GraphMemory: &loomv1.GraphMemoryConfig{Enabled: false},
			}},
			"explicit-on": {Name: "explicit-on", Memory: &loomv1.MemoryConfig{
				GraphMemory: &loomv1.GraphMemoryConfig{Enabled: true},
			}},
			"no-graph-block":  {Name: "no-graph-block", Memory: &loomv1.MemoryConfig{}},
			"no-memory-block": {Name: "no-memory-block"},
		},
		agentInfo: map[string]*AgentInstanceInfo{
			"guid-off": {ID: "guid-off", Name: "explicit-off"},
			"guid-on":  {ID: "guid-on", Name: "explicit-on"},
		},
		agentsByName: map[string]string{"explicit-off": "guid-off", "explicit-on": "guid-on"},
	}

	tests := []struct {
		nameOrID string
		want     bool
	}{
		{"explicit-off", false},
		{"guid-off", false}, // task.OwnerAgentID is the GUID, not the YAML name
		{"explicit-on", true},
		{"guid-on", true},
		{"no-graph-block", true},
		{"no-memory-block", true},
		{"never-registered", true}, // default-on, matches subsystem wiring for unknown agents
	}
	for _, tt := range tests {
		t.Run(tt.nameOrID, func(t *testing.T) {
			assert.Equal(t, tt.want, r.GraphMemoryEnabledFor(tt.nameOrID))
		})
	}
}
