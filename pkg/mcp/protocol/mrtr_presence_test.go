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
// requestState presence semantics (review finding 11, PR #327): empty and
// absent are different wire states and must survive the round trip.
package protocol

import (
	"encoding/json"
	"testing"
)

func TestParseInputRequiredEmptyStatePresent(t *testing.T) {
	irr, err := ParseInputRequired(json.RawMessage(`{"resultType":"input_required","requestState":""}`))
	if err != nil {
		t.Fatalf("present-but-empty requestState is schema-valid: %v", err)
	}
	if irr.RequestState == nil || *irr.RequestState != "" {
		t.Fatalf("presence lost: %+v", irr.RequestState)
	}
}

func TestParseInputRequiredAbsentStateAndRequestsRejected(t *testing.T) {
	if _, err := ParseInputRequired(json.RawMessage(`{"resultType":"input_required"}`)); err == nil {
		t.Fatal("neither inputRequests nor requestState must be rejected")
	}
}

func TestAttachRetryInputEchoesEmptyState(t *testing.T) {
	empty := ""
	out, err := AttachRetryInput(json.RawMessage(`{"name":"t"}`), nil, &empty)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	raw, ok := obj["requestState"]
	if !ok {
		t.Fatalf("empty state must be echoed: %s", out)
	}
	if string(raw) != `""` {
		t.Fatalf("exact value must be echoed: %s", raw)
	}
}

func TestAttachRetryInputOmitsAbsentState(t *testing.T) {
	out, err := AttachRetryInput(json.RawMessage(`{"name":"t","requestState":"stale-from-earlier-round"}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["requestState"]; ok {
		t.Fatalf("absent state must not appear on the retry: %s", out)
	}
}
