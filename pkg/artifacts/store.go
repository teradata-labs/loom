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
package artifacts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/teradata-labs/loom/pkg/observability"
)

// SourceType defines the source of an artifact.
type SourceType string

const (
	SourceUser      SourceType = "user"
	SourceGenerated SourceType = "generated"
	SourceAgent     SourceType = "agent"
)

// Artifact represents a file artifact with metadata.
type Artifact struct {
	ID             string
	Name           string
	Path           string
	Source         SourceType
	SourceAgentID  string
	Purpose        string
	ContentType    string
	SizeBytes      int64
	Checksum       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastAccessedAt *time.Time
	AccessCount    int
	Tags           []string
	Metadata       map[string]string
	DeletedAt      *time.Time
}

// Filter defines filtering options for listing artifacts.
type Filter struct {
	Source         *SourceType
	ContentType    *string
	Tags           []string
	MinSize        *int64
	MaxSize        *int64
	AfterDate      *time.Time
	BeforeDate     *time.Time
	IncludeDeleted bool
	Limit          int
	Offset         int
}

// Stats holds artifact storage statistics.
type Stats struct {
	TotalFiles     int
	TotalSizeBytes int64
	UserFiles      int
	GeneratedFiles int
	DeletedFiles   int
}

// ArtifactStore defines the interface for artifact storage operations.
type ArtifactStore interface {
	// Index adds or updates an artifact in the catalog.
	Index(ctx context.Context, artifact *Artifact) error

	// Get retrieves artifact metadata by ID.
	Get(ctx context.Context, id string) (*Artifact, error)

	// GetByName retrieves artifact by file name.
	GetByName(ctx context.Context, name string) (*Artifact, error)

	// List returns all artifacts matching filters.
	List(ctx context.Context, filter *Filter) ([]*Artifact, error)

	// Search performs FTS5 full-text search.
	Search(ctx context.Context, query string, limit int) ([]*Artifact, error)

	// Update updates artifact metadata.
	Update(ctx context.Context, artifact *Artifact) error

	// Delete soft-deletes or hard-deletes an artifact.
	Delete(ctx context.Context, id string, hard bool) error

	// RecordAccess updates last_accessed_at and access_count.
	RecordAccess(ctx context.Context, id string) error

	// GetStats returns storage statistics.
	GetStats(ctx context.Context) (*Stats, error)

	// Close closes the store.
	Close() error
}

// SQLiteStore implements ArtifactStore with SQLite backend.
type SQLiteStore struct {
	db     *sql.DB
	mu     sync.RWMutex
	tracer observability.Tracer
}

// NewSQLiteStore creates a new SQLite-backed artifact store.
// It reuses the existing loom.db database and creates the artifacts table if needed.
func NewSQLiteStore(dbPath string, tracer observability.Tracer) (*SQLiteStore, error) {
	// Open database (reuse existing session DB connection pattern)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	store := &SQLiteStore{
		db:     db,
		tracer: tracer,
	}

	// Initialize schema (handled by session_store.go migration now)
	// Schema will be added to session_store.go's initSchema() function

	return store, nil
}

// Index adds or updates an artifact in the database.
func (s *SQLiteStore) Index(ctx context.Context, artifact *Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var span *observability.Span
	if s.tracer != nil {
		ctx, span = s.tracer.StartSpan(ctx, "artifacts.index")
		defer s.tracer.EndSpan(span)
		span.SetAttribute("artifact.id", artifact.ID)
		span.SetAttribute("artifact.name", artifact.Name)
		span.SetAttribute("artifact.source", string(artifact.Source))
	}

	// Serialize tags and metadata to JSON
	tagsJSON, err := json.Marshal(artifact.Tags)
	if err != nil {
		return fmt.Errorf("failed to marshal tags: %w", err)
	}

	metadataJSON, err := json.Marshal(artifact.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Convert time pointers to nullable integers
	var lastAccessedAt *int64
	if artifact.LastAccessedAt != nil {
		timestamp := artifact.LastAccessedAt.Unix()
		lastAccessedAt = &timestamp
	}

	var deletedAt *int64
	if artifact.DeletedAt != nil {
		timestamp := artifact.DeletedAt.Unix()
		deletedAt = &timestamp
	}

	// Upsert artifact
	query := `
		INSERT INTO artifacts (
			id, name, path, source, source_agent_id, purpose, content_type,
			size_bytes, checksum, created_at, updated_at, last_accessed_at,
			access_count, tags, metadata_json, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			path = excluded.path,
			source = excluded.source,
			source_agent_id = excluded.source_agent_id,
			purpose = excluded.purpose,
			content_type = excluded.content_type,
			size_bytes = excluded.size_bytes,
			checksum = excluded.checksum,
			updated_at = excluded.updated_at,
			last_accessed_at = excluded.last_accessed_at,
			access_count = excluded.access_count,
			tags = excluded.tags,
			metadata_json = excluded.metadata_json,
			deleted_at = excluded.deleted_at
	`

	_, err = s.db.ExecContext(ctx, query,
		artifact.ID,
		artifact.Name,
		artifact.Path,
		string(artifact.Source),
		nullString(artifact.SourceAgentID),
		nullString(artifact.Purpose),
		artifact.ContentType,
		artifact.SizeBytes,
		artifact.Checksum,
		artifact.CreatedAt.Unix(),
		artifact.UpdatedAt.Unix(),
		lastAccessedAt,
		artifact.AccessCount,
		string(tagsJSON),
		string(metadataJSON),
		deletedAt,
	)
	if err != nil {
		if s.tracer != nil {
			span.RecordError(err)
			span.SetAttribute("error", true)
		}
		return fmt.Errorf("failed to index artifact: %w", err)
	}

	return nil
}

// Get retrieves an artifact by ID.
func (s *SQLiteStore) Get(ctx context.Context, id string) (*Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var span *observability.Span
	if s.tracer != nil {
		ctx, span = s.tracer.StartSpan(ctx, "artifacts.get")
		defer s.tracer.EndSpan(span)
		span.SetAttribute("artifact.id", id)
	}

	query := `
		SELECT id, name, path, source, source_agent_id, purpose, content_type,
			size_bytes, checksum, created_at, updated_at, last_accessed_at,
			access_count, tags, metadata_json, deleted_at
		FROM artifacts
		WHERE id = ? AND deleted_at IS NULL
	`

	artifact, err := s.scanArtifact(ctx, s.db.QueryRowContext(ctx, query, id))
	if err == sql.ErrNoRows {
		if s.tracer != nil {
			span.SetAttribute("found", false)
		}
		return nil, fmt.Errorf("artifact not found: %s", id)
	}
	if err != nil {
		if s.tracer != nil {
			span.RecordError(err)
			span.SetAttribute("error", true)
		}
		return nil, err
	}

	if s.tracer != nil {
		span.SetAttribute("found", true)
		span.SetAttribute("artifact.name", artifact.Name)
	}

	return artifact, nil
}

// GetByName retrieves an artifact by name.
func (s *SQLiteStore) GetByName(ctx context.Context, name string) (*Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var span *observability.Span
	if s.tracer != nil {
		ctx, span = s.tracer.StartSpan(ctx, "artifacts.get_by_name")
		defer s.tracer.EndSpan(span)
		span.SetAttribute("artifact.name", name)
	}

	query := `
		SELECT id, name, path, source, source_agent_id, purpose, content_type,
			size_bytes, checksum, created_at, updated_at, last_accessed_at,
			access_count, tags, metadata_json, deleted_at
		FROM artifacts
		WHERE name = ? AND deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1
	`

	artifact, err := s.scanArtifact(ctx, s.db.QueryRowContext(ctx, query, name))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("artifact not found: %s", name)
	}
	if err != nil {
		if s.tracer != nil {
			span.RecordError(err)
		}
		return nil, err
	}

	return artifact, nil
}

// List returns artifacts matching the filter.
func (s *SQLiteStore) List(ctx context.Context, filter *Filter) ([]*Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var span *observability.Span
	if s.tracer != nil {
		ctx, span = s.tracer.StartSpan(ctx, "artifacts.list")
		defer s.tracer.EndSpan(span)
	}

	// Build query
	query := `
		SELECT id, name, path, source, source_agent_id, purpose, content_type,
			size_bytes, checksum, created_at, updated_at, last_accessed_at,
			access_count, tags, metadata_json, deleted_at
		FROM artifacts
		WHERE 1=1
	`
	var args []interface{}

	// Apply filters
	if filter != nil {
		if filter.Source != nil {
			query += " AND source = ?"
			args = append(args, string(*filter.Source))
		}
		if filter.ContentType != nil {
			query += " AND content_type = ?"
			args = append(args, *filter.ContentType)
		}
		if !filter.IncludeDeleted {
			query += " AND deleted_at IS NULL"
		}
		if len(filter.Tags) > 0 {
			// Check if artifact has ALL specified tags
			for _, tag := range filter.Tags {
				query += " AND tags LIKE ?"
				args = append(args, "%\""+tag+"\"%")
			}
		}
		if filter.MinSize != nil {
			query += " AND size_bytes >= ?"
			args = append(args, *filter.MinSize)
		}
		if filter.MaxSize != nil {
			query += " AND size_bytes <= ?"
			args = append(args, *filter.MaxSize)
		}
		if filter.AfterDate != nil {
			query += " AND created_at >= ?"
			args = append(args, filter.AfterDate.Unix())
		}
		if filter.BeforeDate != nil {
			query += " AND created_at <= ?"
			args = append(args, filter.BeforeDate.Unix())
		}
	}

	// Order by created_at descending
	query += " ORDER BY created_at DESC"

	// Apply limit and offset
	if filter != nil {
		if filter.Limit > 0 {
			query += " LIMIT ?"
			args = append(args, filter.Limit)
		}
		if filter.Offset > 0 {
			query += " OFFSET ?"
			args = append(args, filter.Offset)
		}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		if s.tracer != nil {
			span.RecordError(err)
		}
		return nil, fmt.Errorf("failed to list artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []*Artifact
	for rows.Next() {
		artifact, err := s.scanArtifactFromRows(rows)
		if err != nil {
			if s.tracer != nil {
				span.RecordError(err)
			}
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}

	if s.tracer != nil {
		span.SetAttribute("count", len(artifacts))
	}

	return artifacts, nil
}

// Search performs FTS5 full-text search on artifacts.
func (s *SQLiteStore) Search(ctx context.Context, query string, limit int) ([]*Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var span *observability.Span
	if s.tracer != nil {
		ctx, span = s.tracer.StartSpan(ctx, "artifacts.search")
		defer s.tracer.EndSpan(span)
		span.SetAttribute("query", query)
		span.SetAttribute("limit", limit)
	}

	if limit <= 0 {
		limit = 20
	}

	// FTS5 search query
	searchQuery := `
		SELECT a.id, a.name, a.path, a.source, a.source_agent_id, a.purpose,
			a.content_type, a.size_bytes, a.checksum, a.created_at, a.updated_at,
			a.last_accessed_at, a.access_count, a.tags, a.metadata_json, a.deleted_at
		FROM artifacts a
		INNER JOIN artifacts_fts5 fts ON a.id = fts.artifact_id
		WHERE artifacts_fts5 MATCH ?
		ORDER BY rank
		LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, searchQuery, query, limit)
	if err != nil {
		if s.tracer != nil {
			span.RecordError(err)
		}
		return nil, fmt.Errorf("failed to search artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []*Artifact
	for rows.Next() {
		artifact, err := s.scanArtifactFromRows(rows)
		if err != nil {
			if s.tracer != nil {
				span.RecordError(err)
			}
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}

	if s.tracer != nil {
		span.SetAttribute("results", len(artifacts))
	}

	return artifacts, nil
}

// Update updates artifact metadata.
func (s *SQLiteStore) Update(ctx context.Context, artifact *Artifact) error {
	artifact.UpdatedAt = time.Now()
	return s.Index(ctx, artifact)
}

// Delete soft-deletes or hard-deletes an artifact.
func (s *SQLiteStore) Delete(ctx context.Context, id string, hard bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var span *observability.Span
	if s.tracer != nil {
		ctx, span = s.tracer.StartSpan(ctx, "artifacts.delete")
		defer s.tracer.EndSpan(span)
		span.SetAttribute("artifact.id", id)
		span.SetAttribute("hard_delete", hard)
	}

	if hard {
		// Hard delete: remove from database and filesystem
		// First query the artifact path directly (without locking via Get)
		var artifactPath string
		err := s.db.QueryRowContext(ctx, "SELECT path FROM artifacts WHERE id = ?", id).Scan(&artifactPath)
		if err == sql.ErrNoRows {
			return fmt.Errorf("artifact not found: %s", id)
		}
		if err != nil {
			if s.tracer != nil {
				span.RecordError(err)
			}
			return fmt.Errorf("failed to query artifact: %w", err)
		}

		// Delete file from filesystem
		if err := os.Remove(artifactPath); err != nil && !os.IsNotExist(err) {
			if s.tracer != nil {
				span.RecordError(err)
			}
			return fmt.Errorf("failed to delete file: %w", err)
		}

		// Delete from database
		_, err = s.db.ExecContext(ctx, "DELETE FROM artifacts WHERE id = ?", id)
		if err != nil {
			if s.tracer != nil {
				span.RecordError(err)
			}
			return fmt.Errorf("failed to delete artifact from database: %w", err)
		}
	} else {
		// Soft delete: set deleted_at timestamp
		now := time.Now().Unix()
		_, err := s.db.ExecContext(ctx,
			"UPDATE artifacts SET deleted_at = ? WHERE id = ?",
			now, id)
		if err != nil {
			if s.tracer != nil {
				span.RecordError(err)
			}
			return fmt.Errorf("failed to soft delete artifact: %w", err)
		}
	}

	return nil
}

// RecordAccess updates the last accessed timestamp and access count.
func (s *SQLiteStore) RecordAccess(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var span *observability.Span
	if s.tracer != nil {
		ctx, span = s.tracer.StartSpan(ctx, "artifacts.record_access")
		defer s.tracer.EndSpan(span)
		span.SetAttribute("artifact.id", id)
	}

	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		UPDATE artifacts
		SET last_accessed_at = ?, access_count = access_count + 1
		WHERE id = ?
	`, now, id)
	if err != nil {
		if s.tracer != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("failed to record access: %w", err)
	}

	return nil
}

// GetStats returns artifact storage statistics.
func (s *SQLiteStore) GetStats(ctx context.Context) (*Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var span *observability.Span
	if s.tracer != nil {
		ctx, span = s.tracer.StartSpan(ctx, "artifacts.get_stats")
		defer s.tracer.EndSpan(span)
	}

	stats := &Stats{}

	// Total files and size
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(size_bytes), 0)
		FROM artifacts
		WHERE deleted_at IS NULL
	`).Scan(&stats.TotalFiles, &stats.TotalSizeBytes)
	if err != nil {
		if s.tracer != nil {
			span.RecordError(err)
		}
		return nil, fmt.Errorf("failed to get total stats: %w", err)
	}

	// User files
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM artifacts
		WHERE source = 'user' AND deleted_at IS NULL
	`).Scan(&stats.UserFiles)
	if err != nil {
		if s.tracer != nil {
			span.RecordError(err)
		}
		return nil, fmt.Errorf("failed to get user files count: %w", err)
	}

	// Generated files
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM artifacts
		WHERE source = 'generated' AND deleted_at IS NULL
	`).Scan(&stats.GeneratedFiles)
	if err != nil {
		if s.tracer != nil {
			span.RecordError(err)
		}
		return nil, fmt.Errorf("failed to get generated files count: %w", err)
	}

	// Deleted files
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM artifacts
		WHERE deleted_at IS NOT NULL
	`).Scan(&stats.DeletedFiles)
	if err != nil {
		if s.tracer != nil {
			span.RecordError(err)
		}
		return nil, fmt.Errorf("failed to get deleted files count: %w", err)
	}

	if s.tracer != nil {
		span.SetAttribute("total_files", stats.TotalFiles)
		span.SetAttribute("total_size_bytes", stats.TotalSizeBytes)
	}

	return stats, nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// scanArtifact scans a single artifact from a query row.
func (s *SQLiteStore) scanArtifact(ctx context.Context, row *sql.Row) (*Artifact, error) {
	var (
		id, name, path, source, contentType, checksum string
		sourceAgentID, purpose                        sql.NullString
		sizeBytes, createdAt, updatedAt               int64
		lastAccessedAt, deletedAt                     sql.NullInt64
		accessCount                                   int
		tagsJSON, metadataJSON                        string
	)

	err := row.Scan(
		&id, &name, &path, &source, &sourceAgentID, &purpose, &contentType,
		&sizeBytes, &checksum, &createdAt, &updatedAt, &lastAccessedAt,
		&accessCount, &tagsJSON, &metadataJSON, &deletedAt,
	)
	if err != nil {
		return nil, err
	}

	return s.buildArtifact(
		id, name, path, source, sourceAgentID, purpose, contentType,
		sizeBytes, createdAt, updatedAt, checksum, lastAccessedAt, deletedAt,
		accessCount, tagsJSON, metadataJSON,
	)
}

// scanArtifactFromRows scans an artifact from query rows.
func (s *SQLiteStore) scanArtifactFromRows(rows *sql.Rows) (*Artifact, error) {
	var (
		id, name, path, source, contentType, checksum string
		sourceAgentID, purpose                        sql.NullString
		sizeBytes, createdAt, updatedAt               int64
		lastAccessedAt, deletedAt                     sql.NullInt64
		accessCount                                   int
		tagsJSON, metadataJSON                        string
	)

	err := rows.Scan(
		&id, &name, &path, &source, &sourceAgentID, &purpose, &contentType,
		&sizeBytes, &checksum, &createdAt, &updatedAt, &lastAccessedAt,
		&accessCount, &tagsJSON, &metadataJSON, &deletedAt,
	)
	if err != nil {
		return nil, err
	}

	return s.buildArtifact(
		id, name, path, source, sourceAgentID, purpose, contentType,
		sizeBytes, createdAt, updatedAt, checksum, lastAccessedAt, deletedAt,
		accessCount, tagsJSON, metadataJSON,
	)
}

// buildArtifact constructs an Artifact from scanned database fields.
func (s *SQLiteStore) buildArtifact(
	id, name, path, source string,
	sourceAgentID, purpose sql.NullString,
	contentType string,
	sizeBytes, createdAt, updatedAt int64,
	checksum string,
	lastAccessedAt, deletedAt sql.NullInt64,
	accessCount int,
	tagsJSON, metadataJSON string,
) (*Artifact, error) {
	// Deserialize tags
	var tags []string
	if tagsJSON != "" && tagsJSON != "null" {
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tags: %w", err)
		}
	}

	// Deserialize metadata
	var metadata map[string]string
	if metadataJSON != "" && metadataJSON != "null" {
		if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
	}

	artifact := &Artifact{
		ID:          id,
		Name:        name,
		Path:        path,
		Source:      SourceType(source),
		ContentType: contentType,
		SizeBytes:   sizeBytes,
		Checksum:    checksum,
		CreatedAt:   time.Unix(createdAt, 0),
		UpdatedAt:   time.Unix(updatedAt, 0),
		AccessCount: accessCount,
		Tags:        tags,
		Metadata:    metadata,
	}

	if sourceAgentID.Valid {
		artifact.SourceAgentID = sourceAgentID.String
	}
	if purpose.Valid {
		artifact.Purpose = purpose.String
	}
	if lastAccessedAt.Valid {
		t := time.Unix(lastAccessedAt.Int64, 0)
		artifact.LastAccessedAt = &t
	}
	if deletedAt.Valid {
		t := time.Unix(deletedAt.Int64, 0)
		artifact.DeletedAt = &t
	}

	return artifact, nil
}

// Helper functions

// nullString converts an empty string to sql.NullString.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

// GenerateArtifactID generates a unique artifact ID.
func GenerateArtifactID() string {
	return uuid.New().String()
}

// ComputeChecksum calculates the SHA256 checksum of a file.
func ComputeChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("failed to compute checksum: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// GetArtifactsDir returns the artifacts directory path.
func GetArtifactsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, ".loom", "artifacts"), nil
}
