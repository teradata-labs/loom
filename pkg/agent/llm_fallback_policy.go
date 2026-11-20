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

package agent

import (
	"strings"

	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
)

// LLMProviderAllowsFallback reports whether an agent role whose LLM provider
// cannot be built may run on a substitute LLM (the agent's main LLM, or the
// server default). It is the one rule every construction path applies: the
// agent registry, and looms serve at cold start, hot reload and runtime
// create.
//
// It returns false for:
//   - bedrock-mantle: an operator selects Mantle to keep model traffic in
//     their AWS account;
//   - vercel-ai-gateway (and its alias): the routing block can restrict
//     upstreams or require zero data retention.
//
// A silent move to another provider would bypass either choice.
func LLMProviderAllowsFallback(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(vercelgateway.NormalizeProviderName(provider))) {
	case "bedrock-mantle", vercelgateway.ProviderName:
		return false
	}
	return true
}
