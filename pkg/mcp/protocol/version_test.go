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
