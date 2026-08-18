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
// This file implements the 2026-07-28 per-request _meta contract on the
// server: identity extraction into the request context (no handler parses
// _meta itself), result stamping (resultType + serverInfo), and the mandatory
// server/discover RPC.
package server

import (
	"context"
	"encoding/json"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// RequestMeta carries the revision-level identity a stateless (2026-07-28+)
// request presented in params._meta. Handlers and the observability layer
// read it from the context via RequestMetaFromContext.
type RequestMeta struct {
	ProtocolVersion string
	ClientInfo      *protocol.Implementation
	ClientCaps      *protocol.ClientCapabilities
	LogLevel        string
	IdempotencyKey  string

	// RetryInput carries a client's MRTR retry payload (inputResponses +
	// echoed requestState); zero when the request is not a retry. Handlers
	// read it here instead of parsing params themselves.
	RetryInput protocol.RetryInput
}

// Stateless reports whether the request declared a stateless-core revision.
// Safe on a nil receiver (legacy requests carry no RequestMeta).
func (m *RequestMeta) Stateless() bool {
	return m != nil && protocol.IsStatelessVersion(m.ProtocolVersion)
}

type requestMetaKey struct{}

// RequestMetaFromContext returns the request's _meta identity, or nil for
// legacy requests.
func RequestMetaFromContext(ctx context.Context) *RequestMeta {
	m, _ := ctx.Value(requestMetaKey{}).(*RequestMeta)
	return m
}

// withRequestMeta parses params._meta and, when the request declares a
// protocol version (the marker of a stateless-revision request), attaches a
// RequestMeta to the context. Legacy requests — including those carrying
// unrelated _meta content such as progressToken — pass through unchanged.
func withRequestMeta(ctx context.Context, params json.RawMessage) context.Context {
	meta := parseRequestMeta(params)
	if meta == nil {
		return ctx
	}
	return context.WithValue(ctx, requestMetaKey{}, meta)
}

func parseRequestMeta(params json.RawMessage) *RequestMeta {
	if len(params) == 0 {
		return nil
	}
	var probe struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &probe); err != nil || probe.Meta == nil {
		return nil
	}
	var version string
	if raw, ok := probe.Meta[protocol.MetaProtocolVersion]; ok {
		_ = json.Unmarshal(raw, &version)
	}
	if version == "" {
		return nil
	}

	meta := &RequestMeta{ProtocolVersion: version}
	if raw, ok := probe.Meta[protocol.MetaClientInfo]; ok {
		var info protocol.Implementation
		if err := json.Unmarshal(raw, &info); err == nil {
			meta.ClientInfo = &info
		}
	}
	if raw, ok := probe.Meta[protocol.MetaClientCapabilities]; ok {
		var caps protocol.ClientCapabilities
		if err := json.Unmarshal(raw, &caps); err == nil {
			meta.ClientCaps = &caps
		}
	}
	if raw, ok := probe.Meta[protocol.MetaLogLevel]; ok {
		_ = json.Unmarshal(raw, &meta.LogLevel)
	}
	if raw, ok := probe.Meta[protocol.MetaIdempotencyKey]; ok {
		_ = json.Unmarshal(raw, &meta.IdempotencyKey)
	}
	meta.RetryInput = protocol.ParseRetryInput(params)
	return meta
}

// stampResult injects the revision-level result envelope for stateless
// responses: resultType (defaulted to "complete", never overwriting a
// handler-set value such as input_required) and the server's identity under
// _meta. JSON-RPC error responses are never stamped — resultType is a result
// field.
func (s *MCPServer) stampResult(result interface{}) (json.RawMessage, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	obj := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		// Non-object results have no envelope to stamp; pass through.
		return raw, nil
	}
	if _, ok := obj["resultType"]; !ok {
		obj["resultType"] = json.RawMessage(`"` + protocol.ResultTypeComplete + `"`)
	}
	meta := map[string]json.RawMessage{}
	if rawMeta, ok := obj["_meta"]; ok {
		_ = json.Unmarshal(rawMeta, &meta)
	}
	infoJSON, err := json.Marshal(s.info)
	if err != nil {
		return nil, err
	}
	meta[protocol.MetaServerInfo] = infoJSON
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	obj["_meta"] = metaJSON
	return json.Marshal(obj)
}

// handleDiscover answers server/discover, the mandatory RPC of the
// 2026-07-28 revision and the backward-compatibility probe for clients. Only
// revisions this server actually implements are advertised.
func (s *MCPServer) handleDiscover(_ context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
	s.mu.RLock()
	caps := s.capabilities
	s.mu.RUnlock()
	// Extensions ride capabilities.extensions and identity rides _meta, per
	// the official schema.
	if len(s.extensions) > 0 && caps.Extensions == nil {
		caps.Extensions = make(map[string]json.RawMessage, len(s.extensions))
		for k, v := range s.extensions {
			if raw, err := json.Marshal(v); err == nil {
				caps.Extensions[k] = raw
			}
		}
	}
	// resultType, ttlMs, and cacheScope are required members of a conforming
	// DiscoverResult. The result is server metadata, not tenant data, but
	// cacheScope stays "private" by the same default-caution §7.2 applies to
	// list results: an intermediary must opt in to cross-context caching,
	// never get it by default. ttlMs matches the core-tools freshness hint.
	result := protocol.DiscoverResult{
		ResultType:        protocol.ResultTypeComplete,
		SupportedVersions: []string{protocol.Version20260728, protocol.Version20241105},
		Capabilities:      caps,
		Instructions:      s.instructions,
		TTLMs:             300000,
		CacheScope:        "private",
	}
	if err := result.SetServerInfo(s.info); err != nil {
		return nil, err
	}
	return result, nil
}
