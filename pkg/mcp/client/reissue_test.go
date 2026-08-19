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
// Tests for broken-stream re-issue with idempotency keys (migration §7.5).
package client

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

// metaOf extracts the _meta object from recorded raw params.
func metaOf(t *testing.T, params json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(params, &obj))
	var meta map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(obj["_meta"], &meta))
	return meta
}

func TestStatelessCallToolStampsIdempotencyKey(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverResult = statelessDiscoverResult()
	ft.tools = []protocol.Tool{simpleTool("do_thing", nil)}
	ft.callResult = json.RawMessage(`{"content":[],"resultType":"complete"}`)
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	_, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.NoError(t, err)

	ft.mu.Lock()
	defer ft.mu.Unlock()
	require.Len(t, ft.callParams, 1)
	meta := metaOf(t, ft.callParams[0])
	var key string
	require.NoError(t, json.Unmarshal(meta[protocol.MetaIdempotencyKey], &key))
	assert.NotEmpty(t, key)
}

func TestStreamLostReissuesOnceWithSameKey(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverResult = statelessDiscoverResult()
	ft.tools = []protocol.Tool{simpleTool("do_thing", nil)}
	ft.callStreamLost = 1
	ft.callResult = json.RawMessage(`{"content":[{"type":"text","text":"ok"}],"resultType":"complete"}`)
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	result, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.NoError(t, err, "one stream loss must be recovered by re-issue")
	require.NotNil(t, result)

	ft.mu.Lock()
	defer ft.mu.Unlock()
	require.Len(t, ft.callParams, 2, "exactly one re-issue")

	var key1, key2 string
	require.NoError(t, json.Unmarshal(metaOf(t, ft.callParams[0])[protocol.MetaIdempotencyKey], &key1))
	require.NoError(t, json.Unmarshal(metaOf(t, ft.callParams[1])[protocol.MetaIdempotencyKey], &key2))
	assert.Equal(t, key1, key2, "re-issue must reuse the idempotency key")
	assert.NotEmpty(t, key1)
}

func TestStreamLostTwiceFailsAfterSingleReissue(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverResult = statelessDiscoverResult()
	ft.tools = []protocol.Tool{simpleTool("do_thing", nil)}
	ft.callStreamLost = 2
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	_, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.Error(t, err)
	var rpcErr *protocol.Error
	require.True(t, errors.As(err, &rpcErr))
	assert.Equal(t, transport.CodeStreamLost, rpcErr.Code)

	ft.mu.Lock()
	defer ft.mu.Unlock()
	assert.Len(t, ft.callParams, 2, "re-issue budget is one")
}

func TestLegacyModeDoesNotStampKeyOrReissue(t *testing.T) {
	ft := newScriptedTransport()
	ft.tools = []protocol.Tool{simpleTool("do_thing", nil)}
	ft.callStreamLost = 1
	c := connectClient(t, ft, Config{ProtocolVersion: "legacy"})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	_, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.Error(t, err, "legacy mode surfaces the loss instead of silently re-executing")

	ft.mu.Lock()
	defer ft.mu.Unlock()
	require.Len(t, ft.callParams, 1)
	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(ft.callParams[0], &obj))
	_, hasMeta := obj["_meta"]
	assert.False(t, hasMeta, "legacy requests are not stamped")
}

func TestStampMetaKeyPreservesExistingMeta(t *testing.T) {
	params, err := protocol.StampMeta(json.RawMessage(`{"name":"t"}`), protocol.Version20260728,
		protocol.Implementation{Name: "loom"}, protocol.ClientCapabilities{})
	require.NoError(t, err)
	params, err = protocol.StampMetaKey(params, protocol.MetaIdempotencyKey, "k-123")
	require.NoError(t, err)

	meta := metaOf(t, params)
	assert.Contains(t, meta, protocol.MetaProtocolVersion)
	assert.Contains(t, meta, protocol.MetaClientInfo)
	var key string
	require.NoError(t, json.Unmarshal(meta[protocol.MetaIdempotencyKey], &key))
	assert.Equal(t, "k-123", key)
}

// TestReissueGatedOnIdempotency (review finding 7, PR #328): a lost response
// stream is re-issued only when the retry cannot execute the operation
// twice — reads, or tools/call under its idempotency key. Arbitrary methods
// (RawRequest mutations) surface CodeStreamLost to the caller instead of
// silently executing twice.
func TestReissueGatedOnIdempotency(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverResult = statelessDiscoverResult()
	ft.streamLostOn = map[string]int{"session/mutate": 1}
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	// Non-idempotent, key-less method: the stream loss surfaces, no retry.
	_, err := c.RawRequest(context.Background(), "session/mutate", json.RawMessage(`{}`))
	require.Error(t, err)
	var rpcErr *protocol.Error
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, transport.CodeStreamLost, rpcErr.Code)
	ft.mu.Lock()
	assert.Empty(t, ft.attempts, "the mutation must not have been re-issued")
	ft.mu.Unlock()

	// Idempotent read: re-issued transparently.
	ft.mu.Lock()
	ft.streamLostOn["tools/list"] = 1
	ft.mu.Unlock()
	_, err = c.ListTools(context.Background())
	require.NoError(t, err, "idempotent reads are safe to re-issue")
}
