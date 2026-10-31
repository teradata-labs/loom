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

	pg "github.com/teradata-labs/loom/pkg/storage/postgres"
)

// PostgresStore is the same registry over Postgres. Its schema arrives by
// migrations 000030 and 000031. A workspace is its owner's: every query runs
// under the owner's identity, so row-level security applies, and every query
// that names a workspace or a session by id also says whose it must be — an
// owner who names another's reads it as missing and changes nothing.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const pgWsColumns = `id, owner_user_id, name, kind, root_path, is_personal, status, config_json, created_at, updated_at`

// as runs fn in one transaction under owner's identity.
func (s *PostgresStore) as(ctx context.Context, owner string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return pg.ExecInTx(pg.ContextWithUserID(ctx, owner), s.pool, fn)
}

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
	err = pg.ExecInTx(pg.ContextWithUserID(ctx, ws.OwnerUserID), s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		INSERT INTO workspaces (id, owner_user_id, name, kind, root_path, is_personal, status, config_json, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)`,
			ws.ID, ws.OwnerUserID, ws.Name, string(ws.Kind), ws.RootPath, ws.IsPersonal,
			string(ws.Status), cfg, ws.CreatedAt, ws.UpdatedAt)
		return err
	})
	if err != nil {
		if isPgUnique(err) {
			return ErrExists
		}
		return fmt.Errorf("create workspace: %w", err)
	}
	return nil
}

func (s *PostgresStore) one(ctx context.Context, owner, where string, args ...any) (*Workspace, error) {
	var ws *Workspace
	err := pg.ExecInTx(pg.ContextWithUserID(ctx, owner), s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		ws, err = scanPgWorkspace(tx.QueryRow(ctx, `SELECT `+pgWsColumns+` FROM workspaces WHERE `+where+` AND status <> 'deleted'`, args...))
		return err
	})
	return ws, err
}

func (s *PostgresStore) Get(ctx context.Context, owner, id string) (*Workspace, error) {
	return s.one(ctx, owner, `id = $1 AND owner_user_id = $2`, id, owner)
}

func (s *PostgresStore) GetByName(ctx context.Context, owner, name string) (*Workspace, error) {
	return s.one(ctx, owner, `owner_user_id = $1 AND name = $2`, owner, name)
}

func (s *PostgresStore) Personal(ctx context.Context, owner string) (*Workspace, error) {
	return s.one(ctx, owner, `owner_user_id = $1 AND is_personal`, owner)
}

func (s *PostgresStore) List(ctx context.Context, owner string) ([]*Workspace, error) {
	var out []*Workspace
	err := pg.ExecInTx(pg.ContextWithUserID(ctx, owner), s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pgWsColumns+` FROM workspaces WHERE owner_user_id = $1 AND status <> 'deleted' ORDER BY updated_at DESC`, owner)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			ws, err := scanPgWorkspace(rows)
			if err != nil {
				return err
			}
			out = append(out, ws)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) SetStatus(ctx context.Context, owner, id string, status Status) error {
	return s.exec(ctx, owner, "set status", `UPDATE workspaces SET status = $1, updated_at = NOW() WHERE id = $2 AND owner_user_id = $3`, string(status), id, owner)
}

func (s *PostgresStore) UpdateConfig(ctx context.Context, owner, id string, config map[string]any) error {
	cfg, err := marshalConfig(config)
	if err != nil {
		return err
	}
	return s.exec(ctx, owner, "update config", `UPDATE workspaces SET config_json = $1::jsonb, updated_at = NOW() WHERE id = $2 AND owner_user_id = $3`, cfg, id, owner)
}

// exec runs one owner-scoped write and reports a write that touched nothing as
// not found: another owner's row and a missing one read the same.
func (s *PostgresStore) exec(ctx context.Context, owner, what, query string, args ...any) error {
	var tag pgconn.CommandTag
	err := s.as(ctx, owner, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		tag, err = tx.Exec(ctx, query, args...)
		return err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return pgAffectedOrNotFound(tag)
}

// ownWorkspace is the caller's own workspace by id, inside tx.
const ownWorkspace = `SELECT 1 FROM workspaces WHERE id = $1 AND owner_user_id = $2 AND status <> 'deleted'`

func (s *PostgresStore) AddRepo(ctx context.Context, owner string, r *Repo) error {
	err := s.as(ctx, owner, func(ctx context.Context, tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, ownWorkspace, r.WorkspaceID, owner).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.Exec(ctx, `
		INSERT INTO workspace_repos (workspace_id, name, url, ref, worktree_path) VALUES ($1, $2, $3, $4, $5)`,
			r.WorkspaceID, r.Name, r.URL, r.Ref, r.WorktreePath); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE workspaces SET updated_at = NOW() WHERE id = $1`, r.WorkspaceID)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		if isPgUnique(err) {
			return ErrExists
		}
		return fmt.Errorf("add repo: %w", err)
	}
	return nil
}

func (s *PostgresStore) RemoveRepo(ctx context.Context, owner, workspaceID, name string) error {
	return s.exec(ctx, owner, "remove repo", `
		DELETE FROM workspace_repos WHERE workspace_id = $1 AND name = $2
		AND workspace_id IN (SELECT id FROM workspaces WHERE owner_user_id = $3)`, workspaceID, name, owner)
}

func (s *PostgresStore) Repos(ctx context.Context, owner, workspaceID string) ([]*Repo, error) {
	var out []*Repo
	err := s.as(ctx, owner, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
		SELECT r.workspace_id, r.name, r.url, r.ref, r.worktree_path
		FROM workspace_repos r JOIN workspaces w ON w.id = r.workspace_id
		WHERE r.workspace_id = $1 AND w.owner_user_id = $2 ORDER BY r.name`, workspaceID, owner)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r := &Repo{}
			if err := rows.Scan(&r.WorkspaceID, &r.Name, &r.URL, &r.Ref, &r.WorktreePath); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) BindSession(ctx context.Context, owner, sessionID, workspaceID string) error {
	return s.as(ctx, owner, func(ctx context.Context, tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, ownWorkspace, workspaceID, owner).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE sessions SET workspace_id = $1 WHERE id = $2 AND user_id = $3 AND workspace_id = ''`, workspaceID, sessionID, owner)
		if err != nil {
			return fmt.Errorf("bind session: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var existing string
		err = tx.QueryRow(ctx, `SELECT workspace_id FROM sessions WHERE id = $1 AND user_id = $2`, sessionID, owner).Scan(&existing)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSessionNotFound
		}
		if err != nil {
			return err
		}
		return ErrAlreadyBound
	})
}

func (s *PostgresStore) SessionWorkspace(ctx context.Context, owner, sessionID string) (string, error) {
	var ws string
	err := s.as(ctx, owner, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT workspace_id FROM sessions WHERE id = $1 AND user_id = $2`, sessionID, owner).Scan(&ws)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrSessionNotFound
	}
	return ws, err
}

func (s *PostgresStore) Sessions(ctx context.Context, owner, workspaceID string) ([]string, error) {
	var ids []string
	err := s.as(ctx, owner, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM sessions WHERE workspace_id = $1 AND user_id = $2 ORDER BY updated_at DESC`, workspaceID, owner)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return ids, nil
}

func (s *PostgresStore) AdoptUnboundSessions(ctx context.Context, owner, workspaceID string) (int64, error) {
	var n int64
	err := pg.ExecInTx(pg.ContextWithUserID(ctx, owner), s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE sessions SET workspace_id = $1 WHERE user_id = $2 AND workspace_id = ''`, workspaceID, owner)
		n = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("adopt sessions: %w", err)
	}
	return n, nil
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
