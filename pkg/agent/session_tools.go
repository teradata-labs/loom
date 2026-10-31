// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"database/sql"
	"time"
)

// ToolRecord is one tool call as it was recorded: what ran, what it cost in time
// and bytes, and whether it failed.
//
// Distinct from ToolExecution, which is the live shape carrying the result object
// itself. What is worth reading back is smaller than what was written: the size of
// a result rather than the result, because the size is what filled the context and
// the content is already in the transcript.
type ToolRecord struct {
	Tool string
	// Input is the call's arguments as recorded, used to recognise a call repeated
	// verbatim — the signature of an agent stuck on one command.
	Input string
	// Error is empty when the call succeeded.
	Error string
	// Duration is how long the call took.
	Duration time.Duration
	// ResultBytes is how much came back. Tool results are what fill a context
	// window, so this is the number that explains a cost curve.
	ResultBytes int
	// Admission is the audit verdict, empty when the call was not governed.
	Admission string
	At        time.Time
}

// Failed reports whether the call errored.
func (r ToolRecord) Failed() bool { return r.Error != "" }

// ToolExecutions reads a session's tool calls, oldest first.
func (s *SessionStore) ToolExecutions(ctx context.Context, sessionID string) ([]ToolRecord, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.tool_executions")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	// Inputs hold commands and paths: another user's session reads as missing.
	if owned, err := s.ownsSession(ctx, sessionID); err != nil {
		return nil, err
	} else if !owned {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT tool_name, COALESCE(input_json, ''), COALESCE(error, ''),
		       COALESCE(execution_time_ms, 0), LENGTH(COALESCE(result_json, '')),
		       COALESCE(admission_decision, ''), timestamp
		FROM tool_executions WHERE session_id = ?
		ORDER BY timestamp, id`, sessionID)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ToolRecord
	for rows.Next() {
		var rec ToolRecord
		var ms int64
		var at int64
		if err := rows.Scan(&rec.Tool, &rec.Input, &rec.Error, &ms,
			&rec.ResultBytes, &rec.Admission, &at); err != nil {
			return nil, err
		}
		rec.Duration = time.Duration(ms) * time.Millisecond
		rec.At = time.Unix(at, 0)
		out = append(out, rec)
	}
	return out, rows.Err()
}

var _ = sql.ErrNoRows
