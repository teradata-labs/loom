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

package vercelgateway

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInspectPayload(t *testing.T) {
	const model = "anthropic/claude-sonnet-4.5"
	tests := []struct {
		name      string
		payload   string
		wantCost  float64
		wantAttrs map[string]string
	}{
		{name: "nothing of interest", payload: `{"choices":[{"delta":{"content":"hi"}}]}`},
		{name: "not json", payload: `data: [DONE]`},
		{name: "chat completion id and same model", payload: `{"id":"gen_01ABC","model":"anthropic/claude-sonnet-4.5"}`,
			wantAttrs: map[string]string{AttrGenerationID: "gen_01ABC", AttrFinalModel: model, AttrFallbackUsed: "false"}},
		{name: "upstream echoes bare model id", payload: `{"id":"msg_1","model":"claude-sonnet-4.5"}`,
			wantAttrs: map[string]string{AttrFinalModel: "claude-sonnet-4.5", AttrFallbackUsed: "false"}},
		{name: "fallback model served", payload: `{"id":"gen_2","model":"openai/gpt-5"}`,
			wantAttrs: map[string]string{AttrGenerationID: "gen_2", AttrFinalModel: "openai/gpt-5", AttrFallbackUsed: "true"}},
		{name: "anthropic message_start", payload: `{"type":"message_start","message":{"id":"gen_3","model":"anthropic/claude-sonnet-4.5"}}`,
			wantAttrs: map[string]string{AttrGenerationID: "gen_3", AttrFinalModel: model, AttrFallbackUsed: "false"}},
		{name: "provider metadata camelCase", payload: `{"providerMetadata":{"gateway":{"cost":"0.0045405","generationId":"gen_9",
			"routing":{"finalProvider":"bedrock","modelAttemptCount":1,"totalProviderAttemptCount":2}}}}`,
			wantCost:  0.0045405,
			wantAttrs: map[string]string{AttrGenerationID: "gen_9", AttrFinalProvider: "bedrock", AttrAttemptCount: "2"}},
		{name: "provider metadata snake_case, numeric cost, model fallback", payload: `{"provider_metadata":{"gateway":{"cost":0.25,
			"routing":{"finalProvider":"openai","modelAttemptCount":2}}}}`,
			wantCost:  0.25,
			wantAttrs: map[string]string{AttrFinalProvider: "openai", AttrAttemptCount: "2", AttrFallbackUsed: "true"}},
		{name: "negative cost ignored", payload: `{"providerMetadata":{"gateway":{"cost":"-1"}}}`},
		{name: "nan cost ignored", payload: `{"providerMetadata":{"gateway":{"cost":"NaN"}}}`},
		{name: "null cost ignored", payload: `{"providerMetadata":{"gateway":{"cost":null}}}`},
		{name: "non-gen id not reported", payload: `{"id":"chatcmpl-123","providerMetadata":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := inspectPayload(model, []byte(tt.payload))
			assert.InDelta(t, tt.wantCost, f.CostUSD, 1e-15)
			assert.Equal(t, tt.wantAttrs, f.Attributes)
		})
	}
}

func TestParseCost(t *testing.T) {
	tests := []struct {
		raw  string
		want float64
		ok   bool
	}{
		{`"0.01"`, 0.01, true},
		{`0.5`, 0.5, true},
		{`" 2 "`, 2, true},
		{`"0"`, 0, false},
		{`""`, 0, false},
		{`"Inf"`, 0, false},
		{`"1e400"`, 0, false},
		{`"abc"`, 0, false},
		{`null`, 0, false},
		{``, 0, false},
		{`"unterminated`, 0, false},
	}
	for _, tt := range tests {
		got, ok := parseCost(json.RawMessage(tt.raw))
		assert.Equal(t, tt.ok, ok, tt.raw)
		assert.InDelta(t, tt.want, got, 1e-15, tt.raw)
	}
}

// FuzzInspectPayload: arbitrary payloads never panic, and any reported cost
// is finite and positive.
func FuzzInspectPayload(f *testing.F) {
	for _, s := range []string{
		`{"id":"gen_1","model":"x/y"}`,
		`{"providerMetadata":{"gateway":{"cost":"0.1","routing":{"finalProvider":"a"}}}}`,
		`{"message":{"id":"gen_","model":""}}`,
		`{"provider_metadata":{"gateway":{"cost":1e308}}}`,
		`not json "gen_`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, payload string) {
		facts := inspectPayload("anthropic/claude-sonnet-4.5", []byte(payload))
		if facts.CostUSD != 0 {
			require.Positive(t, facts.CostUSD)
			require.False(t, math.IsInf(facts.CostUSD, 0) || math.IsNaN(facts.CostUSD))
		}
	})
}
