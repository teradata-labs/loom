// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package eventlog

import (
	"context"
	"testing"
	"time"
)

// fakeEventLogStore is a minimal in-memory EventLogStore used to prove the
// interface shape compiles and is usable by a caller before any real backend
// exists. Later tasks (Postgres/SQLite backends, ResumeSession) get their own
// dedicated tests against real stores; this one only pins the contract.
type fakeEventLogStore struct{}

// Stub implementations to satisfy EventLogStore interface.
func (f *fakeEventLogStore) RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType EventType, toolUseID string, payload []byte) error {
	return nil
}

func (f *fakeEventLogStore) RecordResult(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType EventType, toolUseID string, payload []byte) error {
	return nil
}

func (f *fakeEventLogStore) LoadEvents(ctx context.Context, sessionID string) ([]ExecutionEvent, error) {
	return nil, nil
}

func (f *fakeEventLogStore) NextStepIndex(ctx context.Context, sessionID string) (int, error) {
	return 0, nil
}

func (f *fakeEventLogStore) AcquireSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	return false, nil
}

func (f *fakeEventLogStore) RenewSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	return false, nil
}

func (f *fakeEventLogStore) ReleaseSessionLease(ctx context.Context, sessionID, holder string) error {
	return nil
}

func (f *fakeEventLogStore) PruneResolvedStep(ctx context.Context, sessionID string, stepIndex int) error {
	return nil
}

func (f *fakeEventLogStore) PruneStepsBefore(ctx context.Context, sessionID string, stepIndex int) error {
	return nil
}

var _ EventLogStore = (*fakeEventLogStore)(nil)

// The lease must outlive several renewal intervals (TTL/3) so one slow
// renewal does not lose it, and stay short enough that a crashed holder
// does not block recovery for long.
func TestDefaultSessionLeaseTTL(t *testing.T) {
	if DefaultSessionLeaseTTL < 30*time.Second || DefaultSessionLeaseTTL > 10*time.Minute {
		t.Errorf("DefaultSessionLeaseTTL = %v, want between 30s and 10m", DefaultSessionLeaseTTL)
	}
}

func TestEventType_Constants(t *testing.T) {
	cases := map[EventType]string{
		EventLLMCallIntent:  "llm_call_intent",
		EventLLMCallResult:  "llm_call_result",
		EventToolCallIntent: "tool_call_intent",
		EventToolCallResult: "tool_call_result",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("EventType constant = %q, want %q", got, want)
		}
	}
}
