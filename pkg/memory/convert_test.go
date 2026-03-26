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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEntityRoundTrip(t *testing.T) {
	original := &Entity{
		ID:             "ent-123",
		AgentID:        "agent-1",
		Name:           "test-entity",
		EntityType:     "person",
		PropertiesJSON: `{"key":"value"}`,
		Owner:          "owner-1",
		CreatedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}

	pb := EntityToProto(original)
	require.NotNil(t, pb)
	roundTripped := EntityFromProto(pb)
	require.NotNil(t, roundTripped)

	assert.Equal(t, original.ID, roundTripped.ID)
	assert.Equal(t, original.AgentID, roundTripped.AgentID)
	assert.Equal(t, original.Name, roundTripped.Name)
	assert.Equal(t, original.EntityType, roundTripped.EntityType)
	assert.Equal(t, original.PropertiesJSON, roundTripped.PropertiesJSON)
	assert.Equal(t, original.Owner, roundTripped.Owner)
	// Unix() conversion loses sub-second precision.
	assert.Equal(t, original.CreatedAt.Unix(), roundTripped.CreatedAt.Unix())
	assert.Equal(t, original.UpdatedAt.Unix(), roundTripped.UpdatedAt.Unix())
}

func TestEdgeRoundTrip(t *testing.T) {
	original := &Edge{
		ID:             "edge-456",
		AgentID:        "agent-1",
		SourceID:       "ent-1",
		TargetID:       "ent-2",
		Relation:       "USES",
		PropertiesJSON: `{"since":"2026-01-01"}`,
		CreatedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}

	pb := EdgeToProto(original)
	require.NotNil(t, pb)
	roundTripped := EdgeFromProto(pb)
	require.NotNil(t, roundTripped)

	assert.Equal(t, original.ID, roundTripped.ID)
	assert.Equal(t, original.SourceID, roundTripped.SourceID)
	assert.Equal(t, original.TargetID, roundTripped.TargetID)
	assert.Equal(t, original.Relation, roundTripped.Relation)
	assert.Equal(t, original.PropertiesJSON, roundTripped.PropertiesJSON)
}

func TestMemoryRoundTrip(t *testing.T) {
	accessedAt := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)

	original := &Memory{
		ID:                "mem-789",
		AgentID:           "agent-1",
		Content:           "This is a test memory",
		Summary:           "Test memory",
		MemoryType:        MemoryTypeFact,
		Source:            "conversation",
		SourceID:          "session-1",
		Owner:             "owner-1",
		MemoryAgentID:     "agent-1",
		Tags:              []string{"test", "important"},
		Salience:          0.75,
		TokenCount:        42,
		SummaryTokenCount: 10,
		AccessCount:       3,
		EntityIDs:         []string{"ent-1", "ent-2"},
		PropertiesJSON:    `{"reasoning":"test"}`,
		CreatedAt:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		AccessedAt:        &accessedAt,
		ExpiresAt:         &expiresAt,
		IsSuperseded:      true,
	}

	pb := MemoryToProto(original)
	require.NotNil(t, pb)
	roundTripped := MemoryFromProto(pb)
	require.NotNil(t, roundTripped)

	assert.Equal(t, original.ID, roundTripped.ID)
	assert.Equal(t, original.Content, roundTripped.Content)
	assert.Equal(t, original.Summary, roundTripped.Summary)
	assert.Equal(t, original.MemoryType, roundTripped.MemoryType)
	assert.Equal(t, original.Tags, roundTripped.Tags)
	assert.InDelta(t, original.Salience, roundTripped.Salience, 0.001)
	assert.Equal(t, original.TokenCount, roundTripped.TokenCount)
	assert.Equal(t, original.SummaryTokenCount, roundTripped.SummaryTokenCount)
	assert.Equal(t, original.AccessCount, roundTripped.AccessCount)
	assert.Equal(t, original.EntityIDs, roundTripped.EntityIDs)
	assert.Equal(t, original.IsSuperseded, roundTripped.IsSuperseded)

	require.NotNil(t, roundTripped.AccessedAt)
	assert.Equal(t, original.AccessedAt.Unix(), roundTripped.AccessedAt.Unix())
	require.NotNil(t, roundTripped.ExpiresAt)
	assert.Equal(t, original.ExpiresAt.Unix(), roundTripped.ExpiresAt.Unix())
}

func TestMemoryRoundTrip_NilOptionals(t *testing.T) {
	original := &Memory{
		ID:         "mem-001",
		AgentID:    "agent-1",
		Content:    "Simple memory",
		MemoryType: MemoryTypeFact,
		Salience:   0.5,
		CreatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		AccessedAt: nil,
		ExpiresAt:  nil,
	}

	pb := MemoryToProto(original)
	roundTripped := MemoryFromProto(pb)

	assert.Nil(t, roundTripped.AccessedAt)
	assert.Nil(t, roundTripped.ExpiresAt)
}

func TestLineageRoundTrip(t *testing.T) {
	original := &MemoryLineage{
		NewMemoryID:  "mem-new",
		OldMemoryID:  "mem-old",
		RelationType: LineageSupersedes,
		CreatedAt:    time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC),
	}

	pb := LineageToProto(original)
	require.NotNil(t, pb)
	roundTripped := LineageFromProto(pb)
	require.NotNil(t, roundTripped)

	assert.Equal(t, original.NewMemoryID, roundTripped.NewMemoryID)
	assert.Equal(t, original.OldMemoryID, roundTripped.OldMemoryID)
	assert.Equal(t, original.RelationType, roundTripped.RelationType)
	assert.Equal(t, original.CreatedAt.Unix(), roundTripped.CreatedAt.Unix())
}

func TestScoredMemoryRoundTrip(t *testing.T) {
	original := &ScoredMemory{
		Memory: &Memory{
			ID:         "mem-scored",
			AgentID:    "agent-1",
			Content:    "Scored memory",
			MemoryType: MemoryTypeDecision,
			Salience:   0.8,
			CreatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		ComputedSalience: 0.72,
		RelevanceScore:   0.95,
		CombinedScore:    0.684,
		UsedSummary:      true,
	}

	pb := ScoredMemoryToProto(original)
	require.NotNil(t, pb)
	roundTripped := ScoredMemoryFromProto(pb)
	require.NotNil(t, roundTripped)

	assert.Equal(t, original.Memory.ID, roundTripped.Memory.ID)
	assert.InDelta(t, original.ComputedSalience, roundTripped.ComputedSalience, 0.01)
	assert.InDelta(t, original.RelevanceScore, roundTripped.RelevanceScore, 0.01)
	assert.InDelta(t, original.CombinedScore, roundTripped.CombinedScore, 0.01)
	assert.Equal(t, original.UsedSummary, roundTripped.UsedSummary)
}

func TestGraphStatsRoundTrip(t *testing.T) {
	original := &GraphStats{
		EntityCount:       10,
		EdgeCount:         25,
		MemoryCount:       100,
		ActiveMemoryCount: 85,
		TotalMemoryTokens: 50000,
		MemoriesByType: map[string]int{
			MemoryTypeFact:       40,
			MemoryTypePreference: 20,
			MemoryTypeDecision:   10,
		},
	}

	pb := GraphStatsToProto(original)
	require.NotNil(t, pb)
	roundTripped := GraphStatsFromProto(pb)
	require.NotNil(t, roundTripped)

	assert.Equal(t, original.EntityCount, roundTripped.EntityCount)
	assert.Equal(t, original.EdgeCount, roundTripped.EdgeCount)
	assert.Equal(t, original.MemoryCount, roundTripped.MemoryCount)
	assert.Equal(t, original.ActiveMemoryCount, roundTripped.ActiveMemoryCount)
	assert.Equal(t, original.TotalMemoryTokens, roundTripped.TotalMemoryTokens)
	assert.Equal(t, original.MemoriesByType, roundTripped.MemoriesByType)
}

func TestNilConversions(t *testing.T) {
	assert.Nil(t, EntityToProto(nil))
	assert.Nil(t, EntityFromProto(nil))
	assert.Nil(t, EdgeToProto(nil))
	assert.Nil(t, EdgeFromProto(nil))
	assert.Nil(t, MemoryToProto(nil))
	assert.Nil(t, MemoryFromProto(nil))
	assert.Nil(t, LineageToProto(nil))
	assert.Nil(t, LineageFromProto(nil))
	assert.Nil(t, ScoredMemoryToProto(nil))
	assert.Nil(t, ScoredMemoryFromProto(nil))
	assert.Nil(t, EntityRecallToProto(nil))
	assert.Nil(t, EntityRecallFromProto(nil))
	assert.Nil(t, GraphStatsToProto(nil))
	assert.Nil(t, GraphStatsFromProto(nil))
}
