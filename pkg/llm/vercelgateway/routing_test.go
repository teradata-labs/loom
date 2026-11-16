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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestRouting_Validate(t *testing.T) {
	tests := []struct {
		name    string
		r       *Routing
		wantErr string
	}{
		{name: "nil", r: nil},
		{name: "zero", r: &Routing{}},
		{name: "full", r: &Routing{Order: []string{"bedrock", "anthropic"}, Only: []string{"bedrock", "vertex"},
			Sort: SortTTFT, FallbackModels: []string{"openai/gpt-5"}, ZeroDataRetention: true, Caching: CachingAuto}},
		{name: "bad sort", r: &Routing{Sort: "fast"}, wantErr: "sort"},
		{name: "bad caching", r: &Routing{Caching: "on"}, wantErr: "caching"},
		{name: "empty slug", r: &Routing{Order: []string{""}}, wantErr: "provider slug"},
		{name: "upper slug", r: &Routing{Only: []string{"Bedrock"}}, wantErr: "provider slug"},
		{name: "slug with space", r: &Routing{Only: []string{"bed rock"}}, wantErr: "provider slug"},
		{name: "duplicate order", r: &Routing{Order: []string{"a", "a"}}, wantErr: "twice"},
		{name: "bad fallback", r: &Routing{FallbackModels: []string{"gpt-5"}}, wantErr: "fallback_models"},
		{name: "duplicate fallback", r: &Routing{FallbackModels: []string{"a/b", "a/b"}}, wantErr: "twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.r.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestRouting_ExtraBody(t *testing.T) {
	assert.Nil(t, (*Routing)(nil).ExtraBody())
	assert.Nil(t, (&Routing{}).ExtraBody())

	r := &Routing{Order: []string{"bedrock"}, Only: []string{"bedrock", "anthropic"}, Sort: SortCost,
		FallbackModels: []string{"openai/gpt-5"}, ZeroDataRetention: true, Caching: CachingAuto}
	raw, err := json.Marshal(r.ExtraBody())
	require.NoError(t, err)
	assert.JSONEq(t, `{"providerOptions":{"gateway":{
		"order":["bedrock"],"only":["bedrock","anthropic"],"sort":"cost",
		"models":["openai/gpt-5"],"zeroDataRetention":true,"caching":"auto"}}}`, string(raw))

	// Only set fields are sent.
	raw, err = json.Marshal((&Routing{Sort: SortTPS}).ExtraBody())
	require.NoError(t, err)
	assert.JSONEq(t, `{"providerOptions":{"gateway":{"sort":"tps"}}}`, string(raw))

	// The body holds copies: mutating the config afterwards changes nothing.
	body := r.ExtraBody()
	r.Order[0] = "mutated"
	gw := body["providerOptions"].(map[string]any)["gateway"].(map[string]any)
	assert.Equal(t, []string{"bedrock"}, gw["order"])
}

func TestRouting_ProtoRoundTrip(t *testing.T) {
	assert.Nil(t, RoutingFromProto(nil))
	assert.Nil(t, (*Routing)(nil).ToProto())
	p := &loomv1.LLMGatewayRouting{Order: []string{"a"}, Only: []string{"b"}, Sort: "ttft",
		FallbackModels: []string{"x/y"}, ZeroDataRetention: true, Caching: "auto"}
	r := RoutingFromProto(p)
	assert.Equal(t, &Routing{Order: []string{"a"}, Only: []string{"b"}, Sort: "ttft",
		FallbackModels: []string{"x/y"}, ZeroDataRetention: true, Caching: "auto"}, r)
	assert.True(t, proto.Equal(p, r.ToProto()))
	assert.True(t, (&Routing{}).IsZero())
	assert.False(t, r.IsZero())
}
