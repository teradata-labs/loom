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

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
)

// GraphMemoryStore implements memory.GraphMemoryStore for SQLite.
type GraphMemoryStore struct {
	db      *sql.DB
	tc      memory.TokenCounter
	tracer  observability.Tracer
	salConf memory.SalienceConfig
}

// NewGraphMemoryStore creates a new SQLite-backed graph memory store.
func NewGraphMemoryStore(db *sql.DB, tc memory.TokenCounter, tracer observability.Tracer, opts ...GraphMemoryOption) *GraphMemoryStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	s := &GraphMemoryStore{db: db, tc: tc, tracer: tracer, salConf: memory.DefaultSalienceConfig()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// GraphMemoryOption configures a GraphMemoryStore.
type GraphMemoryOption func(*GraphMemoryStore)

// WithSalienceConfig overrides the default salience configuration.
func WithSalienceConfig(cfg memory.SalienceConfig) GraphMemoryOption {
	return func(s *GraphMemoryStore) {
		s.salConf = cfg
	}
}

// Compile-time interface check.
var _ memory.GraphMemoryStore = (*GraphMemoryStore)(nil)

// =============================================================================
// Entity CRUD
// =============================================================================

func (s *GraphMemoryStore) CreateEntity(ctx context.Context, entity *memory.Entity) (*memory.Entity, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.create_entity")
	defer s.tracer.EndSpan(span)

	if entity.ID == "" {
		entity.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	entity.CreatedAt = now
	entity.UpdatedAt = now

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO graph_entities (id, agent_id, name, entity_type, properties_json, owner, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, datetime(?), datetime(?))`,
		entity.ID, entity.AgentID, entity.Name, entity.EntityType,
		entity.PropertiesJSON, entity.Owner,
		now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("create entity: %w", err)
	}
	return entity, nil
}

func (s *GraphMemoryStore) GetEntity(ctx context.Context, agentID, name string) (*memory.Entity, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.get_entity")
	defer s.tracer.EndSpan(span)

	row := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, name, entity_type, properties_json, COALESCE(owner,''), created_at, updated_at
		 FROM graph_entities WHERE agent_id = ? AND name = ?`,
		agentID, name,
	)
	return scanEntity(row)
}

func (s *GraphMemoryStore) UpdateEntity(ctx context.Context, entity *memory.Entity) (*memory.Entity, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.update_entity")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	entity.UpdatedAt = now

	result, err := s.db.ExecContext(ctx,
		`UPDATE graph_entities SET entity_type = ?, properties_json = ?, updated_at = datetime(?)
		 WHERE agent_id = ? AND name = ?`,
		entity.EntityType, entity.PropertiesJSON, now.Format(time.RFC3339),
		entity.AgentID, entity.Name,
	)
	if err != nil {
		return nil, fmt.Errorf("update entity: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, fmt.Errorf("entity not found: agent_id=%s name=%s", entity.AgentID, entity.Name)
	}

	return s.GetEntity(ctx, entity.AgentID, entity.Name)
}

func (s *GraphMemoryStore) ListEntities(ctx context.Context, agentID, entityType string, limit, offset int) ([]*memory.Entity, int, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.list_entities")
	defer s.tracer.EndSpan(span)

	if limit <= 0 {
		limit = 50
	}

	var countQuery, query string
	var args []interface{}

	if entityType != "" {
		countQuery = `SELECT COUNT(*) FROM graph_entities WHERE agent_id = ? AND entity_type = ?`
		query = `SELECT id, agent_id, name, entity_type, properties_json, COALESCE(owner,''), created_at, updated_at
				 FROM graph_entities WHERE agent_id = ? AND entity_type = ?
				 ORDER BY name LIMIT ? OFFSET ?`
		args = []interface{}{agentID, entityType}
	} else {
		countQuery = `SELECT COUNT(*) FROM graph_entities WHERE agent_id = ?`
		query = `SELECT id, agent_id, name, entity_type, properties_json, COALESCE(owner,''), created_at, updated_at
				 FROM graph_entities WHERE agent_id = ?
				 ORDER BY name LIMIT ? OFFSET ?`
		args = []interface{}{agentID}
	}

	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count entities: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list entities: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var entities []*memory.Entity
	for rows.Next() {
		e, err := scanEntityFromRows(rows)
		if err != nil {
			return nil, 0, err
		}
		entities = append(entities, e)
	}
	return entities, total, rows.Err()
}

func (s *GraphMemoryStore) SearchEntities(ctx context.Context, agentID, query string, limit int) ([]*memory.Entity, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.search_entities")
	defer s.tracer.EndSpan(span)

	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, e.agent_id, e.name, e.entity_type, e.properties_json, COALESCE(e.owner,''), e.created_at, e.updated_at
		 FROM graph_entities e
		 JOIN graph_entities_fts f ON f.entity_id = e.id
		 WHERE f.graph_entities_fts MATCH ? AND e.agent_id = ?
		 ORDER BY rank LIMIT ?`,
		query, agentID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search entities: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var entities []*memory.Entity
	for rows.Next() {
		e, err := scanEntityFromRows(rows)
		if err != nil {
			return nil, err
		}
		entities = append(entities, e)
	}
	return entities, rows.Err()
}

func (s *GraphMemoryStore) DeleteEntity(ctx context.Context, agentID, name string) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.delete_entity")
	defer s.tracer.EndSpan(span)

	result, err := s.db.ExecContext(ctx,
		`DELETE FROM graph_entities WHERE agent_id = ? AND name = ?`,
		agentID, name,
	)
	if err != nil {
		return fmt.Errorf("delete entity: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("entity not found: agent_id=%s name=%s", agentID, name)
	}
	return nil
}

// =============================================================================
// Edge CRUD
// =============================================================================

func (s *GraphMemoryStore) Relate(ctx context.Context, edge *memory.Edge) (*memory.Edge, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.relate")
	defer s.tracer.EndSpan(span)

	if edge.ID == "" {
		edge.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	edge.CreatedAt = now
	edge.UpdatedAt = now

	// Upsert: ON CONFLICT update properties and timestamp.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO graph_edges (id, agent_id, source_id, target_id, relation, properties_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, datetime(?), datetime(?))
		 ON CONFLICT(source_id, target_id, relation) DO UPDATE SET
		   properties_json = excluded.properties_json,
		   updated_at = excluded.updated_at`,
		edge.ID, edge.AgentID, edge.SourceID, edge.TargetID, edge.Relation,
		edge.PropertiesJSON, now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("relate: %w", err)
	}
	return edge, nil
}

func (s *GraphMemoryStore) Unrelate(ctx context.Context, sourceID, targetID, relation string) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.unrelate")
	defer s.tracer.EndSpan(span)

	_, err := s.db.ExecContext(ctx,
		`DELETE FROM graph_edges WHERE source_id = ? AND target_id = ? AND relation = ?`,
		sourceID, targetID, relation,
	)
	return err
}

func (s *GraphMemoryStore) Neighbors(ctx context.Context, entityID string, relation string, direction string, depth int) ([]*memory.Edge, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.neighbors")
	defer s.tracer.EndSpan(span)

	if depth <= 0 {
		depth = 1
	}

	var query string
	var args []interface{}

	switch direction {
	case "inbound":
		query = `WITH RECURSIVE traverse(eid, target_id, source_id, depth, path) AS (
			SELECT e.id, e.target_id, e.source_id, 1, e.target_id || ',' || e.source_id
			FROM graph_edges e
			WHERE e.target_id = ? AND (? = '' OR e.relation = ?)
			UNION ALL
			SELECT e.id, e.target_id, e.source_id, t.depth + 1, t.path || ',' || e.source_id
			FROM graph_edges e
			JOIN traverse t ON e.target_id = t.source_id
			WHERE t.depth < ? AND t.path NOT LIKE '%' || e.source_id || '%'
			  AND (? = '' OR e.relation = ?)
		)
		SELECT e.id, e.agent_id, e.source_id, e.target_id, e.relation, e.properties_json, e.created_at, e.updated_at
		FROM traverse t
		JOIN graph_edges e ON e.id = t.eid`
		args = []interface{}{entityID, relation, relation, depth, relation, relation}
	case "both":
		query = `WITH RECURSIVE traverse(eid, from_id, to_id, depth, path) AS (
			SELECT e.id, e.source_id, e.target_id, 1, e.source_id || ',' || e.target_id
			FROM graph_edges e
			WHERE (e.source_id = ? OR e.target_id = ?) AND (? = '' OR e.relation = ?)
			UNION ALL
			SELECT e.id, e.source_id, e.target_id, t.depth + 1, t.path || ',' || e.target_id
			FROM graph_edges e
			JOIN traverse t ON (e.source_id = t.to_id OR e.target_id = t.from_id)
			WHERE t.depth < ? AND t.path NOT LIKE '%' || e.target_id || '%'
			  AND (? = '' OR e.relation = ?)
		)
		SELECT e.id, e.agent_id, e.source_id, e.target_id, e.relation, e.properties_json, e.created_at, e.updated_at
		FROM traverse t
		JOIN graph_edges e ON e.id = t.eid`
		args = []interface{}{entityID, entityID, relation, relation, depth, relation, relation}
	default: // outbound
		query = `WITH RECURSIVE traverse(eid, source_id, target_id, depth, path) AS (
			SELECT e.id, e.source_id, e.target_id, 1, e.source_id || ',' || e.target_id
			FROM graph_edges e
			WHERE e.source_id = ? AND (? = '' OR e.relation = ?)
			UNION ALL
			SELECT e.id, e.source_id, e.target_id, t.depth + 1, t.path || ',' || e.target_id
			FROM graph_edges e
			JOIN traverse t ON e.source_id = t.target_id
			WHERE t.depth < ? AND t.path NOT LIKE '%' || e.target_id || '%'
			  AND (? = '' OR e.relation = ?)
		)
		SELECT e.id, e.agent_id, e.source_id, e.target_id, e.relation, e.properties_json, e.created_at, e.updated_at
		FROM traverse t
		JOIN graph_edges e ON e.id = t.eid`
		args = []interface{}{entityID, relation, relation, depth, relation, relation}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("neighbors: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var edges []*memory.Edge
	seen := make(map[string]bool)
	for rows.Next() {
		e, err := scanEdgeFromRows(rows)
		if err != nil {
			return nil, err
		}
		if !seen[e.ID] {
			edges = append(edges, e)
			seen[e.ID] = true
		}
	}
	return edges, rows.Err()
}

func (s *GraphMemoryStore) ListEdgesFrom(ctx context.Context, entityID string) ([]*memory.Edge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, source_id, target_id, relation, properties_json, created_at, updated_at
		 FROM graph_edges WHERE source_id = ? ORDER BY relation`, entityID)
	if err != nil {
		return nil, fmt.Errorf("list edges from: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	return scanEdgeRows(rows)
}

func (s *GraphMemoryStore) ListEdgesTo(ctx context.Context, entityID string) ([]*memory.Edge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, source_id, target_id, relation, properties_json, created_at, updated_at
		 FROM graph_edges WHERE target_id = ? ORDER BY relation`, entityID)
	if err != nil {
		return nil, fmt.Errorf("list edges to: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	return scanEdgeRows(rows)
}

func scanEdgeRows(rows *sql.Rows) ([]*memory.Edge, error) {
	var edges []*memory.Edge
	for rows.Next() {
		e, err := scanEdgeFromRows(rows)
		if err != nil {
			return nil, err
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// =============================================================================
// Memory Operations
// =============================================================================

func (s *GraphMemoryStore) Remember(ctx context.Context, mem *memory.Memory) (*memory.Memory, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.remember")
	defer s.tracer.EndSpan(span)

	s.prepareMemory(mem)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := insertMemoryTx(ctx, tx, mem); err != nil {
		return nil, err
	}
	if err := linkEntitiesTx(ctx, tx, mem); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return mem, nil
}

func (s *GraphMemoryStore) GetMemory(ctx context.Context, agentID, memoryID string) (*memory.Memory, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.get_memory")
	defer s.tracer.EndSpan(span)

	row := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, content, summary, memory_type, COALESCE(source,''), COALESCE(source_id,''),
		        COALESCE(owner,''), COALESCE(memory_agent_id,''), tags, salience,
		        token_count, summary_token_count, access_count, properties_json,
		        created_at, accessed_at, expires_at
		 FROM graph_memories WHERE agent_id = ? AND id = ?`,
		agentID, memoryID,
	)
	mem, err := scanMemory(row)
	if err != nil {
		return nil, err
	}

	// Load entity IDs from junction table.
	eids, err := s.loadMemoryEntityIDs(ctx, memoryID)
	if err != nil {
		return nil, err
	}
	mem.EntityIDs = eids

	// Check superseded status.
	var supersededCount int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM graph_memory_lineage WHERE old_memory_id = ? AND relation_type = 'SUPERSEDES'`,
		memoryID,
	).Scan(&supersededCount); err != nil {
		return nil, fmt.Errorf("check superseded status: %w", err)
	}
	mem.IsSuperseded = supersededCount > 0

	return mem, nil
}

func (s *GraphMemoryStore) Recall(ctx context.Context, opts memory.RecallOpts) ([]*memory.Memory, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.recall")
	defer s.tracer.EndSpan(span)

	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	if opts.MinSalience <= 0 {
		opts.MinSalience = memory.DefaultMinSalience
	}

	var conditions []string
	var args []interface{}

	conditions = append(conditions, "m.agent_id = ?")
	args = append(args, opts.AgentID)

	// Exclude expired.
	conditions = append(conditions, "(m.expires_at IS NULL OR m.expires_at > datetime('now'))")

	// Exclude superseded.
	conditions = append(conditions,
		`m.id NOT IN (SELECT old_memory_id FROM graph_memory_lineage WHERE relation_type = 'SUPERSEDES')`)

	// Salience threshold.
	conditions = append(conditions, "m.salience >= ?")
	args = append(args, opts.MinSalience)

	// Optional type filter.
	if opts.MemoryType != "" {
		conditions = append(conditions, "m.memory_type = ?")
		args = append(args, opts.MemoryType)
	}

	// Optional entity scope.
	if len(opts.EntityIDs) > 0 {
		placeholders := make([]string, len(opts.EntityIDs))
		for i, eid := range opts.EntityIDs {
			placeholders[i] = "?"
			args = append(args, eid)
		}
		conditions = append(conditions, fmt.Sprintf(
			`m.id IN (SELECT memory_id FROM graph_memory_entities WHERE entity_id IN (%s))`,
			strings.Join(placeholders, ",")))
	}

	// Optional tag filter: match exact JSON string values within the tags array.
	// Uses instr with JSON double-quote delimiters to avoid substring false positives
	// (e.g., searching for "test" won't match "test_data").
	if len(opts.Tags) > 0 {
		for _, tag := range opts.Tags {
			conditions = append(conditions, `instr(m.tags, '"' || ? || '"') > 0`)
			args = append(args, tag)
		}
	}

	where := strings.Join(conditions, " AND ")

	var query string
	if opts.Query != "" {
		// FTS search with BM25 ranking.
		query = fmt.Sprintf(
			`SELECT m.id, m.agent_id, m.content, m.summary, m.memory_type,
			        COALESCE(m.source,''), COALESCE(m.source_id,''), COALESCE(m.owner,''),
			        COALESCE(m.memory_agent_id,''), m.tags, m.salience,
			        m.token_count, m.summary_token_count, m.access_count, m.properties_json,
			        m.created_at, m.accessed_at, m.expires_at
			 FROM graph_memories m
			 JOIN graph_memories_fts f ON f.memory_id = m.id
			 WHERE f.graph_memories_fts MATCH ? AND %s
			 ORDER BY (m.salience * (-rank)) DESC, m.created_at DESC
			 LIMIT ?`, where)
		args = append([]interface{}{opts.Query}, args...)
	} else {
		query = fmt.Sprintf(
			`SELECT m.id, m.agent_id, m.content, m.summary, m.memory_type,
			        COALESCE(m.source,''), COALESCE(m.source_id,''), COALESCE(m.owner,''),
			        COALESCE(m.memory_agent_id,''), m.tags, m.salience,
			        m.token_count, m.summary_token_count, m.access_count, m.properties_json,
			        m.created_at, m.accessed_at, m.expires_at
			 FROM graph_memories m
			 WHERE %s
			 ORDER BY m.salience DESC, m.created_at DESC
			 LIMIT ?`, where)
	}
	args = append(args, opts.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("recall: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var memories []*memory.Memory
	for rows.Next() {
		mem, err := scanMemoryFromRows(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, mem)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Touch accessed memories.
	if len(memories) > 0 {
		ids := make([]string, len(memories))
		for i, m := range memories {
			ids[i] = m.ID
		}
		if err := s.TouchMemories(ctx, ids); err != nil {
			span.RecordError(fmt.Errorf("touch memories after recall: %w", err))
		}
	}

	return memories, nil
}

func (s *GraphMemoryStore) Forget(ctx context.Context, memoryID string) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.forget")
	defer s.tracer.EndSpan(span)

	_, err := s.db.ExecContext(ctx,
		`UPDATE graph_memories SET expires_at = datetime('now') WHERE id = ?`,
		memoryID,
	)
	return err
}

func (s *GraphMemoryStore) Supersede(ctx context.Context, oldMemoryID string, newMem *memory.Memory) (*memory.Memory, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.supersede")
	defer s.tracer.EndSpan(span)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Get old memory's salience.
	var oldSalience float64
	err = tx.QueryRowContext(ctx,
		`SELECT salience FROM graph_memories WHERE id = ?`, oldMemoryID,
	).Scan(&oldSalience)
	if err != nil {
		return nil, fmt.Errorf("get old memory: %w", err)
	}

	// Inherit salience from old memory.
	if newMem.Salience == 0 {
		newMem.Salience = oldSalience
	}

	s.prepareMemory(newMem)

	if err := insertMemoryTx(ctx, tx, newMem); err != nil {
		return nil, err
	}
	if err := linkEntitiesTx(ctx, tx, newMem); err != nil {
		return nil, err
	}

	// Insert lineage within the same transaction.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO graph_memory_lineage (new_memory_id, old_memory_id, relation_type, created_at)
		 VALUES (?, ?, 'SUPERSEDES', datetime('now'))`,
		newMem.ID, oldMemoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("insert lineage: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit supersede: %w", err)
	}

	return newMem, nil
}

func (s *GraphMemoryStore) Consolidate(ctx context.Context, memoryIDs []string, consolidated *memory.Memory) (*memory.Memory, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.consolidate")
	defer s.tracer.EndSpan(span)

	if consolidated.MemoryType == "" {
		consolidated.MemoryType = memory.MemoryTypeConsolidation
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Find max salience from sources.
	if consolidated.Salience == 0 {
		placeholders := make([]string, len(memoryIDs))
		args := make([]interface{}, len(memoryIDs))
		for i, id := range memoryIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		var maxSalience float64
		if err := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT MAX(salience) FROM graph_memories WHERE id IN (%s)`,
				strings.Join(placeholders, ",")),
			args...,
		).Scan(&maxSalience); err != nil {
			return nil, fmt.Errorf("max salience: %w", err)
		}
		consolidated.Salience = maxSalience
	}

	s.prepareMemory(consolidated)

	if err := insertMemoryTx(ctx, tx, consolidated); err != nil {
		return nil, err
	}
	if err := linkEntitiesTx(ctx, tx, consolidated); err != nil {
		return nil, err
	}

	// Insert lineage for each source and decay source salience.
	for _, oldID := range memoryIDs {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO graph_memory_lineage (new_memory_id, old_memory_id, relation_type, created_at)
			 VALUES (?, ?, 'CONSOLIDATES', datetime('now'))`,
			consolidated.ID, oldID,
		)
		if err != nil {
			return nil, fmt.Errorf("insert lineage for %s: %w", oldID, err)
		}

		_, err = tx.ExecContext(ctx,
			`UPDATE graph_memories SET salience = salience * ? WHERE id = ?`,
			s.salConf.ConsolidationDecay, oldID,
		)
		if err != nil {
			return nil, fmt.Errorf("decay salience for %s: %w", oldID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit consolidate: %w", err)
	}

	return consolidated, nil
}

func (s *GraphMemoryStore) GetLineage(ctx context.Context, memoryID string) ([]*memory.MemoryLineage, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.get_lineage")
	defer s.tracer.EndSpan(span)

	rows, err := s.db.QueryContext(ctx,
		`SELECT new_memory_id, old_memory_id, relation_type, created_at
		 FROM graph_memory_lineage
		 WHERE new_memory_id = ? OR old_memory_id = ?
		 ORDER BY created_at`,
		memoryID, memoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("get lineage: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var lineages []*memory.MemoryLineage
	for rows.Next() {
		var l memory.MemoryLineage
		var createdAtStr string
		if err := rows.Scan(&l.NewMemoryID, &l.OldMemoryID, &l.RelationType, &createdAtStr); err != nil {
			return nil, err
		}
		l.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		if l.CreatedAt.IsZero() {
			l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
		}
		lineages = append(lineages, &l)
	}
	return lineages, rows.Err()
}

func (s *GraphMemoryStore) TouchMemories(ctx context.Context, memoryIDs []string) error {
	if len(memoryIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(memoryIDs))
	args := make([]interface{}, len(memoryIDs))
	for i, id := range memoryIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	_, err := s.db.ExecContext(ctx,
		fmt.Sprintf(
			`UPDATE graph_memories SET accessed_at = datetime('now'), access_count = access_count + 1
			 WHERE id IN (%s)`, strings.Join(placeholders, ",")),
		args...,
	)
	return err
}

func (s *GraphMemoryStore) DecayAll(ctx context.Context, agentID string, decayFactor float64) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.decay_all")
	defer s.tracer.EndSpan(span)

	_, err := s.db.ExecContext(ctx,
		`UPDATE graph_memories SET salience = MAX(0.0, salience * ?)
		 WHERE agent_id = ? AND salience > 0.01`,
		decayFactor, agentID,
	)
	return err
}

func (s *GraphMemoryStore) ContextFor(ctx context.Context, opts memory.ContextForOpts) (*memory.EntityRecall, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.context_for")
	defer s.tracer.EndSpan(span)

	budget := memory.DefaultBudgetConfig(opts.MaxTokens)

	// Phase 1: Entity profile.
	entity, err := s.GetEntity(ctx, opts.AgentID, opts.EntityName)
	if err != nil {
		// Entity not found — return empty recall instead of crashing.
		return &memory.EntityRecall{}, nil
	}

	result := &memory.EntityRecall{Entity: entity}
	tokensUsed := budget.EntityProfileBudget // Reserve for profile.

	// Phase 2: Graph neighborhood (1-hop).
	edgesOut, err := s.ListEdgesFrom(ctx, entity.ID)
	if err != nil {
		span.RecordError(fmt.Errorf("list edges from %s: %w", entity.ID, err))
	} else {
		result.EdgesOut = edgesOut
	}
	edgesIn, err := s.ListEdgesTo(ctx, entity.ID)
	if err != nil {
		span.RecordError(fmt.Errorf("list edges to %s: %w", entity.ID, err))
	} else {
		result.EdgesIn = edgesIn
	}
	tokensUsed += budget.GraphBudget // Reserve for graph.

	// Phase 2b: Resolve entity names for edge endpoints.
	result.EntityNames = s.resolveEntityNames(ctx, result.EdgesOut, result.EdgesIn)

	// Phase 3: Memories within remaining budget.
	remainingBudget := budget.MaxTokens - tokensUsed
	if remainingBudget <= 0 {
		result.TotalTokensUsed = tokensUsed
		return result, nil
	}

	// Collect neighbor entity IDs for scoped memory search.
	entityIDs := []string{entity.ID}
	for _, e := range edgesOut {
		entityIDs = append(entityIDs, e.TargetID)
	}
	for _, e := range edgesIn {
		entityIDs = append(entityIDs, e.SourceID)
	}

	memories, err := s.Recall(ctx, memory.RecallOpts{
		AgentID:   opts.AgentID,
		Query:     opts.Topic,
		EntityIDs: entityIDs,
		Limit:     50,
	})
	if err != nil {
		span.RecordError(fmt.Errorf("recall memories for %s: %w", opts.EntityName, err))
		return result, nil // Partial result without memories.
	}

	result.TotalCandidates = len(memories)

	// Convert to scored and allocate budget.
	scored := make([]memory.ScoredMemory, len(memories))
	for i, m := range memories {
		scored[i] = memory.ScoredMemory{
			Memory:           m,
			ComputedSalience: m.Salience,
			CombinedScore:    m.Salience,
		}
	}

	packed, memTokens := memory.AllocateMemoryBudget(scored, remainingBudget)
	result.Memories = packed
	result.TotalTokensUsed = tokensUsed + memTokens

	return result, nil
}

// resolveEntityNames batch-queries entity names for all IDs referenced in edges.
// Returns a map of entity ID -> entity name for use in EntityRecall.Format().
func (s *GraphMemoryStore) resolveEntityNames(ctx context.Context, edgesOut, edgesIn []*memory.Edge) map[string]string {
	ids := make(map[string]bool)
	for _, e := range edgesOut {
		ids[e.TargetID] = true
	}
	for _, e := range edgesIn {
		ids[e.SourceID] = true
	}
	if len(ids) == 0 {
		return nil
	}

	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}

	query := fmt.Sprintf(`SELECT id, name FROM graph_entities WHERE id IN (%s)`, // #nosec G201 -- placeholders are "?" only; values passed via args
		strings.Join(placeholders, ","))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close() //nolint:errcheck

	names := make(map[string]string, len(ids))
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		names[id] = name
	}
	return names
}

func (s *GraphMemoryStore) GetStats(ctx context.Context, agentID string) (*memory.GraphStats, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.graph_memory.get_stats")
	defer s.tracer.EndSpan(span)

	stats := &memory.GraphStats{MemoriesByType: make(map[string]int)}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM graph_entities WHERE agent_id = ?`, agentID,
	).Scan(&stats.EntityCount); err != nil {
		return nil, fmt.Errorf("count entities: %w", err)
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM graph_edges WHERE agent_id = ?`, agentID,
	).Scan(&stats.EdgeCount); err != nil {
		return nil, fmt.Errorf("count edges: %w", err)
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM graph_memories WHERE agent_id = ?`, agentID,
	).Scan(&stats.MemoryCount); err != nil {
		return nil, fmt.Errorf("count memories: %w", err)
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM graph_memories WHERE agent_id = ? AND (expires_at IS NULL OR expires_at > datetime('now'))`,
		agentID,
	).Scan(&stats.ActiveMemoryCount); err != nil {
		return nil, fmt.Errorf("count active memories: %w", err)
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(token_count), 0) FROM graph_memories WHERE agent_id = ?`, agentID,
	).Scan(&stats.TotalMemoryTokens); err != nil {
		return nil, fmt.Errorf("sum memory tokens: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT memory_type, COUNT(*) FROM graph_memories WHERE agent_id = ? GROUP BY memory_type`, agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("group memories by type: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var mtype string
		var count int
		if err := rows.Scan(&mtype, &count); err != nil {
			return nil, fmt.Errorf("scan memory type count: %w", err)
		}
		stats.MemoriesByType[mtype] = count
	}

	return stats, rows.Err()
}

func (s *GraphMemoryStore) Close() error {
	return nil // db owned externally
}

// =============================================================================
// Transaction Helpers
// =============================================================================

// prepareMemory initializes a memory's ID, timestamps, and token counts.
func (s *GraphMemoryStore) prepareMemory(mem *memory.Memory) {
	if mem.ID == "" {
		mem.ID = uuid.New().String()
	}
	mem.CreatedAt = time.Now().UTC()
	if s.tc != nil {
		mem.TokenCount = s.tc.CountTokens(mem.Content)
		if mem.Summary != "" {
			mem.SummaryTokenCount = s.tc.CountTokens(mem.Summary)
		}
	}
	if mem.Salience == 0 {
		mem.Salience = memory.DefaultSalience
	}
}

// insertMemoryTx inserts a memory row within an existing transaction.
func insertMemoryTx(ctx context.Context, tx *sql.Tx, mem *memory.Memory) error {
	tagsJSON, err := json.Marshal(mem.Tags)
	if err != nil {
		tagsJSON = []byte("[]")
	}

	var expiresAt interface{}
	if mem.ExpiresAt != nil {
		expiresAt = mem.ExpiresAt.Format(time.RFC3339)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO graph_memories
		 (id, agent_id, content, summary, memory_type, source, source_id, owner, memory_agent_id,
		  tags, salience, token_count, summary_token_count, access_count, properties_json,
		  created_at, accessed_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, datetime(?), NULL, ?)`,
		mem.ID, mem.AgentID, mem.Content, mem.Summary, mem.MemoryType,
		mem.Source, mem.SourceID, mem.Owner, mem.MemoryAgentID,
		string(tagsJSON), mem.Salience, mem.TokenCount, mem.SummaryTokenCount,
		mem.PropertiesJSON, mem.CreatedAt.Format(time.RFC3339), expiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert memory: %w", err)
	}
	return nil
}

// linkEntitiesTx links entity IDs to a memory via the junction table within a transaction.
// If mem.EntityRoles is populated, it takes precedence (includes per-entity roles).
// Otherwise falls back to mem.EntityIDs with default RoleAbout.
func linkEntitiesTx(ctx context.Context, tx *sql.Tx, mem *memory.Memory) error {
	if len(mem.EntityRoles) > 0 {
		for _, er := range mem.EntityRoles {
			role := er.Role
			if role == "" {
				role = memory.RoleAbout
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO graph_memory_entities (memory_id, entity_id, role) VALUES (?, ?, ?)`,
				mem.ID, er.ID, role,
			)
			if err != nil {
				return fmt.Errorf("link entity %s (role %s): %w", er.ID, role, err)
			}
		}
		return nil
	}
	// Fallback: EntityIDs without explicit roles → default to RoleAbout.
	for _, eid := range mem.EntityIDs {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO graph_memory_entities (memory_id, entity_id, role) VALUES (?, ?, ?)`,
			mem.ID, eid, memory.RoleAbout,
		)
		if err != nil {
			return fmt.Errorf("link entity %s: %w", eid, err)
		}
	}
	return nil
}

// =============================================================================
// Scan / Parse Helpers
// =============================================================================

func (s *GraphMemoryStore) loadMemoryEntityIDs(ctx context.Context, memoryID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id FROM graph_memory_entities WHERE memory_id = ?`, memoryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func scanEntity(row *sql.Row) (*memory.Entity, error) {
	var e memory.Entity
	var createdAt, updatedAt string
	err := row.Scan(&e.ID, &e.AgentID, &e.Name, &e.EntityType, &e.PropertiesJSON, &e.Owner, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	e.CreatedAt = parseTime(createdAt)
	e.UpdatedAt = parseTime(updatedAt)
	return &e, nil
}

func scanEntityFromRows(rows *sql.Rows) (*memory.Entity, error) {
	var e memory.Entity
	var createdAt, updatedAt string
	err := rows.Scan(&e.ID, &e.AgentID, &e.Name, &e.EntityType, &e.PropertiesJSON, &e.Owner, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	e.CreatedAt = parseTime(createdAt)
	e.UpdatedAt = parseTime(updatedAt)
	return &e, nil
}

func scanEdgeFromRows(rows *sql.Rows) (*memory.Edge, error) {
	var e memory.Edge
	var createdAt, updatedAt string
	err := rows.Scan(&e.ID, &e.AgentID, &e.SourceID, &e.TargetID, &e.Relation, &e.PropertiesJSON, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	e.CreatedAt = parseTime(createdAt)
	e.UpdatedAt = parseTime(updatedAt)
	return &e, nil
}

func scanMemory(row *sql.Row) (*memory.Memory, error) {
	var m memory.Memory
	var tagsJSON, createdAtStr string
	var accessedAt, expiresAt sql.NullString

	err := row.Scan(&m.ID, &m.AgentID, &m.Content, &m.Summary, &m.MemoryType,
		&m.Source, &m.SourceID, &m.Owner, &m.MemoryAgentID,
		&tagsJSON, &m.Salience, &m.TokenCount, &m.SummaryTokenCount,
		&m.AccessCount, &m.PropertiesJSON, &createdAtStr, &accessedAt, &expiresAt,
	)
	if err != nil {
		return nil, err
	}

	m.CreatedAt = parseTime(createdAtStr)
	if tagsJSON != "" && tagsJSON != "null" {
		if err := json.Unmarshal([]byte(tagsJSON), &m.Tags); err != nil {
			return nil, fmt.Errorf("unmarshal tags for memory %s: %w", m.ID, err)
		}
	}

	if accessedAt.Valid && accessedAt.String != "" {
		t := parseTime(accessedAt.String)
		m.AccessedAt = &t
	}
	if expiresAt.Valid && expiresAt.String != "" {
		t := parseTime(expiresAt.String)
		m.ExpiresAt = &t
	}

	return &m, nil
}

func scanMemoryFromRows(rows *sql.Rows) (*memory.Memory, error) {
	var m memory.Memory
	var tagsJSON, createdAtStr string
	var accessedAt, expiresAt sql.NullString

	err := rows.Scan(&m.ID, &m.AgentID, &m.Content, &m.Summary, &m.MemoryType,
		&m.Source, &m.SourceID, &m.Owner, &m.MemoryAgentID,
		&tagsJSON, &m.Salience, &m.TokenCount, &m.SummaryTokenCount,
		&m.AccessCount, &m.PropertiesJSON, &createdAtStr, &accessedAt, &expiresAt,
	)
	if err != nil {
		return nil, err
	}

	m.CreatedAt = parseTime(createdAtStr)
	if tagsJSON != "" && tagsJSON != "null" {
		if err := json.Unmarshal([]byte(tagsJSON), &m.Tags); err != nil {
			return nil, fmt.Errorf("unmarshal tags for memory %s: %w", m.ID, err)
		}
	}

	if accessedAt.Valid && accessedAt.String != "" {
		t := parseTime(accessedAt.String)
		m.AccessedAt = &t
	}
	if expiresAt.Valid && expiresAt.String != "" {
		t := parseTime(expiresAt.String)
		m.ExpiresAt = &t
	}

	return &m, nil
}

func parseTime(s string) time.Time {
	// Try RFC3339 first, then SQLite datetime format.
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t
	}
	t, err = time.Parse("2006-01-02 15:04:05", s)
	if err == nil {
		return t
	}
	t, err = time.Parse("2006-01-02T15:04:05", s)
	if err == nil {
		return t
	}
	return time.Time{}
}
