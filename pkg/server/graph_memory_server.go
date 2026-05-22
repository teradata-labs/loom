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

package server

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/memory"
)

// GraphMemoryServer implements loomv1.GraphMemoryServiceServer by delegating
// to a memory.GraphMemoryStore. It translates between proto request/response
// types and the domain types used by the store.
type GraphMemoryServer struct {
	loomv1.UnimplementedGraphMemoryServiceServer
	store  memory.GraphMemoryStore
	logger *zap.Logger
}

// NewGraphMemoryServer creates a new GraphMemoryServer.
func NewGraphMemoryServer(store memory.GraphMemoryStore, logger *zap.Logger) *GraphMemoryServer {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &GraphMemoryServer{store: store, logger: logger}
}

// =============================================================================
// Entity CRUD
// =============================================================================

func (s *GraphMemoryServer) CreateEntity(ctx context.Context, req *loomv1.CreateEntityRequest) (*loomv1.CreateEntityResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	entity := &memory.Entity{
		AgentID:        req.AgentId,
		Name:           req.Name,
		EntityType:     req.EntityType,
		PropertiesJSON: req.PropertiesJson,
		Owner:          req.Owner,
	}

	created, err := s.store.CreateEntity(ctx, entity)
	if err != nil {
		s.logger.Error("create entity failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "create entity: %v", err)
	}

	return &loomv1.CreateEntityResponse{Entity: entityToProto(created)}, nil
}

func (s *GraphMemoryServer) GetEntity(ctx context.Context, req *loomv1.GetEntityRequest) (*loomv1.GetEntityResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	entity, err := s.store.GetEntity(ctx, req.AgentId, req.Name)
	if err != nil {
		if isNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "entity not found: %s", req.Name)
		}
		s.logger.Error("get entity failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "get entity: %v", err)
	}

	return &loomv1.GetEntityResponse{Entity: entityToProto(entity)}, nil
}

func (s *GraphMemoryServer) UpdateEntity(ctx context.Context, req *loomv1.UpdateEntityRequest) (*loomv1.UpdateEntityResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	entity := &memory.Entity{
		AgentID:        req.AgentId,
		Name:           req.Name,
		EntityType:     req.EntityType,
		PropertiesJSON: req.PropertiesJson,
	}

	updated, err := s.store.UpdateEntity(ctx, entity)
	if err != nil {
		if isNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "entity not found: %s", req.Name)
		}
		s.logger.Error("update entity failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "update entity: %v", err)
	}

	return &loomv1.UpdateEntityResponse{Entity: entityToProto(updated)}, nil
}

func (s *GraphMemoryServer) ListEntities(ctx context.Context, req *loomv1.ListEntitiesRequest) (*loomv1.ListEntitiesResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	limit := int(req.Limit)
	if limit <= 0 {
		limit = 50
	}
	offset := int(req.Offset)

	entities, total, err := s.store.ListEntities(ctx, req.AgentId, req.EntityType, limit, offset)
	if err != nil {
		s.logger.Error("list entities failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "list entities: %v", err)
	}

	protoEntities := make([]*loomv1.GraphEntity, 0, len(entities))
	for _, e := range entities {
		protoEntities = append(protoEntities, entityToProto(e))
	}

	return &loomv1.ListEntitiesResponse{
		Entities:   protoEntities,
		TotalCount: int32(total), // #nosec G115
	}, nil
}

func (s *GraphMemoryServer) DeleteEntity(ctx context.Context, req *loomv1.DeleteEntityRequest) (*loomv1.DeleteEntityResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	err := s.store.DeleteEntity(ctx, req.AgentId, req.Name)
	if err != nil {
		if isNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "entity not found: %s", req.Name)
		}
		s.logger.Error("delete entity failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "delete entity: %v", err)
	}

	return &loomv1.DeleteEntityResponse{Success: true}, nil
}

// =============================================================================
// Edge CRUD
// =============================================================================

func (s *GraphMemoryServer) Relate(ctx context.Context, req *loomv1.RelateRequest) (*loomv1.RelateResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.SourceName == "" {
		return nil, status.Error(codes.InvalidArgument, "source_name is required")
	}
	if req.TargetName == "" {
		return nil, status.Error(codes.InvalidArgument, "target_name is required")
	}
	if req.Relation == "" {
		return nil, status.Error(codes.InvalidArgument, "relation is required")
	}

	// Resolve entity names to IDs.
	source, err := s.store.GetEntity(ctx, req.AgentId, req.SourceName)
	if err != nil {
		if isNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "source entity not found: %s", req.SourceName)
		}
		return nil, status.Errorf(codes.Internal, "resolve source entity: %v", err)
	}
	target, err := s.store.GetEntity(ctx, req.AgentId, req.TargetName)
	if err != nil {
		if isNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "target entity not found: %s", req.TargetName)
		}
		return nil, status.Errorf(codes.Internal, "resolve target entity: %v", err)
	}

	edge := &memory.Edge{
		AgentID:        req.AgentId,
		SourceID:       source.ID,
		TargetID:       target.ID,
		Relation:       req.Relation,
		PropertiesJSON: req.PropertiesJson,
	}

	created, err := s.store.Relate(ctx, edge)
	if err != nil {
		s.logger.Error("relate failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "relate: %v", err)
	}

	return &loomv1.RelateResponse{Edge: edgeToProto(created)}, nil
}

func (s *GraphMemoryServer) Unrelate(ctx context.Context, req *loomv1.UnrelateRequest) (*loomv1.UnrelateResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.SourceName == "" {
		return nil, status.Error(codes.InvalidArgument, "source_name is required")
	}
	if req.TargetName == "" {
		return nil, status.Error(codes.InvalidArgument, "target_name is required")
	}
	if req.Relation == "" {
		return nil, status.Error(codes.InvalidArgument, "relation is required")
	}

	// Resolve entity names to IDs.
	source, err := s.store.GetEntity(ctx, req.AgentId, req.SourceName)
	if err != nil {
		if isNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "source entity not found: %s", req.SourceName)
		}
		return nil, status.Errorf(codes.Internal, "resolve source entity: %v", err)
	}
	target, err := s.store.GetEntity(ctx, req.AgentId, req.TargetName)
	if err != nil {
		if isNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "target entity not found: %s", req.TargetName)
		}
		return nil, status.Errorf(codes.Internal, "resolve target entity: %v", err)
	}

	err = s.store.Unrelate(ctx, source.ID, target.ID, req.Relation)
	if err != nil {
		s.logger.Error("unrelate failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "unrelate: %v", err)
	}

	return &loomv1.UnrelateResponse{Success: true}, nil
}

func (s *GraphMemoryServer) Neighbors(ctx context.Context, req *loomv1.NeighborsRequest) (*loomv1.NeighborsResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.EntityName == "" {
		return nil, status.Error(codes.InvalidArgument, "entity_name is required")
	}

	// Resolve entity name to ID.
	entity, err := s.store.GetEntity(ctx, req.AgentId, req.EntityName)
	if err != nil {
		if isNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "entity not found: %s", req.EntityName)
		}
		return nil, status.Errorf(codes.Internal, "resolve entity: %v", err)
	}

	direction := neighborDirectionToString(req.Direction)
	depth := int(req.Depth)
	if depth <= 0 {
		depth = 1
	}

	edges, err := s.store.Neighbors(ctx, entity.ID, req.Relation, direction, depth)
	if err != nil {
		s.logger.Error("neighbors failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "neighbors: %v", err)
	}

	// Collect unique entity IDs from edges and resolve them.
	entityIDs := make(map[string]struct{})
	for _, e := range edges {
		entityIDs[e.SourceID] = struct{}{}
		entityIDs[e.TargetID] = struct{}{}
	}

	protoEdges := make([]*loomv1.GraphEdge, 0, len(edges))
	for _, e := range edges {
		protoEdges = append(protoEdges, edgeToProto(e))
	}

	// Return the entity itself plus any connected entities found in edges.
	protoEntities := []*loomv1.GraphEntity{entityToProto(entity)}

	return &loomv1.NeighborsResponse{
		Edges:    protoEdges,
		Entities: protoEntities,
	}, nil
}

// =============================================================================
// Memory Operations
// =============================================================================

func (s *GraphMemoryServer) Remember(ctx context.Context, req *loomv1.RememberRequest) (*loomv1.RememberResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.Content == "" {
		return nil, status.Error(codes.InvalidArgument, "content is required")
	}

	mem := &memory.Memory{
		AgentID:        req.AgentId,
		Content:        req.Content,
		Summary:        req.Summary,
		MemoryType:     req.MemoryType,
		Source:         req.Source,
		SourceID:       req.SourceId,
		Owner:          req.Owner,
		MemoryAgentID:  req.MemoryAgentId,
		Tags:           req.Tags,
		Salience:       float64(req.Salience),
		EntityIDs:      req.EntityIds,
		PropertiesJSON: req.PropertiesJson,
	}

	if req.ExpiresAt > 0 {
		t := unixMilliToTimePtr(req.ExpiresAt)
		mem.ExpiresAt = t
	}

	created, err := s.store.Remember(ctx, mem)
	if err != nil {
		s.logger.Error("remember failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "remember: %v", err)
	}

	return &loomv1.RememberResponse{Memory: memoryToProto(created)}, nil
}

func (s *GraphMemoryServer) Recall(ctx context.Context, req *loomv1.RecallRequest) (*loomv1.RecallResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	opts := memory.RecallOpts{
		AgentID:     req.AgentId,
		Query:       req.Query,
		MemoryType:  req.MemoryType,
		EntityIDs:   req.EntityIds,
		Tags:        req.Tags,
		MinSalience: float64(req.MinSalience),
		MaxTokens:   int(req.MaxTokens),
		Limit:       int(req.Limit),
	}

	memories, err := s.store.Recall(ctx, opts)
	if err != nil {
		s.logger.Error("recall failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "recall: %v", err)
	}

	scored := make([]*loomv1.ScoredMemory, 0, len(memories))
	for _, m := range memories {
		scored = append(scored, &loomv1.ScoredMemory{
			Memory:           memoryToProto(m),
			ComputedSalience: float32(m.Salience),
			CombinedScore:    float32(m.Salience),
		})
	}

	return &loomv1.RecallResponse{
		Memories:        scored,
		TotalCandidates: int32(len(memories)), // #nosec G115
	}, nil
}

func (s *GraphMemoryServer) Forget(ctx context.Context, req *loomv1.ForgetRequest) (*loomv1.ForgetResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.MemoryId == "" {
		return nil, status.Error(codes.InvalidArgument, "memory_id is required")
	}

	err := s.store.Forget(ctx, req.MemoryId)
	if err != nil {
		s.logger.Error("forget failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "forget: %v", err)
	}

	return &loomv1.ForgetResponse{Success: true}, nil
}

func (s *GraphMemoryServer) Supersede(ctx context.Context, req *loomv1.SupersedeRequest) (*loomv1.SupersedeResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.OldMemoryId == "" {
		return nil, status.Error(codes.InvalidArgument, "old_memory_id is required")
	}
	if req.NewContent == "" {
		return nil, status.Error(codes.InvalidArgument, "new_content is required")
	}

	// Build the new memory from the old one's metadata.
	oldMem, err := s.store.GetMemory(ctx, req.AgentId, req.OldMemoryId)
	if err != nil {
		if isNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "old memory not found: %s", req.OldMemoryId)
		}
		return nil, status.Errorf(codes.Internal, "get old memory: %v", err)
	}

	newMem := &memory.Memory{
		AgentID:        req.AgentId,
		Content:        req.NewContent,
		Summary:        req.NewSummary,
		MemoryType:     oldMem.MemoryType,
		Source:         oldMem.Source,
		SourceID:       oldMem.SourceID,
		Owner:          oldMem.Owner,
		MemoryAgentID:  oldMem.MemoryAgentID,
		Tags:           req.NewTags,
		EntityIDs:      oldMem.EntityIDs,
		PropertiesJSON: req.NewPropertiesJson,
	}

	created, err := s.store.Supersede(ctx, req.OldMemoryId, newMem)
	if err != nil {
		s.logger.Error("supersede failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "supersede: %v", err)
	}

	lineage := &loomv1.MemoryLineage{
		NewMemoryId:  created.ID,
		OldMemoryId:  req.OldMemoryId,
		RelationType: loomv1.LineageRelationType_LINEAGE_RELATION_TYPE_SUPERSEDES,
		CreatedAt:    created.CreatedAt.UnixMilli(),
	}

	return &loomv1.SupersedeResponse{
		NewMemory: memoryToProto(created),
		Lineage:   lineage,
	}, nil
}

func (s *GraphMemoryServer) Consolidate(ctx context.Context, req *loomv1.ConsolidateRequest) (*loomv1.ConsolidateResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if len(req.MemoryIds) < 2 {
		return nil, status.Error(codes.InvalidArgument, "at least 2 memory_ids are required")
	}
	if req.Content == "" {
		return nil, status.Error(codes.InvalidArgument, "content is required")
	}

	consolidated := &memory.Memory{
		AgentID:    req.AgentId,
		Content:    req.Content,
		Summary:    req.Summary,
		MemoryType: memory.MemoryTypeConsolidation,
		Tags:       req.Tags,
		Salience:   float64(req.Salience),
	}

	created, err := s.store.Consolidate(ctx, req.MemoryIds, consolidated)
	if err != nil {
		s.logger.Error("consolidate failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "consolidate: %v", err)
	}

	lineages := make([]*loomv1.MemoryLineage, 0, len(req.MemoryIds))
	for _, oldID := range req.MemoryIds {
		lineages = append(lineages, &loomv1.MemoryLineage{
			NewMemoryId:  created.ID,
			OldMemoryId:  oldID,
			RelationType: loomv1.LineageRelationType_LINEAGE_RELATION_TYPE_CONSOLIDATES,
			CreatedAt:    created.CreatedAt.UnixMilli(),
		})
	}

	return &loomv1.ConsolidateResponse{
		ConsolidatedMemory: memoryToProto(created),
		Lineage:            lineages,
	}, nil
}

// =============================================================================
// Composite Queries
// =============================================================================

func (s *GraphMemoryServer) ContextFor(ctx context.Context, req *loomv1.ContextForRequest) (*loomv1.ContextForResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if req.EntityName == "" {
		return nil, status.Error(codes.InvalidArgument, "entity_name is required")
	}

	maxTokens := int(req.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = 2000
	}

	opts := memory.ContextForOpts{
		AgentID:    req.AgentId,
		EntityName: req.EntityName,
		Topic:      req.Topic,
		MaxTokens:  maxTokens,
	}

	recall, err := s.store.ContextFor(ctx, opts)
	if err != nil {
		s.logger.Error("context_for failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "context_for: %v", err)
	}

	return &loomv1.ContextForResponse{Recall: entityRecallToProto(recall)}, nil
}

// =============================================================================
// Stats
// =============================================================================

func (s *GraphMemoryServer) GetGraphStats(ctx context.Context, req *loomv1.GetGraphStatsRequest) (*loomv1.GetGraphStatsResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	stats, err := s.store.GetStats(ctx, req.AgentId)
	if err != nil {
		s.logger.Error("get_graph_stats failed", zap.Error(err), zap.String("agent_id", req.AgentId))
		return nil, status.Errorf(codes.Internal, "get graph stats: %v", err)
	}

	return &loomv1.GetGraphStatsResponse{Stats: graphStatsToProto(stats)}, nil
}

// =============================================================================
// Proto Converters
// =============================================================================

func entityToProto(e *memory.Entity) *loomv1.GraphEntity {
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
		CreatedAt:      e.CreatedAt.UnixMilli(),
		UpdatedAt:      e.UpdatedAt.UnixMilli(),
	}
}

func edgeToProto(e *memory.Edge) *loomv1.GraphEdge {
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
		CreatedAt:      e.CreatedAt.UnixMilli(),
		UpdatedAt:      e.UpdatedAt.UnixMilli(),
	}
}

func memoryToProto(m *memory.Memory) *loomv1.GraphMemory {
	if m == nil {
		return nil
	}
	p := &loomv1.GraphMemory{
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
		TokenCount:        int32(m.TokenCount),        // #nosec G115
		SummaryTokenCount: int32(m.SummaryTokenCount), // #nosec G115
		AccessCount:       int32(m.AccessCount),       // #nosec G115
		EntityIds:         m.EntityIDs,
		PropertiesJson:    m.PropertiesJSON,
		CreatedAt:         m.CreatedAt.UnixMilli(),
		IsSuperseded:      m.IsSuperseded,
	}
	if m.AccessedAt != nil {
		p.AccessedAt = m.AccessedAt.UnixMilli()
	}
	if m.ExpiresAt != nil {
		p.ExpiresAt = m.ExpiresAt.UnixMilli()
	}
	return p
}

func entityRecallToProto(er *memory.EntityRecall) *loomv1.EntityRecall {
	if er == nil {
		return nil
	}
	p := &loomv1.EntityRecall{
		Entity:          entityToProto(er.Entity),
		TotalTokensUsed: int32(er.TotalTokensUsed), // #nosec G115
		TotalCandidates: int32(er.TotalCandidates), // #nosec G115
	}

	for _, sm := range er.Memories {
		p.Memories = append(p.Memories, &loomv1.ScoredMemory{
			Memory:           memoryToProto(sm.Memory),
			ComputedSalience: float32(sm.ComputedSalience),
			RelevanceScore:   float32(sm.RelevanceScore),
			CombinedScore:    float32(sm.CombinedScore),
			UsedSummary:      sm.UsedSummary,
		})
	}

	for _, e := range er.EdgesOut {
		p.EdgesOut = append(p.EdgesOut, edgeToProto(e))
	}
	for _, e := range er.EdgesIn {
		p.EdgesIn = append(p.EdgesIn, edgeToProto(e))
	}

	return p
}

func graphStatsToProto(s *memory.GraphStats) *loomv1.GraphStats {
	if s == nil {
		return nil
	}
	memoriesByType := make(map[string]int32, len(s.MemoriesByType))
	for k, v := range s.MemoriesByType {
		memoriesByType[k] = int32(v) // #nosec G115
	}
	return &loomv1.GraphStats{
		EntityCount:       int32(s.EntityCount),       // #nosec G115
		EdgeCount:         int32(s.EdgeCount),         // #nosec G115
		MemoryCount:       int32(s.MemoryCount),       // #nosec G115
		ActiveMemoryCount: int32(s.ActiveMemoryCount), // #nosec G115
		TotalMemoryTokens: int32(s.TotalMemoryTokens), // #nosec G115
		MemoriesByType:    memoriesByType,
	}
}

// =============================================================================
// Helpers
// =============================================================================

// neighborDirectionToString converts the proto enum to the string the store expects.
func neighborDirectionToString(d loomv1.NeighborDirection) string {
	switch d {
	case loomv1.NeighborDirection_NEIGHBOR_DIRECTION_INBOUND:
		return "inbound"
	case loomv1.NeighborDirection_NEIGHBOR_DIRECTION_BOTH:
		return "both"
	case loomv1.NeighborDirection_NEIGHBOR_DIRECTION_OUTBOUND:
		return "outbound"
	default:
		return "outbound"
	}
}

// isNotFound returns true if the error indicates a not-found condition.
func isNotFound(err error) bool {
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return strings.Contains(err.Error(), "not found")
}

// unixMilliToTimePtr converts a Unix millisecond timestamp to a *time.Time.
func unixMilliToTimePtr(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms)
	return &t
}
