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

// Package memory provides salience-driven graph-backed episodic memory.
// Entities (mutable) represent current state. Memories (immutable/append-only)
// represent historical record. The memory_entities junction table bridges
// graph and episodic memory (many-to-many).
package memory

import (
	"fmt"
	"strings"
	"time"
)

// Entity is a mutable node representing current state in the knowledge graph.
type Entity struct {
	ID             string
	AgentID        string
	Name           string
	EntityType     string // person, tool, pattern, concept, project, device, etc.
	PropertiesJSON string
	Owner          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Edge is a mutable directed relationship between two entities.
type Edge struct {
	ID             string
	AgentID        string
	SourceID       string
	TargetID       string
	Relation       string // USES, FOLLOWS, KNOWS_ABOUT, PARENT_OF, WORKS_AT, etc.
	PropertiesJSON string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Memory is IMMUTABLE once created. Only AccessedAt, AccessCount,
// Salience, and ExpiresAt may be mutated (via specific engine operations).
type Memory struct {
	ID                string
	AgentID           string
	Content           string
	Summary           string
	MemoryType        string // fact, preference, decision, experience, failure, observation, consolidation
	Source            string // conversation, observation, manual, agent
	SourceID          string
	Owner             string
	MemoryAgentID     string
	Tags              []string
	Salience          float64
	TokenCount        int
	SummaryTokenCount int
	AccessCount       int
	EntityIDs         []string
	PropertiesJSON    string
	CreatedAt         time.Time
	AccessedAt        *time.Time // nil = never accessed
	ExpiresAt         *time.Time // nil = never expires
	IsSuperseded      bool
}

// MemoryLineage tracks SUPERSEDES and CONSOLIDATES chains.
type MemoryLineage struct {
	NewMemoryID  string
	OldMemoryID  string
	RelationType string // SUPERSEDES or CONSOLIDATES
	CreatedAt    time.Time
}

// ScoredMemory is a memory with computed ranking scores.
type ScoredMemory struct {
	Memory           *Memory
	ComputedSalience float64
	RelevanceScore   float64
	CombinedScore    float64
	UsedSummary      bool // true if summary used instead of full content
}

// EntityRecall is the composite result of a ContextFor query.
type EntityRecall struct {
	Entity          *Entity
	Memories        []ScoredMemory
	EdgesOut        []*Edge
	EdgesIn         []*Edge
	TotalTokensUsed int
	TotalCandidates int
}

// Format renders the EntityRecall as a string for injection into LLM context.
func (er *EntityRecall) Format() string {
	if er == nil || er.Entity == nil {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Graph Memory: %s (%s)\n", er.Entity.Name, er.Entity.EntityType)
	if er.Entity.PropertiesJSON != "" && er.Entity.PropertiesJSON != "{}" {
		fmt.Fprintf(&b, "Properties: %s\n", er.Entity.PropertiesJSON)
	}

	if len(er.EdgesOut) > 0 || len(er.EdgesIn) > 0 {
		b.WriteString("\n### Relationships\n")
		for _, e := range er.EdgesOut {
			fmt.Fprintf(&b, "- %s -> [%s] -> %s\n", er.Entity.Name, e.Relation, e.TargetID)
		}
		for _, e := range er.EdgesIn {
			fmt.Fprintf(&b, "- %s -> [%s] -> %s\n", e.SourceID, e.Relation, er.Entity.Name)
		}
	}

	if len(er.Memories) > 0 {
		b.WriteString("\n### Relevant Memories\n")
		for _, sm := range er.Memories {
			content := sm.Memory.Content
			if sm.UsedSummary && sm.Memory.Summary != "" {
				content = sm.Memory.Summary
			}
			fmt.Fprintf(&b, "- [%s] (salience=%.2f): %s\n",
				sm.Memory.MemoryType, sm.ComputedSalience, content)
		}
	}

	return b.String()
}

// GraphStats provides entity/memory/token counts.
type GraphStats struct {
	EntityCount       int
	EdgeCount         int
	MemoryCount       int
	ActiveMemoryCount int
	TotalMemoryTokens int
	MemoriesByType    map[string]int
}

// Valid memory types.
const (
	MemoryTypeFact          = "fact"
	MemoryTypePreference    = "preference"
	MemoryTypeDecision      = "decision"
	MemoryTypeExperience    = "experience"
	MemoryTypeFailure       = "failure"
	MemoryTypeObservation   = "observation"
	MemoryTypeConsolidation = "consolidation"
)

// Valid lineage relation types.
const (
	LineageSupersedes   = "SUPERSEDES"
	LineageConsolidates = "CONSOLIDATES"
)

// Valid memory entity roles.
const (
	RoleAbout    = "about"
	RoleBy       = "by"
	RoleFor      = "for"
	RoleMentions = "mentions"
)
