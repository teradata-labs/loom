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

package memory

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEntityRecall_Format_NilReceiver(t *testing.T) {
	var er *EntityRecall
	assert.Equal(t, "", er.Format())
}

func TestEntityRecall_Format_NilEntity(t *testing.T) {
	er := &EntityRecall{Entity: nil}
	assert.Equal(t, "", er.Format())
}

func TestEntityRecall_Format_EntityOnly(t *testing.T) {
	er := &EntityRecall{
		Entity: &Entity{Name: "alice", EntityType: "person"},
	}
	formatted := er.Format()
	assert.Contains(t, formatted, "## Graph Memory: alice (person)")
	assert.NotContains(t, formatted, "Relationships")
	assert.NotContains(t, formatted, "Relevant Memories")
}

func TestEntityRecall_Format_WithProperties(t *testing.T) {
	er := &EntityRecall{
		Entity: &Entity{
			Name:           "alice",
			EntityType:     "person",
			PropertiesJSON: `{"role":"engineer"}`,
		},
	}
	formatted := er.Format()
	assert.Contains(t, formatted, `Properties: {"role":"engineer"}`)
}

func TestEntityRecall_Format_EmptyProperties(t *testing.T) {
	er := &EntityRecall{
		Entity: &Entity{
			Name:           "alice",
			EntityType:     "person",
			PropertiesJSON: "{}",
		},
	}
	formatted := er.Format()
	assert.NotContains(t, formatted, "Properties:")
}

func TestEntityRecall_Format_WithEdges(t *testing.T) {
	er := &EntityRecall{
		Entity: &Entity{Name: "alice", EntityType: "person"},
		EdgesOut: []*Edge{
			{Relation: "WORKS_ON", TargetID: "project-1"},
			{Relation: "USES", TargetID: "go-lang"},
		},
		EdgesIn: []*Edge{
			{Relation: "MANAGES", SourceID: "bob"},
		},
	}
	formatted := er.Format()
	assert.Contains(t, formatted, "### Relationships")
	assert.Contains(t, formatted, "alice -> [WORKS_ON] -> project-1")
	assert.Contains(t, formatted, "alice -> [USES] -> go-lang")
	assert.Contains(t, formatted, "bob -> [MANAGES] -> alice")
}

func TestEntityRecall_Format_WithMemories(t *testing.T) {
	er := &EntityRecall{
		Entity: &Entity{Name: "alice", EntityType: "person"},
		Memories: []ScoredMemory{
			{
				Memory:           &Memory{Content: "Alice prefers Go", MemoryType: "preference"},
				ComputedSalience: 0.85,
				UsedSummary:      false,
			},
			{
				Memory:           &Memory{Content: "Full content here", Summary: "Short summary", MemoryType: "fact"},
				ComputedSalience: 0.60,
				UsedSummary:      true,
			},
		},
	}
	formatted := er.Format()
	assert.Contains(t, formatted, "### Relevant Memories")
	assert.Contains(t, formatted, "[preference] (salience=0.85): Alice prefers Go")
	// When UsedSummary=true and summary is not empty, should use summary.
	assert.Contains(t, formatted, "[fact] (salience=0.60): Short summary")
	assert.NotContains(t, formatted, "Full content here")
}

func TestEntityRecall_Format_Full(t *testing.T) {
	er := &EntityRecall{
		Entity: &Entity{
			Name:           "loom",
			EntityType:     "project",
			PropertiesJSON: `{"language":"go"}`,
		},
		EdgesOut: []*Edge{
			{Relation: "USES", TargetID: "grpc"},
		},
		EdgesIn: []*Edge{
			{Relation: "WORKS_ON", SourceID: "alice"},
		},
		Memories: []ScoredMemory{
			{
				Memory:           &Memory{Content: "Loom is an agent framework", MemoryType: "fact"},
				ComputedSalience: 0.90,
			},
		},
		TotalTokensUsed: 500,
		TotalCandidates: 3,
	}
	formatted := er.Format()
	assert.Contains(t, formatted, "## Graph Memory: loom (project)")
	assert.Contains(t, formatted, `Properties: {"language":"go"}`)
	assert.Contains(t, formatted, "### Relationships")
	assert.Contains(t, formatted, "loom -> [USES] -> grpc")
	assert.Contains(t, formatted, "alice -> [WORKS_ON] -> loom")
	assert.Contains(t, formatted, "### Relevant Memories")
	assert.Contains(t, formatted, "[fact] (salience=0.90): Loom is an agent framework")
}

func TestEntityRecall_Format_EntityNamesResolution(t *testing.T) {
	er := &EntityRecall{
		Entity: &Entity{Name: "ilsun", EntityType: "person"},
		EdgesOut: []*Edge{
			{Relation: "WORKS_ON", TargetID: "uuid-project-1"},
			{Relation: "KNOWS_ABOUT", TargetID: "uuid-missing"},
		},
		EdgesIn: []*Edge{
			{Relation: "COLLEAGUE_OF", SourceID: "uuid-marcus"},
		},
		EntityNames: map[string]string{
			"uuid-project-1": "team_phoenix",
			"uuid-marcus":    "marcus",
		},
	}
	formatted := er.Format()
	// Resolved names should appear instead of UUIDs.
	assert.Contains(t, formatted, "ilsun -> [WORKS_ON] -> team_phoenix")
	assert.Contains(t, formatted, "marcus -> [COLLEAGUE_OF] -> ilsun")
	// UUID with no name mapping should fall back to the raw ID.
	assert.Contains(t, formatted, "ilsun -> [KNOWS_ABOUT] -> uuid-missing")
}

func TestEntityRecall_Format_UserMarker(t *testing.T) {
	er := &EntityRecall{
		Entity: &Entity{
			Name:           "ilsun",
			EntityType:     "person",
			PropertiesJSON: `{"is_user":true}`,
		},
	}
	formatted := er.Format()
	// User entities should be annotated in the header.
	assert.Contains(t, formatted, "## Graph Memory: ilsun (person, user)")
	// The is_user-only properties should not be printed as a separate line.
	assert.NotContains(t, formatted, "Properties:")
}

func TestEntityRecall_Format_UserMarkerWithOtherProps(t *testing.T) {
	er := &EntityRecall{
		Entity: &Entity{
			Name:           "ilsun",
			EntityType:     "person",
			PropertiesJSON: `{"is_user":true,"team":"phoenix"}`,
		},
	}
	formatted := er.Format()
	assert.Contains(t, formatted, "## Graph Memory: ilsun (person, user)")
	// Other properties should still be shown.
	assert.Contains(t, formatted, "Properties:")
}
