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
	"math"
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
// drift between them. It does not log: a config that is valid but likely not
// what was meant is accepted, and ConfigWarnings lists why for the caller to
// log once.
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
		// NaN fails every comparison, so the range check alone accepts it.
		if math.IsNaN(b.ActMin) || b.ActMin < 0 || b.ActMin > 1 {
			return fmt.Errorf("decision.bands[%d] (%s): act_min must be in [0, 1], got %v", i, b.Site, b.ActMin)
		}
		if math.IsNaN(b.TrueMin) || b.TrueMin < 0 || b.TrueMin > 1 {
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

// actingSites are the sites whose call site acts on Outcome.Act() in this
// release: a non-shadow band there replaces the existing mechanism whenever
// the answer clears act_min. A site not listed here only records shadow rows
// whatever its band says. The names are the constants in pkg/decision/sites,
// which imports this package; TestConfigWarningsActingSitesMatchSites keeps
// the two in step.
var actingSites = map[string]bool{
	"recall.rerank":      true,
	"tool_search.rerank": true,
	"memory.extract":     true,
	"workflow.branch":    true,
	"stage.validation":   true,
	"swarm.tie_break":    true,
	"debate.consensus":   true,
	"skill.route":        true,
}

// tightenOnlySites are the acting sites that read DecisionBandMode and
// default to tighten-only: a band there acts only on a fail (stage.validation)
// or not-reached (debate.consensus) verdict unless it says mode: replace.
var tightenOnlySites = map[string]bool{
	"stage.validation": true,
	"debate.consensus": true,
}

// SiteActs reports whether the call site for site acts on a decider answer
// when its band is not shadow. Judge sites (judge.<id>) act: a decision
// judge returns the decider's verdict.
func SiteActs(site string) bool {
	return actingSites[site] || strings.HasPrefix(site, "judge.")
}

// ConfigWarnings lists settings that are valid but likely not what was
// meant. It does not log; the caller logs each warning once, where the
// decider is built for an agent (ValidateConfig runs on several paths for
// the same config).
//
// Today that is every band without shadow: true, one warning each, naming
// the site, so the first thing an operator sees is which sites are live. At
// a site that acts (SiteActs) the band is LIVE: an answer at or above
// act_min decides there instead of the existing mechanism, and act_min 0
// (also what a band that leaves act_min out gets) acts on every answer
// however uncertain. At a site that only records, the Router still reports
// DECISION_PATH_DECIDER for answers that clear act_min although nothing
// acts on them, so shadow reports show DECIDER rows for decisions the
// decider did not make.
func ConfigWarnings(cfg *loomv1.DecisionConfig) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for i, b := range cfg.Bands {
		if b == nil || b.Shadow {
			continue
		}
		if !SiteActs(b.Site) {
			out = append(out, fmt.Sprintf(
				"decision.bands[%d] (%s): band is not shadow, so answers at or above act_min %g are recorded as DECIDER, "+
					"but nothing acts on decider answers at site %s in this release (the existing mechanism still decides). "+
					"Set shadow: true to only record",
				i, b.Site, b.ActMin, b.Site))
			continue
		}
		msg := fmt.Sprintf(
			"decision.bands[%d] (%s): site %s is LIVE: answers at or above act_min %g act on the site instead of the existing mechanism",
			i, b.Site, b.Site, b.ActMin)
		if tightenOnlySites[b.Site] && b.Mode != loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE {
			msg += " (tighten-only: only a fail or not-reached verdict acts)"
		}
		if b.ActMin == 0 {
			msg += "; act_min is 0, so the site acts on every answer however uncertain"
		}
		out = append(out, msg+". Set shadow: true to only record")
	}
	return out
}
