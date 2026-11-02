// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SQLiteStore keeps workspaces in the same SQLite file as the sessions.
//
// Its tables are created here, inline, the way the session store creates its
// own: the pkg/storage/sqlite migrator only runs under the storage backend,
// which the CLI does not use, so a numbered migration there would never reach
// the database the CLI actually opens.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore prepares the schema on an open handle. The handle is shared
// with the session store — the sessions table this package binds into is
// created by that store, which must therefore open first.
func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error) {
	s := &SQLiteStore{db: db}
	if err := s.initSchema(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) initSchema(ctx context.Context) error {
	const schema = `
	CREATE TABLE IF NOT EXISTS workspaces (
		id             TEXT    PRIMARY KEY,
		owner_user_id  TEXT    NOT NULL DEFAULT 'default-user',
		name           TEXT    NOT NULL,
		kind           TEXT    NOT NULL,
		root_path      TEXT    NOT NULL,
		is_personal    INTEGER NOT NULL DEFAULT 0,
		status         TEXT    NOT NULL DEFAULT 'active',
		config_json    TEXT    NOT NULL DEFAULT '{}',
		created_at     INTEGER NOT NULL,
		updated_at     INTEGER NOT NULL,
		UNIQUE (owner_user_id, name)
	);
	CREATE UNIQUE INDEX IF NOT EXISTS ws_personal
		ON workspaces (owner_user_id) WHERE is_personal = 1;

	CREATE TABLE IF NOT EXISTS workspace_repos (
		workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		name           TEXT NOT NULL,
		url            TEXT NOT NULL,
		ref            TEXT NOT NULL,
		worktree_path  TEXT NOT NULL,
		PRIMARY KEY (workspace_id, name)
	);
	CREATE INDEX IF NOT EXISTS idx_sessions_workspace ON sessions (workspace_id);
	`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("workspace schema: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Create(ctx context.Context, ws *Workspace) error {
	if !ws.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidKind, ws.Kind)
	}
	if ws.Status == "" {
		ws.Status = StatusActive
	}
	now := time.Now()
	if ws.CreatedAt.IsZero() {
		ws.CreatedAt = now
	}
	ws.UpdatedAt = now
	cfg, err := marshalConfig(ws.Config)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workspaces (id, owner_user_id, name, kind, root_path, is_personal, status, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ws.ID, ws.OwnerUserID, ws.Name, string(ws.Kind), ws.RootPath, boolInt(ws.IsPersonal),
		string(ws.Status), cfg, ws.CreatedAt.Unix(), ws.UpdatedAt.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrExists
		}
		return fmt.Errorf("create workspace: %w", err)
	}
	return nil
}

const wsColumns = `id, owner_user_id, name, kind, root_path, is_personal, status, config_json, created_at, updated_at`

func (s *SQLiteStore) Get(ctx context.Context, owner, id string) (*Workspace, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+wsColumns+` FROM workspaces WHERE id = ? AND owner_user_id = ? AND status != 'deleted'`, id, owner)
	return scanWorkspace(row)
}

func (s *SQLiteStore) GetByName(ctx context.Context, owner, name string) (*Workspace, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+wsColumns+` FROM workspaces WHERE owner_user_id = ? AND name = ? AND status != 'deleted'`, owner, name)
	return scanWorkspace(row)
}

func (s *SQLiteStore) Personal(ctx context.Context, owner string) (*Workspace, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+wsColumns+` FROM workspaces WHERE owner_user_id = ? AND is_personal = 1 AND status != 'deleted'`, owner)
	return scanWorkspace(row)
}

func (s *SQLiteStore) List(ctx context.Context, owner string) ([]*Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+wsColumns+` FROM workspaces WHERE owner_user_id = ? AND status != 'deleted' ORDER BY updated_at DESC`, owner)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Workspace
	for rows.Next() {
		ws, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SetStatus(ctx context.Context, owner, id string, status Status) error {
	res, err := s.db.ExecContext(ctx, `UPDATE workspaces SET status = ?, updated_at = ? WHERE id = ? AND owner_user_id = ?`, string(status), time.Now().Unix(), id, owner)
	if err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	return affectedOrNotFound(res)
}

func (s *SQLiteStore) UpdateConfig(ctx context.Context, owner, id string, config map[string]any) error {
	cfg, err := marshalConfig(config)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE workspaces SET config_json = ?, updated_at = ? WHERE id = ? AND owner_user_id = ?`, cfg, time.Now().Unix(), id, owner)
	if err != nil {
		return fmt.Errorf("update config: %w", err)
	}
	return affectedOrNotFound(res)
}

func (s *SQLiteStore) AddRepo(ctx context.Context, owner string, r *Repo) error {
	if err := s.ownWorkspace(ctx, owner, r.WorkspaceID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workspace_repos (workspace_id, name, url, ref, worktree_path) VALUES (?, ?, ?, ?, ?)`,
		r.WorkspaceID, r.Name, r.URL, r.Ref, r.WorktreePath)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrExists
		}
		return fmt.Errorf("add repo: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE workspaces SET updated_at = ? WHERE id = ?`, time.Now().Unix(), r.WorkspaceID)
	return nil
}

func (s *SQLiteStore) RemoveRepo(ctx context.Context, owner, workspaceID, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspace_repos WHERE workspace_id = ? AND name = ? AND workspace_id IN (SELECT id FROM workspaces WHERE owner_user_id = ?)`, workspaceID, name, owner)
	if err != nil {
		return fmt.Errorf("remove repo: %w", err)
	}
	return affectedOrNotFound(res)
}

func (s *SQLiteStore) Repos(ctx context.Context, owner, workspaceID string) ([]*Repo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.workspace_id, r.name, r.url, r.ref, r.worktree_path FROM workspace_repos r JOIN workspaces w ON w.id = r.workspace_id WHERE r.workspace_id = ? AND w.owner_user_id = ? ORDER BY r.name`, workspaceID, owner)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Repo
	for rows.Next() {
		r := &Repo{}
		if err := rows.Scan(&r.WorkspaceID, &r.Name, &r.URL, &r.Ref, &r.WorktreePath); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BindSession is set-once by construction: the UPDATE only matches an unbound
// row, so a second bind changes nothing and is reported as such.
func (s *SQLiteStore) BindSession(ctx context.Context, owner, sessionID, workspaceID string) error {
	if err := s.ownWorkspace(ctx, owner, workspaceID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET workspace_id = ? WHERE id = ? AND user_id = ? AND (workspace_id = '' OR workspace_id IS NULL)`, workspaceID, sessionID, owner)
	if err != nil {
		return fmt.Errorf("bind session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return nil
	}
	var existing sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT workspace_id FROM sessions WHERE id = ? AND user_id = ?`, sessionID, owner).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionNotFound
	}
	if err != nil {
		return err
	}
	return ErrAlreadyBound
}

func (s *SQLiteStore) SessionWorkspace(ctx context.Context, owner, sessionID string) (string, error) {
	var ws sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM sessions WHERE id = ? AND user_id = ?`, sessionID, owner).Scan(&ws)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSessionNotFound
	}
	if err != nil {
		return "", err
	}
	return ws.String, nil
}

func (s *SQLiteStore) Sessions(ctx context.Context, owner, workspaceID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM sessions WHERE workspace_id = ? AND user_id = ? ORDER BY updated_at DESC`, workspaceID, owner)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
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

func (s *SQLiteStore) AdoptUnboundSessions(ctx context.Context, owner, workspaceID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET workspace_id = ? WHERE user_id = ? AND (workspace_id = '' OR workspace_id IS NULL)`, workspaceID, owner)
	if err != nil {
		return 0, fmt.Errorf("adopt sessions: %w", err)
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

// ownWorkspace is ErrNotFound unless the workspace is owner's: nothing is
// added to, or bound into, another owner's workspace.
func (s *SQLiteStore) ownWorkspace(ctx context.Context, owner, id string) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id = ? AND owner_user_id = ? AND status != 'deleted'`, id, owner).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func scanWorkspace(r rowScanner) (*Workspace, error) {
	var (
		ws           Workspace
		kind, status string
		personal     int
		cfg          string
		created, upd int64
	)
	err := r.Scan(&ws.ID, &ws.OwnerUserID, &ws.Name, &kind, &ws.RootPath, &personal, &status, &cfg, &created, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan workspace: %w", err)
	}
	ws.Kind = Kind(kind)
	ws.Status = Status(status)
	ws.IsPersonal = personal == 1
	ws.CreatedAt = time.Unix(created, 0)
	ws.UpdatedAt = time.Unix(upd, 0)
	if err := json.Unmarshal([]byte(cfg), &ws.Config); err != nil {
		ws.Config = map[string]any{}
	}
	return &ws, nil
}

func marshalConfig(c map[string]any) (string, error) {
	if c == nil {
		return "{}", nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	return string(b), nil
}

func affectedOrNotFound(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "UNIQUE constraint failed") || contains(msg, "constraint failed: UNIQUE") || contains(msg, "duplicate key")
}

func contains(s, sub string) bool {
	return len(sub) <= len(s) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
