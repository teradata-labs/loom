// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// overlapLLM tracks how many Chat calls are in flight at once. Every turn
// makes one tool call (a fresh ID per call) and then answers, so a turn spans
// two LLM calls with a tool row in between, which is the window in which two
// unserialized turns on one session interleave.
type overlapLLM struct {
	inFlight atomic.Int64
	peak     atomic.Int64
	ids      atomic.Int64
	hold     time.Duration

	mu       sync.Mutex
	requests [][]llmtypes.Message
}

func (m *overlapLLM) Chat(_ context.Context, msgs []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	n := m.inFlight.Add(1)
	defer m.inFlight.Add(-1)
	for {
		p := m.peak.Load()
		if n <= p || m.peak.CompareAndSwap(p, n) {
			break
		}
	}
	m.mu.Lock()
	m.requests = append(m.requests, append([]llmtypes.Message(nil), msgs...))
	m.mu.Unlock()
	time.Sleep(m.hold)

	// A turn's first call asks for the tool; once the last message is that
	// tool's result, the turn answers.
	if last := msgs[len(msgs)-1]; last.Role == "tool" {
		return &llmtypes.LLMResponse{Content: "done", StopReason: "end_turn"}, nil
	}
	id := m.ids.Add(1)
	return &llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{
			ID:    "call-" + string(rune('a'+id-1)),
			Name:  "noop",
			Input: map[string]interface{}{"v": "x"},
		}},
		StopReason: "tool_use",
	}, nil
}

func (m *overlapLLM) Name() string  { return "overlap" }
func (m *overlapLLM) Model() string { return "overlap-v1" }

type noopTool struct{}

func (noopTool) Name() string        { return "noop" }
func (noopTool) Description() string { return "does nothing" }
func (noopTool) Backend() string     { return "" }
func (noopTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object", Properties: map[string]*shuttle.JSONSchema{"v": {Type: "string"}}}
}
func (noopTool) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}

func newTurnLockAgent(t *testing.T, llm LLMProvider, opts ...Option) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, append([]Option{WithConfig(cfg)}, opts...)...)
	ag.RegisterTool(noopTool{})
	return ag
}

func sessionLockCount(a *Agent) int {
	a.sessionLocksMu.Lock()
	defer a.sessionLocksMu.Unlock()
	return len(a.sessionLocks)
}

// assertToolPairsIntact fails if any request carries a tool_result whose
// tool_use is not the assistant row directly before its run of tool rows, or
// carries one tool_use ID's result twice: the shape Bedrock rejected with
// "each tool_use must have a single result".
func assertToolPairsIntact(t *testing.T, requests [][]llmtypes.Message) {
	t.Helper()
	for ri, msgs := range requests {
		seen := map[string]int{}
		var open map[string]bool
		for _, m := range msgs {
			switch m.Role {
			case "assistant":
				open = map[string]bool{}
				for _, tc := range m.ToolCalls {
					open[tc.ID] = true
				}
			case "tool":
				seen[m.ToolUseID]++
				assert.True(t, open[m.ToolUseID], "request %d: tool_result %s does not follow its tool_use", ri, m.ToolUseID)
			default:
				open = nil
			}
		}
		for id, n := range seen {
			assert.Equal(t, 1, n, "request %d: tool_result %s appears %d times", ri, id, n)
		}
	}
}

// Two turns on ONE session (a user message and a workflow injection, say)
// run one after the other: never two LLM calls at once, both succeed, and no
// request mixes the other turn's tool rows.
func TestSessionTurnLock_SameSessionTurnsDoNotOverlap(t *testing.T) {
	llm := &overlapLLM{hold: 40 * time.Millisecond}
	ag := newTurnLockAgent(t, llm)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = ag.Chat(context.Background(), "s-shared", "turn")
		}(i)
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, int64(1), llm.peak.Load(), "turns on one session must not overlap")
	llm.mu.Lock()
	assertToolPairsIntact(t, llm.requests)
	llm.mu.Unlock()
	assert.Zero(t, sessionLockCount(ag), "no lock entry may outlive its turns")
}

// Different sessions are independent: their turns still run in parallel.
func TestSessionTurnLock_DifferentSessionsOverlap(t *testing.T) {
	llm := &overlapLLM{hold: 150 * time.Millisecond}
	ag := newTurnLockAgent(t, llm)

	var wg sync.WaitGroup
	for _, sid := range []string{"s-one", "s-two"} {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			_, err := ag.Chat(context.Background(), sid, "turn")
			assert.NoError(t, err)
		}(sid)
	}
	wg.Wait()
	assert.Equal(t, int64(2), llm.peak.Load(), "turns on different sessions must run concurrently")
}

// A turn waiting for the slot gives up when its context ends, and leaves no
// lock entry behind.
func TestSessionTurnLock_CancelledWaiterReturnsContextError(t *testing.T) {
	ag := newTurnLockAgent(t, &overlapLLM{})

	release, err := ag.lockSession(context.Background(), "s-busy")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = ag.Chat(ctx, "s-busy", "waiting turn")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
	assert.Equal(t, 1, sessionLockCount(ag), "only the holder's entry remains")

	release()
	release() // idempotent
	assert.Zero(t, sessionLockCount(ag))

	// The session is usable again.
	_, err = ag.Chat(context.Background(), "s-busy", "next turn")
	require.NoError(t, err)
}

// An idle session is taken even when the caller's context is already done:
// the acquire must not fail at random on a free slot.
func TestSessionTurnLock_FreeSlotTakenWithDoneContext(t *testing.T) {
	ag := newTurnLockAgent(t, &overlapLLM{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 50; i++ {
		release, err := ag.lockSession(ctx, "s-idle")
		require.NoError(t, err)
		release()
	}
	assert.Zero(t, sessionLockCount(ag))
}

// A resume and a new turn share the slot: ResumeChat waits for a running turn
// on its session (and gives up with the caller's context).
func TestSessionTurnLock_ResumeWaitsForRunningTurn(t *testing.T) {
	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "park.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })
	ag := newTurnLockAgent(t, &overlapLLM{},
		WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(shuttle.NewInMemoryHumanRequestStore(), 0, NewProgressNotifier()))

	release, err := ag.lockSession(context.Background(), "s-park")
	require.NoError(t, err)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = ag.ResumeChat(ctx, "s-park", ParkDecision{RequestID: "r1", Approved: true}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
}
