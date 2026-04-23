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
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
)

// GraphMemoryStore implements memory.GraphMemoryStore using PostgreSQL.
type GraphMemoryStore struct {
	pool    *pgxpool.Pool
	tc      memory.TokenCounter
	tracer  observability.Tracer
	salConf memory.SalienceConfig
}

// NewGraphMemoryStore creates a new PostgreSQL-backed graph memory store.
func NewGraphMemoryStore(pool *pgxpool.Pool, tc memory.TokenCounter, tracer observability.Tracer, opts ...GraphMemoryOption) *GraphMemoryStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	s := &GraphMemoryStore{
		pool:    pool,
		tc:      tc,
		tracer:  tracer,
		salConf: memory.DefaultSalienceConfig(),
	}
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
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.create_entity")
	defer s.tracer.EndSpan(span)

	if entity.ID == "" {
		entity.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	entity.CreatedAt = now
	entity.UpdatedAt = now

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		_, err := tx.Exec(ctx,
			`INSERT INTO graph_entities (id, agent_id, name, entity_type, properties_json, owner, user_id, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			entity.ID, entity.AgentID, entity.Name, entity.EntityType,
			entity.PropertiesJSON, entity.Owner, userID, now, now,
		)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create entity: %w", err)
	}
	return entity, nil
}

func (s *GraphMemoryStore) GetEntity(ctx context.Context, agentID, name string) (*memory.Entity, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.get_entity")
	defer s.tracer.EndSpan(span)

	var result *memory.Entity
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, agent_id, name, entity_type, COALESCE(properties_json::text, '{}'),
			        COALESCE(owner, ''), created_at, updated_at
			 FROM graph_entities
			 WHERE agent_id = $1 AND name = $2 AND deleted_at IS NULL`,
			agentID, name,
		)
		var err error
		result, err = pgScanEntity(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *GraphMemoryStore) UpdateEntity(ctx context.Context, entity *memory.Entity) (*memory.Entity, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.update_entity")
	defer s.tracer.EndSpan(span)

	now := time.Now().UTC()
	entity.UpdatedAt = now

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE graph_entities SET entity_type = $1, properties_json = $2, updated_at = $3
			 WHERE agent_id = $4 AND name = $5 AND deleted_at IS NULL`,
			entity.EntityType, entity.PropertiesJSON, now,
			entity.AgentID, entity.Name,
		)
		if err != nil {
			return fmt.Errorf("update entity: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("entity not found: agent_id=%s name=%s", entity.AgentID, entity.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Read back updated entity.
	return s.GetEntity(ctx, entity.AgentID, entity.Name)
}

func (s *GraphMemoryStore) ListEntities(ctx context.Context, agentID, entityType string, limit, offset int) ([]*memory.Entity, int, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.list_entities")
	defer s.tracer.EndSpan(span)

	if limit <= 0 {
		limit = 50
	}

	var total int
	var entities []*memory.Entity

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var conditions []string
		var args []interface{}
		argN := 1

		conditions = append(conditions, fmt.Sprintf("agent_id = $%d", argN))
		args = append(args, agentID)
		argN++

		conditions = append(conditions, "deleted_at IS NULL")

		if entityType != "" {
			conditions = append(conditions, fmt.Sprintf("entity_type = $%d", argN))
			args = append(args, entityType)
			argN++
		}

		where := strings.Join(conditions, " AND ")

		// Count.
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM graph_entities WHERE "+where, args...,
		).Scan(&total); err != nil {
			return fmt.Errorf("count entities: %w", err)
		}

		// Fetch.
		fetchArgs := make([]interface{}, len(args))
		copy(fetchArgs, args)
		fetchArgs = append(fetchArgs, limit, offset)

		query := fmt.Sprintf(
			`SELECT id, agent_id, name, entity_type, COALESCE(properties_json::text, '{}'),
			        COALESCE(owner, ''), created_at, updated_at
			 FROM graph_entities WHERE %s
			 ORDER BY name
			 LIMIT $%d OFFSET $%d`, where, argN, argN+1)

		rows, err := tx.Query(ctx, query, fetchArgs...)
		if err != nil {
			return fmt.Errorf("list entities: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			e, err := pgScanEntityFromRows(rows)
			if err != nil {
				return err
			}
			entities = append(entities, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return entities, total, nil
}

func (s *GraphMemoryStore) SearchEntities(ctx context.Context, agentID, query string, limit int) ([]*memory.Entity, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.search_entities")
	defer s.tracer.EndSpan(span)

	if limit <= 0 {
		limit = 20
	}

	var entities []*memory.Entity
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, agent_id, name, entity_type, COALESCE(properties_json::text, '{}'),
			        COALESCE(owner, ''), created_at, updated_at
			 FROM graph_entities
			 WHERE entity_search @@ plainto_tsquery('english', $1)
			   AND agent_id = $2
			   AND deleted_at IS NULL
			 ORDER BY ts_rank(entity_search, plainto_tsquery('english', $1)) DESC
			 LIMIT $3`,
			query, agentID, limit,
		)
		if err != nil {
			return fmt.Errorf("search entities: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			e, err := pgScanEntityFromRows(rows)
			if err != nil {
				return err
			}
			entities = append(entities, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return entities, nil
}

func (s *GraphMemoryStore) DeleteEntity(ctx context.Context, agentID, name string) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.delete_entity")
	defer s.tracer.EndSpan(span)

	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE graph_entities SET deleted_at = NOW()
			 WHERE agent_id = $1 AND name = $2 AND deleted_at IS NULL`,
			agentID, name,
		)
		if err != nil {
			return fmt.Errorf("delete entity: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("entity not found: agent_id=%s name=%s", agentID, name)
		}
		return nil
	})
}

// =============================================================================
// Edge CRUD
// =============================================================================

func (s *GraphMemoryStore) Relate(ctx context.Context, edge *memory.Edge) (*memory.Edge, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.relate")
	defer s.tracer.EndSpan(span)

	if edge.ID == "" {
		edge.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	edge.CreatedAt = now
	edge.UpdatedAt = now

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		// Upsert: ON CONFLICT update properties and timestamp.
		_, err := tx.Exec(ctx,
			`INSERT INTO graph_edges (id, agent_id, source_id, target_id, relation, properties_json, user_id, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			 ON CONFLICT(source_id, target_id, relation) DO UPDATE SET
			   properties_json = EXCLUDED.properties_json,
			   updated_at = EXCLUDED.updated_at`,
			edge.ID, edge.AgentID, edge.SourceID, edge.TargetID, edge.Relation,
			edge.PropertiesJSON, userID, now, now,
		)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("relate: %w", err)
	}
	return edge, nil
}

func (s *GraphMemoryStore) Unrelate(ctx context.Context, sourceID, targetID, relation string) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.unrelate")
	defer s.tracer.EndSpan(span)

	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM graph_edges WHERE source_id = $1 AND target_id = $2 AND relation = $3`,
			sourceID, targetID, relation,
		)
		return err
	})
}

func (s *GraphMemoryStore) Neighbors(ctx context.Context, entityID string, relation string, direction string, depth int) ([]*memory.Edge, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.neighbors")
	defer s.tracer.EndSpan(span)

	if depth <= 0 {
		depth = 1
	}

	var query string
	var args []interface{}

	// PostgreSQL uses $N positional params and || for string concatenation.
	// The recursive CTEs use POSITION() instead of LIKE '%' || val || '%' for cycle detection.
	switch direction {
	case "inbound":
		query = `WITH RECURSIVE traverse(eid, target_id, source_id, depth, path) AS (
			SELECT e.id, e.target_id, e.source_id, 1, e.target_id || ',' || e.source_id
			FROM graph_edges e
			WHERE e.target_id = $1 AND ($2 = '' OR e.relation = $2) AND e.deleted_at IS NULL
			UNION ALL
			SELECT e.id, e.target_id, e.source_id, t.depth + 1, t.path || ',' || e.source_id
			FROM graph_edges e
			JOIN traverse t ON e.target_id = t.source_id
			WHERE t.depth < $3 AND POSITION(e.source_id IN t.path) = 0
			  AND ($2 = '' OR e.relation = $2)
			  AND e.deleted_at IS NULL
		)
		SELECT e.id, e.agent_id, e.source_id, e.target_id, e.relation,
		       COALESCE(e.properties_json::text, '{}'), e.created_at, e.updated_at
		FROM traverse t
		JOIN graph_edges e ON e.id = t.eid`
		args = []interface{}{entityID, relation, depth}
	case "both":
		query = `WITH RECURSIVE traverse(eid, from_id, to_id, depth, path) AS (
			SELECT e.id, e.source_id, e.target_id, 1, e.source_id || ',' || e.target_id
			FROM graph_edges e
			WHERE (e.source_id = $1 OR e.target_id = $1) AND ($2 = '' OR e.relation = $2) AND e.deleted_at IS NULL
			UNION ALL
			SELECT e.id, e.source_id, e.target_id, t.depth + 1, t.path || ',' || e.target_id
			FROM graph_edges e
			JOIN traverse t ON (e.source_id = t.to_id OR e.target_id = t.from_id)
			WHERE t.depth < $3 AND POSITION(e.target_id IN t.path) = 0
			  AND ($2 = '' OR e.relation = $2)
			  AND e.deleted_at IS NULL
		)
		SELECT e.id, e.agent_id, e.source_id, e.target_id, e.relation,
		       COALESCE(e.properties_json::text, '{}'), e.created_at, e.updated_at
		FROM traverse t
		JOIN graph_edges e ON e.id = t.eid`
		args = []interface{}{entityID, relation, depth}
	default: // outbound
		query = `WITH RECURSIVE traverse(eid, source_id, target_id, depth, path) AS (
			SELECT e.id, e.source_id, e.target_id, 1, e.source_id || ',' || e.target_id
			FROM graph_edges e
			WHERE e.source_id = $1 AND ($2 = '' OR e.relation = $2) AND e.deleted_at IS NULL
			UNION ALL
			SELECT e.id, e.source_id, e.target_id, t.depth + 1, t.path || ',' || e.target_id
			FROM graph_edges e
			JOIN traverse t ON e.source_id = t.target_id
			WHERE t.depth < $3 AND POSITION(e.target_id IN t.path) = 0
			  AND ($2 = '' OR e.relation = $2)
			  AND e.deleted_at IS NULL
		)
		SELECT e.id, e.agent_id, e.source_id, e.target_id, e.relation,
		       COALESCE(e.properties_json::text, '{}'), e.created_at, e.updated_at
		FROM traverse t
		JOIN graph_edges e ON e.id = t.eid`
		args = []interface{}{entityID, relation, depth}
	}

	var edges []*memory.Edge
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("neighbors: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		seen := make(map[string]bool)
		for rows.Next() {
			e, err := pgScanEdgeFromRows(rows)
			if err != nil {
				return err
			}
			if !seen[e.ID] {
				edges = append(edges, e)
				seen[e.ID] = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return edges, nil
}

func (s *GraphMemoryStore) ListEdgesFrom(ctx context.Context, entityID string) ([]*memory.Edge, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.list_edges_from")
	defer s.tracer.EndSpan(span)

	var edges []*memory.Edge
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, agent_id, source_id, target_id, relation,
			        COALESCE(properties_json::text, '{}'), created_at, updated_at
			 FROM graph_edges
			 WHERE source_id = $1 AND deleted_at IS NULL
			 ORDER BY relation`, entityID)
		if err != nil {
			return fmt.Errorf("list edges from: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			e, err := pgScanEdgeFromRows(rows)
			if err != nil {
				return err
			}
			edges = append(edges, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return edges, nil
}

func (s *GraphMemoryStore) ListEdgesTo(ctx context.Context, entityID string) ([]*memory.Edge, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.list_edges_to")
	defer s.tracer.EndSpan(span)

	var edges []*memory.Edge
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, agent_id, source_id, target_id, relation,
			        COALESCE(properties_json::text, '{}'), created_at, updated_at
			 FROM graph_edges
			 WHERE target_id = $1 AND deleted_at IS NULL
			 ORDER BY relation`, entityID)
		if err != nil {
			return fmt.Errorf("list edges to: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			e, err := pgScanEdgeFromRows(rows)
			if err != nil {
				return err
			}
			edges = append(edges, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return edges, nil
}

// =============================================================================
// Memory Operations
// =============================================================================

func (s *GraphMemoryStore) Remember(ctx context.Context, mem *memory.Memory) (*memory.Memory, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.remember")
	defer s.tracer.EndSpan(span)

	s.prepareMemory(mem)

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := pgInsertMemoryTx(ctx, tx, mem); err != nil {
			return err
		}
		return pgLinkEntitiesTx(ctx, tx, mem)
	})
	if err != nil {
		return nil, err
	}
	return mem, nil
}

func (s *GraphMemoryStore) GetMemory(ctx context.Context, agentID, memoryID string) (*memory.Memory, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.get_memory")
	defer s.tracer.EndSpan(span)

	var result *memory.Memory
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, agent_id, content, COALESCE(summary, ''), memory_type,
			        COALESCE(source, ''), COALESCE(source_id, ''),
			        COALESCE(owner, ''), COALESCE(memory_agent_id, ''),
			        tags, salience,
			        token_count, summary_token_count, access_count,
			        COALESCE(properties_json::text, '{}'),
			        created_at, accessed_at, expires_at
			 FROM graph_memories
			 WHERE agent_id = $1 AND id = $2 AND deleted_at IS NULL`,
			agentID, memoryID,
		)
		var err error
		result, err = pgScanMemory(row)
		if err != nil {
			return err
		}

		// Load entity IDs from junction table.
		eids, err := pgLoadMemoryEntityIDs(ctx, tx, memoryID)
		if err != nil {
			return err
		}
		result.EntityIDs = eids

		// Check superseded status.
		var supersededCount int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM graph_memory_lineage WHERE old_memory_id = $1 AND relation_type = 'SUPERSEDES'`,
			memoryID,
		).Scan(&supersededCount); err != nil {
			return fmt.Errorf("check superseded status: %w", err)
		}
		result.IsSuperseded = supersededCount > 0

		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *GraphMemoryStore) Recall(ctx context.Context, opts memory.RecallOpts) ([]*memory.Memory, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.recall")
	defer s.tracer.EndSpan(span)

	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	if opts.MinSalience <= 0 {
		opts.MinSalience = memory.DefaultMinSalience
	}

	var memories []*memory.Memory

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var conditions []string
		var args []interface{}
		argN := 1

		conditions = append(conditions, fmt.Sprintf("m.agent_id = $%d", argN))
		args = append(args, opts.AgentID)
		argN++

		// Exclude expired.
		conditions = append(conditions, "(m.expires_at IS NULL OR m.expires_at > NOW())")

		// Exclude superseded.
		conditions = append(conditions,
			`m.id NOT IN (SELECT old_memory_id FROM graph_memory_lineage WHERE relation_type = 'SUPERSEDES')`)

		// Exclude soft-deleted.
		conditions = append(conditions, "m.deleted_at IS NULL")

		// Salience threshold.
		conditions = append(conditions, fmt.Sprintf("m.salience >= $%d", argN))
		args = append(args, opts.MinSalience)
		argN++

		// Optional type filter.
		if opts.MemoryType != "" {
			conditions = append(conditions, fmt.Sprintf("m.memory_type = $%d", argN))
			args = append(args, opts.MemoryType)
			argN++
		}

		// Optional entity scope.
		if len(opts.EntityIDs) > 0 {
			placeholders := make([]string, len(opts.EntityIDs))
			for i, eid := range opts.EntityIDs {
				placeholders[i] = fmt.Sprintf("$%d", argN)
				args = append(args, eid)
				argN++
			}
			conditions = append(conditions, fmt.Sprintf(
				`m.id IN (SELECT memory_id FROM graph_memory_entities WHERE entity_id IN (%s))`,
				strings.Join(placeholders, ",")))
		}

		// Optional tag filter: JSONB containment.
		if len(opts.Tags) > 0 {
			for _, tag := range opts.Tags {
				conditions = append(conditions, fmt.Sprintf("m.tags @> $%d::jsonb", argN))
				tagJSON, _ := json.Marshal([]string{tag})
				args = append(args, string(tagJSON))
				argN++
			}
		}

		where := strings.Join(conditions, " AND ")

		var query string
		if opts.Query != "" {
			// FTS search with ts_rank ranking.
			query = fmt.Sprintf(
				`SELECT m.id, m.agent_id, m.content, COALESCE(m.summary, ''), m.memory_type,
				        COALESCE(m.source, ''), COALESCE(m.source_id, ''), COALESCE(m.owner, ''),
				        COALESCE(m.memory_agent_id, ''), m.tags, m.salience,
				        m.token_count, m.summary_token_count, m.access_count,
				        COALESCE(m.properties_json::text, '{}'),
				        m.created_at, m.accessed_at, m.expires_at
				 FROM graph_memories m
				 WHERE m.memory_search @@ plainto_tsquery('english', $%d) AND %s
				 ORDER BY (m.salience::float * ts_rank(m.memory_search, plainto_tsquery('english', $%d))) DESC, m.created_at DESC
				 LIMIT $%d`, argN, where, argN, argN+1)
			args = append(args, opts.Query, opts.Limit)
		} else {
			query = fmt.Sprintf(
				`SELECT m.id, m.agent_id, m.content, COALESCE(m.summary, ''), m.memory_type,
				        COALESCE(m.source, ''), COALESCE(m.source_id, ''), COALESCE(m.owner, ''),
				        COALESCE(m.memory_agent_id, ''), m.tags, m.salience,
				        m.token_count, m.summary_token_count, m.access_count,
				        COALESCE(m.properties_json::text, '{}'),
				        m.created_at, m.accessed_at, m.expires_at
				 FROM graph_memories m
				 WHERE %s
				 ORDER BY m.salience DESC, m.created_at DESC
				 LIMIT $%d`, where, argN)
			args = append(args, opts.Limit)
		}

		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("recall: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			mem, err := pgScanMemoryFromRows(rows)
			if err != nil {
				return err
			}
			memories = append(memories, mem)
		}
		return rows.Err()
	})
	if err != nil {
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
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.forget")
	defer s.tracer.EndSpan(span)

	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE graph_memories SET expires_at = NOW() WHERE id = $1`,
			memoryID,
		)
		return err
	})
}

func (s *GraphMemoryStore) Supersede(ctx context.Context, oldMemoryID string, newMem *memory.Memory) (*memory.Memory, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.supersede")
	defer s.tracer.EndSpan(span)

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Get old memory's salience. NUMERIC(5,4) scans as string in pgx.
		var salienceStr string
		err := tx.QueryRow(ctx,
			`SELECT salience FROM graph_memories WHERE id = $1`, oldMemoryID,
		).Scan(&salienceStr)
		if err != nil {
			return fmt.Errorf("get old memory: %w", err)
		}
		oldSalience, _ := strconv.ParseFloat(salienceStr, 64)

		// Inherit salience from old memory.
		if newMem.Salience == 0 {
			newMem.Salience = oldSalience
		}

		s.prepareMemory(newMem)

		if err := pgInsertMemoryTx(ctx, tx, newMem); err != nil {
			return err
		}
		if err := pgLinkEntitiesTx(ctx, tx, newMem); err != nil {
			return err
		}

		// Insert lineage.
		_, err = tx.Exec(ctx,
			`INSERT INTO graph_memory_lineage (new_memory_id, old_memory_id, relation_type, created_at)
			 VALUES ($1, $2, 'SUPERSEDES', NOW())`,
			newMem.ID, oldMemoryID,
		)
		if err != nil {
			return fmt.Errorf("insert lineage: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return newMem, nil
}

func (s *GraphMemoryStore) Consolidate(ctx context.Context, memoryIDs []string, consolidated *memory.Memory) (*memory.Memory, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.consolidate")
	defer s.tracer.EndSpan(span)

	if consolidated.MemoryType == "" {
		consolidated.MemoryType = memory.MemoryTypeConsolidation
	}

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Find max salience from sources.
		if consolidated.Salience == 0 {
			placeholders := make([]string, len(memoryIDs))
			args := make([]interface{}, len(memoryIDs))
			for i, id := range memoryIDs {
				placeholders[i] = fmt.Sprintf("$%d", i+1)
				args[i] = id
			}
			var maxSalienceStr string
			if err := tx.QueryRow(ctx,
				fmt.Sprintf(`SELECT COALESCE(MAX(salience), 0.5) FROM graph_memories WHERE id IN (%s)`, // #nosec G201 -- placeholders are $N only
					strings.Join(placeholders, ",")),
				args...,
			).Scan(&maxSalienceStr); err != nil {
				return fmt.Errorf("max salience: %w", err)
			}
			maxSalience, _ := strconv.ParseFloat(maxSalienceStr, 64)
			consolidated.Salience = maxSalience
		}

		s.prepareMemory(consolidated)

		if err := pgInsertMemoryTx(ctx, tx, consolidated); err != nil {
			return err
		}
		if err := pgLinkEntitiesTx(ctx, tx, consolidated); err != nil {
			return err
		}

		// Insert lineage for each source and decay source salience.
		for _, oldID := range memoryIDs {
			_, err := tx.Exec(ctx,
				`INSERT INTO graph_memory_lineage (new_memory_id, old_memory_id, relation_type, created_at)
				 VALUES ($1, $2, 'CONSOLIDATES', NOW())`,
				consolidated.ID, oldID,
			)
			if err != nil {
				return fmt.Errorf("insert lineage for %s: %w", oldID, err)
			}

			_, err = tx.Exec(ctx,
				`UPDATE graph_memories SET salience = salience * $1 WHERE id = $2`,
				s.salConf.ConsolidationDecay, oldID,
			)
			if err != nil {
				return fmt.Errorf("decay salience for %s: %w", oldID, err)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return consolidated, nil
}

func (s *GraphMemoryStore) GetLineage(ctx context.Context, memoryID string) ([]*memory.MemoryLineage, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.get_lineage")
	defer s.tracer.EndSpan(span)

	var lineages []*memory.MemoryLineage
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT new_memory_id, old_memory_id, relation_type, created_at
			 FROM graph_memory_lineage
			 WHERE new_memory_id = $1 OR old_memory_id = $1
			 ORDER BY created_at`,
			memoryID,
		)
		if err != nil {
			return fmt.Errorf("get lineage: %w", err)
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			var l memory.MemoryLineage
			if err := rows.Scan(&l.NewMemoryID, &l.OldMemoryID, &l.RelationType, &l.CreatedAt); err != nil {
				return fmt.Errorf("scan lineage: %w", err)
			}
			lineages = append(lineages, &l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return lineages, nil
}

func (s *GraphMemoryStore) TouchMemories(ctx context.Context, memoryIDs []string) error {
	if len(memoryIDs) == 0 {
		return nil
	}

	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		placeholders := make([]string, len(memoryIDs))
		args := make([]interface{}, len(memoryIDs))
		for i, id := range memoryIDs {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
			args[i] = id
		}

		_, err := tx.Exec(ctx,
			fmt.Sprintf( // #nosec G201 -- placeholders are $N only
				`UPDATE graph_memories SET accessed_at = NOW(), access_count = access_count + 1
				 WHERE id IN (%s)`, strings.Join(placeholders, ",")),
			args...,
		)
		return err
	})
}

func (s *GraphMemoryStore) DecayAll(ctx context.Context, agentID string, decayFactor float64) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.decay_all")
	defer s.tracer.EndSpan(span)

	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE graph_memories SET salience = GREATEST(0.0, salience * $1)
			 WHERE agent_id = $2 AND salience > 0.01`,
			decayFactor, agentID,
		)
		return err
	})
}

// =============================================================================
// Composite Query
// =============================================================================

func (s *GraphMemoryStore) ContextFor(ctx context.Context, opts memory.ContextForOpts) (*memory.EntityRecall, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.context_for")
	defer s.tracer.EndSpan(span)

	budget := memory.DefaultBudgetConfig(opts.MaxTokens)

	// Phase 1: Entity profile.
	entity, err := s.GetEntity(ctx, opts.AgentID, opts.EntityName)
	if err != nil {
		// Entity not found -- return empty recall instead of crashing.
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
	args := make([]interface{}, 0, len(ids))
	i := 1
	for id := range ids {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		args = append(args, id)
		i++
	}

	query := fmt.Sprintf(`SELECT id, name FROM graph_entities WHERE id IN (%s)`, // #nosec G201 -- placeholders are $N only
		strings.Join(placeholders, ","))

	names := make(map[string]string, len(ids))
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close() //nolint:errcheck

		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				continue
			}
			names[id] = name
		}
		return rows.Err()
	})
	if err != nil {
		return nil
	}
	return names
}

// =============================================================================
// Stats
// =============================================================================

func (s *GraphMemoryStore) GetStats(ctx context.Context, agentID string) (*memory.GraphStats, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.graph_memory.get_stats")
	defer s.tracer.EndSpan(span)

	stats := &memory.GraphStats{MemoriesByType: make(map[string]int)}

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM graph_entities WHERE agent_id = $1 AND deleted_at IS NULL`, agentID,
		).Scan(&stats.EntityCount); err != nil {
			return fmt.Errorf("count entities: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM graph_edges WHERE agent_id = $1 AND deleted_at IS NULL`, agentID,
		).Scan(&stats.EdgeCount); err != nil {
			return fmt.Errorf("count edges: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM graph_memories WHERE agent_id = $1 AND deleted_at IS NULL`, agentID,
		).Scan(&stats.MemoryCount); err != nil {
			return fmt.Errorf("count memories: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM graph_memories WHERE agent_id = $1 AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > NOW())`,
			agentID,
		).Scan(&stats.ActiveMemoryCount); err != nil {
			return fmt.Errorf("count active memories: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(token_count), 0) FROM graph_memories WHERE agent_id = $1 AND deleted_at IS NULL`, agentID,
		).Scan(&stats.TotalMemoryTokens); err != nil {
			return fmt.Errorf("sum memory tokens: %w", err)
		}

		rows, err := tx.Query(ctx,
			`SELECT memory_type, COUNT(*) FROM graph_memories WHERE agent_id = $1 AND deleted_at IS NULL GROUP BY memory_type`, agentID,
		)
		if err != nil {
			return fmt.Errorf("group memories by type: %w", err)
		}
		defer rows.Close() //nolint:errcheck
		for rows.Next() {
			var mtype string
			var count int
			if err := rows.Scan(&mtype, &count); err != nil {
				return fmt.Errorf("scan memory type count: %w", err)
			}
			stats.MemoriesByType[mtype] = count
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return stats, nil
}

// Close is a no-op; the pool is managed by the Backend.
func (s *GraphMemoryStore) Close() error {
	return nil
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

// pgInsertMemoryTx inserts a memory row within an existing pgx transaction.
func pgInsertMemoryTx(ctx context.Context, tx pgx.Tx, mem *memory.Memory) error {
	tagsJSON, err := json.Marshal(mem.Tags)
	if err != nil {
		tagsJSON = []byte("[]")
	}

	userID := UserIDFromContext(ctx)

	_, err = tx.Exec(ctx,
		`INSERT INTO graph_memories
		 (id, agent_id, content, summary, memory_type, source, source_id, owner, memory_agent_id,
		  tags, salience, token_count, summary_token_count, access_count, properties_json,
		  user_id, created_at, accessed_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 0, $14, $15, $16, NULL, $17)`,
		mem.ID, mem.AgentID, mem.Content, mem.Summary, mem.MemoryType,
		mem.Source, mem.SourceID, mem.Owner, mem.MemoryAgentID,
		string(tagsJSON), mem.Salience, mem.TokenCount, mem.SummaryTokenCount,
		mem.PropertiesJSON, userID, mem.CreatedAt, mem.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert memory: %w", err)
	}
	return nil
}

// pgLinkEntitiesTx links entity IDs to a memory via the junction table within a pgx transaction.
// If mem.EntityRoles is populated, it takes precedence (includes per-entity roles).
// Otherwise falls back to mem.EntityIDs with default RoleAbout.
func pgLinkEntitiesTx(ctx context.Context, tx pgx.Tx, mem *memory.Memory) error {
	if len(mem.EntityRoles) > 0 {
		for _, er := range mem.EntityRoles {
			role := er.Role
			if role == "" {
				role = memory.RoleAbout
			}
			_, err := tx.Exec(ctx,
				`INSERT INTO graph_memory_entities (memory_id, entity_id, role) VALUES ($1, $2, $3)`,
				mem.ID, er.ID, role,
			)
			if err != nil {
				return fmt.Errorf("link entity %s (role %s): %w", er.ID, role, err)
			}
		}
		return nil
	}
	// Fallback: EntityIDs without explicit roles -> default to RoleAbout.
	for _, eid := range mem.EntityIDs {
		_, err := tx.Exec(ctx,
			`INSERT INTO graph_memory_entities (memory_id, entity_id, role) VALUES ($1, $2, $3)`,
			mem.ID, eid, memory.RoleAbout,
		)
		if err != nil {
			return fmt.Errorf("link entity %s: %w", eid, err)
		}
	}
	return nil
}

// =============================================================================
// Scan Helpers
// =============================================================================

func pgScanEntity(row pgx.Row) (*memory.Entity, error) {
	var e memory.Entity
	err := row.Scan(&e.ID, &e.AgentID, &e.Name, &e.EntityType, &e.PropertiesJSON, &e.Owner, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func pgScanEntityFromRows(rows pgx.Rows) (*memory.Entity, error) {
	var e memory.Entity
	err := rows.Scan(&e.ID, &e.AgentID, &e.Name, &e.EntityType, &e.PropertiesJSON, &e.Owner, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func pgScanEdgeFromRows(rows pgx.Rows) (*memory.Edge, error) {
	var e memory.Edge
	err := rows.Scan(&e.ID, &e.AgentID, &e.SourceID, &e.TargetID, &e.Relation, &e.PropertiesJSON, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func pgScanMemory(row pgx.Row) (*memory.Memory, error) {
	var m memory.Memory
	var tagsJSON []byte
	var salienceStr string
	var accessedAt, expiresAt *time.Time

	err := row.Scan(&m.ID, &m.AgentID, &m.Content, &m.Summary, &m.MemoryType,
		&m.Source, &m.SourceID, &m.Owner, &m.MemoryAgentID,
		&tagsJSON, &salienceStr, &m.TokenCount, &m.SummaryTokenCount,
		&m.AccessCount, &m.PropertiesJSON, &m.CreatedAt, &accessedAt, &expiresAt,
	)
	if err != nil {
		return nil, err
	}

	m.Salience, _ = strconv.ParseFloat(salienceStr, 64)
	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &m.Tags)
	}
	m.AccessedAt = accessedAt
	m.ExpiresAt = expiresAt

	return &m, nil
}

func pgScanMemoryFromRows(rows pgx.Rows) (*memory.Memory, error) {
	var m memory.Memory
	var tagsJSON []byte
	var salienceStr string
	var accessedAt, expiresAt *time.Time

	err := rows.Scan(&m.ID, &m.AgentID, &m.Content, &m.Summary, &m.MemoryType,
		&m.Source, &m.SourceID, &m.Owner, &m.MemoryAgentID,
		&tagsJSON, &salienceStr, &m.TokenCount, &m.SummaryTokenCount,
		&m.AccessCount, &m.PropertiesJSON, &m.CreatedAt, &accessedAt, &expiresAt,
	)
	if err != nil {
		return nil, err
	}

	m.Salience, _ = strconv.ParseFloat(salienceStr, 64)
	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &m.Tags)
	}
	m.AccessedAt = accessedAt
	m.ExpiresAt = expiresAt

	return &m, nil
}

// VectorRecall is not yet supported by the Postgres backend. The interface
// requires it for the sqlite brute-force cosine path; once pgvector wiring
// lands here, replace this stub with a real implementation.
func (s *GraphMemoryStore) VectorRecall(ctx context.Context, opts memory.VectorRecallOpts) ([]*memory.Memory, error) {
	return nil, fmt.Errorf("VectorRecall not implemented for postgres backend yet")
}

func pgLoadMemoryEntityIDs(ctx context.Context, tx pgx.Tx, memoryID string) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT entity_id FROM graph_memory_entities WHERE memory_id = $1`, memoryID,
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
