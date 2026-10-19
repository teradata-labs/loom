// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package loops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Runs of one conversation are ordered by created_at and then by rowid.
// created_at is whole seconds, so work handed out in the same second ties — and
// the tie has to break on insertion order, not on the id, which is a uuid and
// sorts arbitrarily. A router reading "oldest first" would otherwise be told a
// made-up order for anything spawned in one turn.

// AgentRuns is the routing half of the store: work given to a named agent.
//
// It is a separate interface from Store because a routing run is never leased,
// never checkpointed and never walked — six of Store's methods have no meaning
// for it. Sharing the table without sharing the interface is what keeps one
// answer to "what work is out" while letting each half say only what is true of
// it.
type AgentRuns interface {
	// CreateAgentRun records work handed to an agent. The spec columns stay
	// empty, which is what marks the row as routing's.
	CreateAgentRun(ctx context.Context, run *Run) error
	// AgentRun returns one run, or ErrNotFound.
	AgentRun(ctx context.Context, id string) (*Run, error)
	// AgentRunBySession returns the run whose own log this session is. A verb an
	// agent uses on itself knows only the session it is running in, so this is
	// how a run identifies itself.
	AgentRunBySession(ctx context.Context, sessionID string) (*Run, error)
	// OpenAgentRuns returns a conversation's runs that are still working or
	// waiting, oldest first.
	OpenAgentRuns(ctx context.Context, conversationID string) ([]*Run, error)
	// AllAgentRuns returns every run of a conversation, finished ones included,
	// oldest first.
	//
	// Finished work is still the conversation's history: what was made, what
	// failed, and what a revision would be revising. A list of only what is in
	// flight answers "is it done yet" and nothing else.
	AllAgentRuns(ctx context.Context, conversationID string) ([]*Run, error)
	// ConnectedAgentRuns returns every run of a conversation that was handed the
	// conversation, finished or not. What was said in a conversation stays part
	// of it after the run that said it closed, so this ignores status.
	ConnectedAgentRuns(ctx context.Context, conversationID string) ([]*Run, error)
	// FinishAgentRun closes a run: how it ended, and what it reported.
	FinishAgentRun(ctx context.Context, id string, status Status, report string) error
	// StopOpenAgentRuns ends every open run of a conversation, for when the
	// conversation itself goes away. Returns how many it stopped.
	StopOpenAgentRuns(ctx context.Context, conversationID string) (int, error)
}

// CreateAgentRun records work handed to an agent.
func (s *SQLiteStore) CreateAgentRun(ctx context.Context, run *Run) error {
	if run.AgentID == "" {
		return fmt.Errorf("loops: an agent run needs an agent")
	}
	if run.OwnSession == "" {
		return fmt.Errorf("loops: an agent run needs its own session")
	}
	if run.Kind != Connected && run.Kind != Spawned {
		return fmt.Errorf("loops: kind must be connected or spawned, got %q", run.Kind)
	}
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
		 brief, dir, status, state_path, spent_usd, cap_usd, created_at, updated_at,
		 agent_id, kind, own_session)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.WorkspaceID, run.SessionID, run.ParentRunID, run.ParentState,
		"", "", run.Brief, "", string(run.Status), "[]", run.SpentUSD, run.CapUSD,
		run.CreatedAt.Unix(), run.UpdatedAt.Unix(),
		run.AgentID, string(run.Kind), run.OwnSession)
	return err
}

// AgentRun returns one run, refusing a loop run by the same discriminator that
// creates the split: a caller asking for an agent run must not silently receive
// a spec's.
func (s *SQLiteStore) AgentRun(ctx context.Context, id string) (*Run, error) {
	run, err := scanRun(s.db.QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM loop_runs WHERE id = ? AND agent_id != ''`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return run, err
}

func (s *SQLiteStore) AgentRunBySession(ctx context.Context, sessionID string) (*Run, error) {
	if sessionID == "" {
		return nil, ErrNotFound
	}
	run, err := scanRun(s.db.QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM loop_runs WHERE own_session = ? AND agent_id != ''`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return run, err
}

func (s *SQLiteStore) OpenAgentRuns(ctx context.Context, conversationID string) ([]*Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM loop_runs
		WHERE session_id = ? AND agent_id != '' AND status IN (?,?)
		ORDER BY created_at, rowid`, conversationID, string(Running), string(Waiting))
	if err != nil {
		return nil, err
	}
	return collectRuns(rows)
}

func (s *SQLiteStore) AllAgentRuns(ctx context.Context, conversationID string) ([]*Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM loop_runs
		WHERE session_id = ? AND agent_id != ''
		ORDER BY created_at, rowid`, conversationID)
	if err != nil {
		return nil, err
	}
	return collectRuns(rows)
}

func (s *SQLiteStore) ConnectedAgentRuns(ctx context.Context, conversationID string) ([]*Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM loop_runs
		WHERE session_id = ? AND agent_id != '' AND kind = ?
		ORDER BY created_at, rowid`, conversationID, string(Connected))
	if err != nil {
		return nil, err
	}
	return collectRuns(rows)
}

// FinishAgentRun closes a run. Separate from the loop half's Finish because a
// routing run has no terminal state to record: a loop ends in one of its states
// and its caller routes on which, whereas an agent given work either did it or
// did not.
func (s *SQLiteStore) FinishAgentRun(ctx context.Context, id string, status Status, report string) error {
	now := time.Now().Unix()
	return s.exec(ctx, id, `UPDATE loop_runs SET status = ?, report = ?,
		suspend_reason = '', updated_at = ?, ended_at = ? WHERE id = ? AND agent_id != ''`,
		string(status), report, now, now, id)
}

// StopOpenAgentRuns closes out a conversation's work. Without it a cleared
// conversation leaves runs that no conversation can reach, so nothing will ever
// finish them.
func (s *SQLiteStore) StopOpenAgentRuns(ctx context.Context, conversationID string) (int, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `UPDATE loop_runs
		SET status = ?, suspend_reason = '', lease_owner = '',
		    updated_at = ?, ended_at = ?
		WHERE session_id = ? AND agent_id != '' AND status IN (?,?)`,
		string(Stopped), now, now, conversationID, string(Running), string(Waiting))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// isDuplicateColumn recognises a column that is already there, which is what an
// ALTER on an existing database reports. Matched on the message because the
// driver gives no code for it.
func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}
