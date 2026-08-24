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
// Tests for the client MRTR driver (2026-07-28, SEP-2322).
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

const inputRequiredElicit = `{
  "resultType": "input_required",
  "inputRequests": {
    "github_login": {
      "method": "elicitation/create",
      "params": {"message": "Please provide your GitHub username",
                 "requestedSchema": {"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}
    }
  },
  "requestState": "sealed-state-round-1"
}`

const completeResult = `{"content":[{"type":"text","text":"done"}],"resultType":"complete"}`

// acceptHandler answers every elicitation with an accept.
func acceptHandler(t *testing.T, calls *int) InputHandler {
	return func(ctx context.Context, reqs protocol.InputRequests) (protocol.InputResponses, error) {
		*calls++
		out := protocol.InputResponses{}
		for key, req := range reqs {
			require.Equal(t, "elicitation/create", req.Method)
			out[key] = json.RawMessage(`{"action":"accept","content":{"name":"octocat"}}`)
		}
		return out, nil
	}
}

func mrtrClient(t *testing.T, ft *scriptedTransport, cfg Config) *Client {
	t.Helper()
	if ft.discoverResult == nil {
		ft.discoverResult = statelessDiscoverResult()
	}
	if ft.tools == nil {
		ft.tools = []protocol.Tool{simpleTool("do_thing", nil)}
	}
	c := connectClient(t, ft, cfg)
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	return c
}

func TestMRTRSingleRoundCompletes(t *testing.T) {
	ft := newScriptedTransport()
	ft.callResults = []json.RawMessage{json.RawMessage(inputRequiredElicit), json.RawMessage(completeResult)}
	calls := 0
	c := mrtrClient(t, ft, Config{MRTR: MRTRConfig{Handler: acceptHandler(t, &calls)}})

	result, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{"arg": "v"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, calls, "handler invoked exactly once")

	ft.mu.Lock()
	defer ft.mu.Unlock()
	require.Len(t, ft.callParams, 2)

	// The retry carries original arguments + inputResponses + echoed state.
	var retry map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(ft.callParams[1], &retry))
	assert.Contains(t, retry, "arguments")
	var state string
	require.NoError(t, json.Unmarshal(retry["requestState"], &state))
	assert.Equal(t, "sealed-state-round-1", state)
	var responses protocol.InputResponses
	require.NoError(t, json.Unmarshal(retry["inputResponses"], &responses))
	assert.Contains(t, responses, "github_login")

	// Same idempotency key on both rounds; the initial request has no
	// inputResponses.
	var k1, k2 string
	require.NoError(t, json.Unmarshal(metaOf(t, ft.callParams[0])[protocol.MetaIdempotencyKey], &k1))
	require.NoError(t, json.Unmarshal(metaOf(t, ft.callParams[1])[protocol.MetaIdempotencyKey], &k2))
	assert.Equal(t, k1, k2)
	var initial map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(ft.callParams[0], &initial))
	assert.NotContains(t, initial, "inputResponses")
}

func TestMRTRStateOnlyRetriesWithoutHandlerCall(t *testing.T) {
	// No inputRequests: the client may retry immediately, echoing the state,
	// without invoking the handler.
	ft := newScriptedTransport()
	ft.callResults = []json.RawMessage{
		json.RawMessage(`{"resultType":"input_required","requestState":"state-only"}`),
		json.RawMessage(completeResult),
	}
	calls := 0
	c := mrtrClient(t, ft, Config{MRTR: MRTRConfig{Handler: acceptHandler(t, &calls)}})

	_, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.NoError(t, err)
	assert.Zero(t, calls, "handler must not run when there are no inputRequests")

	ft.mu.Lock()
	defer ft.mu.Unlock()
	require.Len(t, ft.callParams, 2)
	var retry map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(ft.callParams[1], &retry))
	var state string
	require.NoError(t, json.Unmarshal(retry["requestState"], &state))
	assert.Equal(t, "state-only", state)
	assert.NotContains(t, retry, "inputResponses")
}

func TestMRTRRoundBudgetExhaustion(t *testing.T) {
	ft := newScriptedTransport()
	ft.callResult = json.RawMessage(inputRequiredElicit) // input_required forever
	calls := 0
	c := mrtrClient(t, ft, Config{MRTR: MRTRConfig{Handler: acceptHandler(t, &calls), MaxRounds: 3}})

	_, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.Error(t, err)
	var exceeded *MRTRRoundsExceededError
	require.True(t, errors.As(err, &exceeded), "want MRTRRoundsExceededError, got %T: %v", err, err)
	assert.Equal(t, "tools/call", exceeded.Method)
	assert.Equal(t, 3, exceeded.Rounds)

	ft.mu.Lock()
	defer ft.mu.Unlock()
	assert.Len(t, ft.callParams, 4, "1 initial + 3 retries")
}

func TestMRTRHandlerErrorAborts(t *testing.T) {
	ft := newScriptedTransport()
	ft.callResult = json.RawMessage(inputRequiredElicit)
	c := mrtrClient(t, ft, Config{MRTR: MRTRConfig{
		Handler: func(ctx context.Context, reqs protocol.InputRequests) (protocol.InputResponses, error) {
			return nil, fmt.Errorf("user declined at the gate")
		},
	}})

	_, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user declined at the gate")

	ft.mu.Lock()
	defer ft.mu.Unlock()
	assert.Len(t, ft.callParams, 1, "no retry after handler abort")
}

func TestMRTRHandlerAdvertisesElicitation(t *testing.T) {
	ft := newScriptedTransport()
	ft.callResult = json.RawMessage(completeResult)
	calls := 0
	c := mrtrClient(t, ft, Config{MRTR: MRTRConfig{Handler: acceptHandler(t, &calls)}})

	_, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.NoError(t, err)

	ft.mu.Lock()
	defer ft.mu.Unlock()
	meta := metaOf(t, ft.callParams[0])
	var caps protocol.ClientCapabilities
	require.NoError(t, json.Unmarshal(meta[protocol.MetaClientCapabilities], &caps))
	assert.NotNil(t, caps.Elicitation, "handler-equipped client must advertise elicitation")
}

func TestNoHandlerDoesNotAdvertiseElicitation(t *testing.T) {
	ft := newScriptedTransport()
	ft.callResult = json.RawMessage(completeResult)
	c := mrtrClient(t, ft, Config{})

	_, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.NoError(t, err)

	ft.mu.Lock()
	defer ft.mu.Unlock()
	var caps protocol.ClientCapabilities
	require.NoError(t, json.Unmarshal(metaOf(t, ft.callParams[0])[protocol.MetaClientCapabilities], &caps))
	assert.Nil(t, caps.Elicitation)
}

// TestMRTREmptyRequestStateEchoedVerbatim (review finding 11, PR #327): a
// present-but-empty requestState is schema-valid ("at least one of
// inputRequests or requestState") and the client MUST echo the exact value —
// treating "" as absent would drop a field the server chose to send.
func TestMRTREmptyRequestStateEchoedVerbatim(t *testing.T) {
	ft := newScriptedTransport()
	ft.callResults = []json.RawMessage{
		json.RawMessage(`{"resultType":"input_required","requestState":""}`),
		json.RawMessage(completeResult),
	}
	calls := 0
	c := mrtrClient(t, ft, Config{MRTR: MRTRConfig{Handler: acceptHandler(t, &calls)}})

	_, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.NoError(t, err)

	ft.mu.Lock()
	defer ft.mu.Unlock()
	require.Len(t, ft.callParams, 2, "empty requestState alone must still drive a retry")
	var retry map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(ft.callParams[1], &retry))
	stateRaw, present := retry["requestState"]
	require.True(t, present, "present-but-empty requestState must be echoed, not dropped")
	var state string
	require.NoError(t, json.Unmarshal(stateRaw, &state))
	assert.Equal(t, "", state)
}
