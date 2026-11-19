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
	"strings"
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
		{name: "camelCase slug (live: vertexAnthropic)", r: &Routing{Only: []string{"vertexAnthropic", "claudeaws"}}},
		{name: "slug with space", r: &Routing{Only: []string{"bed rock"}}, wantErr: "provider slug"},
		{name: "duplicate order", r: &Routing{Order: []string{"a", "a"}}, wantErr: "twice"},
		{name: "bad fallback", r: &Routing{FallbackModels: []string{"gpt-5"}}, wantErr: "fallback_models"},
		{name: "duplicate fallback", r: &Routing{FallbackModels: []string{"a/b", "a/b"}}, wantErr: "twice"},

		{name: "byok field names any case (viper lowercases looms.yaml keys)", r: &Routing{BYOK: []BYOKCredential{{Provider: "vertex",
			Env:    map[string]string{"googlecredentials.privatekey": "LOOM_BYOK_GCP_PK"},
			Values: map[string]string{"project": "p", "location": "l", "googlecredentials.clientemail": "e"}}}}},
		{name: "byok anthropic", r: &Routing{BYOK: []BYOKCredential{{Provider: "anthropic", Env: map[string]string{"apiKey": "LOOM_BYOK_ANTHROPIC_API_KEY"}}}}},
		{name: "byok azure", r: &Routing{BYOK: []BYOKCredential{{Provider: "azure", Env: map[string]string{"apiKey": "LOOM_BYOK_AZ_KEY"},
			Values: map[string]string{"resourceName": "my-res"}, ModelMappings: map[string]string{"openai/gpt-5.5": "gpt55-deploy"}}}}},
		{name: "byok vertex", r: &Routing{BYOK: []BYOKCredential{{Provider: "vertex",
			Env:    map[string]string{"googleCredentials.privateKey": "LOOM_BYOK_GCP_PK"},
			Values: map[string]string{"project": "p", "location": "us-east5", "googleCredentials.clientEmail": "sa@p.iam.gserviceaccount.com"}}}}},
		{name: "byok bedrock optional region", r: &Routing{BYOK: []BYOKCredential{{Provider: "bedrock",
			Env: map[string]string{"accessKeyId": "LOOM_BYOK_AK", "secretAccessKey": "LOOM_BYOK_SK"}}}}},
		{name: "byok unknown provider", r: &Routing{BYOK: []BYOKCredential{{Provider: "groq", Env: map[string]string{"apiKey": "LOOM_BYOK_K"}}}}, wantErr: "BYOK credentials for"},
		{name: "byok unknown field", r: &Routing{BYOK: []BYOKCredential{{Provider: "anthropic", Env: map[string]string{"apiKey": "LOOM_BYOK_K", "token": "LOOM_BYOK_T"}}}}, wantErr: `no field "token"`},
		{name: "byok secret as literal", r: &Routing{BYOK: []BYOKCredential{{Provider: "anthropic", Values: map[string]string{"apiKey": "sk-ant-literal"}}}}, wantErr: "is a secret"},
		{name: "byok missing required", r: &Routing{BYOK: []BYOKCredential{{Provider: "azure", Env: map[string]string{"apiKey": "LOOM_BYOK_K"}}}}, wantErr: `missing required field "resourceName"`},
		{name: "byok variable without the prefix", r: &Routing{BYOK: []BYOKCredential{{Provider: "anthropic", Env: map[string]string{"apiKey": "DATABASE_PASSWORD"}}}}, wantErr: "start with LOOM_BYOK_"},
		{name: "byok bad env name", r: &Routing{BYOK: []BYOKCredential{{Provider: "openai", Env: map[string]string{"apiKey": "$KEY"}}}}, wantErr: "not an environment variable name"},
		{name: "byok field in env and values", r: &Routing{BYOK: []BYOKCredential{{Provider: "bedrock",
			Env: map[string]string{"accessKeyId": "LOOM_BYOK_AK", "secretAccessKey": "LOOM_BYOK_SK", "region": "LOOM_BYOK_R"}, Values: map[string]string{"region": "us-east-1"}}}}, wantErr: "both env and values"},
		{name: "byok mappings off azure", r: &Routing{BYOK: []BYOKCredential{{Provider: "openai", Env: map[string]string{"apiKey": "LOOM_BYOK_K"},
			ModelMappings: map[string]string{"a/b": "c"}}}}, wantErr: "azure credentials only"},
		{name: "provider timeout ok", r: &Routing{ProviderTimeoutsMS: map[string]int{"anthropic": 15000}}},
		{name: "provider timeout low", r: &Routing{ProviderTimeoutsMS: map[string]int{"anthropic": 999}}, wantErr: "between 1000 and 789000"},
		{name: "provider timeout high", r: &Routing{ProviderTimeoutsMS: map[string]int{"anthropic": 789001}}, wantErr: "between"},
		{name: "provider timeout bad slug", r: &Routing{ProviderTimeoutsMS: map[string]int{"bad slug": 2000}}, wantErr: "not a provider slug"},
		{name: "region zone", r: &Routing{InferenceRegion: &InferenceRegion{Scope: "zone", GeoRegion: "eu"}}},
		{name: "region global", r: &Routing{InferenceRegion: &InferenceRegion{Scope: "global"}}},
		{name: "region global with geo", r: &Routing{InferenceRegion: &InferenceRegion{Scope: "global", GeoRegion: "eu"}}, wantErr: "takes no geo_region"},
		{name: "region bad geo", r: &Routing{InferenceRegion: &InferenceRegion{Scope: "zone", GeoRegion: "mars"}}, wantErr: "us, eu, au or jp"},
		{name: "region bad scope", r: &Routing{InferenceRegion: &InferenceRegion{Scope: "planet"}}, wantErr: "scope"},
		{name: "hq include", r: &Routing{Headquarters: []string{"US", "GB"}}},
		{name: "hq exclude", r: &Routing{Headquarters: []string{"!CN", "!RU"}}},
		{name: "hq mixed", r: &Routing{Headquarters: []string{"US", "!CN"}}, wantErr: "mixes"},
		{name: "hq lowercase", r: &Routing{Headquarters: []string{"us"}}, wantErr: "ISO 3166-1"},
		{name: "user too long", r: &Routing{User: strings.Repeat("u", 257)}, wantErr: "at most 256"},
		{name: "too many tags", r: &Routing{Tags: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}}, wantErr: "at most 10"},
		{name: "empty tag", r: &Routing{Tags: []string{""}}, wantErr: "1 to 64"},
		{name: "long tag", r: &Routing{Tags: []string{strings.Repeat("t", 65)}}, wantErr: "1 to 64"},
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

func TestRouting_ResolveExtraBody(t *testing.T) {
	render := func(r *Routing, env map[string]string) (string, error) {
		body, err := r.ResolveExtraBody(func(k string) string { return env[k] })
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		return string(raw), nil
	}
	for _, r := range []*Routing{nil, {}} {
		body, err := r.ResolveExtraBody(nil)
		require.NoError(t, err)
		assert.Nil(t, body)
	}

	r := &Routing{Order: []string{"bedrock"}, Only: []string{"bedrock", "anthropic"}, Sort: SortCost,
		FallbackModels: []string{"openai/gpt-5"}, ZeroDataRetention: true, Caching: CachingAuto}
	raw, err := render(r, nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"providerOptions":{"gateway":{
		"order":["bedrock"],"only":["bedrock","anthropic"],"sort":"cost",
		"models":["openai/gpt-5"],"zeroDataRetention":true,"caching":"auto"}}}`, raw)

	// Only set fields are sent.
	raw, err = render(&Routing{Sort: SortTPS}, nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"providerOptions":{"gateway":{"sort":"tps"}}}`, raw)

	// The body holds copies: mutating the config afterwards changes nothing.
	body, err := r.ResolveExtraBody(nil)
	require.NoError(t, err)
	r.Order[0] = "mutated"
	gw := body["providerOptions"].(map[string]any)["gateway"].(map[string]any)
	assert.Equal(t, []string{"bedrock"}, gw["order"])

	// Every new key, in the documented shapes.
	off := false
	full := &Routing{
		BYOK: []BYOKCredential{
			{Provider: "anthropic", Env: map[string]string{"apiKey": "LOOM_BYOK_K1"}},
			{Provider: "anthropic", Env: map[string]string{"apiKey": "LOOM_BYOK_K2"}},
			{Provider: "vertex", Env: map[string]string{"googleCredentials.privateKey": "LOOM_BYOK_PK"},
				Values: map[string]string{"project": "p", "location": "l", "googleCredentials.clientEmail": "e"}},
			{Provider: "azure", Env: map[string]string{"apiKey": "LOOM_BYOK_AZ"}, Values: map[string]string{"resourceName": "res"},
				ModelMappings: map[string]string{"openai/gpt-5.5": "dep-b", "openai/gpt-5": "dep-a"}},
		},
		ProviderTimeoutsMS:     map[string]int{"anthropic": 15000},
		SessionAffinity:        &off,
		DisallowPromptTraining: true,
		InferenceRegion:        &InferenceRegion{Scope: "zone", GeoRegion: "us"},
		Headquarters:           []string{"US"},
		User:                   "user-42",
		Tags:                   []string{"feature:chat"},
	}
	env := map[string]string{"LOOM_BYOK_K1": "first-key", "LOOM_BYOK_K2": "second-key", "LOOM_BYOK_PK": "pem", "LOOM_BYOK_AZ": "az-key"}
	raw, err = render(full, env)
	require.NoError(t, err)
	assert.JSONEq(t, `{"providerOptions":{"gateway":{
		"byok":{
			"anthropic":[{"apiKey":"first-key"},{"apiKey":"second-key"}],
			"vertex":[{"project":"p","location":"l","googleCredentials":{"privateKey":"pem","clientEmail":"e"}}],
			"azure":[{"apiKey":"az-key","resourceName":"res","modelMappings":[
				{"gatewayModelSlug":"openai/gpt-5","customModelId":"dep-a"},
				{"gatewayModelSlug":"openai/gpt-5.5","customModelId":"dep-b"}]}]},
		"providerTimeouts":{"byok":{"anthropic":15000}},
		"sessionAffinity":false,
		"disallowPromptTraining":true,
		"inferenceRegion":{"scope":"zone","geoRegion":"us"},
		"headquarters":["US"],
		"user":"user-42",
		"tags":["feature:chat"]}}}`, raw)

	// A missing variable fails without echoing the entry or any value.
	_, err = render(full, map[string]string{"LOOM_BYOK_K1": "first-key"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anthropic.apiKey: the environment variable named under env is not set")
	assert.NotContains(t, err.Error(), "first-key")
	assert.NotContains(t, err.Error(), "K2")
}

func TestRouting_ProtoRoundTrip(t *testing.T) {
	assert.Nil(t, RoutingFromProto(nil))
	assert.Nil(t, (*Routing)(nil).ToProto())
	affinity := true
	p := &loomv1.LLMGatewayRouting{Order: []string{"a"}, Only: []string{"b"}, Sort: "ttft",
		FallbackModels: []string{"x/y"}, ZeroDataRetention: true, Caching: "auto",
		Byok: []*loomv1.GatewayBYOKCredential{{Provider: "azure", Env: map[string]string{"apiKey": "LOOM_BYOK_AZ"},
			Values: map[string]string{"resourceName": "r"}, ModelMappings: map[string]string{"a/b": "c"}}},
		ProviderTimeoutsMs: map[string]int32{"azure": 5000}, SessionAffinity: &affinity,
		DisallowPromptTraining: true, InferenceRegion: &loomv1.GatewayInferenceRegion{Scope: "zone", GeoRegion: "eu"},
		Headquarters: []string{"!CN"}, User: "u", Tags: []string{"t"}}
	r := RoutingFromProto(p)
	assert.Equal(t, &Routing{Order: []string{"a"}, Only: []string{"b"}, Sort: "ttft",
		FallbackModels: []string{"x/y"}, ZeroDataRetention: true, Caching: "auto",
		BYOK: []BYOKCredential{{Provider: "azure", Env: map[string]string{"apiKey": "LOOM_BYOK_AZ"},
			Values: map[string]string{"resourceName": "r"}, ModelMappings: map[string]string{"a/b": "c"}}},
		ProviderTimeoutsMS: map[string]int{"azure": 5000}, SessionAffinity: &affinity,
		DisallowPromptTraining: true, InferenceRegion: &InferenceRegion{Scope: "zone", GeoRegion: "eu"},
		Headquarters: []string{"!CN"}, User: "u", Tags: []string{"t"}}, r)
	assert.True(t, proto.Equal(p, r.ToProto()))
	unset := RoutingFromProto(&loomv1.LLMGatewayRouting{})
	assert.Nil(t, unset.SessionAffinity, "an unset optional stays unset")
	assert.True(t, unset.IsZero())
	assert.True(t, (&Routing{}).IsZero())
	assert.False(t, r.IsZero())
}

// Agent YAML expands ${VAR} before it is parsed, so a mistaken
// env: {apiKey: ${SECRET}} delivers the secret itself as the "variable name".
// Neither validation nor resolution may echo it.
func TestRouting_BYOKErrorsNeverEchoTheEnvEntry(t *testing.T) {
	const anthropicKey = "sk-ant-api03-fake-test-value-0123456789" // fake, test only
	const awsKeyID = "AKIAFAKETESTVALUE0123"                       // fake, shaped like a variable name

	err := (&Routing{BYOK: []BYOKCredential{{Provider: "anthropic", Env: map[string]string{"apiKey": anthropicKey}}}}).Validate()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), anthropicKey)
	assert.Contains(t, err.Error(), "not an environment variable name")

	// An AWS key id is shaped like a variable name; the LOOM_BYOK_ prefix
	// rule rejects it, still without echoing it.
	r := &Routing{BYOK: []BYOKCredential{{Provider: "bedrock", Env: map[string]string{"accessKeyId": awsKeyID, "secretAccessKey": "LOOM_BYOK_SK_NAME"}}}}
	err = r.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start with LOOM_BYOK_")
	assert.NotContains(t, err.Error(), awsKeyID)

	// Resolution of a prefixed name that is unset does not echo the name.
	r = &Routing{BYOK: []BYOKCredential{{Provider: "anthropic", Env: map[string]string{"apiKey": "LOOM_BYOK_UNSET_FOR_TEST"}}}}
	require.NoError(t, r.Validate())
	_, err = r.ResolveExtraBody(func(string) string { return "" })
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "LOOM_BYOK_UNSET_FOR_TEST")
	assert.NotContains(t, err.Error(), awsKeyID)
}
