// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the same registry over Postgres. Its schema arrives by
// migration 000030; this type assumes it. Row-level security, membership and
// the users foreign key are cloud's layer and are not here.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const pgWsColumns = `id, owner_user_id, name, kind, root_path, is_personal, status, config_json, created_at, updated_at`

func (s *PostgresStore) Create(ctx context.Context, ws *Workspace) error {
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
	_, err = s.pool.Exec(ctx, `
		INSERT INTO workspaces (id, owner_user_id, name, kind, root_path, is_personal, status, config_json, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)`,
		ws.ID, ws.OwnerUserID, ws.Name, string(ws.Kind), ws.RootPath, ws.IsPersonal,
		string(ws.Status), cfg, ws.CreatedAt, ws.UpdatedAt)
	if err != nil {
		if isPgUnique(err) {
			return ErrExists
		}
		return fmt.Errorf("create workspace: %w", err)
	}
	return nil
}

func (s *PostgresStore) Get(ctx context.Context, id string) (*Workspace, error) {
	return scanPgWorkspace(s.pool.QueryRow(ctx, `SELECT `+pgWsColumns+` FROM workspaces WHERE id = $1 AND status <> 'deleted'`, id))
}

func (s *PostgresStore) GetByName(ctx context.Context, owner, name string) (*Workspace, error) {
	return scanPgWorkspace(s.pool.QueryRow(ctx, `SELECT `+pgWsColumns+` FROM workspaces WHERE owner_user_id = $1 AND name = $2 AND status <> 'deleted'`, owner, name))
}

func (s *PostgresStore) Personal(ctx context.Context, owner string) (*Workspace, error) {
	return scanPgWorkspace(s.pool.QueryRow(ctx, `SELECT `+pgWsColumns+` FROM workspaces WHERE owner_user_id = $1 AND is_personal AND status <> 'deleted'`, owner))
}

func (s *PostgresStore) List(ctx context.Context, owner string) ([]*Workspace, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+pgWsColumns+` FROM workspaces WHERE owner_user_id = $1 AND status <> 'deleted' ORDER BY updated_at DESC`, owner)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	var out []*Workspace
	for rows.Next() {
		ws, err := scanPgWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

func (s *PostgresStore) SetStatus(ctx context.Context, id string, status Status) error {
	tag, err := s.pool.Exec(ctx, `UPDATE workspaces SET status = $1, updated_at = NOW() WHERE id = $2`, string(status), id)
	if err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	return pgAffectedOrNotFound(tag)
}

func (s *PostgresStore) UpdateConfig(ctx context.Context, id string, config map[string]any) error {
	cfg, err := marshalConfig(config)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE workspaces SET config_json = $1::jsonb, updated_at = NOW() WHERE id = $2`, cfg, id)
	if err != nil {
		return fmt.Errorf("update config: %w", err)
	}
	return pgAffectedOrNotFound(tag)
}

func (s *PostgresStore) AddRepo(ctx context.Context, r *Repo) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workspace_repos (workspace_id, name, url, ref, worktree_path) VALUES ($1, $2, $3, $4, $5)`,
		r.WorkspaceID, r.Name, r.URL, r.Ref, r.WorktreePath)
	if err != nil {
		if isPgUnique(err) {
			return ErrExists
		}
		return fmt.Errorf("add repo: %w", err)
	}
	_, _ = s.pool.Exec(ctx, `UPDATE workspaces SET updated_at = NOW() WHERE id = $1`, r.WorkspaceID)
	return nil
}

func (s *PostgresStore) RemoveRepo(ctx context.Context, workspaceID, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_repos WHERE workspace_id = $1 AND name = $2`, workspaceID, name)
	if err != nil {
		return fmt.Errorf("remove repo: %w", err)
	}
	return pgAffectedOrNotFound(tag)
}

func (s *PostgresStore) Repos(ctx context.Context, workspaceID string) ([]*Repo, error) {
	rows, err := s.pool.Query(ctx, `SELECT workspace_id, name, url, ref, worktree_path FROM workspace_repos WHERE workspace_id = $1 ORDER BY name`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer rows.Close()
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

func (s *PostgresStore) BindSession(ctx context.Context, sessionID, workspaceID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE sessions SET workspace_id = $1 WHERE id = $2 AND workspace_id = ''`, workspaceID, sessionID)
	if err != nil {
		return fmt.Errorf("bind session: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var existing string
	err = s.pool.QueryRow(ctx, `SELECT workspace_id FROM sessions WHERE id = $1`, sessionID).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionNotFound
	}
	if err != nil {
		return err
	}
	return ErrAlreadyBound
}

func (s *PostgresStore) SessionWorkspace(ctx context.Context, sessionID string) (string, error) {
	var ws string
	err := s.pool.QueryRow(ctx, `SELECT workspace_id FROM sessions WHERE id = $1`, sessionID).Scan(&ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrSessionNotFound
	}
	return ws, err
}

func (s *PostgresStore) Sessions(ctx context.Context, workspaceID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM sessions WHERE workspace_id = $1 ORDER BY updated_at DESC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
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

func (s *PostgresStore) AdoptUnboundSessions(ctx context.Context, owner, workspaceID string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE sessions SET workspace_id = $1 WHERE user_id = $2 AND workspace_id = ''`, workspaceID, owner)
	if err != nil {
		return 0, fmt.Errorf("adopt sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------

func scanPgWorkspace(r pgx.Row) (*Workspace, error) {
	var (
		ws           Workspace
		kind, status string
		cfg          []byte
	)
	err := r.Scan(&ws.ID, &ws.OwnerUserID, &ws.Name, &kind, &ws.RootPath, &ws.IsPersonal, &status, &cfg, &ws.CreatedAt, &ws.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan workspace: %w", err)
	}
	ws.Kind = Kind(kind)
	ws.Status = Status(status)
	if err := json.Unmarshal(cfg, &ws.Config); err != nil {
		ws.Config = map[string]any{}
	}
	return &ws, nil
}

func pgAffectedOrNotFound(tag pgconn.CommandTag) error {
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func isPgUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
