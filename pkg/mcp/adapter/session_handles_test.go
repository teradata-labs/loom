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
package adapter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// mintSchemaSnake declares the release convention with the snake_case
// spelling; mintSchemaCamel with the camelCase spelling. The release wire
// spelling must follow the tool's own schema, never a client-side case
// convention.
func mintSchemaSnake() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target":         map[string]interface{}{"type": "string"},
			"release_handle": map[string]interface{}{"type": "string"},
		},
	}
}

func mintSchemaCamel() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target":        map[string]interface{}{"type": "string"},
			"releaseHandle": map[string]interface{}{"type": "string"},
		},
	}
}

func mintResult(handle string) json.RawMessage {
	r, _ := json.Marshal(protocol.CallToolResult{Content: []protocol.Content{
		{Type: "text", Text: `{"session_handle":"` + handle + `","target":"default"}`},
	}})
	return r
}

// TestAutoReleaseOnConversationEnd: a successful call whose result carries a
// session_handle — minted by a tool whose schema declares the release
// convention — is tracked by the ctx collector and released best-effort when
// the conversation ends, using the schema's own property spelling
// (issue #345).
func TestAutoReleaseOnConversationEnd(t *testing.T) {
	released, _ := json.Marshal(protocol.CallToolResult{Content: []protocol.Content{
		{Type: "text", Text: `{"target":"released"}`},
	}})
	ft := newWaitTransportWithSchema(mintSchemaSnake(), mintResult("tdsh_test123"), released)
	adapter := waitAdapter(t, ft)

	ctx, collector := WithHandleCollector(context.Background())
	res, err := adapter.Execute(ctx, map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, 1, collector.Count(), "minted handle must be tracked")

	collector.ReleaseAll(nil)
	assert.Equal(t, 0, collector.Count())
	assert.Equal(t, 2, ft.calls(), "release call must reach the wire")
	// The release carried the tracked handle under the schema's spelling.
	last := ft.lastCallParams()
	assert.Equal(t, "tdsh_test123", last["release_handle"])
}

// TestAutoReleaseUsesSchemaSpelling: a tool whose schema declares
// releaseHandle (camelCase) gets its release with that exact wire spelling —
// derived from the schema, not from any client-side case convention.
func TestAutoReleaseUsesSchemaSpelling(t *testing.T) {
	released, _ := json.Marshal(protocol.CallToolResult{Content: []protocol.Content{
		{Type: "text", Text: `{"released":true}`},
	}})
	ft := newWaitTransportWithSchema(mintSchemaCamel(), mintResult("tdsh_camel"), released)
	adapter := waitAdapter(t, ft)

	ctx, collector := WithHandleCollector(context.Background())
	_, err := adapter.Execute(ctx, map[string]interface{}{})
	require.NoError(t, err)
	require.Equal(t, 1, collector.Count())

	collector.ReleaseAll(nil)
	require.Equal(t, 2, ft.calls(), "release call must reach the wire")
	last := ft.lastCallParams()
	assert.Equal(t, "tdsh_camel", last["releaseHandle"], "wire spelling must follow the tool schema")
	_, hasSnake := last["release_handle"]
	assert.False(t, hasSnake, "no convention-spelled duplicate")
}

// TestAutoReleaseSkipsUnsatisfiableRequired: a mint tool whose schema
// requires fields beyond the release property would reject a one-argument
// release in client-side validation before it ever reached the server — the
// release is skipped with a warning and the handle leaks to server TTL (the
// pre-#345 behavior) instead of producing a guaranteed-failing call.
func TestAutoReleaseSkipsUnsatisfiableRequired(t *testing.T) {
	schema := mintSchemaSnake()
	schema["required"] = []interface{}{"target"}
	ft := newWaitTransportWithSchema(schema, mintResult("tdsh_reqgate"))
	adapter := waitAdapter(t, ft)

	ctx, collector := WithHandleCollector(context.Background())
	res, err := adapter.Execute(ctx, map[string]interface{}{"target": "db1"})
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, 1, collector.Count(), "handle is tracked (property declared)")

	core, logs := observer.New(zap.WarnLevel)
	collector.ReleaseAll(zap.New(core))

	assert.Equal(t, 1, ft.calls(), "no release attempt may reach the wire")
	skips := logs.FilterMessageSnippet("auto-release skipped").All()
	require.Len(t, skips, 1, "the skip must be logged")
	ctxMap := skips[0].ContextMap()
	assert.Equal(t, "waitfake", ctxMap["server"])
	assert.Equal(t, "connect", ctxMap["tool"])
	assert.Equal(t, "tdsh_reqgate", ctxMap["handle"])
}

// TestNoTrackingWithoutReleaseProperty: a tool that never declared the
// release convention (no release_handle/releaseHandle property) mints
// nothing trackable — a top-level session_handle in its result is ignored,
// and no auto-release is ever attempted against it. This is what keeps a
// permissive server (one that treats unknown arguments as a fresh request)
// from receiving auto-release calls that mint fresh handles.
func TestNoTrackingWithoutReleaseProperty(t *testing.T) {
	ft := newWaitTransport(mintResult("tdsh_never"))
	adapter := waitAdapter(t, ft)

	ctx, collector := WithHandleCollector(context.Background())
	res, err := adapter.Execute(ctx, map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, res.Success)
	assert.Equal(t, 0, collector.Count(), "no tracking without the schema-declared convention")

	collector.ReleaseAll(nil)
	assert.Equal(t, 1, ft.calls(), "no release call against a tool that never opted in")
}

// TestAutoReleaseSkipsExplicitlyReleased: an agent that releases its own
// handle is not double-released at conversation end.
func TestAutoReleaseSkipsExplicitlyReleased(t *testing.T) {
	released, _ := json.Marshal(protocol.CallToolResult{Content: []protocol.Content{
		{Type: "text", Text: `{"target":"released"}`},
	}})
	ft := newWaitTransportWithSchema(mintSchemaSnake(), mintResult("tdsh_selfclean"), released)
	adapter := waitAdapter(t, ft)

	ctx, collector := WithHandleCollector(context.Background())
	_, err := adapter.Execute(ctx, map[string]interface{}{})
	require.NoError(t, err)
	_, err = adapter.Execute(ctx, map[string]interface{}{"release_handle": "tdsh_selfclean"})
	require.NoError(t, err)

	require.Equal(t, 0, collector.Count(), "explicit release must untrack the handle")
	collector.ReleaseAll(nil)
	assert.Equal(t, 2, ft.calls(), "no extra release call")
}

// TestAutoReleaseMultiContentMint: a mint result with multiple content items
// (text payload plus a resource_link) still has its session_handle tracked —
// the collector reads the multi-item shape convertMCPContent produces.
func TestAutoReleaseMultiContentMint(t *testing.T) {
	mint, _ := json.Marshal(protocol.CallToolResult{Content: []protocol.Content{
		{Type: "text", Text: `{"session_handle":"tdsh_multi","target":"default"}`},
		{Type: "resource_link", URI: "test://slots", Name: "availability"},
	}})
	released, _ := json.Marshal(protocol.CallToolResult{Content: []protocol.Content{
		{Type: "text", Text: `{"released":true}`},
	}})
	ft := newWaitTransportWithSchema(mintSchemaSnake(), mint, released)
	adapter := waitAdapter(t, ft)

	ctx, collector := WithHandleCollector(context.Background())
	res, err := adapter.Execute(ctx, map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, 1, collector.Count(), "multi-content mint must be tracked")

	collector.ReleaseAll(nil)
	require.Equal(t, 2, ft.calls())
	assert.Equal(t, "tdsh_multi", ft.lastCallParams()["release_handle"])
}

// TestNoCollectorNoTracking: without a collector in ctx (workflow paths),
// behavior is unchanged.
func TestNoCollectorNoTracking(t *testing.T) {
	ft := newWaitTransportWithSchema(mintSchemaSnake(), mintResult("tdsh_untracked"))
	adapter := waitAdapter(t, ft)

	res, err := adapter.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, res.Success)
	assert.Equal(t, 1, ft.calls())
}

// TestReleaseAllTotalBudgetAndConcurrency: releases run concurrently under
// ONE shared budget — a hung server neither serializes the other releases
// behind its timeout nor extends the pass beyond ~releaseAllTimeout total.
func TestReleaseAllTotalBudgetAndConcurrency(t *testing.T) {
	released, _ := json.Marshal(protocol.CallToolResult{Content: []protocol.Content{
		{Type: "text", Text: `{"released":true}`},
	}})
	ft := newWaitTransportWithSchema(mintSchemaSnake())
	ft.mu.Lock()
	ft.callHook = func(args map[string]interface{}) (json.RawMessage, bool) {
		if args["release_handle"] == "tdsh_hang" {
			return nil, false // hung server: never answers this release
		}
		return released, true
	}
	ft.mu.Unlock()
	adapter := waitAdapter(t, ft)

	_, collector := WithHandleCollector(context.Background())
	collector.add(adapter, "tdsh_hang")
	collector.add(adapter, "tdsh_fast")

	fastReleased := func() bool {
		for i := 0; i < ft.calls(); i++ {
			if ft.argsOfCall(i)["release_handle"] == "tdsh_fast" {
				return true
			}
		}
		return false
	}

	start := time.Now()
	done := make(chan struct{})
	go func() {
		collector.ReleaseAll(nil)
		close(done)
	}()

	// The fast release must reach the wire while the hung one is still
	// pending — a sequential pass would queue it behind the full timeout.
	require.Eventually(t, fastReleased, 2*time.Second, 10*time.Millisecond,
		"fast release must not be serialized behind the hung one")

	select {
	case <-done:
	case <-time.After(releaseAllTimeout + 3*time.Second):
		t.Fatal("ReleaseAll exceeded its total budget")
	}
	assert.Less(t, time.Since(start), releaseAllTimeout+2*time.Second,
		"the budget is total, not per handle")
	assert.Equal(t, 0, collector.Count())
}

// TestReleaseHandleProperty: the release property resolves from the tool's
// own schema, both spellings, never invented.
func TestReleaseHandleProperty(t *testing.T) {
	tests := []struct {
		name     string
		schema   map[string]interface{}
		wantProp string
		wantOK   bool
	}{
		{
			name:     "snake_case declared",
			schema:   mintSchemaSnake(),
			wantProp: "release_handle",
			wantOK:   true,
		},
		{
			name:     "camelCase declared",
			schema:   mintSchemaCamel(),
			wantProp: "releaseHandle",
			wantOK:   true,
		},
		{
			name: "both declared prefers camelCase",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"releaseHandle":  map[string]interface{}{"type": "string"},
					"release_handle": map[string]interface{}{"type": "string"},
				},
			},
			wantProp: "releaseHandle",
			wantOK:   true,
		},
		{
			name:   "not declared",
			schema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"target": map[string]interface{}{"type": "string"}}},
			wantOK: false,
		},
		{
			name:   "no properties",
			schema: map[string]interface{}{"type": "object"},
			wantOK: false,
		},
		{
			name:   "nil schema",
			schema: nil,
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop, ok := releaseHandleProperty(tt.schema)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantProp, prop)
		})
	}
}

// TestReleaseSatisfiesRequired: a one-argument release is only attempted when
// required ⊆ {release property}, across the shapes required lists appear in.
func TestReleaseSatisfiesRequired(t *testing.T) {
	tests := []struct {
		name     string
		required interface{} // nil means absent
		prop     string
		want     bool
	}{
		{name: "no required clause", required: nil, prop: "release_handle", want: true},
		{name: "empty interface list", required: []interface{}{}, prop: "release_handle", want: true},
		{name: "only the release property (interface list)", required: []interface{}{"release_handle"}, prop: "release_handle", want: true},
		{name: "only the release property (string list)", required: []string{"releaseHandle"}, prop: "releaseHandle", want: true},
		{name: "requires another field", required: []interface{}{"target"}, prop: "release_handle", want: false},
		{name: "requires release plus another", required: []interface{}{"release_handle", "target"}, prop: "release_handle", want: false},
		{name: "requires another field (string list)", required: []string{"target"}, prop: "release_handle", want: false},
		{name: "malformed clause", required: "target", prop: "release_handle", want: false},
		{name: "non-string entry", required: []interface{}{42}, prop: "release_handle", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := map[string]interface{}{"type": "object"}
			if tt.required != nil {
				schema["required"] = tt.required
			}
			assert.Equal(t, tt.want, releaseSatisfiesRequired(schema, tt.prop))
		})
	}
}

// TestSessionHandleFromData: the extractor reads every shape
// convertMCPContent produces, including multi-item content slices.
func TestSessionHandleFromData(t *testing.T) {
	tests := []struct {
		name string
		data interface{}
		want string
	}{
		{
			name: "single text content (JSON string)",
			data: `{"session_handle":"tdsh_str","target":"x"}`,
			want: "tdsh_str",
		},
		{
			name: "object payload",
			data: map[string]interface{}{"session_handle": "tdsh_map"},
			want: "tdsh_map",
		},
		{
			name: "multi-item content slice with JSON text item",
			data: []map[string]interface{}{
				{"type": "resource_link", "uri": "test://slots"},
				{"type": "text", "text": `{"session_handle":"tdsh_slice"}`},
			},
			want: "tdsh_slice",
		},
		{
			name: "multi-item slice after JSON round-trip",
			data: []interface{}{
				map[string]interface{}{"type": "text", "text": `{"session_handle":"tdsh_roundtrip"}`},
			},
			want: "tdsh_roundtrip",
		},
		{
			name: "content item carrying the field directly",
			data: []map[string]interface{}{{"session_handle": "tdsh_direct"}},
			want: "tdsh_direct",
		},
		{name: "non-JSON string", data: "plain text result", want: ""},
		{name: "no handle anywhere", data: []map[string]interface{}{{"type": "text", "text": `{"other":"x"}`}}, want: ""},
		{name: "nil", data: nil, want: ""},
		{name: "oversized handle rejected", data: map[string]interface{}{"session_handle": string(make([]byte, 300))}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sessionHandleFromData(tt.data))
		})
	}
}
