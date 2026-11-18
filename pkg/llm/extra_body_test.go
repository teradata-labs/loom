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

package llm

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sampleRequest struct {
	Model    string `json:"model"`
	Messages []int  `json:"messages,omitempty"`
	Stream   bool   `json:"stream"`
	Untagged string
	Skipped  string `json:"-"`
	private  string //nolint:unused // proves unexported fields are ignored
}

func TestJSONFieldNames(t *testing.T) {
	got := JSONFieldNames(&sampleRequest{})
	assert.Equal(t, map[string]struct{}{
		"model": {}, "messages": {}, "stream": {}, "Untagged": {},
	}, got)
	assert.Empty(t, JSONFieldNames(42))
	assert.Empty(t, JSONFieldNames(nil))
}

func TestValidateExtraBody(t *testing.T) {
	reserved := JSONFieldNames(sampleRequest{})
	tests := []struct {
		name    string
		extra   ExtraBody
		want    string
		wantErr string
	}{
		{name: "nil", extra: nil, want: ""},
		{name: "empty", extra: ExtraBody{}, want: ""},
		{name: "one key", extra: ExtraBody{"providerOptions": map[string]any{"gateway": map[string]any{"order": []string{"bedrock"}}}},
			want: `{"providerOptions":{"gateway":{"order":["bedrock"]}}}`},
		{name: "sorted keys", extra: ExtraBody{"b": 1, "a": 2}, want: `{"a":2,"b":1}`},
		{name: "collision", extra: ExtraBody{"model": "x", "stream": true, "ok": 1}, wantErr: "model, stream"},
		{name: "empty key", extra: ExtraBody{" ": 1}, wantErr: "empty key"},
		{name: "invalid utf8 key", extra: ExtraBody{"\xb7": 1}, wantErr: "not valid UTF-8"},
		{name: "unmarshalable", extra: ExtraBody{"f": math.Inf(1)}, wantErr: "extra body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := ValidateExtraBody(tt.extra, reserved)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(raw))
		})
	}
}

func TestMergeExtraBody(t *testing.T) {
	tests := []struct {
		name, body, extra, want, wantErr string
	}{
		{name: "no extra", body: `{"a":1}`, extra: ``, want: `{"a":1}`},
		{name: "empty extra", body: `{"a":1}`, extra: `{}`, want: `{"a":1}`},
		{name: "append", body: `{"a":1}`, extra: `{"b":2}`, want: `{"a":1,"b":2}`},
		{name: "empty body", body: `{}`, extra: `{"b":2}`, want: `{"b":2}`},
		{name: "whitespace", body: " {\"a\":1}\n", extra: ` {"b":2} `, want: `{"a":1,"b":2}`},
		{name: "body not object", body: `[1]`, extra: `{"b":2}`, wantErr: "not a JSON object"},
		{name: "extra not object", body: `{"a":1}`, extra: `[2]`, wantErr: "not a JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MergeExtraBody([]byte(tt.body), []byte(tt.extra))
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.extra == "" || tt.extra == "{}" {
				assert.Equal(t, tt.body, string(got))
				return
			}
			assert.JSONEq(t, tt.want, string(got))
		})
	}
}

// FuzzMergeExtraBody: merging two valid JSON objects with disjoint keys always
// yields a valid JSON object holding the union of both member sets.
func FuzzMergeExtraBody(f *testing.F) {
	f.Add(`{"model":"m"}`, "providerOptions", `{"gateway":{"only":["x"]}}`)
	f.Add(`{}`, "k", `1`)
	f.Add(`{"a":[1,2,{"b":"}"}]}`, "c", `"{"`)
	f.Fuzz(func(t *testing.T, body, key, value string) {
		var bodyObj map[string]json.RawMessage
		if json.Unmarshal([]byte(body), &bodyObj) != nil {
			return
		}
		var v any
		if json.Unmarshal([]byte(value), &v) != nil {
			return
		}
		reserved := make(map[string]struct{}, len(bodyObj))
		for k := range bodyObj {
			reserved[k] = struct{}{}
		}
		extra, err := ValidateExtraBody(ExtraBody{key: v}, reserved)
		if err != nil {
			return // collision or empty key: rejected before merge
		}
		merged, err := MergeExtraBody([]byte(body), extra)
		require.NoError(t, err)
		var out map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(merged, &out), "merged body must be valid JSON: %s", merged)
		assert.Len(t, out, len(bodyObj)+1)
		_, ok := out[key]
		assert.True(t, ok)
	})
}

func TestResponseFacts_MergeAndApply(t *testing.T) {
	var f ResponseFacts
	f.Merge(ResponseFacts{})
	assert.Zero(t, f.CostUSD)
	assert.Nil(t, f.Attributes)

	f.Merge(ResponseFacts{CostUSD: 0.5, Attributes: map[string]string{"gateway.a": "1"}})
	f.Merge(ResponseFacts{Attributes: map[string]string{"gateway.a": "2", "gateway.b": "x"}})
	assert.Equal(t, 0.5, f.CostUSD, "a chunk without cost keeps the earlier cost")
	f.Merge(ResponseFacts{CostUSD: 0.7})
	assert.Equal(t, 0.7, f.CostUSD)
	assert.Equal(t, map[string]string{"gateway.a": "2", "gateway.b": "x"}, f.Attributes)

	md := f.Apply(nil)
	assert.Equal(t, "2", md["gateway.a"])
	md2 := map[string]interface{}{"model": "m"}
	md2 = f.Apply(md2)
	assert.Equal(t, "m", md2["model"])
	assert.Equal(t, "x", md2["gateway.b"])
	assert.Nil(t, ResponseFacts{}.Apply(nil))
}

// A ResponseInspector is a plain func; this pins the signature callers use.
var _ ResponseInspector = func(http.Header, []byte) ResponseFacts { return ResponseFacts{} }
