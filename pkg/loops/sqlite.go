// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package loops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLiteStore is the CLI's store. Its tables sit beside sessions, workspaces
// and messages in one file, so a run, the workspace it belongs to and the
// conversation that spawned it are one transaction away from each other.
type SQLiteStore struct{ db *sql.DB }

// NewSQLiteStore creates the tables if they are absent and returns the store.
func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error) {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS loop_runs (
			id                  TEXT PRIMARY KEY,
			workspace_id        TEXT NOT NULL,
			session_id          TEXT NOT NULL DEFAULT '',
			parent_run_id       TEXT NOT NULL DEFAULT '',
			parent_state        TEXT NOT NULL DEFAULT '',
			spec_name           TEXT NOT NULL,
			spec_json           TEXT NOT NULL,
			brief               TEXT NOT NULL DEFAULT '',
			dir                 TEXT NOT NULL,
			status              TEXT NOT NULL,
			state_path          TEXT NOT NULL DEFAULT '[]',
			suspend_reason      TEXT NOT NULL DEFAULT '',
			terminal            TEXT NOT NULL DEFAULT '',
			report              TEXT NOT NULL DEFAULT '',
			spent_usd           REAL NOT NULL DEFAULT 0,
			cap_usd             REAL NOT NULL DEFAULT 0,
			granted_cost_usd    REAL NOT NULL DEFAULT 0,
			granted_transitions INTEGER NOT NULL DEFAULT 0,
			lease_owner         TEXT NOT NULL DEFAULT '',
			-- Milliseconds, not seconds: this is a liveness clock, and a
			-- second is too coarse to tell "just proved it" from "a while ago".
			lease_heartbeat     INTEGER NOT NULL DEFAULT 0,
			created_at          INTEGER NOT NULL,
			updated_at          INTEGER NOT NULL,
			ended_at            INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS lr_workspace_status ON loop_runs (workspace_id, status)`,
		`CREATE INDEX IF NOT EXISTS lr_parent ON loop_runs (parent_run_id)`,
		`CREATE INDEX IF NOT EXISTS lr_lease ON loop_runs (status, lease_heartbeat)`,
		`CREATE TABLE IF NOT EXISTS loop_steps (
			run_id      TEXT NOT NULL,
			seq         INTEGER NOT NULL,
			state       TEXT NOT NULL,
			agent_id    TEXT NOT NULL DEFAULT '',
			tool        TEXT NOT NULL DEFAULT '',
			signal      TEXT NOT NULL DEFAULT '',
			visit       INTEGER NOT NULL DEFAULT 1,
			cost_usd    REAL NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			at          INTEGER NOT NULL,
			PRIMARY KEY (run_id, seq)
		)`,
	}
	// Routing's columns are added rather than declared, so a database written by
	// an earlier build gains them on open. SQLite has no IF NOT EXISTS for a
	// column, so a re-add is recognised by its error and ignored.
	stmts = append(stmts,
		`ALTER TABLE loop_runs ADD COLUMN agent_id    TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE loop_runs ADD COLUMN kind        TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE loop_runs ADD COLUMN own_session TEXT NOT NULL DEFAULT ''`,
		// What the run has worked out so far — its cursors, its children's
		// outcomes — as JSON. Written on every transition beside state_path,
		// because re-entering a state needs both.
		`ALTER TABLE loop_runs ADD COLUMN vars        TEXT NOT NULL DEFAULT '{}'`,
		// What a park left behind, on the run's row. The table that once held
		// a history of these is dropped: a run has one position, and this is
		// only what a park adds to it.
		`ALTER TABLE loop_runs ADD COLUMN park        BLOB NOT NULL DEFAULT X''`,
		// A manager's word for its parked run, kept until the person says the
		// run goes on with it. Answering the park — any resume — spends it.
		`ALTER TABLE loop_runs ADD COLUMN word        TEXT NOT NULL DEFAULT ''`,
		// The run's own record of its words and its stop: the message its
		// next turn reads, and the word the step it stands at stopped on.
		`ALTER TABLE loop_runs ADD COLUMN message     TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE loop_runs ADD COLUMN stop_word   TEXT NOT NULL DEFAULT ''`,
		// What the person sees the run called, named from its task.
		`ALTER TABLE loop_runs ADD COLUMN title       TEXT NOT NULL DEFAULT ''`,
		`DROP TABLE IF EXISTS loop_checkpoints`,
		// Rows an earlier reconcile mistook for abandoned loops and parked:
		// agent runs, and children walked inside their parent. Neither was
		// anyone's to park; both are put back where they stood.
		`UPDATE loop_runs SET status = 'running', suspend_reason = ''
			WHERE (agent_id != '' OR parent_run_id != '')
			AND status = 'waiting' AND suspend_reason = 'process_abandoned'`,
		`CREATE INDEX IF NOT EXISTS lr_agent_open ON loop_runs (session_id, agent_id, status)`,
	)
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			if isDuplicateColumn(err) {
				continue
			}
			return nil, fmt.Errorf("loops schema: %w", err)
		}
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Create(ctx context.Context, run *Run) error {
	now := time.Now()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	run.UpdatedAt = now
	if run.Status == "" {
		run.Status = Running
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO loop_runs
		(id, workspace_id, session_id, parent_run_id, parent_state, spec_name, spec_json,
		 brief, dir, status, state_path, spent_usd, cap_usd, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.WorkspaceID, run.SessionID, run.ParentRunID, run.ParentState,
		run.SpecName, "", run.Brief, run.Dir, string(run.Status),
		encodePath(run.StatePath), run.SpentUSD, run.CapUSD,
		run.CreatedAt.Unix(), run.UpdatedAt.Unix())
	return err
}

const runColumns = `id, workspace_id, session_id, parent_run_id, parent_state, spec_name,
	brief, dir, status, state_path, suspend_reason, terminal, report,
	spent_usd, cap_usd, granted_cost_usd, granted_transitions,
	lease_owner, lease_heartbeat, created_at, updated_at, ended_at,
	agent_id, kind, own_session, vars, park, word, message, stop_word, title`

func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var (
		r                           Run
		status, path                string
		reason                      string
		heartbeat, created, updated int64
		ended                       int64
		kind                        string
		vars                        string
		park                        []byte
	)
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.SessionID, &r.ParentRunID, &r.ParentState,
		&r.SpecName, &r.Brief, &r.Dir, &status, &path, &reason,
		&r.Terminal, &r.Report, &r.SpentUSD, &r.CapUSD, &r.GrantedCostUSD,
		&r.GrantedTransitions, &r.LeaseOwner, &heartbeat, &created, &updated, &ended,
		&r.AgentID, &kind, &r.OwnSession, &vars, &park, &r.Word, &r.Message, &r.StopWord, &r.Title)
	if err != nil {
		return nil, err
	}
	r.Kind = Kind(kind)
	if len(park) > 0 {
		r.ParkRecord = park
	}
	if vars != "" && vars != "{}" {
		_ = json.Unmarshal([]byte(vars), &r.Vars)
	}
	r.Status = Status(status)
	r.SuspendReason = SuspendReason(reason)
	r.StatePath = decodePath(path)
	r.LeaseHeartbeat = unixMilli(heartbeat)
	r.CreatedAt = unix(created)
	r.UpdatedAt = unix(updated)
	r.EndedAt = unix(ended)
	return &r, nil
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (*Run, error) {
	run, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM loop_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return run, err
}

func (s *SQLiteStore) List(ctx context.Context, workspaceID string) ([]*Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runColumns+` FROM loop_runs WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	return collectRuns(rows)
}

func (s *SQLiteStore) Children(ctx context.Context, parentRunID string) ([]*Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runColumns+` FROM loop_runs WHERE parent_run_id = ? ORDER BY created_at`, parentRunID)
	if err != nil {
		return nil, err
	}
	return collectRuns(rows)
}

func collectRuns(rows *sql.Rows) ([]*Run, error) {
	defer func() { _ = rows.Close() }()
	var out []*Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Progress(ctx context.Context, id string, statePath []string, spentUSD float64) error {
	return s.exec(ctx, id, `UPDATE loop_runs SET state_path = ?, spent_usd = ?, updated_at = ? WHERE id = ?`,
		encodePath(statePath), spentUSD, time.Now().Unix(), id)
}

func (s *SQLiteStore) Moved(ctx context.Context, id string, statePath []string, vars map[string]string, addUSD float64) error {
	encoded, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	return s.exec(ctx, id, `UPDATE loop_runs SET state_path = ?, vars = ?, spent_usd = spent_usd + ?, updated_at = ? WHERE id = ?`,
		encodePath(statePath), string(encoded), addUSD, time.Now().Unix(), id)
}

func (s *SQLiteStore) Park(ctx context.Context, id string, reason SuspendReason) error {
	// What a run is stopped for decides what it is: stopped on time is not the
	// same condition as stopped on a person, and everything that asks "does
	// this need me" reads the status rather than the reason.
	status := Waiting
	if reason == ReasonTimer {
		status = TimerWait
	}
	return s.exec(ctx, id, `UPDATE loop_runs SET status = ?, suspend_reason = ?,
		updated_at = ? WHERE id = ?`, string(status), string(reason), time.Now().Unix(), id)
}

func (s *SQLiteStore) Stop(ctx context.Context, id string) error {
	return s.exec(ctx, id, `UPDATE loop_runs SET status = ?, lease_owner = '', updated_at = ?
		WHERE id = ?`, string(Stopped), time.Now().Unix(), id)
}

// Discard walks the delegation tree down from the run and deletes every row
// in it, steps first. Ids are collected before anything is deleted so a
// partial failure leaves rows, never orphans without a parent to find them by.
func (s *SQLiteStore) Discard(ctx context.Context, id string) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	ids := []string{id}
	for i := 0; i < len(ids); i++ {
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM loop_runs WHERE parent_run_id = ?`, ids[i])
		if err != nil {
			return err
		}
		for rows.Next() {
			var child string
			if err := rows.Scan(&child); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, child)
		}
		_ = rows.Close()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, rid := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM loop_steps WHERE run_id = ?`, rid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM loop_runs WHERE id = ?`, rid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) Finish(ctx context.Context, id string, status Status, terminal, report string) error {
	now := time.Now().Unix()
	return s.exec(ctx, id, `UPDATE loop_runs SET status = ?, terminal = ?, report = ?,
		suspend_reason = '', lease_owner = '', state_path = '[]', updated_at = ?, ended_at = ?
		WHERE id = ?`, string(status), terminal, report, now, now, id)
}

func (s *SQLiteStore) Reopen(ctx context.Context, id string, statePath []string, vars map[string]string) error {
	encoded, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	return s.exec(ctx, id, `UPDATE loop_runs SET status = ?, terminal = '', report = '', suspend_reason = '',
		stop_word = '', state_path = ?, vars = ?, ended_at = 0, updated_at = ? WHERE id = ?`,
		string(Running), encodePath(statePath), string(encoded), time.Now().Unix(), id)
}

func (s *SQLiteStore) Entered(ctx context.Context, id string, statePath []string, message string) error {
	return s.exec(ctx, id, `UPDATE loop_runs SET state_path = ?, message = ?, stop_word = '', updated_at = ? WHERE id = ?`,
		encodePath(statePath), message, time.Now().Unix(), id)
}

func (s *SQLiteStore) StoppedOn(ctx context.Context, id string, statePath []string, word string, vars map[string]string) error {
	encoded, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	return s.exec(ctx, id, `UPDATE loop_runs SET state_path = ?, stop_word = ?, vars = ?, updated_at = ? WHERE id = ?`,
		encodePath(statePath), word, string(encoded), time.Now().Unix(), id)
}

func (s *SQLiteStore) Answer(ctx context.Context, id, message string, vars map[string]string) error {
	encoded, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	return s.exec(ctx, id, `UPDATE loop_runs SET message = ?, vars = ?, updated_at = ? WHERE id = ?`,
		message, string(encoded), time.Now().Unix(), id)
}

func (s *SQLiteStore) HoldWord(ctx context.Context, id, word string) error {
	return s.exec(ctx, id, `UPDATE loop_runs SET word = ?, updated_at = ? WHERE id = ?`,
		word, time.Now().Unix(), id)
}

func (s *SQLiteStore) Name(ctx context.Context, id, title string) error {
	return s.exec(ctx, id, `UPDATE loop_runs SET title = ? WHERE id = ?`, title, id)
}

func (s *SQLiteStore) Grant(ctx context.Context, id string, costUSD float64, transitions int) error {
	return s.exec(ctx, id, `UPDATE loop_runs SET granted_cost_usd = granted_cost_usd + ?,
		granted_transitions = granted_transitions + ?, updated_at = ? WHERE id = ?`,
		costUSD, transitions, time.Now().Unix(), id)
}

// Acquire is the one place two processes could collide, so the test is done in
// the UPDATE: it succeeds only if the run is unheld or its holder has gone
// quiet for longer than staleAfter.
func (s *SQLiteStore) Acquire(ctx context.Context, id, owner string, staleAfter time.Duration) error {
	now := time.Now()
	cutoff := now.Add(-staleAfter).UnixMilli()
	res, err := s.db.ExecContext(ctx, `UPDATE loop_runs
		SET status = ?, suspend_reason = '', word = '', lease_owner = ?, lease_heartbeat = ?, updated_at = ?
		WHERE id = ? AND (lease_owner = '' OR lease_owner = ? OR lease_heartbeat < ?)`,
		string(Running), owner, now.UnixMilli(), now.Unix(), id, owner, cutoff)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Get(ctx, id); err != nil {
			return err
		}
		return ErrLeaseHeld
	}
	return nil
}

func (s *SQLiteStore) Adopt(ctx context.Context, id, owner string, staleAfter time.Duration) error {
	now := time.Now()
	cutoff := now.Add(-staleAfter).UnixMilli()
	res, err := s.db.ExecContext(ctx, `UPDATE loop_runs
		SET lease_owner = ?, lease_heartbeat = ?, updated_at = ?
		WHERE id = ? AND (lease_owner = '' OR lease_owner = ? OR lease_heartbeat < ?)`,
		owner, now.UnixMilli(), now.Unix(), id, owner, cutoff)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Get(ctx, id); err != nil {
			return err
		}
		return ErrLeaseHeld
	}
	return nil
}

func (s *SQLiteStore) Heartbeat(ctx context.Context, id, owner string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE loop_runs SET lease_heartbeat = ? WHERE id = ? AND lease_owner = ?`,
		time.Now().UnixMilli(), id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLeaseHeld
	}
	return nil
}

func (s *SQLiteStore) Release(ctx context.Context, id, owner string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE loop_runs SET lease_owner = '', updated_at = ? WHERE id = ? AND lease_owner = ?`,
		time.Now().Unix(), id, owner)
	return err
}

func (s *SQLiteStore) Abandoned(ctx context.Context, staleAfter time.Duration) ([]*Run, error) {
	// Two kinds of row never hold a lease and so must not be read as
	// abandoned: agent runs, which are routing's and belong to one process
	// for their whole life; and delegated children, which are walked inside
	// their parent's process and are resumed through the parent.
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM loop_runs
		WHERE status = ? AND agent_id = '' AND parent_run_id = '' AND lease_heartbeat < ?
		ORDER BY created_at`,
		string(Running), time.Now().Add(-staleAfter).UnixMilli())
	if err != nil {
		return nil, err
	}
	return collectRuns(rows)
}

func (s *SQLiteStore) AppendStep(ctx context.Context, step *Step) error {
	if step.At.IsZero() {
		step.At = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var next int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM loop_steps WHERE run_id = ?`, step.RunID).Scan(&next); err != nil {
		return err
	}
	step.Seq = next
	if _, err := tx.ExecContext(ctx, `INSERT INTO loop_steps
		(run_id, seq, state, agent_id, tool, signal, visit, cost_usd, duration_ms, at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		step.RunID, step.Seq, step.State, step.AgentID, step.Tool, step.Signal,
		step.Visit, step.CostUSD, step.DurationMS, step.At.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) Walk(ctx context.Context, runID string) ([]*Step, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, seq, state, agent_id, tool, signal,
		visit, cost_usd, duration_ms, at FROM loop_steps WHERE run_id = ? ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Step
	for rows.Next() {
		var st Step
		var at int64
		if err := rows.Scan(&st.RunID, &st.Seq, &st.State, &st.AgentID, &st.Tool,
			&st.Signal, &st.Visit, &st.CostUSD, &st.DurationMS, &at); err != nil {
			return nil, err
		}
		st.At = unix(at)
		out = append(out, &st)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SavePark(ctx context.Context, runID string, record []byte) error {
	if record == nil {
		record = []byte{}
	}
	return s.exec(ctx, runID, `UPDATE loop_runs SET park = ?, updated_at = ? WHERE id = ?`,
		record, time.Now().Unix(), runID)
}

// exec runs an update that must match a run, turning "matched nothing" into
// ErrNotFound rather than silent success.
func (s *SQLiteStore) exec(ctx context.Context, id, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func encodePath(path []string) string {
	if len(path) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(path)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func decodePath(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		// A path written by an older shape: keep it readable rather than lost.
		return strings.Split(s, "/")
	}
	return out
}

func unixMilli(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func unix(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}
