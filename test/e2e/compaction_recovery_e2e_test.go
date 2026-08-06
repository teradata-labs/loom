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

//go:build integration

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestE2E_DurableHistory_RetrievableAfterContextWindowReset asserts D-D ac-3:
// the full pre-compaction message history is retrievable from the durable
// session store.
//
// Compaction rewrites the agent's in-memory SegmentedMemory (L1/L2/swap) but
// never touches the durable SessionStorage, which retains every message
// verbatim via PersistMessage. This surface (storage integration, no real LLM)
// cannot trigger a real LLM compaction, so it drives the same working-window
// clear that compaction performs: WeaveRequest.reset_context resets L1/L2/swap
// before processing. After that clear, the verbatim text of every earlier turn
// must still be retrievable through GetConversationHistory, proving the durable
// store — not the working window — is the recovery source.
func TestE2E_DurableHistory_RetrievableAfterContextWindowReset(t *testing.T) {
	client := loomClient(t)
	waitForHealthy(t, client)

	userID := uniqueTestID("durable-recover")
	ctx := withUserID(context.Background(), userID)

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("durable-recover-session"),
	})
	require.NoError(t, err, "CreateSession should succeed")
	sessionID := createResp.GetId()
	require.NotEmpty(t, sessionID)
	cleanupSession(t, client, userID, sessionID)

	// Build a multi-turn history. Each turn carries a unique marker so its
	// persistence can be verified individually. These are the turns that a
	// later compaction would evict from the working window.
	const preResetTurns = 4
	markers := make([]string, 0, preResetTurns)
	for i := 0; i < preResetTurns; i++ {
		marker := fmt.Sprintf("durable-marker-%d-%s", i, uniqueTestID("turn"))
		markers = append(markers, marker)

		weaveCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
			SessionId: sessionID,
			Query:     fmt.Sprintf("Remember this token: %s. Reply with just OK.", marker),
		})
		cancel()
		require.NoError(t, err, "Weave turn %d should succeed", i)
	}

	// Clear the working context window — the same L1/L2/swap reset compaction
	// performs — while sending one more turn. The durable store is untouched.
	resetMarker := fmt.Sprintf("post-reset-marker-%s", uniqueTestID("turn"))
	weaveCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	_, err = client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId:    sessionID,
		Query:        fmt.Sprintf("New topic token: %s. Reply with just OK.", resetMarker),
		ResetContext: true,
	})
	cancel()
	require.NoError(t, err, "Weave with reset_context should succeed")

	// The durable session store must still return the complete verbatim
	// history: every pre-reset turn plus the post-reset turn.
	histResp, err := client.GetConversationHistory(ctx, &loomv1.GetConversationHistoryRequest{
		SessionId: sessionID,
		Limit:     1000,
	})
	require.NoError(t, err, "GetConversationHistory should succeed")

	var allContent strings.Builder
	for _, m := range histResp.GetMessages() {
		allContent.WriteString(m.GetContent())
		allContent.WriteString("\n")
	}
	transcript := allContent.String()

	// Each pre-reset marker must survive the working-window clear — this is the
	// recoverability property: the full pre-compaction history is durable.
	for i, marker := range markers {
		assert.Contains(t, transcript, marker,
			"pre-reset turn %d (%s) must remain retrievable from the durable store after the context window was cleared",
			i, marker)
	}

	// The post-reset turn is also durably persisted.
	assert.Contains(t, transcript, resetMarker,
		"post-reset turn must be persisted to the durable store")

	// Durable store holds at least a user+assistant message per turn.
	assert.GreaterOrEqual(t, len(histResp.GetMessages()), (preResetTurns+1)*2,
		"durable history should contain at least user+assistant messages for all %d turns", preResetTurns+1)

	t.Logf("Durable store returned %d messages (total_count=%d); all %d pre-reset markers retrievable after context window reset",
		len(histResp.GetMessages()), histResp.GetTotalCount(), preResetTurns)
}
