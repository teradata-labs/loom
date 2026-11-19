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

package factory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
)

type rowsSource map[string][]*loomv1.ModelInfo

func (s rowsSource) Lookup(_ context.Context, provider, id string) *loomv1.ModelInfo {
	for _, m := range s[provider] {
		if m.GetId() == id {
			return m
		}
	}
	return nil
}

func (s rowsSource) List(context.Context) map[string][]*loomv1.ModelInfo { return s }

// A catalog discovered after the registry was built (the gateway's
// /v1/models, fetched in the background) reaches the model list, so the TUI
// picker and ListAvailableModels show gateway models.
func TestModelRegistry_ListsCatalogsDiscoveredLater(t *testing.T) {
	t.Cleanup(func() { catalog.Register(nil) })
	catalog.Register(nil)
	r := NewModelRegistry()
	assert.Empty(t, r.GetModelsForProvider(vercelgateway.ProviderName))

	rows := []*loomv1.ModelInfo{
		{Id: "openai/gpt-5.5", Provider: vercelgateway.ProviderName, ContextWindow: 400000, ShowInDropdown: true},
		{Id: "anthropic/claude-sonnet-5.5", Provider: vercelgateway.ProviderName, ContextWindow: 1000000, ShowInDropdown: true},
	}
	// Two endpoints (global and a regional host) list the same models.
	catalog.RegisterNamed("vercel-ai-gateway|https://ai-gateway.vercel.sh", rowsSource{vercelgateway.ProviderName: rows})
	catalog.RegisterNamed("vercel-ai-gateway|https://eu.ai-gateway.vercel.sh", rowsSource{vercelgateway.ProviderName: rows})

	got := r.GetModelsForProvider(vercelgateway.ProviderName)
	require.Len(t, got, 2, "listed once each across endpoints")

	f := NewProviderFactory(FactoryConfig{VercelGateway: vercelgateway.Config{APIKey: "vck_test_not_a_real_key", Model: "openai/gpt-5.5"}})
	var gateway []*loomv1.ModelInfo
	for _, m := range r.GetAvailableModels(f) {
		if m.GetProvider() == vercelgateway.ProviderName {
			gateway = append(gateway, m)
		}
	}
	require.Len(t, gateway, 2)
	for _, m := range gateway {
		assert.True(t, m.GetAvailable(), "%s is available with a gateway key", m.GetId())
	}

	// Built-in providers keep their built-in rows.
	assert.NotEmpty(t, r.GetModelsForProvider("anthropic"))
}
