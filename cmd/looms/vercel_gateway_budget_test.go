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

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
)

// memoryDefaultWindow is the window agent memory uses when none is resolved
// (agent.defaultContextWindowTokens).
const memoryDefaultWindow = 200_000

// sessionBudget returns the window a new session of an agent built on mem
// budgets against.
func sessionBudget(t *testing.T, mem *agent.Memory) int {
	t.Helper()
	session := mem.GetOrCreateSession(context.Background(), "budget-session")
	sm, ok := session.SegmentedMem.(*agent.SegmentedMemory)
	require.True(t, ok, "session should use SegmentedMemory")
	return sm.GetTokenBudgetMax()
}

// F4 on PR #668, as `looms serve` builds an agent: the server builds the
// agent's gateway client, then the agent at once. The catalog says the model
// has a 128K window. The agent budgets against 128K, not the 200K default,
// unless the operator set a window. A catalog fetch that fails, or that does
// not list the model, keeps the default, logs a warning naming the model, and
// does not fail the build.
func TestServeGatewayAgent_BudgetsAgainstCatalogWindow(t *testing.T) {
	const (
		listed   = "acme/serve-budget-listed"
		unlisted = "acme/serve-budget-unlisted"
		large    = "acme/serve-budget-large"
	)
	catalogBody := `{"data":[` +
		`{"id":"` + listed + `","type":"language","context_window":128000,"max_tokens":8000,"pricing":{"input":"0.000001","output":"0.000002"}},` +
		`{"id":"` + large + `","type":"language","context_window":1000000,"max_tokens":8000,"pricing":{"input":"0.000001","output":"0.000002"}}]}`
	tests := []struct {
		name         string
		modelsStatus int
		model        string
		llm          *loomv1.LLMConfig
		wantBudget   int
		wantWarning  bool
	}{
		{name: "catalog window used", modelsStatus: http.StatusOK, model: listed, wantBudget: 128_000},
		{name: "explicit max_context_tokens wins", modelsStatus: http.StatusOK, model: listed,
			llm: &loomv1.LLMConfig{MaxContextTokens: 100_000}, wantBudget: 100_000},
		{name: "explicit max_model_context_tokens wins", modelsStatus: http.StatusOK, model: listed,
			llm: &loomv1.LLMConfig{MaxModelContextTokens: 64_000}, wantBudget: 64_000},
		{name: "catalog window above the default keeps the default", modelsStatus: http.StatusOK, model: large, wantBudget: memoryDefaultWindow},
		{name: "catalog missing the model", modelsStatus: http.StatusOK, model: unlisted, wantBudget: memoryDefaultWindow, wantWarning: true},
		{name: "catalog fetch error", modelsStatus: http.StatusInternalServerError, model: listed, wantBudget: memoryDefaultWindow, wantWarning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() { catalog.Register(nil) })
			isolateGatewayEnv(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != vercelgateway.ModelsPath {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tt.modelsStatus)
				if tt.modelsStatus == http.StatusOK {
					_, _ = io.WriteString(w, catalogBody)
				}
			}))
			defer srv.Close()

			core, logs := observer.New(zap.WarnLevel)
			restore := zap.ReplaceGlobals(zap.New(core))
			defer restore()

			server := &Config{LLM: LLMConfig{VercelGatewayEndpoint: srv.URL, VercelGatewayAPIKey: testGatewayKey}}
			llmCfg := &loomv1.LLMConfig{}
			if tt.llm != nil {
				llmCfg = tt.llm
			}
			llmCfg.Provider, llmCfg.Model = vercelgateway.ProviderName, tt.model
			client, err := createLLMProviderFromProtoConfig(llmCfg, server, zap.L())
			require.NoError(t, err, "an unreachable or failing catalog never fails the build")

			mem := agent.NewMemory()
			agentCfg := serveAgentConfig(&loomv1.AgentConfig{Name: "gw-budget", Llm: llmCfg}, false)
			_ = agent.NewAgent(&mockBackend{}, client, agent.WithMemory(mem), agent.WithConfig(agentCfg))

			assert.Equal(t, tt.wantBudget, sessionBudget(t, mem), "session memory budget")
			warned := logs.FilterMessageSnippet("no context window for the model")
			if tt.wantWarning {
				require.Equal(t, 1, warned.Len(), "one catalog-miss warning")
				assert.Equal(t, tt.model, warned.All()[0].ContextMap()["model"], "the warning names the model")
			} else {
				assert.Zero(t, warned.Len(), "no catalog-miss warning")
			}
		})
	}
}

// A gateway that does not answer /v1/models holds client builds only for one
// bounded wait per endpoint. Builds made while the first fetch runs share
// that deadline: concurrent builds end together, and a build after the
// deadline does not wait again. Once the fetch has finished, no build waits.
func TestEnsureVercelGatewayCatalog_WaitIsBounded(t *testing.T) {
	t.Cleanup(func() { catalog.Register(nil) })
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(unblock)

	core, logs := observer.New(zap.WarnLevel)
	logger := zap.New(core)
	const wait = 200 * time.Millisecond

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ensureVercelGatewayCatalogWithin(srv.URL, logger, wait)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, wait-20*time.Millisecond, "builds wait for the first fetch")
	assert.Less(t, elapsed, 3*wait, "concurrent builds end within the bound (the next check catches a per-call wait)")
	require.GreaterOrEqual(t, logs.FilterMessageSnippet("first fetch has not finished").Len(), 1, "a slow first fetch is logged")

	start = time.Now()
	ensureVercelGatewayCatalogWithin(srv.URL, logger, time.Minute)
	assert.Less(t, time.Since(start), wait/2, "after the shared deadline, a build does not wait again")

	unblock()
	require.Eventually(t, func() bool {
		return logs.FilterMessageSnippet("catalog fetch failed").Len() > 0
	}, 5*time.Second, 10*time.Millisecond, "the first fetch finishes and its failure is logged")

	start = time.Now()
	ensureVercelGatewayCatalogWithin(srv.URL, logger, time.Minute)
	assert.Less(t, time.Since(start), time.Second, "after the first fetch, a build does not wait")
}
