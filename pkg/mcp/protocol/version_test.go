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
package protocol

import (
	"encoding/json"
	"testing"
)

func TestNegotiateVersionPrefersStateless(t *testing.T) {
	v, ok := NegotiateVersion([]string{"2025-11-25", "2026-07-28"})
	if !ok || v != Version20260728 {
		t.Fatalf("got %q ok=%v, want %q", v, ok, Version20260728)
	}
}

func TestNegotiateVersionLegacyOnly(t *testing.T) {
	v, ok := NegotiateVersion([]string{"2025-03-26", "2024-11-05"})
	if !ok || v != Version20250326 {
		t.Fatalf("got %q ok=%v, want %q", v, ok, Version20250326)
	}
	if IsStatelessVersion(v) {
		t.Fatal("2025-03-26 must not be stateless")
	}
}

func TestNegotiateVersionNoOverlap(t *testing.T) {
	if _, ok := NegotiateVersion([]string{"2030-01-01"}); ok {
		t.Fatal("expected no overlap")
	}
	if _, ok := NegotiateVersion(nil); ok {
		t.Fatal("expected no overlap on empty")
	}
}

func TestStampMetaOnNilParams(t *testing.T) {
	out, err := StampMeta(nil, Version20260728, Implementation{Name: "loom", Version: "1.4.0"}, ClientCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	meta := obj["_meta"]
	var version string
	if err := json.Unmarshal(meta[MetaProtocolVersion], &version); err != nil || version != Version20260728 {
		t.Fatalf("bad protocolVersion in _meta: %s err=%v", meta[MetaProtocolVersion], err)
	}
	var info Implementation
	if err := json.Unmarshal(meta[MetaClientInfo], &info); err != nil || info.Name != "loom" {
		t.Fatalf("bad clientInfo in _meta: %s err=%v", meta[MetaClientInfo], err)
	}
}

func TestStampMetaPreservesParamsAndForeignMetaKeys(t *testing.T) {
	params := json.RawMessage(`{"name":"weave_start","arguments":{"x":1},"_meta":{"traceparent":"00-abc-def-01"}}`)
	out, err := StampMeta(params, Version20260728, Implementation{Name: "loom"}, ClientCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	var obj struct {
		Name      string                     `json:"name"`
		Arguments map[string]int             `json:"arguments"`
		Meta      map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	if obj.Name != "weave_start" || obj.Arguments["x"] != 1 {
		t.Fatalf("params clobbered: %s", out)
	}
	if string(obj.Meta["traceparent"]) != `"00-abc-def-01"` {
		t.Fatalf("foreign _meta key lost: %s", out)
	}
	if _, ok := obj.Meta[MetaProtocolVersion]; !ok {
		t.Fatal("identity keys missing")
	}
}

func TestStampMetaRejectsNonObjectParams(t *testing.T) {
	if _, err := StampMeta(json.RawMessage(`[1,2,3]`), Version20260728, Implementation{}, ClientCapabilities{}); err == nil {
		t.Fatal("expected error for array params")
	}
}

func TestParseResultEnvelopeDefaultsToComplete(t *testing.T) {
	for _, raw := range []string{`{"tools":[]}`, ``, `{"resultType":""}`} {
		env := ParseResultEnvelope(json.RawMessage(raw))
		if env.ResultType != ResultTypeComplete {
			t.Fatalf("raw=%q got %q", raw, env.ResultType)
		}
	}
	env := ParseResultEnvelope(json.RawMessage(`{"resultType":"input_required","inputRequests":[]}`))
	if env.ResultType != ResultTypeInputRequired {
		t.Fatalf("got %q", env.ResultType)
	}
}

func TestServerInfoFromMeta(t *testing.T) {
	raw := json.RawMessage(`{"resultType":"complete","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"tera-bridge","version":"2.1"}}}`)
	env := ParseResultEnvelope(raw)
	info, ok := env.ServerInfoFromMeta()
	if !ok || info.Name != "tera-bridge" || info.Version != "2.1" {
		t.Fatalf("got %+v ok=%v", info, ok)
	}
	if _, ok := ParseResultEnvelope(json.RawMessage(`{}`)).ServerInfoFromMeta(); ok {
		t.Fatal("expected no serverInfo")
	}
}

// TestDiscoverResultParsesOfficialExample decodes the verbatim response
// example from the published 2026-07-28 Discovery specification
// (modelcontextprotocol.io/specification/2026-07-28/server/discover) so the
// client's wire model cannot drift from the official field names again:
// supportedVersions (not protocolVersions), server identity under
// _meta[io.modelcontextprotocol/serverInfo] (not top-level serverInfo).
func TestDiscoverResultParsesOfficialExample(t *testing.T) {
	official := json.RawMessage(`{
		"resultType": "complete",
		"supportedVersions": ["2026-07-28"],
		"capabilities": {
			"tools": {},
			"resources": {}
		},
		"_meta": {
			"io.modelcontextprotocol/serverInfo": {
				"name": "ExampleServer",
				"version": "1.0.0"
			}
		},
		"instructions": "This server provides weather and resource utilities.",
		"ttlMs": 3600000,
		"cacheScope": "public"
	}`)

	var result DiscoverResult
	if err := json.Unmarshal(official, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.SupportedVersions) != 1 || result.SupportedVersions[0] != Version20260728 {
		t.Fatalf("supportedVersions not parsed: %+v", result.SupportedVersions)
	}
	if result.Capabilities.Tools == nil || result.Capabilities.Resources == nil {
		t.Fatalf("capabilities not parsed: %+v", result.Capabilities)
	}
	info, ok := result.ServerInfo()
	if !ok || info.Name != "ExampleServer" || info.Version != "1.0.0" {
		t.Fatalf("serverInfo not extracted from _meta: %+v ok=%v", info, ok)
	}
	if result.ResultType != ResultTypeComplete {
		t.Fatalf("resultType: %q", result.ResultType)
	}
	if result.Instructions == "" || result.TTLMs != 3600000 || result.CacheScope != "public" {
		t.Fatalf("caching/instruction fields not parsed: %+v", result)
	}

	version, negotiated := NegotiateVersion(result.SupportedVersions)
	if !negotiated || version != Version20260728 {
		t.Fatalf("negotiation against official example failed: %q %v", version, negotiated)
	}
}

// TestDiscoverResultServerInfoAbsent covers the SHOULD nature of serverInfo:
// a response without it is valid and must not fail parsing.
func TestDiscoverResultServerInfoAbsent(t *testing.T) {
	var result DiscoverResult
	raw := `{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{},"ttlMs":0,"cacheScope":"private"}`
	if err := json.Unmarshal(json.RawMessage(raw), &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result.ServerInfo(); ok {
		t.Fatal("expected absent serverInfo")
	}
}

// TestDiscoverResultRoundTripsServerInfo covers the server-side marshalling
// counterpart: SetServerInfo places the identity where ServerInfo reads it.
func TestDiscoverResultRoundTripsServerInfo(t *testing.T) {
	res := DiscoverResult{
		ResultType:        ResultTypeComplete,
		SupportedVersions: []string{Version20260728},
		CacheScope:        "private",
	}
	if err := res.SetServerInfo(Implementation{Name: "loom", Version: "1.4.0"}); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["supportedVersions"]; !ok {
		t.Fatalf("marshalled result missing supportedVersions: %s", out)
	}
	if _, ok := probe["serverInfo"]; ok {
		t.Fatalf("serverInfo must not appear top-level: %s", out)
	}
	var back DiscoverResult
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if info, ok := back.ServerInfo(); !ok || info.Name != "loom" {
		t.Fatalf("serverInfo did not round-trip: %+v ok=%v", info, ok)
	}
}
