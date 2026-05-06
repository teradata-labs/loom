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

package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/teradata-labs/loom/pkg/skills"
)

// Store is the persistence interface for the hierarchical skill index.
// Two implementations are provided:
//   - MemoryStore: in-process, used for unit tests and ephemeral builds.
//   - SQLStore:    backed by the skill_indices / skill_index_nodes tables
//                  added by migrations 000007 (SQLite) / 000012 (Postgres).
type Store interface {
	// SaveIndex writes or replaces an entire index (metadata + nodes).
	SaveIndex(ctx context.Context, idx *skills.SkillIndex) error
	// LoadIndex returns the index keyed by its id, or (nil, nil) if absent.
	LoadIndex(ctx context.Context, id string) (*skills.SkillIndex, error)
	// LatestIndex returns the most-recently-built index, or (nil, nil)
	// when no index has ever been persisted.
	LatestIndex(ctx context.Context) (*skills.SkillIndex, error)
	// UpsertNode writes a single node, used during incremental hot-reload.
	UpsertNode(ctx context.Context, indexID string, node *skills.SkillIndexNode) error
	// DeleteIndex removes an index and its nodes.
	DeleteIndex(ctx context.Context, id string) error
	// Close releases any underlying resources.
	Close() error
}

// MemoryStore is an in-process Store. Goroutine-safe; suitable for tests
// and for transient build pipelines.
type MemoryStore struct {
	mu       sync.RWMutex
	byID     map[string]*skills.SkillIndex
	latestID string
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: make(map[string]*skills.SkillIndex)}
}

// SaveIndex stores the full index keyed by idx.ID.
func (m *MemoryStore) SaveIndex(_ context.Context, idx *skills.SkillIndex) error {
	if idx == nil {
		return fmt.Errorf("memorystore: nil index")
	}
	if idx.ID == "" {
		return fmt.Errorf("memorystore: index id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cloned := cloneIndex(idx)
	m.byID[cloned.ID] = cloned
	if cloned.BuiltAtMs >= m.latestBuiltAtMsLocked() {
		m.latestID = cloned.ID
	}
	return nil
}

// LoadIndex returns a deep-copy of the indexed entry.
func (m *MemoryStore) LoadIndex(_ context.Context, id string) (*skills.SkillIndex, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	idx, ok := m.byID[id]
	if !ok {
		return nil, nil
	}
	return cloneIndex(idx), nil
}

// LatestIndex returns the most recently saved index by BuiltAtMs.
func (m *MemoryStore) LatestIndex(_ context.Context) (*skills.SkillIndex, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.latestID == "" {
		return nil, nil
	}
	idx, ok := m.byID[m.latestID]
	if !ok {
		return nil, nil
	}
	return cloneIndex(idx), nil
}

// UpsertNode replaces or appends a single node by id within the named index.
// Returns an error when the named index does not exist.
func (m *MemoryStore) UpsertNode(_ context.Context, indexID string, node *skills.SkillIndexNode) error {
	if node == nil {
		return fmt.Errorf("memorystore: nil node")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	idx, ok := m.byID[indexID]
	if !ok {
		return fmt.Errorf("memorystore: index %q not found", indexID)
	}
	for i, n := range idx.Nodes {
		if n.ID == node.ID {
			idx.Nodes[i] = cloneNode(node)
			return nil
		}
	}
	idx.Nodes = append(idx.Nodes, cloneNode(node))
	return nil
}

// DeleteIndex removes the named index. No-op when absent.
func (m *MemoryStore) DeleteIndex(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byID, id)
	if m.latestID == id {
		// Recompute latest by scanning.
		var newest int64
		var newestID string
		for k, v := range m.byID {
			if v.BuiltAtMs >= newest {
				newest = v.BuiltAtMs
				newestID = k
			}
		}
		m.latestID = newestID
	}
	return nil
}

// Close is a no-op for the in-memory store.
func (m *MemoryStore) Close() error { return nil }

func (m *MemoryStore) latestBuiltAtMsLocked() int64 {
	if m.latestID == "" {
		return 0
	}
	if idx, ok := m.byID[m.latestID]; ok {
		return idx.BuiltAtMs
	}
	return 0
}

// SQLStore is a Store backed by *sql.DB. Compatible with both the SQLite
// (migration 000007) and PostgreSQL (migration 000012) schemas because the
// queries use parameter placeholders that the dialect helper rewrites.
//
// Concurrency: SaveIndex runs in a single transaction so concurrent writers
// don't observe partial trees. Reads are not transactional but consume only
// a single index id at a time, so they're safe.
type SQLStore struct {
	db        *sql.DB
	dialect   Dialect
	deleteAll bool
}

// Dialect names the SQL dialect for placeholder rewriting. Defaults to
// SQLite ("?") behavior; pass DialectPostgres to use $1/$2 placeholders.
type Dialect int

const (
	// DialectSQLite uses ? positional placeholders. Default.
	DialectSQLite Dialect = iota
	// DialectPostgres uses $1, $2, ... placeholders.
	DialectPostgres
)

// NewSQLStore wraps a *sql.DB as a Store. The caller owns the lifecycle
// of db; Close is a no-op so it does not race with other consumers of
// the same connection.
func NewSQLStore(db *sql.DB, dialect Dialect) *SQLStore {
	return &SQLStore{db: db, dialect: dialect}
}

// SaveIndex writes the index metadata row and replaces all nodes for that
// root_id atomically. Re-running with the same id is idempotent.
func (s *SQLStore) SaveIndex(ctx context.Context, idx *skills.SkillIndex) error {
	if idx == nil {
		return fmt.Errorf("sqlstore: nil index")
	}
	if idx.ID == "" || idx.RootID == "" {
		return fmt.Errorf("sqlstore: index id and root id are required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Upsert the index metadata row.
	upsertIdxQ := s.placeholders(`
		INSERT INTO skill_indices (id, root_id, built_at_ms, built_by_model)
		VALUES (?, ?, ?, ?)
	`)
	switch s.dialect {
	case DialectPostgres:
		upsertIdxQ += ` ON CONFLICT (id) DO UPDATE SET
			root_id = EXCLUDED.root_id,
			built_at_ms = EXCLUDED.built_at_ms,
			built_by_model = EXCLUDED.built_by_model`
	default:
		upsertIdxQ = s.placeholders(`
			INSERT OR REPLACE INTO skill_indices (id, root_id, built_at_ms, built_by_model)
			VALUES (?, ?, ?, ?)
		`)
	}
	if _, err := tx.ExecContext(ctx, upsertIdxQ, idx.ID, idx.RootID, idx.BuiltAtMs, idx.BuiltByModel); err != nil {
		return fmt.Errorf("sqlstore: upsert index: %w", err)
	}

	// Replace nodes for this root_id wholesale. Smaller-than-the-index
	// surgical updates use UpsertNode instead.
	delNodesQ := s.placeholders("DELETE FROM skill_index_nodes WHERE root_id = ?")
	if _, err := tx.ExecContext(ctx, delNodesQ, idx.RootID); err != nil {
		return fmt.Errorf("sqlstore: delete nodes: %w", err)
	}

	insertNodeQ := s.placeholders(`
		INSERT INTO skill_index_nodes
			(id, root_id, title, summary, children_json, skill_refs_json,
			 depth, labels_json, content_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	for _, n := range idx.Nodes {
		childrenJSON, _ := json.Marshal(n.Children)
		skillRefsJSON, _ := json.Marshal(n.SkillRefs)
		labelsJSON, _ := json.Marshal(n.Labels)
		if _, err := tx.ExecContext(ctx, insertNodeQ,
			n.ID, idx.RootID, n.Title, n.Summary,
			string(childrenJSON), string(skillRefsJSON),
			n.Depth, string(labelsJSON), n.ContentHash); err != nil {
			return fmt.Errorf("sqlstore: insert node %q: %w", n.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlstore: commit: %w", err)
	}
	return nil
}

// LoadIndex returns the index identified by id, or (nil, nil) when absent.
func (s *SQLStore) LoadIndex(ctx context.Context, id string) (*skills.SkillIndex, error) {
	q := s.placeholders(`
		SELECT id, root_id, built_at_ms, built_by_model
		FROM skill_indices WHERE id = ?
	`)
	row := s.db.QueryRowContext(ctx, q, id)
	idx := &skills.SkillIndex{}
	if err := row.Scan(&idx.ID, &idx.RootID, &idx.BuiltAtMs, &idx.BuiltByModel); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("sqlstore: load index: %w", err)
	}
	nodes, err := s.loadNodes(ctx, idx.RootID)
	if err != nil {
		return nil, err
	}
	idx.Nodes = nodes
	return idx, nil
}

// LatestIndex returns the most recently built index by built_at_ms.
func (s *SQLStore) LatestIndex(ctx context.Context) (*skills.SkillIndex, error) {
	q := `
		SELECT id, root_id, built_at_ms, built_by_model
		FROM skill_indices
		ORDER BY built_at_ms DESC
		LIMIT 1
	`
	row := s.db.QueryRowContext(ctx, q)
	idx := &skills.SkillIndex{}
	if err := row.Scan(&idx.ID, &idx.RootID, &idx.BuiltAtMs, &idx.BuiltByModel); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("sqlstore: latest index: %w", err)
	}
	nodes, err := s.loadNodes(ctx, idx.RootID)
	if err != nil {
		return nil, err
	}
	idx.Nodes = nodes
	return idx, nil
}

// UpsertNode replaces a single node row, used by hot-reload to refresh
// just the affected subtree without rebuilding the entire index.
func (s *SQLStore) UpsertNode(ctx context.Context, indexID string, node *skills.SkillIndexNode) error {
	if node == nil {
		return fmt.Errorf("sqlstore: nil node")
	}
	// Look up the index to fetch its root_id.
	idxQ := s.placeholders("SELECT root_id FROM skill_indices WHERE id = ?")
	var rootID string
	if err := s.db.QueryRowContext(ctx, idxQ, indexID).Scan(&rootID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("sqlstore: index %q not found", indexID)
		}
		return fmt.Errorf("sqlstore: lookup index: %w", err)
	}

	childrenJSON, _ := json.Marshal(node.Children)
	skillRefsJSON, _ := json.Marshal(node.SkillRefs)
	labelsJSON, _ := json.Marshal(node.Labels)

	var q string
	switch s.dialect {
	case DialectPostgres:
		q = `INSERT INTO skill_index_nodes
			(id, root_id, title, summary, children_json, skill_refs_json,
			 depth, labels_json, content_hash)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8::jsonb, $9)
			ON CONFLICT (id) DO UPDATE SET
				title = EXCLUDED.title,
				summary = EXCLUDED.summary,
				children_json = EXCLUDED.children_json,
				skill_refs_json = EXCLUDED.skill_refs_json,
				depth = EXCLUDED.depth,
				labels_json = EXCLUDED.labels_json,
				content_hash = EXCLUDED.content_hash,
				updated_at = NOW()`
	default:
		q = `INSERT OR REPLACE INTO skill_index_nodes
			(id, root_id, title, summary, children_json, skill_refs_json,
			 depth, labels_json, content_hash, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`
	}

	if _, err := s.db.ExecContext(ctx, q,
		node.ID, rootID, node.Title, node.Summary,
		string(childrenJSON), string(skillRefsJSON),
		node.Depth, string(labelsJSON), node.ContentHash); err != nil {
		return fmt.Errorf("sqlstore: upsert node %q: %w", node.ID, err)
	}
	return nil
}

// DeleteIndex removes both the index metadata row and all its nodes.
func (s *SQLStore) DeleteIndex(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	idxQ := s.placeholders("SELECT root_id FROM skill_indices WHERE id = ?")
	var rootID string
	if err := tx.QueryRowContext(ctx, idxQ, id).Scan(&rootID); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("sqlstore: lookup root: %w", err)
	}

	delNodesQ := s.placeholders("DELETE FROM skill_index_nodes WHERE root_id = ?")
	if _, err := tx.ExecContext(ctx, delNodesQ, rootID); err != nil {
		return fmt.Errorf("sqlstore: delete nodes: %w", err)
	}

	delIdxQ := s.placeholders("DELETE FROM skill_indices WHERE id = ?")
	if _, err := tx.ExecContext(ctx, delIdxQ, id); err != nil {
		return fmt.Errorf("sqlstore: delete index: %w", err)
	}
	return tx.Commit()
}

// Close is a no-op; the SQLStore does not own the underlying *sql.DB.
func (s *SQLStore) Close() error { return nil }

func (s *SQLStore) loadNodes(ctx context.Context, rootID string) ([]*skills.SkillIndexNode, error) {
	q := s.placeholders(`
		SELECT id, title, summary, children_json, skill_refs_json,
		       depth, labels_json, content_hash
		FROM skill_index_nodes
		WHERE root_id = ?
		ORDER BY depth, id
	`)
	rows, err := s.db.QueryContext(ctx, q, rootID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: query nodes: %w", err)
	}
	defer rows.Close()

	var out []*skills.SkillIndexNode
	for rows.Next() {
		var (
			n              skills.SkillIndexNode
			childrenJSON   string
			skillRefsJSON  string
			labelsJSON     string
		)
		if err := rows.Scan(&n.ID, &n.Title, &n.Summary, &childrenJSON,
			&skillRefsJSON, &n.Depth, &labelsJSON, &n.ContentHash); err != nil {
			return nil, fmt.Errorf("sqlstore: scan node: %w", err)
		}
		_ = json.Unmarshal([]byte(childrenJSON), &n.Children)
		_ = json.Unmarshal([]byte(skillRefsJSON), &n.SkillRefs)
		_ = json.Unmarshal([]byte(labelsJSON), &n.Labels)
		out = append(out, &n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: iterate nodes: %w", err)
	}
	return out, nil
}

// placeholders rewrites "?" placeholders to the dialect-specific form.
// SQLite uses ? natively; Postgres uses $1, $2, ...
func (s *SQLStore) placeholders(q string) string {
	if s.dialect != DialectPostgres {
		return q
	}
	out := make([]byte, 0, len(q)+8)
	idx := 1
	for i := 0; i < len(q); i++ {
		if q[i] != '?' {
			out = append(out, q[i])
			continue
		}
		out = append(out, '$')
		out = append(out, fmt.Sprintf("%d", idx)...)
		idx++
	}
	return string(out)
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

func cloneIndex(idx *skills.SkillIndex) *skills.SkillIndex {
	if idx == nil {
		return nil
	}
	out := *idx
	out.Nodes = make([]*skills.SkillIndexNode, 0, len(idx.Nodes))
	for _, n := range idx.Nodes {
		out.Nodes = append(out.Nodes, cloneNode(n))
	}
	return &out
}

func cloneNode(n *skills.SkillIndexNode) *skills.SkillIndexNode {
	if n == nil {
		return nil
	}
	out := *n
	if len(n.Children) > 0 {
		out.Children = append([]string(nil), n.Children...)
	}
	if len(n.SkillRefs) > 0 {
		out.SkillRefs = append([]string(nil), n.SkillRefs...)
	}
	if len(n.Labels) > 0 {
		out.Labels = make(map[string]string, len(n.Labels))
		for k, v := range n.Labels {
			out.Labels[k] = v
		}
	}
	return &out
}
