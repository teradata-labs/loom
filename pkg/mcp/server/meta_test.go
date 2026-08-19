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
// Tests for the 2026-07-28 server _meta middleware, result stamping, and
// server/discover.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

func statelessReq(id int, method string) []byte {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "method": method,
		"params": map[string]interface{}{
			"_meta": map[string]interface{}{
				protocol.MetaProtocolVersion:    protocol.Version20260728,
				protocol.MetaClientInfo:         map[string]string{"name": "loom", "version": "1.4.0"},
				protocol.MetaClientCapabilities: map[string]interface{}{},
			},
		},
	})
	return body
}

func legacyReq(id int, method string) []byte {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "method": method, "params": map[string]interface{}{},
	})
	return body
}

func decodeResponse(t *testing.T, raw []byte) (result map[string]json.RawMessage, rpcErr *protocol.Error) {
	t.Helper()
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *protocol.Error `json:"error"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	if resp.Error != nil {
		return nil, resp.Error
	}
	result = map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	return result, nil
}

func TestServerDiscoverAdvertisesImplementedRevisions(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)

	raw, err := s.HandleMessage(context.Background(), legacyReq(1, "server/discover"))
	require.NoError(t, err)
	result, rpcErr := decodeResponse(t, raw)
	require.Nil(t, rpcErr)

	var versions []string
	require.NoError(t, json.Unmarshal(result["supportedVersions"], &versions))
	assert.Equal(t, []string{protocol.Version20260728, protocol.Version20241105}, versions,
		"only implemented revisions may be advertised")

	var meta map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(result["_meta"], &meta))
	var info protocol.Implementation
	require.NoError(t, json.Unmarshal(meta[protocol.MetaServerInfo], &info))
	assert.Equal(t, "loom-mcp", info.Name)
}

func TestStatelessResultIsStamped(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	s.RegisterHandler("echo", func(_ context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
		return map[string]string{"hello": "world"}, nil
	})

	raw, err := s.HandleMessage(context.Background(), statelessReq(2, "echo"))
	require.NoError(t, err)
	result, rpcErr := decodeResponse(t, raw)
	require.Nil(t, rpcErr)

	var resultType string
	require.NoError(t, json.Unmarshal(result["resultType"], &resultType))
	assert.Equal(t, protocol.ResultTypeComplete, resultType)

	var meta map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(result["_meta"], &meta))
	var info protocol.Implementation
	require.NoError(t, json.Unmarshal(meta[protocol.MetaServerInfo], &info))
	assert.Equal(t, "loom-mcp", info.Name)
}

func TestLegacyResultNotStamped(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	s.RegisterHandler("echo", func(_ context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
		return map[string]string{"hello": "world"}, nil
	})

	raw, err := s.HandleMessage(context.Background(), legacyReq(3, "echo"))
	require.NoError(t, err)
	result, rpcErr := decodeResponse(t, raw)
	require.Nil(t, rpcErr)
	assert.NotContains(t, result, "resultType")
	assert.NotContains(t, result, "_meta")
}

func TestHandlerResultTypePreserved(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	s.RegisterHandler("pause", func(_ context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"resultType": protocol.ResultTypeInputRequired, "requestState": "st"}, nil
	})

	raw, err := s.HandleMessage(context.Background(), statelessReq(4, "pause"))
	require.NoError(t, err)
	result, rpcErr := decodeResponse(t, raw)
	require.Nil(t, rpcErr)

	var resultType string
	require.NoError(t, json.Unmarshal(result["resultType"], &resultType))
	assert.Equal(t, protocol.ResultTypeInputRequired, resultType, "stamping must not overwrite a handler-set resultType")
	assert.Contains(t, result, "_meta", "serverInfo still stamped")
}

func TestErrorResponsesNotStamped(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	s.RegisterHandler("boom", func(_ context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("kaboom")
	})

	raw, err := s.HandleMessage(context.Background(), statelessReq(5, "boom"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "resultType", "error responses carry no result envelope")
}

func TestPingSplitsByRevision(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)

	// Legacy ping answers.
	raw, err := s.HandleMessage(context.Background(), legacyReq(6, "ping"))
	require.NoError(t, err)
	_, rpcErr := decodeResponse(t, raw)
	assert.Nil(t, rpcErr)

	// Stateless ping does not exist.
	raw, err = s.HandleMessage(context.Background(), statelessReq(7, "ping"))
	require.NoError(t, err)
	_, rpcErr = decodeResponse(t, raw)
	require.NotNil(t, rpcErr)
	assert.Equal(t, protocol.MethodNotFound, rpcErr.Code)
}

func TestMalformedMetaTreatedAsLegacy(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	s.RegisterHandler("echo", func(_ context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
		return map[string]string{"ok": "yes"}, nil
	})

	body := []byte(`{"jsonrpc":"2.0","id":8,"method":"echo","params":{"_meta":"not-an-object"}}`)
	raw, err := s.HandleMessage(context.Background(), body)
	require.NoError(t, err)
	result, rpcErr := decodeResponse(t, raw)
	require.Nil(t, rpcErr)
	assert.NotContains(t, result, "resultType", "malformed _meta falls back to legacy handling")
}

func TestRequestMetaExposedToHandlers(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	var seen *RequestMeta
	s.RegisterHandler("inspect", func(ctx context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
		seen = RequestMetaFromContext(ctx)
		return map[string]string{}, nil
	})

	_, err := s.HandleMessage(context.Background(), statelessReq(9, "inspect"))
	require.NoError(t, err)
	require.NotNil(t, seen)
	assert.True(t, seen.Stateless())
	require.NotNil(t, seen.ClientInfo)
	assert.Equal(t, "loom", seen.ClientInfo.Name)
}

// TestStampResultNilShapes (review finding 9, PR #328): JSON null unmarshals
// into a nil map without error, so a nil result and a handler-set
// "_meta": null both used to panic on assignment. Both are valid successful
// results and must stamp.
func TestStampResultNilShapes(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)

	out, err := s.stampResult(nil)
	if err != nil {
		t.Fatalf("nil result must stamp: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	if string(probe["resultType"]) != `"complete"` {
		t.Fatalf("nil result not defaulted to complete: %s", out)
	}

	out, err = s.stampResult(map[string]interface{}{"_meta": nil, "ok": true})
	if err != nil {
		t.Fatalf("_meta:null must stamp: %v", err)
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(probe["_meta"], &meta); err != nil || meta == nil {
		t.Fatalf("_meta not rebuilt: %s", out)
	}
}
