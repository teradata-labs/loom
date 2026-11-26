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

// Package failurerecovery implements a durable memory of abstract
// problem -> solution lessons learned when the agent recovers from a failure,
// so it stops re-solving the same failures across sessions.
//
// The feature is read two ways: reactively on a tool failure (keyed on the
// abstracted error) and proactively at the start of a user query (keyed on task
// intent). It writes exactly once, on recovery, and only ever persists abstract
// PII-free lessons — never raw arguments, queries, or error strings.
//
// See docs/plans/failure-recovery-memory-spec.md for the full design.
package failurerecovery

import "time"

// Failure classes covered by Stage 1. The class is an open enum: new classes are
// additive (register a detection call site and a class:<value> tag).
const (
	ClassToolExec  = "tool_exec" // tool returned an execution error
	ClassToolArgs  = "tool_args" // malformed/invalid tool arguments
	ClassGuardrail = "guardrail" // guardrail/validation failure
)

// defaultClasses is the initial covered set (spec §11).
var defaultClasses = []string{ClassToolExec, ClassToolArgs, ClassGuardrail}

const (
	defaultSimilarityThreshold float32 = 0.82
	// defaultProactiveThreshold is the minimum cosine similarity a lesson must
	// have against the (optionally LLM-predicted) intent query to be surfaced by
	// the proactive hook. Unlike the strict reactive gate, this is a soft floor:
	// it drops clearly-unrelated lessons so the hook can stay silent when nothing
	// is relevant, while still returning near-misses that the injected directive
	// lets the agent filter by its planned action.
	defaultProactiveThreshold float32 = 0.3
	defaultMaxHints                   = 2
	defaultInitialSalience    float64 = 0.6
	// proactiveTopK is how many nearest lessons the proactive hook retrieves.
	// Proactive is advisory: it fetches up to this many candidates above the
	// soft Proactive.Threshold floor and lets the injected directive filter them
	// (the agent applies only those matching its planned action).
	proactiveTopK = 3
	// maxGuardCandidates caps how many stored lessons the deterministic
	// pre-exec guard scans per planned call. The set is per-agent and small; the
	// cap bounds cost on a pathological history without affecting correctness.
	maxGuardCandidates = 50
	// adviceSkeletonThreshold is the minimum Jaccard overlap between a planned
	// call's structural skeleton and a stored failing call's skeleton (object
	// identifiers removed) for the cross-object pre-exec advisory to fire. A
	// same-shape call on a different table scores 1.0; this floor keeps the
	// advisory specific to genuinely similar call shapes.
	adviceSkeletonThreshold = 0.7
	// adviceMinSkeletonOverlap requires at least this many shared skeleton
	// tokens, so a couple of shared wrapper tokens (tool name + an arg key)
	// alone cannot trip the advisory.
	adviceMinSkeletonOverlap = 3
)

// Config controls the failure-recovery memory feature. The zero value is
// disabled. It is the runtime form of the proto BehaviorConfig.failure_recovery
// field (loomv1.FailureRecoveryConfig); pkg/agent converts proto to Config with
// FailureRecoveryConfigFromProto. Zero-valued fields take the defaults applied
// by withDefaults.
type Config struct {
	// Enabled turns the whole feature on. Off by default.
	Enabled bool `json:"enabled"`
	// UserScoped partitions lessons per calling user when set. Off by default,
	// which keeps the current agent-wide (collective) behavior: every user of
	// an agent shares its lessons. When on, a lesson is stored under the
	// caller's user id and recall only returns that user's own lessons — no
	// cross-user sharing. Callers with no resolvable identity fall back to the
	// single-tenant DefaultUserID bucket, so isolation always has a concrete
	// owner. Existing ownerless lessons are unaffected when this stays off.
	UserScoped bool `json:"user_scoped,omitempty"`
	// PreserveDetails keeps concrete identifiers — table, column, and schema
	// names, object identifiers, error codes, and keywords — in LLM-drafted
	// lessons instead of abstracting them away, which makes lessons more precise
	// and recall sharper. It only takes effect when UserScoped is also on
	// (enforced in NewEngine): identifiers are the author's own schema, safe to
	// store and show back only to that author, but preserving them in shared
	// (non-user-scoped) memory would leak one user's schema to another. Off by
	// default. Applies to the LLM path only; the rule path never emits specifics.
	PreserveDetails bool `json:"preserve_details,omitempty"`
	// SemanticOnly disables the three deterministic signature-matching layers —
	// the resource-gated planning advisory (object/table-name match), the
	// pre-execution guard (exact replay + offending-token overlap), and the
	// cross-object pre-execution advice (skeleton similarity) — leaving only
	// semantic retrieval: embedding recall with FTS5 fallback, on both the
	// reactive and proactive paths. Signatures are still stored (the field only
	// gates reads), so turning it back off re-enables matching against lessons
	// learned while it was on. Off by default.
	SemanticOnly bool `json:"semantic_only,omitempty"`
	// SimilarityThreshold is the minimum cosine similarity for a reactive match.
	SimilarityThreshold float32 `json:"similarity_threshold,omitempty"`
	// MaxHints caps how many hints are injected per read.
	MaxHints int `json:"max_hints,omitempty"`
	// Classes is the covered failure-class allowlist.
	Classes []string `json:"classes,omitempty"`
	// InitialSalience is the salience assigned to a newly learned lesson.
	InitialSalience float64 `json:"initial_salience,omitempty"`
	// Proactive configures per-query pitfall pre-warning (gated separately).
	Proactive ProactiveConfig `json:"proactive,omitempty"`
	// LLMAbstraction optionally drafts richer lessons with an LLM before the
	// deterministic leak gate. Off by default; requires a model wired via
	// WithLLMAbstractor. Falls back to rule abstraction whenever the model is
	// absent, errors, times out, or its output fails the gate. See abstractor.go.
	LLMAbstraction LLMAbstractionConfig `json:"llm_abstraction,omitempty"`
}

// LLMAbstractionConfig gates optional LLM-drafted lessons.
type LLMAbstractionConfig struct {
	// Enabled turns on LLM drafting. Requires a model via WithLLMAbstractor.
	Enabled bool `json:"enabled"`
	// TimeoutSec bounds each LLM draft call. Defaults to 8s.
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// llmTimeout returns the per-draft LLM timeout, applying the default when unset.
func (c Config) llmTimeout() time.Duration {
	if c.LLMAbstraction.TimeoutSec > 0 {
		return time.Duration(c.LLMAbstraction.TimeoutSec) * time.Second
	}
	return defaultLLMAbstractTimeout
}

// ProactiveConfig gates the per-query intent-based pitfall pre-warning.
type ProactiveConfig struct {
	Enabled bool `json:"enabled"`
	// Threshold is the minimum cosine similarity for a proactive match. It is a
	// soft floor (default defaultProactiveThreshold): lessons less similar than
	// this are dropped so the hook returns fewer than top-K — or nothing — when
	// no lesson is genuinely relevant, instead of always padding to top-K with
	// the least-far rows. Set explicitly in agent YAML to tune recall vs. noise.
	Threshold float32 `json:"threshold,omitempty"`
	// LLMIntent, when set, rewrites the proactive recall query: instead of
	// matching the raw user intent (task-space) against stored lessons
	// (failure-space), the agent's LLM first predicts the task's likely pitfall
	// in failure-space vocabulary, and that prediction becomes the query. This
	// aligns both sides of the comparison and sharply improves proactive recall,
	// at the cost of one extra LLM call per user turn (best-effort: falls back to
	// the raw intent on any error, timeout, or "no pitfall" verdict). Requires a
	// model wired via WithLLMAbstractor. Off by default.
	LLMIntent bool `json:"llm_intent,omitempty"`
}

// withDefaults returns a copy of c with unset fields filled from the spec
// defaults. It never enables the feature or proactive — those are explicit.
func (c Config) withDefaults() Config {
	if c.SimilarityThreshold <= 0 {
		c.SimilarityThreshold = defaultSimilarityThreshold
	}
	if c.Proactive.Threshold <= 0 {
		c.Proactive.Threshold = defaultProactiveThreshold
	}
	if c.MaxHints <= 0 {
		c.MaxHints = defaultMaxHints
	}
	if c.InitialSalience <= 0 {
		c.InitialSalience = defaultInitialSalience
	}
	if len(c.Classes) == 0 {
		c.Classes = append([]string(nil), defaultClasses...)
	}
	return c
}

// classAllowed reports whether the given failure class is in the covered set.
func (c Config) classAllowed(class string) bool {
	for _, allowed := range c.Classes {
		if allowed == class {
			return true
		}
	}
	return false
}
