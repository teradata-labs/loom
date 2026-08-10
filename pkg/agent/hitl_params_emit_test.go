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
package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// D-13 acceptance test (CT-1P, emit half): the pending event's HITLRequestInfo
// carries Params and ParamsTruncated mirroring the stored request, for both
// origins. Each case drives a real surface — the shuttle Executor's Execute for
// the hook-held ask, ContactHumanTool.Execute for the question — with a
// recording ProgressCallback installed in ctx, and compares the emitted card
// against the row the origin actually stored.

// AC4 (ask origin): a hook-held call emits a card whose Params is the stored
// request's whole parameter map and whose ParamsTruncated matches — false for an
// under-cap call.
func TestParamsEmit_AskHold_MirrorsStoredParams(t *testing.T) {
	f := newAskEmitFixture(t, "sess-1", "user-1", 2*time.Second, 10*time.Millisecond, "delete_table")

	params := map[string]interface{}{
		"stmt":    "DELETE FROM users WHERE id=42",
		"confirm": true,
		"rows":    42,
	}

	done := make(chan *shuttle.Result, 1)
	go func() {
		res, _ := f.exec.Execute(f.ctx, "delete_table", params)
		done <- res
	}()

	hr := waitForPending(t, f.store)
	ev := f.rec.waitForHITLEvents(t, 1)[0]

	require.NotNil(t, ev.HITLRequest)
	require.Equal(t, hr.ID, ev.HITLRequest.RequestID)
	require.Equal(t, params, ev.HITLRequest.Params, "the emitted card carries the held call's full params")
	require.Equal(t, hr.Params, ev.HITLRequest.Params, "the card mirrors the stored request")
	require.False(t, ev.HITLRequest.ParamsTruncated)
	require.Equal(t, hr.ParamsTruncated, ev.HITLRequest.ParamsTruncated)

	require.NoError(t, f.store.RespondToRequest(context.Background(), hr.ID, "approved", "", "reviewer@example.com", nil))
	<-done
}

// AC4 (ask origin, cut payload): when the bound cuts the held call's params, the
// emitted card carries the same retained subset and the same true flag as the
// stored request — the emit re-derives nothing.
func TestParamsEmit_AskHold_MirrorsTruncatedParams(t *testing.T) {
	f := newAskEmitFixture(t, "sess-1", "user-1", 2*time.Second, 10*time.Millisecond, "bulk_load")

	done := make(chan *shuttle.Result, 1)
	go func() {
		res, _ := f.exec.Execute(f.ctx, "bulk_load", overCapEmitParams())
		done <- res
	}()

	hr := waitForPending(t, f.store)
	ev := f.rec.waitForHITLEvents(t, 1)[0]

	require.NotNil(t, ev.HITLRequest)
	require.Equal(t, hr.ID, ev.HITLRequest.RequestID)
	require.True(t, hr.ParamsTruncated, "the oversized call is stored with the cut flagged")
	require.True(t, ev.HITLRequest.ParamsTruncated, "the card reports the cut too")
	require.Equal(t, hr.Params, ev.HITLRequest.Params, "the card carries the same retained pairs as the stored row")

	require.NoError(t, f.store.RespondToRequest(context.Background(), hr.ID, "approved", "", "reviewer@example.com", nil))
	<-done
}

// AC4 (question origin): a contact_human hold emits a card with no params and
// ParamsTruncated false, matching the stored request — one shape from both
// origins.
func TestParamsEmit_ContactHumanHold_MirrorsEmptyParams(t *testing.T) {
	store := shuttle.NewInMemoryHumanRequestStore()
	rec := &progressRecorder{}
	tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
		Store:        store,
		Notifier:     agent.NewProgressNotifier(),
		Timeout:      2 * time.Second,
		PollInterval: 10 * time.Millisecond,
	})

	ctx := agent.ContextWithProgressCallback(
		session.WithSessionID(context.Background(), "sess-1"),
		rec.callback(),
	)

	done := make(chan *shuttle.Result, 1)
	go func() {
		res, _ := tool.Execute(ctx, map[string]interface{}{"question": "Approve deleting table X?"})
		done <- res
	}()

	hr := waitForPending(t, store)
	ev := rec.waitForHITLEvents(t, 1)[0]

	require.NotNil(t, ev.HITLRequest)
	require.Equal(t, hr.ID, ev.HITLRequest.RequestID)
	require.Empty(t, ev.HITLRequest.Params, "a question has no held call, so the card carries no params")
	require.Empty(t, hr.Params)
	require.False(t, ev.HITLRequest.ParamsTruncated)
	require.Equal(t, hr.ParamsTruncated, ev.HITLRequest.ParamsTruncated)

	require.NoError(t, store.RespondToRequest(context.Background(), hr.ID, "responded", "yes", "reviewer@example.com", nil))
	<-done
}

// overCapEmitParams builds a parameter map whose JSON encoding runs well past
// the 8192-byte bound the ask resolver applies, spread over many equally sized
// pairs so the cut falls between pairs.
func overCapEmitParams() map[string]interface{} {
	params := make(map[string]interface{}, 64)
	for i := 0; i < 64; i++ {
		params[fmt.Sprintf("k%03d", i)] = strings.Repeat("v", 256)
	}
	return params
}
