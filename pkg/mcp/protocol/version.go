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
// This file implements protocol revision negotiation for the 2026-07-28
// stateless MCP revision alongside the legacy handshake-based revisions.
package protocol

import (
	"encoding/json"
	"fmt"
)

// Known MCP protocol revisions, oldest to newest.
const (
	Version20241105 = "2024-11-05"
	Version20250326 = "2025-03-26"
	Version20250618 = "2025-06-18"
	Version20251125 = "2025-11-25"
	Version20260728 = "2026-07-28"
)

// PreferredVersion is the revision this implementation prefers when the
// server offers a choice.
const PreferredVersion = Version20260728

// LatestLegacyVersion is the newest handshake-based (pre-stateless) revision
// this implementation can speak. The initialize handshake requests it when no
// narrower revision was pinned or discovered: legacy negotiation has the
// client ask for the latest version it supports, and the server answers with
// that version or the latest one it can speak instead.
const LatestLegacyVersion = Version20251125

// supportedVersions lists every revision this implementation can speak, in
// descending preference order. Revisions before 2026-07-28 use the
// initialize handshake and protocol-level sessions; 2026-07-28 and later are
// stateless and carry identity in params._meta.
var supportedVersions = []string{
	Version20260728,
	Version20251125,
	Version20250618,
	Version20250326,
	Version20241105,
}

// IsSupportedVersion reports whether this implementation can speak the given
// protocol revision.
func IsSupportedVersion(v string) bool {
	for _, s := range supportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// IsStatelessVersion reports whether the given revision uses the stateless
// core (no initialize handshake, no Mcp-Session-Id, per-request _meta).
// Revision date strings sort lexicographically, so a plain comparison is
// correct.
func IsStatelessVersion(v string) bool {
	return v >= Version20260728
}

// NegotiateVersion picks the most preferred mutually supported revision from
// the versions a server advertises via server/discover. It returns false when
// there is no overlap.
func NegotiateVersion(serverVersions []string) (string, bool) {
	offered := make(map[string]bool, len(serverVersions))
	for _, v := range serverVersions {
		offered[v] = true
	}
	for _, v := range supportedVersions {
		if offered[v] {
			return v, true
		}
	}
	return "", false
}

// Reserved _meta keys defined by the 2026-07-28 revision. Under the stateless
// core, every request carries its protocol version and client capabilities,
// and both sides identify themselves per message rather than per connection.
const (
	MetaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	MetaClientInfo         = "io.modelcontextprotocol/clientInfo"
	MetaServerInfo         = "io.modelcontextprotocol/serverInfo"
	MetaSubscriptionID     = "io.modelcontextprotocol/subscriptionId"
	MetaLogLevel           = "io.modelcontextprotocol/logLevel"
)

// MetaIdempotencyKey is Loom's vendor _meta key carrying the idempotency key
// for side-effectful calls. The client mints a fresh UUID per logical call
// and reuses it verbatim when re-issuing after a lost response stream, so a
// dedupe-aware server can join the retry to the original run instead of
// executing twice (migration spec §7.5).
const MetaIdempotencyKey = "com.teradata.loom/idempotencyKey"

// StampMetaKey sets a single _meta key on a request's params, preserving all
// existing _meta content. It follows the same object rules as StampMeta.
func StampMetaKey(params json.RawMessage, key string, value interface{}) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &obj); err != nil {
			return nil, fmt.Errorf("params must be a JSON object to carry _meta: %w", err)
		}
	}
	meta := map[string]json.RawMessage{}
	if raw, ok := obj["_meta"]; ok {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("existing _meta is not a JSON object: %w", err)
		}
	}
	valJSON, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	meta[key] = valJSON
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	obj["_meta"] = metaJSON
	return json.Marshal(obj)
}

// Result types required on every result by the 2026-07-28 revision. Results
// from earlier-protocol servers omit the field and MUST be treated as
// complete.
const (
	ResultTypeComplete      = "complete"
	ResultTypeInputRequired = "input_required"
)

// Error codes reserved for the MCP specification under the 2026-07-28 error
// code allocation policy (-32020 to -32099).
const (
	HeaderMismatch                  = -32020
	MissingRequiredClientCapability = -32021
	UnsupportedProtocolVersion      = -32022
)

// DiscoverResult is the response to server/discover, the mandatory RPC that
// 2026-07-28 servers implement to advertise supported revisions, capabilities,
// and identity. Clients call it for up-front version selection or as a
// backward-compatibility probe: pre-2026 servers answer MethodNotFound.
//
// Field names follow the published schema: the version list is
// supportedVersions, and the server identity travels in _meta under
// io.modelcontextprotocol/serverInfo rather than as a top-level field.
// resultType, supportedVersions, capabilities, ttlMs, and cacheScope are all
// required on conforming responses (so servers marshalling this type emit
// them unconditionally); the client parses leniently and insists only on
// supportedVersions.
type DiscoverResult struct {
	ResultType        string                     `json:"resultType"`
	SupportedVersions []string                   `json:"supportedVersions"`
	Capabilities      ServerCapabilities         `json:"capabilities"`
	Instructions      string                     `json:"instructions,omitempty"`
	TTLMs             int64                      `json:"ttlMs"`
	CacheScope        string                     `json:"cacheScope"`
	Meta              map[string]json.RawMessage `json:"_meta,omitempty"`
}

// ServerInfo extracts the server identity from the result's _meta block
// (io.modelcontextprotocol/serverInfo). The boolean is false when the server
// omitted it or the value is malformed — the field is a SHOULD, not a MUST.
func (d *DiscoverResult) ServerInfo() (Implementation, bool) {
	raw, ok := d.Meta[MetaServerInfo]
	if !ok {
		return Implementation{}, false
	}
	var info Implementation
	if err := json.Unmarshal(raw, &info); err != nil {
		return Implementation{}, false
	}
	return info, true
}

// SetServerInfo stamps the server identity into the result's _meta block —
// the marshalling counterpart of ServerInfo for servers emitting the result.
func (d *DiscoverResult) SetServerInfo(info Implementation) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if d.Meta == nil {
		d.Meta = make(map[string]json.RawMessage, 1)
	}
	d.Meta[MetaServerInfo] = raw
	return nil
}

// StampMeta merges the stateless-revision identity keys into a request's
// params object and returns the updated params. A nil or empty params becomes
// an object containing only _meta. Existing _meta keys other than the three
// identity keys are preserved. Params that are not a JSON object cannot carry
// _meta and produce an error; MCP request params are objects throughout the
// specification, so this only trips on malformed callers.
func StampMeta(params json.RawMessage, version string, clientInfo Implementation, caps ClientCapabilities) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &obj); err != nil {
			return nil, fmt.Errorf("params must be a JSON object to carry _meta: %w", err)
		}
	}

	meta := map[string]json.RawMessage{}
	if raw, ok := obj["_meta"]; ok {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("existing _meta is not a JSON object: %w", err)
		}
	}

	versionJSON, err := json.Marshal(version)
	if err != nil {
		return nil, err
	}
	infoJSON, err := json.Marshal(clientInfo)
	if err != nil {
		return nil, err
	}
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		return nil, err
	}

	meta[MetaProtocolVersion] = versionJSON
	meta[MetaClientInfo] = infoJSON
	meta[MetaClientCapabilities] = capsJSON

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	obj["_meta"] = metaJSON

	return json.Marshal(obj)
}

// ResultEnvelope carries the revision-level fields common to every result
// under 2026-07-28: the required resultType discriminator and the _meta block
// in which servers identify themselves.
type ResultEnvelope struct {
	ResultType string                     `json:"resultType,omitempty"`
	Meta       map[string]json.RawMessage `json:"_meta,omitempty"`
}

// ParseResultEnvelope extracts the envelope from a raw result. A missing
// resultType is normalized to complete, as the specification requires for
// results from earlier-protocol servers. Unparseable results are also treated
// as complete so that method-specific decoding reports the real error.
func ParseResultEnvelope(result json.RawMessage) ResultEnvelope {
	env := ResultEnvelope{}
	if len(result) > 0 {
		_ = json.Unmarshal(result, &env)
	}
	if env.ResultType == "" {
		env.ResultType = ResultTypeComplete
	}
	return env
}

// ServerInfoFromMeta extracts the server identity a stateless-revision server
// placed in a result's _meta block. The boolean is false when the key is
// absent or malformed.
func (e ResultEnvelope) ServerInfoFromMeta() (Implementation, bool) {
	raw, ok := e.Meta[MetaServerInfo]
	if !ok {
		return Implementation{}, false
	}
	var info Implementation
	if err := json.Unmarshal(raw, &info); err != nil {
		return Implementation{}, false
	}
	return info, true
}
