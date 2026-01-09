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
package communication

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// SQLiteStore implements ReferenceStore with SQLite persistence
type SQLiteStore struct {
	mu sync.RWMutex

	// db is the SQLite database connection
	db *sql.DB

	// gcInterval controls how often GC runs
	gcInterval time.Duration

	// stopGC signals GC goroutine to stop
	stopGC chan struct{}

	// gcDone signals when GC goroutine has exited
	gcDone chan struct{}
}

// NewSQLiteStore creates a new SQLite-backed reference store with GC
func NewSQLiteStore(dbPath string, gcInterval time.Duration) (*SQLiteStore, error) {
	if gcInterval == 0 {
		gcInterval = 5 * time.Minute // Default 5 minute GC interval
	}

	// Open SQLite database
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	store := &SQLiteStore{
		db:         db,
		gcInterval: gcInterval,
		stopGC:     make(chan struct{}),
		gcDone:     make(chan struct{}),
	}

	// Initialize schema
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Start garbage collection goroutine
	go store.gcLoop()

	return store, nil
}

// initSchema creates the reference_store table
func (s *SQLiteStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS reference_store (
		id TEXT PRIMARY KEY,
		type INTEGER NOT NULL,
		store INTEGER NOT NULL,
		data BLOB NOT NULL,
		ref_count INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL DEFAULT 0,
		size_bytes INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_expires_at ON reference_store(expires_at);
	CREATE INDEX IF NOT EXISTS idx_ref_count ON reference_store(ref_count);
	`

	_, err := s.db.Exec(schema)
	return err
}

// Store implements ReferenceStore.Store
func (s *SQLiteStore) Store(ctx context.Context, data []byte, opts StoreOptions) (*loomv1.Reference, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cannot store empty data")
	}

	// Generate reference ID from data hash
	hash := sha256.Sum256(data)
	refID := hex.EncodeToString(hash[:])

	now := time.Now()
	expiresAt := int64(0)
	if opts.TTL > 0 {
		expiresAt = now.Add(time.Duration(opts.TTL) * time.Second).Unix()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if reference already exists
	var existingRefCount int64
	err := s.db.QueryRowContext(ctx, "SELECT ref_count FROM reference_store WHERE id = ?", refID).Scan(&existingRefCount)
	if err == nil {
		// Reference exists, increment ref_count
		_, err = s.db.ExecContext(ctx, "UPDATE reference_store SET ref_count = ref_count + 1 WHERE id = ?", refID)
		if err != nil {
			return nil, fmt.Errorf("failed to increment ref_count: %w", err)
		}

		// Return existing reference
		return &loomv1.Reference{
			Id:        refID,
			Type:      opts.Type,
			Store:     loomv1.ReferenceStore_REFERENCE_STORE_SQLITE,
			CreatedAt: now.Unix(),
			ExpiresAt: expiresAt,
		}, nil
	} else if err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to check existing reference: %w", err)
	}

	// Insert new reference
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO reference_store (id, type, store, data, ref_count, created_at, expires_at, size_bytes) VALUES (?, ?, ?, ?, 1, ?, ?, ?)",
		refID,
		int32(opts.Type),
		int32(loomv1.ReferenceStore_REFERENCE_STORE_SQLITE),
		data,
		now.Unix(),
		expiresAt,
		len(data),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert reference: %w", err)
	}

	return &loomv1.Reference{
		Id:        refID,
		Type:      opts.Type,
		Store:     loomv1.ReferenceStore_REFERENCE_STORE_SQLITE,
		CreatedAt: now.Unix(),
		ExpiresAt: expiresAt,
	}, nil
}

// Resolve implements ReferenceStore.Resolve
func (s *SQLiteStore) Resolve(ctx context.Context, ref *loomv1.Reference) ([]byte, error) {
	if ref == nil {
		return nil, fmt.Errorf("nil reference")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var data []byte
	var expiresAt int64
	err := s.db.QueryRowContext(ctx, "SELECT data, expires_at FROM reference_store WHERE id = ?", ref.Id).Scan(&data, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("reference not found: %s", ref.Id)
	} else if err != nil {
		return nil, fmt.Errorf("failed to resolve reference: %w", err)
	}

	// Check expiration
	if expiresAt > 0 && time.Now().Unix() > expiresAt {
		return nil, fmt.Errorf("reference expired: %s", ref.Id)
	}

	return data, nil
}

// Retain implements ReferenceStore.Retain
func (s *SQLiteStore) Retain(ctx context.Context, refID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.ExecContext(ctx, "UPDATE reference_store SET ref_count = ref_count + 1 WHERE id = ?", refID)
	if err != nil {
		return fmt.Errorf("failed to retain reference: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("reference not found: %s", refID)
	}

	return nil
}

// Release implements ReferenceStore.Release
func (s *SQLiteStore) Release(ctx context.Context, refID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Decrement ref_count
	result, err := s.db.ExecContext(ctx, "UPDATE reference_store SET ref_count = ref_count - 1 WHERE id = ?", refID)
	if err != nil {
		return fmt.Errorf("failed to release reference: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("reference not found: %s", refID)
	}

	// Check if ref_count reached 0, delete if so
	var refCount int64
	err = s.db.QueryRowContext(ctx, "SELECT ref_count FROM reference_store WHERE id = ?", refID).Scan(&refCount)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("failed to check ref_count: %w", err)
	}

	if refCount <= 0 {
		_, err = s.db.ExecContext(ctx, "DELETE FROM reference_store WHERE id = ?", refID)
		if err != nil {
			return fmt.Errorf("failed to delete reference: %w", err)
		}
	}

	return nil
}

// List implements ReferenceStore.List
func (s *SQLiteStore) List(ctx context.Context) ([]*loomv1.Reference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, "SELECT id, type, store, created_at, expires_at FROM reference_store")
	if err != nil {
		return nil, fmt.Errorf("failed to list references: %w", err)
	}
	defer rows.Close()

	refs := make([]*loomv1.Reference, 0)
	for rows.Next() {
		var id string
		var refType, store int32
		var createdAt, expiresAt int64

		if err := rows.Scan(&id, &refType, &store, &createdAt, &expiresAt); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		refs = append(refs, &loomv1.Reference{
			Id:        id,
			Type:      loomv1.ReferenceType(refType),
			Store:     loomv1.ReferenceStore(store),
			CreatedAt: createdAt,
			ExpiresAt: expiresAt,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	return refs, nil
}

// Stats implements ReferenceStore.Stats
func (s *SQLiteStore) Stats(ctx context.Context) (*StoreStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var stats StoreStats

	// Count active references
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(size_bytes), 0) FROM reference_store").
		Scan(&stats.ActiveRefs, &stats.CurrentBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}

	// Note: TotalRefs, TotalBytes, GCRuns, EvictionCount would require separate tracking tables
	// For simplicity, we only track active refs and current bytes
	stats.TotalRefs = stats.ActiveRefs
	stats.TotalBytes = stats.CurrentBytes

	return &stats, nil
}

// Close implements ReferenceStore.Close
func (s *SQLiteStore) Close() error {
	// Signal GC goroutine to stop
	close(s.stopGC)

	// Wait for GC goroutine to exit
	<-s.gcDone

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Close()
}

// gcLoop runs garbage collection periodically
func (s *SQLiteStore) gcLoop() {
	defer close(s.gcDone)

	ticker := time.NewTicker(s.gcInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.runGC()
		case <-s.stopGC:
			return
		}
	}
}

// runGC performs garbage collection on expired and zero-refcount entries
func (s *SQLiteStore) runGC() {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	now := time.Now().Unix()

	// Delete expired references
	_, err := s.db.ExecContext(ctx, "DELETE FROM reference_store WHERE expires_at > 0 AND expires_at < ?", now)
	if err != nil {
		// Log error but don't fail (GC is best-effort)
		return
	}

	// Delete zero-refcount references
	_, err = s.db.ExecContext(ctx, "DELETE FROM reference_store WHERE ref_count <= 0")
	if err != nil {
		return
	}
}
