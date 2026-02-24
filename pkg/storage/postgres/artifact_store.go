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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/observability"
)

// ArtifactStore implements artifacts.ArtifactStore using PostgreSQL.
type ArtifactStore struct {
	pool   *pgxpool.Pool
	tracer observability.Tracer
}

// NewArtifactStore creates a new PostgreSQL-backed artifact store.
func NewArtifactStore(pool *pgxpool.Pool, tracer observability.Tracer) *ArtifactStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &ArtifactStore{
		pool:   pool,
		tracer: tracer,
	}
}

// Index stores or updates an artifact record.
func (s *ArtifactStore) Index(ctx context.Context, artifact *artifacts.Artifact) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_artifact_store.index")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("artifact_id", artifact.ID)

	tagsJSON, err := json.Marshal(artifact.Tags)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal tags: %w", err)
	}

	metadataJSON, err := json.Marshal(artifact.Metadata)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	err = execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		_, err := tx.Exec(ctx, `
			INSERT INTO artifacts (id, user_id, name, path, source, source_agent_id, purpose, content_type,
				size_bytes, checksum, created_at, updated_at, last_accessed_at, access_count,
				tags, metadata_json, deleted_at, session_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
			ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name,
				path = EXCLUDED.path,
				source = EXCLUDED.source,
				source_agent_id = EXCLUDED.source_agent_id,
				purpose = EXCLUDED.purpose,
				content_type = EXCLUDED.content_type,
				size_bytes = EXCLUDED.size_bytes,
				checksum = EXCLUDED.checksum,
				updated_at = EXCLUDED.updated_at,
				last_accessed_at = EXCLUDED.last_accessed_at,
				access_count = EXCLUDED.access_count,
				tags = EXCLUDED.tags,
				metadata_json = EXCLUDED.metadata_json,
				deleted_at = EXCLUDED.deleted_at,
				session_id = EXCLUDED.session_id`,
			artifact.ID,
			userID,
			artifact.Name,
			artifact.Path,
			string(artifact.Source),
			nullableString(artifact.SourceAgentID),
			nullableString(artifact.Purpose),
			artifact.ContentType,
			artifact.SizeBytes,
			artifact.Checksum,
			artifact.CreatedAt,
			artifact.UpdatedAt,
			artifact.LastAccessedAt, // *time.Time, nil-safe
			artifact.AccessCount,
			tagsJSON,
			metadataJSON,
			artifact.DeletedAt, // *time.Time, nil-safe
			nullableString(artifact.SessionID),
		)
		if err != nil {
			return fmt.Errorf("failed to index artifact: %w", err)
		}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

// Get retrieves an artifact by ID.
func (s *ArtifactStore) Get(ctx context.Context, id string) (*artifacts.Artifact, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_artifact_store.get")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("artifact_id", id)

	var result *artifacts.Artifact
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		row := tx.QueryRow(ctx, `
			SELECT id, name, path, source, source_agent_id, purpose, content_type,
				size_bytes, checksum, created_at, updated_at, last_accessed_at, access_count,
				tags, metadata_json, deleted_at, session_id
			FROM artifacts
			WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
			id, userID,
		)
		var err error
		result, err = scanArtifact(row)
		return err
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return result, nil
}

// GetByName retrieves an artifact by name, optionally scoped to a session.
func (s *ArtifactStore) GetByName(ctx context.Context, name string, sessionID string) (*artifacts.Artifact, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_artifact_store.get_by_name")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("name", name)

	var result *artifacts.Artifact
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		var row pgx.Row
		if sessionID != "" {
			row = tx.QueryRow(ctx, `
				SELECT id, name, path, source, source_agent_id, purpose, content_type,
					size_bytes, checksum, created_at, updated_at, last_accessed_at, access_count,
					tags, metadata_json, deleted_at, session_id
				FROM artifacts
				WHERE name = $1 AND user_id = $2 AND deleted_at IS NULL AND (session_id = $3 OR session_id IS NULL)
				ORDER BY created_at DESC LIMIT 1`,
				name, userID, sessionID,
			)
		} else {
			row = tx.QueryRow(ctx, `
				SELECT id, name, path, source, source_agent_id, purpose, content_type,
					size_bytes, checksum, created_at, updated_at, last_accessed_at, access_count,
					tags, metadata_json, deleted_at, session_id
				FROM artifacts
				WHERE name = $1 AND user_id = $2 AND deleted_at IS NULL
				ORDER BY created_at DESC LIMIT 1`,
				name, userID,
			)
		}
		var err error
		result, err = scanArtifact(row)
		return err
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return result, nil
}

// List retrieves artifacts matching the given filters.
func (s *ArtifactStore) List(ctx context.Context, filter *artifacts.Filter) ([]*artifacts.Artifact, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_artifact_store.list")
	defer s.tracer.EndSpan(span)

	query := `SELECT id, name, path, source, source_agent_id, purpose, content_type,
		size_bytes, checksum, created_at, updated_at, last_accessed_at, access_count,
		tags, metadata_json, deleted_at, session_id
		FROM artifacts WHERE user_id = $1`

	// user_id is extracted inside execInTx callback; pre-allocate slot here
	args := []interface{}{nil} // placeholder; replaced inside execInTx
	argIdx := 2

	if filter != nil {
		if !filter.IncludeDeleted {
			query += " AND deleted_at IS NULL"
		}
		if filter.SessionID != nil && *filter.SessionID != "" {
			query += fmt.Sprintf(" AND session_id = $%d", argIdx)
			args = append(args, *filter.SessionID)
			argIdx++
		}
		if filter.Source != nil {
			query += fmt.Sprintf(" AND source = $%d", argIdx)
			args = append(args, string(*filter.Source))
			argIdx++
		}
		if filter.ContentType != nil && *filter.ContentType != "" {
			query += fmt.Sprintf(" AND content_type = $%d", argIdx)
			args = append(args, *filter.ContentType)
			argIdx++
		}
		for _, tag := range filter.Tags {
			query += fmt.Sprintf(" AND tags @> $%d::jsonb", argIdx)
			tagJSON, err := json.Marshal([]string{tag})
			if err != nil {
				span.RecordError(err)
				return nil, fmt.Errorf("failed to marshal tag filter: %w", err)
			}
			args = append(args, string(tagJSON))
			argIdx++
		}
		if filter.MinSize != nil && *filter.MinSize > 0 {
			query += fmt.Sprintf(" AND size_bytes >= $%d", argIdx)
			args = append(args, *filter.MinSize)
			argIdx++
		}
		if filter.MaxSize != nil && *filter.MaxSize > 0 {
			query += fmt.Sprintf(" AND size_bytes <= $%d", argIdx)
			args = append(args, *filter.MaxSize)
			argIdx++
		}
		if filter.AfterDate != nil {
			query += fmt.Sprintf(" AND created_at >= $%d", argIdx)
			args = append(args, *filter.AfterDate)
			argIdx++
		}
		if filter.BeforeDate != nil {
			query += fmt.Sprintf(" AND created_at <= $%d", argIdx)
			args = append(args, *filter.BeforeDate)
			argIdx++
		}
	} else {
		query += " AND deleted_at IS NULL"
	}

	query += " ORDER BY created_at DESC"

	if filter != nil && filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, filter.Limit)
		argIdx++
	}
	if filter != nil && filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET $%d", argIdx)
		args = append(args, filter.Offset)
	}

	var result []*artifacts.Artifact
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		args[0] = UserIDFromContext(ctx)
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("failed to list artifacts: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			a, err := scanArtifactFromRows(rows)
			if err != nil {
				return err
			}
			result = append(result, a)
		}
		return rows.Err()
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return result, nil
}

// Search performs full-text search on artifacts using tsvector.
func (s *ArtifactStore) Search(ctx context.Context, query string, sessionID string, limit int) ([]*artifacts.Artifact, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_artifact_store.search")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("query", query)

	if limit <= 0 {
		limit = 20
	}

	sqlQuery := `
		SELECT id, name, path, source, source_agent_id, purpose, content_type,
			size_bytes, checksum, created_at, updated_at, last_accessed_at, access_count,
			tags, metadata_json, deleted_at, session_id
		FROM artifacts
		WHERE user_id = $1 AND deleted_at IS NULL AND artifact_search @@ websearch_to_tsquery('english', $2)`

	// user_id placeholder filled inside execInTx; query text is $2
	args := []interface{}{nil, query}
	argIdx := 3

	if sessionID != "" {
		sqlQuery += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, sessionID)
		argIdx++
	}

	sqlQuery += fmt.Sprintf(" ORDER BY ts_rank_cd(artifact_search, websearch_to_tsquery('english', $2)) DESC LIMIT $%d", argIdx)
	args = append(args, limit)

	var result []*artifacts.Artifact
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		args[0] = UserIDFromContext(ctx)
		rows, err := tx.Query(ctx, sqlQuery, args...)
		if err != nil {
			return fmt.Errorf("failed to search artifacts: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			a, err := scanArtifactFromRows(rows)
			if err != nil {
				return err
			}
			result = append(result, a)
		}
		return rows.Err()
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return result, nil
}

// Update updates an existing artifact's metadata.
func (s *ArtifactStore) Update(ctx context.Context, artifact *artifacts.Artifact) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_artifact_store.update")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("artifact_id", artifact.ID)

	// Reuse Index which does an upsert
	return s.Index(ctx, artifact)
}

// Delete soft-deletes or hard-deletes an artifact.
func (s *ArtifactStore) Delete(ctx context.Context, id string, hard bool) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_artifact_store.delete")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("artifact_id", id)
	span.SetAttribute("hard", hard)

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		if hard {
			_, err := tx.Exec(ctx, "DELETE FROM artifacts WHERE id = $1 AND user_id = $2", id, userID)
			if err != nil {
				return fmt.Errorf("failed to hard delete artifact: %w", err)
			}
		} else {
			_, err := tx.Exec(ctx, "UPDATE artifacts SET deleted_at = $1 WHERE id = $2 AND user_id = $3", time.Now().UTC(), id, userID)
			if err != nil {
				return fmt.Errorf("failed to soft delete artifact: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

// RecordAccess updates the last_accessed_at timestamp and increments access_count.
func (s *ArtifactStore) RecordAccess(ctx context.Context, id string) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_artifact_store.record_access")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("artifact_id", id)

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		_, err := tx.Exec(ctx,
			"UPDATE artifacts SET last_accessed_at = $1, access_count = access_count + 1 WHERE id = $2 AND user_id = $3",
			time.Now().UTC(), id, userID,
		)
		if err != nil {
			return fmt.Errorf("failed to record access: %w", err)
		}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

// GetStats returns aggregate statistics about stored artifacts using a single query.
func (s *ArtifactStore) GetStats(ctx context.Context) (*artifacts.Stats, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_artifact_store.get_stats")
	defer s.tracer.EndSpan(span)

	var stats *artifacts.Stats
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		stats = &artifacts.Stats{}
		return tx.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE deleted_at IS NULL),
				COALESCE(SUM(size_bytes) FILTER (WHERE deleted_at IS NULL), 0),
				COUNT(*) FILTER (WHERE source = 'user' AND deleted_at IS NULL),
				COUNT(*) FILTER (WHERE source = 'generated' AND deleted_at IS NULL),
				COUNT(*) FILTER (WHERE deleted_at IS NOT NULL)
			FROM artifacts
			WHERE user_id = $1`,
			userID,
		).Scan(&stats.TotalFiles, &stats.TotalSizeBytes, &stats.UserFiles, &stats.GeneratedFiles, &stats.DeletedFiles)
	})
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get artifact stats: %w", err)
	}

	return stats, nil
}

// Close is a no-op; the pool is managed by the backend.
func (s *ArtifactStore) Close() error {
	return nil
}

// scanArtifact reads a single artifact from a pgx.Row.
func scanArtifact(row pgx.Row) (*artifacts.Artifact, error) {
	var (
		a              artifacts.Artifact
		source         string
		sourceAgentID  *string
		purpose        *string
		lastAccessedAt *time.Time
		tagsJSON       []byte
		metadataJSON   []byte
		deletedAt      *time.Time
		sessionID      *string
	)

	err := row.Scan(
		&a.ID, &a.Name, &a.Path, &source, &sourceAgentID, &purpose, &a.ContentType,
		&a.SizeBytes, &a.Checksum, &a.CreatedAt, &a.UpdatedAt, &lastAccessedAt, &a.AccessCount,
		&tagsJSON, &metadataJSON, &deletedAt, &sessionID,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to scan artifact: %w", err)
	}

	a.Source = artifacts.SourceType(source)
	if sourceAgentID != nil {
		a.SourceAgentID = *sourceAgentID
	}
	if purpose != nil {
		a.Purpose = *purpose
	}
	a.LastAccessedAt = lastAccessedAt
	a.DeletedAt = deletedAt
	if sessionID != nil {
		a.SessionID = *sessionID
	}

	if len(tagsJSON) > 0 {
		if err := json.Unmarshal(tagsJSON, &a.Tags); err != nil {
			return nil, fmt.Errorf("failed to unmarshal artifact tags: %w", err)
		}
	}
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &a.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal artifact metadata: %w", err)
		}
	}

	return &a, nil
}

// scanArtifactFromRows reads a single artifact from pgx.Rows (used in List/Search).
func scanArtifactFromRows(rows pgx.Rows) (*artifacts.Artifact, error) {
	var (
		a              artifacts.Artifact
		source         string
		sourceAgentID  *string
		purpose        *string
		lastAccessedAt *time.Time
		tagsJSON       []byte
		metadataJSON   []byte
		deletedAt      *time.Time
		sessionID      *string
	)

	err := rows.Scan(
		&a.ID, &a.Name, &a.Path, &source, &sourceAgentID, &purpose, &a.ContentType,
		&a.SizeBytes, &a.Checksum, &a.CreatedAt, &a.UpdatedAt, &lastAccessedAt, &a.AccessCount,
		&tagsJSON, &metadataJSON, &deletedAt, &sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan artifact: %w", err)
	}

	a.Source = artifacts.SourceType(source)
	if sourceAgentID != nil {
		a.SourceAgentID = *sourceAgentID
	}
	if purpose != nil {
		a.Purpose = *purpose
	}
	a.LastAccessedAt = lastAccessedAt
	a.DeletedAt = deletedAt
	if sessionID != nil {
		a.SessionID = *sessionID
	}

	if len(tagsJSON) > 0 {
		if err := json.Unmarshal(tagsJSON, &a.Tags); err != nil {
			return nil, fmt.Errorf("failed to unmarshal artifact tags: %w", err)
		}
	}
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &a.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal artifact metadata: %w", err)
		}
	}

	return &a, nil
}

// Compile-time check: ArtifactStore implements artifacts.ArtifactStore.
var _ artifacts.ArtifactStore = (*ArtifactStore)(nil)
