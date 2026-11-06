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
package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

type failOnceLLM struct {
	calls int
}

func (m *failOnceLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.calls++
	if m.calls == 1 {
		return nil, errors.New("LLM unavailable")
	}
	return &llmtypes.LLMResponse{Content: "ok"}, nil
}

func (m *failOnceLLM) Name() string  { return "fail-once" }
func (m *failOnceLLM) Model() string { return "fail-once" }

func countUserRows(t *testing.T, ag *Agent, sessionID string) int {
	t.Helper()
	sess, ok := ag.GetSession(sessionID)
	require.True(t, ok)
	n := 0
	for _, m := range sess.GetMessages() {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// TestDiscardIncompleteTurn_HealedRetryKeepsOneUserRow is the F1 repro from
// loom#535: Chat persists the user turn before the loop, so retrying Chat
// without a discard leaves two identical user rows.
func TestDiscardIncompleteTurn_HealedRetryKeepsOneUserRow(t *testing.T) {
	llm := &failOnceLLM{}
	cfg := DefaultConfig()
	cfg.Retry.Enabled = false // expose a single provider failure to Chat
	ag := NewAgent(nil, llm, WithConfig(cfg))
	ctx := context.Background()
	const sessionID = "discard-turn-session"

	_, err := ag.Chat(ctx, sessionID, "please handle this")
	require.Error(t, err)
	assert.Equal(t, 1, countUserRows(t, ag, sessionID), "failed Chat leaves the user row")

	require.NoError(t, ag.DiscardIncompleteTurn(ctx, sessionID))
	assert.Equal(t, 0, countUserRows(t, ag, sessionID), "discard removes the incomplete turn")

	resp, err := ag.Chat(ctx, sessionID, "please handle this")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, 1, countUserRows(t, ag, sessionID), "healed retry must keep exactly one user row")
}

// TestDiscardIncompleteTurn_LeavesCompletedTurnAlone checks a Chat that never
// appended (nothing incomplete) is a no-op against prior history.
func TestDiscardIncompleteTurn_LeavesCompletedTurnAlone(t *testing.T) {
	llm := &failOnceLLM{calls: 1} // already "healed": first Chat succeeds
	cfg := DefaultConfig()
	cfg.Retry.Enabled = false
	ag := NewAgent(nil, llm, WithConfig(cfg))
	ctx := context.Background()
	const sessionID = "discard-complete-session"

	_, err := ag.Chat(ctx, sessionID, "first")
	require.NoError(t, err)
	assert.Equal(t, 1, countUserRows(t, ag, sessionID))

	require.NoError(t, ag.DiscardIncompleteTurn(ctx, sessionID))
	assert.Equal(t, 1, countUserRows(t, ag, sessionID), "completed turns must not be discarded")
}
