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

package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/jev"
	"github.com/teradata-labs/loom/pkg/memory"
)

// fakeDeciderKey is a fake credential: the collector must never see it.
const fakeDeciderKey = "ts_fake_key_for_endpoint_test_not_real" // #nosec G101 -- test fixture, not a credential

// collector is an httptest server that counts requests and remembers any
// Authorization header it was sent: the attacker's endpoint in the exploit
// the #409 review reproduced.
type collector struct {
	srv      *httptest.Server
	requests atomic.Int32
	sawKey   atomic.Bool
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.requests.Add(1)
		if r.Header.Get("Authorization") != "" {
			c.sawKey.Store(true)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *collector) assertUntouched(t *testing.T) {
	t.Helper()
	assert.Equal(t, int32(0), c.requests.Load(), "the agent-named endpoint received a request")
	assert.False(t, c.sawKey.Load(), "the agent-named endpoint received the server's decider key")
}

// endpointExploitConfig is an agent config that names the collector as its
// decider endpoint, with a live recall band so a decider would be called.
func endpointExploitConfig(name, baseURL string) *loomv1.AgentConfig {
	return &loomv1.AgentConfig{
		Name: name,
		Decision: &loomv1.DecisionConfig{
			Provider: DecisionProviderJev,
			Model:    jev.DefaultModel,
			BaseUrl:  baseURL,
			Bands:    []*loomv1.DecisionBand{{Site: "recall.rerank", ActMin: 0.5}},
		},
	}
}

// The review's exploit: an agent config names an endpoint, and the server's
// env key is sent there. Every path that takes agent config must refuse the
// endpoint with a validation error, and no request may reach the collector.
// Not parallel: it sets the decider key in the environment.
func TestDecisionEndpointFromAgentConfigIsRefused(t *testing.T) {
	t.Setenv(jev.EnvTypeSafeAPIKey, fakeDeciderKey)
	jev.ResetShared()
	t.Cleanup(jev.ResetShared)
	col := newCollector(t)

	t.Run("agent YAML", func(t *testing.T) {
		_, err := LoadConfigFromString(decisionYAMLHead + "  decision:\n    provider: jev\n    base_url: " + col.srv.URL + "\n")
		require.Error(t, err)
		assert.ErrorIs(t, err, decision.ErrEndpointNotServerLevel)
	})

	t.Run("agent YAML with https is refused too", func(t *testing.T) {
		_, err := LoadConfigFromString(decisionYAMLHead + "  decision:\n    provider: llm\n    base_url: https://gateway.example/typesafe\n")
		assert.ErrorIs(t, err, decision.ErrEndpointNotServerLevel, "the rule is about who chooses, not the scheme")
	})

	t.Run("ValidateAgentConfig (CreateAgentFromConfig, loom-mcp create_agent)", func(t *testing.T) {
		err := ValidateAgentConfig(endpointExploitConfig("inline", col.srv.URL))
		assert.ErrorIs(t, err, decision.ErrEndpointNotServerLevel)
	})

	t.Run("registry RegisterConfig + CreateAgent", func(t *testing.T) {
		tmp := t.TempDir()
		reg, err := NewRegistry(RegistryConfig{
			ConfigDir:   tmp,
			DBPath:      filepath.Join(tmp, "r.db"),
			LLMProvider: rerankReplyLLM{reply: "1"},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = reg.Close() })
		reg.RegisterConfig(endpointExploitConfig("exploit", col.srv.URL))
		_, err = reg.CreateAgent(context.Background(), "exploit")
		require.Error(t, err)
		assert.ErrorIs(t, err, decision.ErrEndpointNotServerLevel)
	})

	t.Run("direct WithDecisionConfig builds no decider", func(t *testing.T) {
		cfg := endpointExploitConfig("direct", col.srv.URL)
		ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("direct"),
			WithDecisionConfig(cfg.GetDecision(), &memShadowStore{}))
		assert.Nil(t, ag.decisionRouter, "an agent-named endpoint disables the layer")
		_ = ag.rerankMemories(context.Background(), "q", []*memory.Memory{{ID: "m1", Content: "x"}})
		ag.WaitDecisionShadows()
	})

	col.assertUntouched(t)
}

// The endpoint comes from the server: an agent with a valid decision block
// and no endpoint reaches the server-configured endpoint, and only that one.
func TestDecisionEndpointFromServerSettings(t *testing.T) {
	t.Setenv(jev.EnvTypeSafeAPIKey, fakeDeciderKey)
	jev.ResetShared()
	t.Cleanup(jev.ResetShared)
	prev := jev.CurrentServerEndpoint()
	t.Cleanup(func() { require.NoError(t, jev.SetServerEndpoint(prev)) })

	server := newCollector(t)
	require.NoError(t, jev.SetServerEndpoint(jev.ServerEndpoint{BaseURL: server.srv.URL, AllowInsecureHTTP: true}))

	cfg := endpointExploitConfig("served", "")
	require.NoError(t, ValidateAgentConfig(cfg))
	ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("served"),
		WithDecisionConfig(cfg.GetDecision(), &memShadowStore{}))
	require.NotNil(t, ag.decisionRouter)
	_ = ag.rerankMemories(context.Background(), "q", []*memory.Memory{{ID: "m1", Content: "x"}})
	ag.WaitDecisionShadows()
	assert.Positive(t, server.requests.Load(), "the server-configured endpoint is used")
	assert.True(t, server.sawKey.Load())
}

// ValidateAgentConfig runs the same decision rules as the YAML loader
// (review #409 F9): an alias without allow_alias, act_min out of range and
// duplicate sites are refused on the inline path too.
func TestValidateAgentConfigDecisionRules(t *testing.T) {
	t.Parallel()
	base := func(d *loomv1.DecisionConfig) *loomv1.AgentConfig {
		return &loomv1.AgentConfig{Name: "v", Decision: d}
	}
	tests := []struct {
		name    string
		cfg     *loomv1.DecisionConfig
		wantErr string
	}{
		{name: "nil is off", cfg: nil},
		{name: "valid", cfg: &loomv1.DecisionConfig{Provider: "jev", Model: "jev-1.13.0", Bands: []*loomv1.DecisionBand{{Site: "a", ActMin: 0.9}}}},
		{name: "alias without allow_alias", cfg: &loomv1.DecisionConfig{Provider: "jev", Model: "jev-latest"}, wantErr: "floating alias"},
		{name: "alias with allow_alias", cfg: &loomv1.DecisionConfig{Provider: "jev", Model: "jev-latest", AllowAlias: true}},
		{name: "act_min 7", cfg: &loomv1.DecisionConfig{Provider: "llm", Bands: []*loomv1.DecisionBand{{Site: "a", ActMin: 7}}}, wantErr: "act_min"},
		{name: "act_min negative", cfg: &loomv1.DecisionConfig{Provider: "llm", Bands: []*loomv1.DecisionBand{{Site: "a", ActMin: -0.1}}}, wantErr: "act_min"},
		{name: "duplicate sites", cfg: &loomv1.DecisionConfig{Provider: "llm", Bands: []*loomv1.DecisionBand{{Site: "a"}, {Site: "a"}}}, wantErr: "duplicate site"},
		{name: "empty site", cfg: &loomv1.DecisionConfig{Provider: "llm", Bands: []*loomv1.DecisionBand{{ActMin: 0.5}}}, wantErr: "site is required"},
		{name: "nil band", cfg: &loomv1.DecisionConfig{Provider: "llm", Bands: []*loomv1.DecisionBand{nil}}, wantErr: "band is empty"},
		{name: "bad mode", cfg: &loomv1.DecisionConfig{Provider: "llm", Bands: []*loomv1.DecisionBand{{Site: "a", Mode: 99}}}, wantErr: "mode"},
		{name: "unknown provider", cfg: &loomv1.DecisionConfig{Provider: "gemini"}, wantErr: "decision.provider"},
		{name: "non-canonical provider", cfg: &loomv1.DecisionConfig{Provider: "JEV"}, wantErr: "decision.provider"},
		{name: "negative rpm", cfg: &loomv1.DecisionConfig{Provider: "jev", RequestsPerMinute: -1}, wantErr: "requests_per_minute"},
		{name: "base_url", cfg: &loomv1.DecisionConfig{Provider: "mock", BaseUrl: "https://x.example"}, wantErr: "server-level only"},
		{name: "true_min out of range", cfg: &loomv1.DecisionConfig{Provider: "llm", Bands: []*loomv1.DecisionBand{{Site: "a", TrueMin: 1.5}}}, wantErr: "true_min"},
		{name: "bad aggregate", cfg: &loomv1.DecisionConfig{Provider: "llm", Bands: []*loomv1.DecisionBand{{Site: "a", Aggregate: 9}}}, wantErr: "aggregate"},
		{name: "negative chunk size", cfg: &loomv1.DecisionConfig{Provider: "jev", MaxQuestionsPerRequest: -1}, wantErr: "max_questions_per_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAgentConfig(base(tt.cfg))
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
