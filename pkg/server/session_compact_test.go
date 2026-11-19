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

package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/prompts"
)

// compactAgent is an agent over the stores; with a prompt registry it has an
// LLM compressor, so a compaction can fold.
func (r resumeStores) compactAgent(t *testing.T, withPrompts bool) *agent.Agent {
	t.Helper()
	cfg := agent.DefaultConfig()
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	opts := []agent.Option{
		agent.WithConfig(cfg),
		agent.WithName("resume-agent"),
		agent.WithMemory(agent.NewMemoryWithStore(r.sessions)),
	}
	if withPrompts {
		reg := prompts.NewFileRegistry("../../prompts")
		require.NoError(t, reg.Reload(context.Background()))
		opts = append(opts, agent.WithPrompts(reg))
	}
	return agent.NewAgent(nil, &resumeLLM{}, opts...)
}

func chatTurns(t *testing.T, ag *agent.Agent, sessionID string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		_, err := ag.Chat(context.Background(), sessionID, fmt.Sprintf("question %d", i))
		require.NoError(t, err)
	}
}

func TestCompactSessionRPC_FoldsOlderTurnsAndKeepsTheNewest(t *testing.T) {
	stores := newResumeStores(t)
	ag := stores.compactAgent(t, true)
	chatTurns(t, ag, "s-compact", 4)
	srv := stores.server(t, ag)

	resp, err := srv.CompactSession(context.Background(), &loomv1.CompactSessionRequest{
		SessionId: "s-compact", Focus: "the questions asked",
	})

	require.NoError(t, err)
	assert.True(t, resp.GetCompacted())
	assert.True(t, resp.GetTurnsFolded())
	assert.EqualValues(t, agent.DefaultCompactKeepTurns, resp.GetTurnsKept())
	assert.EqualValues(t, 1, resp.GetSummaryVersion())
	assert.Positive(t, resp.GetTokensBefore())
}

func TestCompactSessionRPC_Refusals(t *testing.T) {
	stores := newResumeStores(t)
	ctx := context.Background()
	ag := stores.compactAgent(t, false)
	chatTurns(t, ag, "s-nosummary", 4)
	srv := stores.server(t, ag)

	for name, req := range map[string]*loomv1.CompactSessionRequest{
		"no session id":   {},
		"negative keep":   {SessionId: "s-nosummary", KeepTurns: -1},
		"focus too large": {SessionId: "s-nosummary", Focus: strings.Repeat("x", agent.MaxCompactFocusBytes+1)},
	} {
		_, err := srv.CompactSession(ctx, req)
		assert.Equal(t, codes.InvalidArgument, status.Code(err), name)
	}

	_, err := srv.CompactSession(ctx, &loomv1.CompactSessionRequest{SessionId: "no-such-session"})
	assert.Equal(t, codes.NotFound, status.Code(err))

	// No compressor: older prose turns cannot be summarized, so nothing changes.
	_, err = srv.CompactSession(ctx, &loomv1.CompactSessionRequest{SessionId: "s-nosummary"})
	assert.Equal(t, codes.Unavailable, status.Code(err), "got %v", err)
}

func TestCompactSessionStatus(t *testing.T) {
	for err, want := range map[error]codes.Code{
		agent.ErrSessionBusy:                      codes.Aborted,
		agent.ErrCompactionBusy:                   codes.Aborted,
		&agent.SessionParkedError{SessionID: "s"}: codes.FailedPrecondition,
		agent.ErrSummaryUnavailable:               codes.Unavailable,
		fmt.Errorf("%w: %w", agent.ErrCompactionNotSaved, errors.New("db")): codes.Unavailable,
		fmt.Errorf("session s: %w", agent.ErrSessionNotLoaded):              codes.Unavailable,
		context.Canceled:   codes.Canceled,
		errors.New("boom"): codes.Internal,
	} {
		assert.Equal(t, want, status.Code(compactSessionStatus(err)), err.Error())
	}
}

// F7: the single-agent Server serves the RPC too.
func TestCompactSessionRPC_SingleAgentServer(t *testing.T) {
	stores := newResumeStores(t)
	ag := stores.compactAgent(t, true)
	chatTurns(t, ag, "s-single", 4)
	srv := NewServer(ag, stores.sessions)

	resp, err := srv.CompactSession(context.Background(), &loomv1.CompactSessionRequest{SessionId: "s-single"})
	require.NoError(t, err)
	assert.True(t, resp.GetTurnsFolded())
	assert.EqualValues(t, agent.DefaultCompactKeepTurns, resp.GetTurnsKept())

	_, err = srv.CompactSession(context.Background(), &loomv1.CompactSessionRequest{SessionId: "no-such-session"})
	assert.Equal(t, codes.NotFound, status.Code(err))

	_, err = srv.CompactSession(context.Background(), &loomv1.CompactSessionRequest{})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}
