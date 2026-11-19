// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package llm

import "strings"

const (
	// ProviderVercelAIGateway is the canonical provider name of the Vercel AI
	// Gateway (llm.provider: vercel-ai-gateway). It is also the name the
	// gateway client reports from Name() and the catalog namespace its model
	// ids are registered under.
	ProviderVercelAIGateway = "vercel-ai-gateway"
	// ProviderVercelAIGatewayAlias is the short name accepted in
	// configuration. NormalizeProviderName maps it to ProviderVercelAIGateway.
	ProviderVercelAIGatewayAlias = "vercel"
)

// NormalizeProviderName returns the canonical spelling of a configured
// provider name, so that limiter keys, catalog namespaces and span attributes
// see one name per provider. It maps the Vercel AI Gateway alias "vercel" and
// the canonical name, in any case and with surrounding space, to
// ProviderVercelAIGateway. It returns every other name unchanged.
//
// Config loaders (pkg/agent, cmd/looms) and the provider factory call it.
// It lives here, and not in the gateway package, so that code that only
// compares provider names does not import a concrete provider.
func NormalizeProviderName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ProviderVercelAIGateway, ProviderVercelAIGatewayAlias:
		return ProviderVercelAIGateway
	}
	return name
}

// IsVercelAIGateway reports whether name selects the Vercel AI Gateway
// provider (the canonical name or its alias, any case).
func IsVercelAIGateway(name string) bool {
	return NormalizeProviderName(name) == ProviderVercelAIGateway
}
