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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Model ids only these tests list, so the named catalog source below cannot
// change what any other test looks up.
const (
	budgetTestSmallModel   = "acme/budget-window-small-128k"
	budgetTestUnknownModel = "acme/budget-window-not-listed"
	budgetTestLargeModel   = "acme/budget-window-large-1m"
	budgetTestLargeWindow  = 1_000_000
	budgetTestCatalogName  = "agent-test|gateway-budget-window"
	budgetTestSmallWindow  = 128_000
	budgetTestSmallMaxOut  = 16_000 // above a tenth of the window, so the reservation takes it
)

// gatewayBudgetCatalog answers for the gateway namespace only, as
// vercelgateway.CatalogSource does.
type gatewayBudgetCatalog map[string]*loomv1.ModelInfo

func (c gatewayBudgetCatalog) Lookup(_ context.Context, provider, modelID string) *loomv1.ModelInfo {
	if !llm.IsVercelAIGateway(provider) {
		return nil
	}
	return c[modelID]
}

func (c gatewayBudgetCatalog) List(context.Context) map[string][]*loomv1.ModelInfo { return nil }

// registerBudgetCatalog installs a gateway catalog that lists
// budgetTestSmallModel with a 128K window, and removes it at cleanup.
func registerBudgetCatalog(t *testing.T) {
	t.Helper()
	catalog.RegisterNamed(budgetTestCatalogName, gatewayBudgetCatalog{
		budgetTestSmallModel: {
			Id:              budgetTestSmallModel,
			Provider:        llm.ProviderVercelAIGateway,
			ContextWindow:   budgetTestSmallWindow,
			MaxOutputTokens: budgetTestSmallMaxOut,
		},
		budgetTestLargeModel: {
			Id:              budgetTestLargeModel,
			Provider:        llm.ProviderVercelAIGateway,
			ContextWindow:   budgetTestLargeWindow,
			MaxOutputTokens: budgetTestSmallMaxOut,
		},
	})
	t.Cleanup(func() { catalog.RegisterNamed(budgetTestCatalogName, nil) })
}

// namedLLM is an LLM that reports a chosen provider and model.
type namedLLM struct{ provider, model string }

func (n *namedLLM) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{Content: "ok"}, nil
}
func (n *namedLLM) Name() string  { return n.provider }
func (n *namedLLM) Model() string { return n.model }

func TestGatewayBudgetWindow(t *testing.T) {
	registerBudgetCatalog(t)
	tests := []struct {
		name       string
		provider   string
		model      string
		configured int
		served     int
		wantWindow int
		wantSource budgetWindowSource
	}{
		{"explicit max_context_tokens wins over the catalog", llm.ProviderVercelAIGateway, budgetTestSmallModel, 64_000, 0, 64_000, budgetWindowConfigured},
		{"explicit max_context_tokens wins over max_model_context_tokens", llm.ProviderVercelAIGateway, budgetTestSmallModel, 64_000, 32_000, 64_000, budgetWindowConfigured},
		{"max_model_context_tokens wins over the catalog", llm.ProviderVercelAIGateway, budgetTestSmallModel, 0, 32_000, 32_000, budgetWindowServed},
		{"catalog window used", llm.ProviderVercelAIGateway, budgetTestSmallModel, 0, 0, budgetTestSmallWindow, budgetWindowCatalog},
		{"catalog window used through the alias", llm.ProviderVercelAIGatewayAlias, budgetTestSmallModel, 0, 0, budgetTestSmallWindow, budgetWindowCatalog},
		{"catalog does not list the model", llm.ProviderVercelAIGateway, budgetTestUnknownModel, 0, 0, 0, budgetWindowCatalogMiss},
		{"catalog window above the default never raises the budget", llm.ProviderVercelAIGateway, budgetTestLargeModel, 0, 0, 0, budgetWindowCatalogNotSmaller},
		{"explicit max_context_tokens can raise the budget", llm.ProviderVercelAIGateway, budgetTestLargeModel, budgetTestLargeWindow, 0, budgetTestLargeWindow, budgetWindowConfigured},
		{"other providers keep the default", "anthropic", budgetTestSmallModel, 0, 0, 0, budgetWindowNotGateway},
		{"other providers keep the default with a served window", "ollama", budgetTestSmallModel, 0, 32_000, 0, budgetWindowNotGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			window, src := gatewayBudgetWindow(tt.provider, tt.model, tt.configured, tt.served)
			assert.Equal(t, tt.wantWindow, window, "window")
			assert.Equal(t, tt.wantSource, src, "source")
		})
	}
}

// budgetOf builds an agent on llmProvider with cfg and returns the window a
// new session's memory budgets against.
func budgetOf(t *testing.T, llmProvider LLMProvider, cfg *Config) (int, *Agent) {
	t.Helper()
	ag := NewAgent(&mockBackend{}, llmProvider, WithConfig(cfg))
	session := ag.memory.GetOrCreateSession(context.Background(), "budget-window-session")
	sm, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "session should use SegmentedMemory")
	return sm.GetTokenBudgetMax(), ag
}

// F4 on PR #668: a gateway model whose catalog entry lists 128K was given the
// memory's 200K default, so the fold let the conversation outgrow the model.
// The catalog fetch failing is the same case as the catalog missing the
// model: Lookup has no entry, and the default applies with a warning.
func TestNewAgent_GatewayBudgetWindow(t *testing.T) {
	registerBudgetCatalog(t)
	tests := []struct {
		name         string
		provider     string
		model        string
		cfg          Config
		wantBudget   int
		wantConfig   int
		wantWarnings int
	}{
		{
			name: "catalog window used", provider: llm.ProviderVercelAIGateway, model: budgetTestSmallModel,
			wantBudget: budgetTestSmallWindow, wantConfig: budgetTestSmallWindow,
		},
		{
			name: "explicit max_context_tokens wins", provider: llm.ProviderVercelAIGateway, model: budgetTestSmallModel,
			cfg: Config{MaxContextTokens: 90_000}, wantBudget: 90_000, wantConfig: 90_000,
		},
		{
			name: "max_model_context_tokens wins over the catalog", provider: llm.ProviderVercelAIGateway, model: budgetTestSmallModel,
			cfg: Config{MaxModelContextTokens: 32_000}, wantBudget: 32_000, wantConfig: 32_000,
		},
		{
			name: "catalog missing the model or fetch failed: default and a warning", provider: llm.ProviderVercelAIGateway, model: budgetTestUnknownModel,
			wantBudget: defaultContextWindowTokens, wantConfig: 0, wantWarnings: 1,
		},
		{
			name: "catalog window above the default keeps the default, no warning", provider: llm.ProviderVercelAIGateway, model: budgetTestLargeModel,
			wantBudget: defaultContextWindowTokens, wantConfig: 0,
		},
		{
			name: "non-gateway provider unchanged", provider: "anthropic", model: budgetTestSmallModel,
			wantBudget: defaultContextWindowTokens, wantConfig: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			restore := zap.ReplaceGlobals(zap.New(core))
			defer restore()

			cfg := tt.cfg
			budget, ag := budgetOf(t, &namedLLM{provider: tt.provider, model: tt.model}, &cfg)
			assert.Equal(t, tt.wantBudget, budget, "session memory budget")
			assert.Equal(t, tt.wantConfig, ag.config.MaxContextTokens, "agent MaxContextTokens")

			warned := logs.FilterMessageSnippet("no context window for the model")
			require.Equal(t, tt.wantWarnings, warned.Len(), "catalog-miss warnings")
			if tt.wantWarnings > 0 {
				assert.Equal(t, tt.model, warned.All()[0].ContextMap()["model"], "the warning names the model")
			}
		})
	}
}

// The reservation follows the resolved window as a configured window's does:
// at least a tenth of it, more when max_tokens or the reserve the builder
// resolved (a configured reserved_output_tokens, or the catalog max output)
// is larger.
func TestNewAgent_GatewayBudgetWindowReservation(t *testing.T) {
	registerBudgetCatalog(t)
	tests := []struct {
		name string
		cfg  Config
		want int
	}{
		{
			// The registry path: max_tokens 4096 and the reserve it resolved
			// with no window (4096). A configured 128K window reserves 12800.
			name: "registry-resolved reserve gets the tenth-of-window floor",
			cfg:  Config{MaxTokens: 4096, ReservedOutputTokens: 4096},
			want: budgetTestSmallWindow / 10,
		},
		{
			name: "catalog max output above the floor is kept",
			cfg:  Config{},
			want: budgetTestSmallMaxOut,
		},
		{
			name: "configured reserve above the floor is kept",
			cfg:  Config{MaxTokens: 4096, ReservedOutputTokens: 30_000},
			want: 30_000,
		},
		{
			// Documented limit: the agent cannot tell a configured reserve
			// from a derived one, so a reserve below the floor is raised.
			name: "configured reserve below the floor is raised to it",
			cfg:  Config{MaxTokens: 4096, ReservedOutputTokens: 8_000},
			want: budgetTestSmallWindow / 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			_, ag := budgetOf(t, &namedLLM{provider: llm.ProviderVercelAIGateway, model: budgetTestSmallModel}, &cfg)
			assert.Equal(t, tt.want, ag.config.ReservedOutputTokens, "reply reservation")
			want := EffectiveOutputReservation(llm.ProviderVercelAIGateway, budgetTestSmallModel, tt.cfg.MaxTokens, 0, budgetTestSmallWindow)
			assert.GreaterOrEqual(t, ag.config.ReservedOutputTokens, want, "never below a configured window's reservation")
		})
	}
}
