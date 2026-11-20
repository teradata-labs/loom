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
// Package sessionlease holds the session lease against the real SQLite stores:
// the execution log's lease table, with its row referencing the session, and
// the session store that counts turns.
package sessionlease

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/backend"
	"github.com/teradata-labs/loom/pkg/types"
)

// holdingLLM replies at once, or holds its next call until released.
type holdingLLM struct {
	mu      sync.Mutex
	hold    chan struct{}
	entered chan struct{}
}

func (m *holdingLLM) Name() string  { return "holding" }
func (m *holdingLLM) Model() string { return "holding" }
func (m *holdingLLM) Chat(ctx context.Context, msgs []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	m.mu.Lock()
	hold, entered := m.hold, m.entered
	m.hold, m.entered = nil, nil
	m.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &types.LLMResponse{Content: "reply", StopReason: "end_turn",
		Usage: types.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
}

func (m *holdingLLM) holdNext() (<-chan struct{}, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hold, m.entered = make(chan struct{}), make(chan struct{})
	hold := m.hold
	return m.entered, func() { close(hold) }
}

// One SQLite file, migrated by the storage backend as looms migrates it: the
// session store and the lease store the backend provides, as looms attaches
// them to every agent.
func realStores(t *testing.T) (agent.SessionStorage, *backend.SQLiteBackend) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "loom.db")
	sb, err := backend.NewSQLiteBackend(&loomv1.SQLiteStorageConfig{Path: path}, observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Close() })
	require.NoError(t, sb.Migrate(context.Background()))
	leases, err := backend.SessionLeases(context.Background(), sb)
	require.NoError(t, err, "the migrated backend has no lease table")
	require.NotNil(t, leases)
	return sb.SessionStorage(), sb
}

// Against the real stores: a first turn of a new session takes the lease (the
// lease row references the session's row, which the turn creates first), a
// second turn while one runs is refused, and the session's turns are counted.
func TestTheLeaseHoldsOnTheRealStores(t *testing.T) {
	store, sb := realStores(t)
	ctx := context.Background()
	llmA, llmB := &holdingLLM{}, &holdingLLM{}
	a := agent.NewAgent(nil, llmA, agent.WithName("manager"), agent.WithMemory(agent.NewMemoryWithStore(store)), agent.WithSessionLeases(sb.EventLogStore()))
	b := agent.NewAgent(nil, llmB, agent.WithName("manager"), agent.WithMemory(agent.NewMemoryWithStore(store)), agent.WithSessionLeases(sb.EventLogStore()))

	_, err := a.Chat(ctx, "s1", "one")
	require.NoError(t, err, "the first turn of a new session could not take the lease")

	entered, release := llmA.holdNext()
	done := make(chan error, 1)
	go func() { _, err := a.Chat(ctx, "s1", "two"); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("the turn ended before it reached the model: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never reached the model")
	}
	holder, remaining, err := sb.EventLogStore().SessionLeaseHolder(ctx, "s1")
	require.NoError(t, err)
	require.NotEmpty(t, holder, "the running turn holds no lease")
	require.Greater(t, remaining, time.Minute)
	_, err = b.Chat(ctx, "s1", "three")
	require.ErrorIs(t, err, agent.ErrSessionBusy)
	release()
	require.NoError(t, <-done)

	_, err = b.Chat(ctx, "s1", "four")
	require.NoError(t, err, "the lease was not released")
	counter, ok := store.(agent.TurnCounter)
	require.True(t, ok, "the backend's session store does not count turns")
	turns, err := counter.Turns(ctx, "s1")
	require.NoError(t, err)
	require.EqualValues(t, 3, turns)
}

// A resume for a session that has no row is refused as nothing parked, not
// failed by the lease row's reference to a session that does not exist.
func TestAResumeOfAnUnknownSessionIsRefusedNotFailed(t *testing.T) {
	store, sb := realStores(t)
	a := agent.NewAgent(nil, &holdingLLM{}, agent.WithName("manager"), agent.WithMemory(agent.NewMemoryWithStore(store)),
		agent.WithSessionLeases(sb.EventLogStore()), agent.WithHITLPark(shuttle.NewInMemoryHumanRequestStore(), 0, nil))
	_, err := a.ResumeChat(context.Background(), "never-existed", agent.ParkDecision{RequestID: "bogus", Approved: true}, nil)
	require.ErrorIs(t, err, agent.ErrNothingParked, "a resume of an unknown session failed on the lease instead of being refused")
	ok, err := store.SessionExists(context.Background(), "never-existed")
	require.NoError(t, err)
	require.False(t, ok)
}
