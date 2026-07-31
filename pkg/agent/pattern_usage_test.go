// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/types"

	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/metaagent/learning"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/patterns"
)

func TestRecordPatternLoaded_DedupAndDrain(t *testing.T) {
	t.Parallel()

	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})

	ag.RecordPatternLoaded("s1", "alpha")
	ag.RecordPatternLoaded("s1", "alpha") // dedup
	ag.RecordPatternLoaded("s1", "beta")
	ag.RecordPatternLoaded("s2", "gamma")
	ag.RecordPatternLoaded("", "ignored") // no session
	ag.RecordPatternLoaded("s3", "")      // no name

	assert.ElementsMatch(t, []string{"alpha", "beta"}, ag.takeLoadedPatterns("s1"))
	assert.Empty(t, ag.takeLoadedPatterns("s1"), "drain-on-read: second take is empty")
	assert.Equal(t, []string{"gamma"}, ag.takeLoadedPatterns("s2"))
	assert.Empty(t, ag.takeLoadedPatterns("s3"))
}

func TestRecordPatternLoaded_Concurrent(t *testing.T) {
	t.Parallel()

	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			session := "s1"
			if n%2 == 0 {
				session = "s2"
			}
			ag.RecordPatternLoaded(session, "pattern")
			if n%4 == 0 {
				ag.takeLoadedPatterns(session)
			}
		}(i)
	}
	wg.Wait()
	// No assertion beyond absence of races (run under -race) and that
	// remaining buckets drain cleanly.
	ag.takeLoadedPatterns("s1")
	ag.takeLoadedPatterns("s2")
}

func TestClassifyChatError(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", classifyChatError(nil))
	assert.Equal(t, "timeout", classifyChatError(context.DeadlineExceeded))
	assert.Equal(t, "canceled", classifyChatError(context.Canceled))
	assert.Equal(t, "timeout", classifyChatError(errors.Join(errors.New("wrap"), context.DeadlineExceeded)))
	assert.Equal(t, "llm_error", classifyChatError(errors.New("boom")))
}

// fakeRecorder captures RecordPatternLoaded calls.
type fakeRecorder struct {
	mu    sync.Mutex
	calls [][2]string
}

func (f *fakeRecorder) RecordPatternLoaded(sessionID, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [2]string{sessionID, name})
}

func TestLoadPatternTool_Recording(t *testing.T) {
	t.Parallel()

	dir := writeLoadPatternFixtures(t)
	orch := patterns.NewOrchestrator(patterns.NewLibrary(nil, dir))

	ctxWithSession := context.WithValue(context.Background(), "session_id", "sess-9") //nolint:staticcheck // matches the loop's tool-context key

	t.Run("records canonical name with session identity", func(t *testing.T) {
		t.Parallel()
		rec := &fakeRecorder{}
		tool := NewLoadPatternTool(orch, rec)
		res, err := tool.Execute(ctxWithSession, map[string]interface{}{"reference": samplePatternRef})
		require.NoError(t, err)
		require.True(t, res.Success)
		require.Len(t, rec.calls, 1)
		assert.Equal(t, [2]string{"sess-9", samplePatternRef}, rec.calls[0])
	})

	t.Run("no session identity skips recording, result unaffected", func(t *testing.T) {
		t.Parallel()
		rec := &fakeRecorder{}
		tool := NewLoadPatternTool(orch, rec)
		res, err := tool.Execute(context.Background(), map[string]interface{}{"reference": samplePatternRef})
		require.NoError(t, err)
		assert.True(t, res.Success)
		assert.Empty(t, rec.calls)
	})

	t.Run("nil recorder is safe", func(t *testing.T) {
		t.Parallel()
		tool := NewLoadPatternTool(orch, nil)
		res, err := tool.Execute(ctxWithSession, map[string]interface{}{"reference": samplePatternRef})
		require.NoError(t, err)
		assert.True(t, res.Success)
	})

	t.Run("unknown reference records nothing", func(t *testing.T) {
		t.Parallel()
		rec := &fakeRecorder{}
		tool := NewLoadPatternTool(orch, rec)
		res, err := tool.Execute(ctxWithSession, map[string]interface{}{"reference": "no-such-pattern"})
		require.NoError(t, err)
		assert.False(t, res.Success)
		assert.Empty(t, rec.calls)
	})
}

// newTrackerForTest builds a real PatternEffectivenessTracker on an in-memory
// SQLite database with the self-improvement schema applied and a long flush
// interval; Stop() performs the deterministic final flush tests assert on.
func newTrackerForTest(t *testing.T) (*learning.PatternEffectivenessTracker, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, learning.InitSelfImprovementSchema(context.Background(), db, observability.NewNoOpTracer()))
	bus := communication.NewMessageBus(nil, nil, nil, nil)
	t.Cleanup(func() { _ = bus.Close() })
	tracker := learning.NewPatternEffectivenessTracker(db, observability.NewNoOpTracer(), bus, time.Hour, time.Hour)
	require.NoError(t, tracker.Start(context.Background()))
	return tracker, db
}

func queryEffectivenessRows(t *testing.T, db *sql.DB) (rows int, patternName string, successes, failures int) {
	t.Helper()
	r := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(pattern_name),''), COALESCE(SUM(success_count),0), COALESCE(SUM(failure_count),0) FROM pattern_effectiveness`)
	require.NoError(t, r.Scan(&rows, &patternName, &successes, &failures))
	return rows, patternName, successes, failures
}

func TestPatternEffectiveness_EndToEnd(t *testing.T) {
	t.Parallel()

	dir := writeLoadPatternFixtures(t)
	tracker, db := newTrackerForTest(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "load_pattern", Input: map[string]interface{}{"reference": samplePatternRef}}}},
		{content: "done, using the pattern"},
	}}

	cfg := DefaultConfig()
	cfg.Name = "pattern-agent"
	cfg.PatternsDir = dir
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.SetPatternTracker(tracker)

	resp, err := ag.Chat(context.Background(), "sess-e2e", "use the sample pattern")
	require.NoError(t, err)
	require.Equal(t, "done, using the pattern", resp.Content)

	require.NoError(t, tracker.Stop(context.Background()))

	rows, name, successes, failures := queryEffectivenessRows(t, db)
	assert.Equal(t, 1, rows, "one effectiveness row after flush")
	assert.Equal(t, samplePatternRef, name)
	assert.Equal(t, 1, successes)
	assert.Equal(t, 0, failures)
}

func TestPatternEffectiveness_TrackingDisabled(t *testing.T) {
	t.Parallel()

	dir := writeLoadPatternFixtures(t)
	tracker, db := newTrackerForTest(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "load_pattern", Input: map[string]interface{}{"reference": samplePatternRef}}}},
		{content: "done"},
	}}

	cfg := DefaultConfig()
	cfg.PatternsDir = dir
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.PatternConfig.EnableTracking = false

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.SetPatternTracker(tracker)

	_, err := ag.Chat(context.Background(), "sess-off", "use the sample pattern")
	require.NoError(t, err)
	require.NoError(t, tracker.Stop(context.Background()))

	rows, _, _, _ := queryEffectivenessRows(t, db)
	assert.Zero(t, rows, "EnableTracking=false records nothing")
}

func TestRecordPatternEffectiveness_OutcomeFolding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		chatErr       error
		meta          map[string]interface{}
		wantSuccesses int
		wantFailures  int
	}{
		{name: "plain success", meta: map[string]interface{}{}, wantSuccesses: 1},
		{name: "chat error is failure", chatErr: errors.New("boom"), wantFailures: 1},
		{name: "output verification failure folds in", meta: map[string]interface{}{"output_verification": "failed"}, wantFailures: 1},
		{name: "cost limit trip folds in", meta: map[string]interface{}{"cost_limit_hit": true}, wantFailures: 1},
		{name: "passed verification stays success", meta: map[string]interface{}{"output_verification": "passed"}, wantSuccesses: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := writeLoadPatternFixtures(t)
			tracker, db := newTrackerForTest(t)

			cfg := DefaultConfig()
			cfg.Name = "fold-agent"
			cfg.PatternsDir = dir
			cfg.PatternConfig = DefaultPatternConfig()
			cfg.PatternConfig.UseLLMClassifier = false

			ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg))
			ag.SetPatternTracker(tracker)

			ag.RecordPatternLoaded("s1", samplePatternRef)
			recorded := ag.recordPatternEffectiveness(context.Background(), "s1", tt.chatErr, tt.meta, 0.01, time.Second)
			require.Len(t, recorded, 1)

			require.NoError(t, tracker.Stop(context.Background()))
			_, _, successes, failures := queryEffectivenessRows(t, db)
			assert.Equal(t, tt.wantSuccesses, successes)
			assert.Equal(t, tt.wantFailures, failures)
		})
	}
}
