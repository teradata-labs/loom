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
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// EntityToProto converts a domain Entity to its proto representation.
func EntityToProto(e *Entity) *loomv1.GraphEntity {
	if e == nil {
		return nil
	}
	return &loomv1.GraphEntity{
		Id:             e.ID,
		AgentId:        e.AgentID,
		Name:           e.Name,
		EntityType:     e.EntityType,
		PropertiesJson: e.PropertiesJSON,
		Owner:          e.Owner,
		CreatedAt:      e.CreatedAt.Unix(),
		UpdatedAt:      e.UpdatedAt.Unix(),
	}
}

// EntityFromProto converts a proto GraphEntity to a domain Entity.
func EntityFromProto(pb *loomv1.GraphEntity) *Entity {
	if pb == nil {
		return nil
	}
	return &Entity{
		ID:             pb.Id,
		AgentID:        pb.AgentId,
		Name:           pb.Name,
		EntityType:     pb.EntityType,
		PropertiesJSON: pb.PropertiesJson,
		Owner:          pb.Owner,
		CreatedAt:      time.Unix(pb.CreatedAt, 0),
		UpdatedAt:      time.Unix(pb.UpdatedAt, 0),
	}
}

// EdgeToProto converts a domain Edge to its proto representation.
func EdgeToProto(e *Edge) *loomv1.GraphEdge {
	if e == nil {
		return nil
	}
	return &loomv1.GraphEdge{
		Id:             e.ID,
		AgentId:        e.AgentID,
		SourceId:       e.SourceID,
		TargetId:       e.TargetID,
		Relation:       e.Relation,
		PropertiesJson: e.PropertiesJSON,
		CreatedAt:      e.CreatedAt.Unix(),
		UpdatedAt:      e.UpdatedAt.Unix(),
	}
}

// EdgeFromProto converts a proto GraphEdge to a domain Edge.
func EdgeFromProto(pb *loomv1.GraphEdge) *Edge {
	if pb == nil {
		return nil
	}
	return &Edge{
		ID:             pb.Id,
		AgentID:        pb.AgentId,
		SourceID:       pb.SourceId,
		TargetID:       pb.TargetId,
		Relation:       pb.Relation,
		PropertiesJSON: pb.PropertiesJson,
		CreatedAt:      time.Unix(pb.CreatedAt, 0),
		UpdatedAt:      time.Unix(pb.UpdatedAt, 0),
	}
}

// MemoryToProto converts a domain Memory to its proto representation.
func MemoryToProto(m *Memory) *loomv1.GraphMemory {
	if m == nil {
		return nil
	}
	pb := &loomv1.GraphMemory{
		Id:                m.ID,
		AgentId:           m.AgentID,
		Content:           m.Content,
		Summary:           m.Summary,
		MemoryType:        m.MemoryType,
		Source:            m.Source,
		SourceId:          m.SourceID,
		Owner:             m.Owner,
		MemoryAgentId:     m.MemoryAgentID,
		Tags:              m.Tags,
		Salience:          float32(m.Salience),
		TokenCount:        int32(m.TokenCount),
		SummaryTokenCount: int32(m.SummaryTokenCount),
		AccessCount:       int32(m.AccessCount),
		EntityIds:         m.EntityIDs,
		PropertiesJson:    m.PropertiesJSON,
		CreatedAt:         m.CreatedAt.Unix(),
		IsSuperseded:      m.IsSuperseded,
	}
	if m.AccessedAt != nil {
		pb.AccessedAt = m.AccessedAt.Unix()
	}
	if m.ExpiresAt != nil {
		pb.ExpiresAt = m.ExpiresAt.Unix()
	}
	return pb
}

// MemoryFromProto converts a proto GraphMemory to a domain Memory.
func MemoryFromProto(pb *loomv1.GraphMemory) *Memory {
	if pb == nil {
		return nil
	}
	m := &Memory{
		ID:                pb.Id,
		AgentID:           pb.AgentId,
		Content:           pb.Content,
		Summary:           pb.Summary,
		MemoryType:        pb.MemoryType,
		Source:            pb.Source,
		SourceID:          pb.SourceId,
		Owner:             pb.Owner,
		MemoryAgentID:     pb.MemoryAgentId,
		Tags:              pb.Tags,
		Salience:          float64(pb.Salience),
		TokenCount:        int(pb.TokenCount),
		SummaryTokenCount: int(pb.SummaryTokenCount),
		AccessCount:       int(pb.AccessCount),
		EntityIDs:         pb.EntityIds,
		PropertiesJSON:    pb.PropertiesJson,
		CreatedAt:         time.Unix(pb.CreatedAt, 0),
		IsSuperseded:      pb.IsSuperseded,
	}
	if pb.AccessedAt != 0 {
		t := time.Unix(pb.AccessedAt, 0)
		m.AccessedAt = &t
	}
	if pb.ExpiresAt != 0 {
		t := time.Unix(pb.ExpiresAt, 0)
		m.ExpiresAt = &t
	}
	return m
}

// LineageToProto converts a domain MemoryLineage to its proto representation.
func LineageToProto(l *MemoryLineage) *loomv1.MemoryLineage {
	if l == nil {
		return nil
	}
	return &loomv1.MemoryLineage{
		NewMemoryId:  l.NewMemoryID,
		OldMemoryId:  l.OldMemoryID,
		RelationType: l.RelationType,
		CreatedAt:    l.CreatedAt.Unix(),
	}
}

// LineageFromProto converts a proto MemoryLineage to a domain MemoryLineage.
func LineageFromProto(pb *loomv1.MemoryLineage) *MemoryLineage {
	if pb == nil {
		return nil
	}
	return &MemoryLineage{
		NewMemoryID:  pb.NewMemoryId,
		OldMemoryID:  pb.OldMemoryId,
		RelationType: pb.RelationType,
		CreatedAt:    time.Unix(pb.CreatedAt, 0),
	}
}

// ScoredMemoryToProto converts a domain ScoredMemory to its proto representation.
func ScoredMemoryToProto(sm *ScoredMemory) *loomv1.ScoredMemory {
	if sm == nil {
		return nil
	}
	return &loomv1.ScoredMemory{
		Memory:           MemoryToProto(sm.Memory),
		ComputedSalience: float32(sm.ComputedSalience),
		RelevanceScore:   float32(sm.RelevanceScore),
		CombinedScore:    float32(sm.CombinedScore),
		UsedSummary:      sm.UsedSummary,
	}
}

// ScoredMemoryFromProto converts a proto ScoredMemory to a domain ScoredMemory.
func ScoredMemoryFromProto(pb *loomv1.ScoredMemory) *ScoredMemory {
	if pb == nil {
		return nil
	}
	return &ScoredMemory{
		Memory:           MemoryFromProto(pb.Memory),
		ComputedSalience: float64(pb.ComputedSalience),
		RelevanceScore:   float64(pb.RelevanceScore),
		CombinedScore:    float64(pb.CombinedScore),
		UsedSummary:      pb.UsedSummary,
	}
}

// EntityRecallToProto converts a domain EntityRecall to its proto representation.
func EntityRecallToProto(er *EntityRecall) *loomv1.EntityRecall {
	if er == nil {
		return nil
	}
	pb := &loomv1.EntityRecall{
		Entity:          EntityToProto(er.Entity),
		TotalTokensUsed: int32(er.TotalTokensUsed),
		TotalCandidates: int32(er.TotalCandidates),
	}
	for _, sm := range er.Memories {
		pb.Memories = append(pb.Memories, ScoredMemoryToProto(&sm))
	}
	for _, e := range er.EdgesOut {
		pb.EdgesOut = append(pb.EdgesOut, EdgeToProto(e))
	}
	for _, e := range er.EdgesIn {
		pb.EdgesIn = append(pb.EdgesIn, EdgeToProto(e))
	}
	return pb
}

// EntityRecallFromProto converts a proto EntityRecall to a domain EntityRecall.
func EntityRecallFromProto(pb *loomv1.EntityRecall) *EntityRecall {
	if pb == nil {
		return nil
	}
	er := &EntityRecall{
		Entity:          EntityFromProto(pb.Entity),
		TotalTokensUsed: int(pb.TotalTokensUsed),
		TotalCandidates: int(pb.TotalCandidates),
	}
	for _, sm := range pb.Memories {
		er.Memories = append(er.Memories, *ScoredMemoryFromProto(sm))
	}
	for _, e := range pb.EdgesOut {
		er.EdgesOut = append(er.EdgesOut, EdgeFromProto(e))
	}
	for _, e := range pb.EdgesIn {
		er.EdgesIn = append(er.EdgesIn, EdgeFromProto(e))
	}
	return er
}

// GraphStatsToProto converts a domain GraphStats to its proto representation.
func GraphStatsToProto(gs *GraphStats) *loomv1.GraphStats {
	if gs == nil {
		return nil
	}
	byType := make(map[string]int32, len(gs.MemoriesByType))
	for k, v := range gs.MemoriesByType {
		byType[k] = int32(v)
	}
	return &loomv1.GraphStats{
		EntityCount:       int32(gs.EntityCount),
		EdgeCount:         int32(gs.EdgeCount),
		MemoryCount:       int32(gs.MemoryCount),
		ActiveMemoryCount: int32(gs.ActiveMemoryCount),
		TotalMemoryTokens: int32(gs.TotalMemoryTokens),
		MemoriesByType:    byType,
	}
}

// GraphStatsFromProto converts a proto GraphStats to a domain GraphStats.
func GraphStatsFromProto(pb *loomv1.GraphStats) *GraphStats {
	if pb == nil {
		return nil
	}
	byType := make(map[string]int, len(pb.MemoriesByType))
	for k, v := range pb.MemoriesByType {
		byType[k] = int(v)
	}
	return &GraphStats{
		EntityCount:       int(pb.EntityCount),
		EdgeCount:         int(pb.EdgeCount),
		MemoryCount:       int(pb.MemoryCount),
		ActiveMemoryCount: int(pb.ActiveMemoryCount),
		TotalMemoryTokens: int(pb.TotalMemoryTokens),
		MemoriesByType:    byType,
	}
}
