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
package types

import (
	"reflect"
	"testing"
)

// AC4 (CT-1P, loom field half): HITLRequestInfo carries the additive Params and
// ParamsTruncated fields. This tier owns only that the fields exist on the
// carrier and round-trip on it; the bridge that populates them from the stored
// HumanRequest is exercised at its own tier (pkg/agent).

// The carrier can be populated to match a stored approval — the held call's full
// parameter map and the bound's flag — and reads the same values back.
func TestHITLRequestInfo_CarriesParamsAndTruncatedFlag(t *testing.T) {
	params := map[string]interface{}{
		"stmt":    "DELETE FROM users WHERE id=42",
		"confirm": true,
		"filter":  map[string]interface{}{"col": "id"},
	}
	info := HITLRequestInfo{
		RequestID:       "req-1",
		Kind:            "approval",
		Summary:         "delete_table DELETE FROM users WHERE id=42",
		Params:          params,
		ParamsTruncated: true,
	}

	if !reflect.DeepEqual(info.Params, params) {
		t.Errorf("Params = %v, want %v", info.Params, params)
	}
	if !info.ParamsTruncated {
		t.Error("ParamsTruncated = false, want true")
	}
}

// The additions are additive: a zero-value carrier is legal — Params is empty
// (the shape a question emits) and ParamsTruncated is false.
func TestHITLRequestInfo_ZeroValueParamsAreLegal(t *testing.T) {
	var info HITLRequestInfo

	if len(info.Params) != 0 {
		t.Errorf("zero-value Params = %v, want empty", info.Params)
	}
	if info.ParamsTruncated {
		t.Error("zero-value ParamsTruncated = true, want false")
	}
}
