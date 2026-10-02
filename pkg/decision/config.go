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

package decision

import (
	"errors"
	"fmt"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Provider names DecisionConfig.provider accepts. An empty provider is off.
const (
	ProviderOff  = "off"
	ProviderLLM  = "llm"
	ProviderMock = "mock"
	ProviderJev  = "jev"
)

// providers is the set ValidateConfig accepts, in canonical (lower-case)
// spelling. The YAML loader normalises case before validating; a proto
// config arriving over gRPC must already be canonical.
var providers = map[string]bool{"": true, ProviderOff: true, ProviderLLM: true, ProviderMock: true, ProviderJev: true}

// floatingAliases are model names that resolve to whatever release the
// provider currently serves. A config names what it runs, so these are
// rejected unless allow_alias is set. The response carries the resolved
// version either way.
var floatingAliases = map[string]bool{"jev-latest": true, "jev-preview": true, "latest": true, "typesafe-ai/jev": true}

// IsFloatingAlias reports whether model is a floating alias (case-insensitive).
func IsFloatingAlias(model string) bool {
	return floatingAliases[strings.ToLower(strings.TrimSpace(model))]
}

// ErrEndpointNotServerLevel is returned when a DecisionConfig that came from
// an agent or judge names a decider endpoint. The endpoint decides where the
// server's decider credential is sent, so only the operator may set it:
// looms.yaml decision.base_url (LOOM_DECISION_BASE_URL) or TYPESAFE_BASE_URL.
var ErrEndpointNotServerLevel = errors.New("decision.base_url is not accepted in agent or judge config: " +
	"the decider endpoint is server-level only (set decision.base_url in looms.yaml, " +
	"LOOM_DECISION_BASE_URL, or TYPESAFE_BASE_URL)")

// ValidateConfig checks a DecisionConfig from any source: agent YAML, an
// inline AgentConfig (CreateAgentFromConfig, loom-mcp create_agent), or a
// judge's JudgeConfig. A nil config is valid (the layer is off). Every path
// that accepts a DecisionConfig runs this one function, so the rules cannot
// drift between them.
func ValidateConfig(cfg *loomv1.DecisionConfig) error {
	if cfg == nil {
		return nil
	}
	if !providers[cfg.Provider] {
		return fmt.Errorf("decision.provider %q: must be one of off, llm, mock, jev", cfg.Provider)
	}
	if strings.TrimSpace(cfg.BaseUrl) != "" {
		return ErrEndpointNotServerLevel
	}
	if IsFloatingAlias(cfg.Model) && !cfg.AllowAlias {
		return fmt.Errorf("decision.model %q is a floating alias; pin a version or set allow_alias: true", cfg.Model)
	}
	if cfg.TimeoutMs < 0 {
		return fmt.Errorf("decision.timeout_ms must be >= 0, got %d", cfg.TimeoutMs)
	}
	if cfg.MaxPerSession < 0 {
		return fmt.Errorf("decision.max_per_session must be >= 0, got %d", cfg.MaxPerSession)
	}
	if cfg.MaxCostUsdPerSession < 0 {
		return fmt.Errorf("decision.max_cost_usd_per_session must be >= 0, got %v", cfg.MaxCostUsdPerSession)
	}
	if cfg.RequestsPerMinute < 0 {
		return fmt.Errorf("decision.requests_per_minute must be >= 0, got %d", cfg.RequestsPerMinute)
	}
	if cfg.MaxQuestionsPerRequest < 0 {
		return fmt.Errorf("decision.max_questions_per_request must be >= 0, got %d", cfg.MaxQuestionsPerRequest)
	}
	seen := make(map[string]bool, len(cfg.Bands))
	for i, b := range cfg.Bands {
		if b == nil {
			return fmt.Errorf("decision.bands[%d]: band is empty", i)
		}
		if strings.TrimSpace(b.Site) == "" {
			return fmt.Errorf("decision.bands[%d]: site is required", i)
		}
		if seen[b.Site] {
			return fmt.Errorf("decision.bands[%d]: duplicate site %q", i, b.Site)
		}
		seen[b.Site] = true
		if b.ActMin < 0 || b.ActMin > 1 {
			return fmt.Errorf("decision.bands[%d] (%s): act_min must be in [0, 1], got %v", i, b.Site, b.ActMin)
		}
		if b.TrueMin < 0 || b.TrueMin > 1 {
			return fmt.Errorf("decision.bands[%d] (%s): true_min must be in [0, 1], got %v", i, b.Site, b.TrueMin)
		}
		switch b.Aggregate {
		case loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_UNSPECIFIED,
			loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_MIN,
			loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION:
		default:
			return fmt.Errorf("decision.bands[%d] (%s): aggregate %v must be min or per_question", i, b.Site, b.Aggregate)
		}
		switch b.Mode {
		case loomv1.DecisionBandMode_DECISION_BAND_MODE_UNSPECIFIED,
			loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE,
			loomv1.DecisionBandMode_DECISION_BAND_MODE_TIGHTEN_ONLY:
		default:
			return fmt.Errorf("decision.bands[%d] (%s): mode %v must be replace or tighten_only", i, b.Site, b.Mode)
		}
	}
	return nil
}
