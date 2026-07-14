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

// Package discovery composes the per-turn skill discovery pipeline:
//  1. Slash command fast path (always available, deterministic)
//  2. Hierarchical PageIndex-style router (LLM-driven, when enabled)
//  3. FTS5 keyword fallback (always available)
//
// The result is filtered by the resolved bindings so an agent never sees a
// skill it has not declaratively bound. Discovery is the top-level glue
// that the agent layer (Phase 9) calls instead of the legacy
// Orchestrator.MatchSkills.
package discovery

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/skills/binding"
	"github.com/teradata-labs/loom/pkg/skills/index"
)

// Discovery composes binding resolution, the hierarchical router, and the
// FTS5 fallback into one Discover() call. It is goroutine-safe: the
// underlying library/router/cache all are.
type Discovery struct {
	library  *skills.Library
	resolver *binding.Resolver
	router   *index.Router
	cache    *index.Cache
	tracer   observability.Tracer
	logger   *zap.Logger

	// resolved binding cache: empty config -> empty result. Each agent
	// session keeps its resolution results stable for the duration of a
	// SkillsConfig snapshot. Keyed on a config hash.
	bindCache *bindingCache
}

// Option configures a Discovery during construction.
type Option func(*Discovery)

// WithRouter wires the hierarchical PageIndex router. Optional; when nil,
// router-first behavior degrades to FTS5-only.
func WithRouter(r *index.Router) Option {
	return func(d *Discovery) { d.router = r }
}

// WithCache wires the per-session decision cache used by the router.
func WithCache(c *index.Cache) Option {
	return func(d *Discovery) { d.cache = c }
}

// WithTracer attaches an observability tracer.
func WithTracer(t observability.Tracer) Option {
	return func(d *Discovery) {
		if t != nil {
			d.tracer = t
		}
	}
}

// WithLogger attaches a zap logger.
func WithLogger(l *zap.Logger) Option {
	return func(d *Discovery) {
		if l != nil {
			d.logger = l
		}
	}
}

// New constructs a Discovery with the given library and resolver. router
// and cache are optional; when both are absent, Discovery falls back to
// the legacy slash + FTS5 path.
func New(library *skills.Library, resolver *binding.Resolver, opts ...Option) *Discovery {
	if library == nil {
		panic("discovery.New: library must not be nil")
	}
	if resolver == nil {
		panic("discovery.New: resolver must not be nil")
	}
	d := &Discovery{
		library:   library,
		resolver:  resolver,
		tracer:    observability.NewNoOpTracer(),
		logger:    zap.NewNop(),
		bindCache: newBindingCache(),
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Candidate is one element of a discovery result.
type Candidate struct {
	// Skill is the resolved skill record.
	Skill *skills.Skill
	// Mode is the binding's effective load policy for this skill.
	Mode skills.SkillBindingMode
	// Confidence is in [0, 1]; 1.0 for slash matches, lower for keyword
	// or router matches. Mirrors skills.MatchResult.Confidence so callers
	// can use Discovery output anywhere they used to use MatchSkills.
	Confidence float64
	// TriggerType describes the path that surfaced this candidate:
	// "slash_command", "router", or "fts".
	TriggerType string
	// TriggerValue is the slash command, the matched keyword, or the
	// router's chosen path, depending on TriggerType.
	TriggerValue string
	// TriggerArgs is the free-text remainder the user typed after a slash
	// command (e.g. "/profile demo.table" -> "demo.table"). It is only set
	// for TriggerType == "slash_command"; empty for keyword/router/always.
	// Callers surface it to the LLM so the skill uses the arguments the user
	// already supplied instead of re-asking for them.
	TriggerArgs string
}

// Discover runs the per-turn pipeline and returns the surviving
// candidates. The slice is bounded by config.MaxConcurrentSkills (default 3).
//
// The order of operations is:
//  1. Resolve bindings (cached per config snapshot).
//  2. Slash-command fast path (highest confidence, bypasses router and FTS).
//  3. Router walk (when configured AND config.RouterEnabled is true).
//  4. FTS5 fallback (always available; runs when slash+router yield zero).
//  5. Filter to bound skills, dedupe, cap by MaxConcurrentSkills, sort by
//     confidence descending.
func (d *Discovery) Discover(ctx context.Context, sessionID, message string,
	config *skills.SkillsConfig) ([]*Candidate, error) {
	ctx, span := d.tracer.StartSpan(ctx, "skills.discovery.discover")
	defer d.tracer.EndSpan(span)

	if config == nil || !config.Enabled {
		return nil, nil
	}

	resolved, err := d.bindCache.GetOrResolve(config, d.resolver)
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return nil, nil
	}

	// Build the eligibility set + per-skill mode lookup once for this turn.
	eligible := make(map[string]bool, len(resolved))
	modeByName := make(map[string]skills.SkillBindingMode, len(resolved))
	for _, rb := range resolved {
		eligible[rb.Skill.Name] = true
		modeByName[rb.Skill.Name] = rb.Mode
	}

	// Phase 1: slash command fast path.
	if cmd, rest := skills.ParseSlashCommand(message); cmd != "" {
		if skill, ok := d.library.FindBySlashCommand(cmd); ok {
			if eligible[skill.Name] {
				out := []*Candidate{{
					Skill:        skill,
					Mode:         modeByName[skill.Name],
					Confidence:   1.0,
					TriggerType:  "slash_command",
					TriggerValue: cmd,
					TriggerArgs:  rest,
				}}
				recordActivatedSkills(span, out)
				return out, nil
			}
		}
	}

	maxConcurrent := config.MaxConcurrentSkills
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}

	// Phase 2: router-first. Only fires when config opts in AND a router
	// is wired in. Router errors are swallowed (the router itself returns
	// no error) and we fall through to FTS5.
	var candidates []*Candidate
	if d.router != nil && config.EffectiveRouterEnabled() {
		bindingsHash := index.HashBindings(toSkillsBindings(resolved))
		routed, err := d.router.Route(ctx, sessionID, message, eligible, bindingsHash)
		if err != nil {
			d.logger.Debug("router returned error; falling through to FTS5",
				zap.String("session", sessionID),
				zap.Error(err))
		}
		for _, s := range routed {
			candidates = append(candidates, &Candidate{
				Skill:        s,
				Mode:         modeByName[s.Name],
				Confidence:   0.85,
				TriggerType:  "router",
				TriggerValue: index.SkillPath(s),
			})
		}
	}

	// Phase 3: FTS5 fallback. Runs when the router was disabled, errored,
	// or returned zero candidates. Min confidence threshold from config.
	if len(candidates) == 0 {
		minConf := config.MinAutoConfidence
		if minConf <= 0 {
			minConf = 0.7
		}
		scored := d.library.FindByKeywords(message)
		for _, ss := range scored {
			if ss.Score < minConf {
				continue
			}
			if !eligible[ss.Skill.Name] {
				continue
			}
			candidates = append(candidates, &Candidate{
				Skill:        ss.Skill,
				Mode:         modeByName[ss.Skill.Name],
				Confidence:   ss.Score,
				TriggerType:  "fts",
				TriggerValue: "keywords",
			})
		}
	}

	// Phase 4: include ALWAYS-mode bindings unconditionally (not filtered
	// by trigger). They surface every turn; useful for "always-on" skills
	// like company guardrails.
	alreadyPicked := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		alreadyPicked[c.Skill.Name] = true
	}
	for _, rb := range resolved {
		if rb.Mode != skills.BindingAlways {
			continue
		}
		if alreadyPicked[rb.Skill.Name] {
			continue
		}
		candidates = append(candidates, &Candidate{
			Skill:        rb.Skill,
			Mode:         rb.Mode,
			Confidence:   1.0,
			TriggerType:  "always",
			TriggerValue: "always-binding",
		})
	}

	// Sort by confidence desc, name asc for stable output, then cap.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Confidence != candidates[j].Confidence {
			return candidates[i].Confidence > candidates[j].Confidence
		}
		return candidates[i].Skill.Name < candidates[j].Skill.Name
	})
	if len(candidates) > maxConcurrent {
		candidates = candidates[:maxConcurrent]
	}
	recordActivatedSkills(span, candidates)
	return candidates, nil
}

// recordActivatedSkills annotates the discovery span with the skills that
// fired this turn (the candidates the agent goes on to activate). The
// loom-cloud span exporter persists span attributes verbatim into
// llm_spans.attributes, so this lets trace consumers — and the dashboard —
// attribute a turn to specific skills without re-deriving them. It is a no-op
// when the tracer produced a nil span (e.g. NoOpTracer).
func recordActivatedSkills(span *observability.Span, candidates []*Candidate) {
	if span == nil {
		return
	}
	span.SetAttribute("skills.activated.count", len(candidates))
	if len(candidates) == 0 {
		return
	}
	names := make([]string, 0, len(candidates))
	detail := make([]map[string]interface{}, 0, len(candidates))
	for _, c := range candidates {
		if c == nil || c.Skill == nil {
			continue
		}
		names = append(names, c.Skill.Name)
		detail = append(detail, map[string]interface{}{
			"name":       c.Skill.Name,
			"trigger":    c.TriggerType,
			"confidence": c.Confidence,
		})
	}
	span.SetAttribute("skills.activated.names", strings.Join(names, ","))
	if b, err := json.Marshal(detail); err == nil {
		span.SetAttribute("skills.activated.detail", string(b))
	}
}

// toSkillsBindings extracts the SkillBinding records from the resolver's
// output for hashing into the cache key.
func toSkillsBindings(resolved []binding.ResolvedBinding) []skills.SkillBinding {
	out := make([]skills.SkillBinding, 0, len(resolved))
	for _, rb := range resolved {
		out = append(out, skills.SkillBinding{
			Name:     rb.Skill.Name,
			Mode:     rb.Mode,
			Priority: rb.Priority,
		})
	}
	return out
}

// =============================================================================
// Binding resolution cache
// =============================================================================

// bindingCache memoizes binding resolutions per config snapshot. Keyed on
// a hash of the SkillsConfig fields the resolver actually consults.
type bindingCache struct {
	mu     sync.Mutex
	last   string
	cached []binding.ResolvedBinding
}

func newBindingCache() *bindingCache {
	return &bindingCache{}
}

// GetOrResolve returns cached bindings when the config snapshot matches the
// last invocation; otherwise calls the resolver and caches the result.
//
// We hash on (Bindings, EnabledSkills, DisabledSkills, Enabled). Other
// SkillsConfig fields don't affect resolution.
func (b *bindingCache) GetOrResolve(config *skills.SkillsConfig,
	resolver *binding.Resolver) ([]binding.ResolvedBinding, error) {
	key := configFingerprint(config)

	b.mu.Lock()
	if key == b.last && b.cached != nil {
		out := b.cached
		b.mu.Unlock()
		return out, nil
	}
	b.mu.Unlock()

	resolved, err := resolver.Resolve(config)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	b.last = key
	b.cached = resolved
	b.mu.Unlock()
	return resolved, nil
}

// configFingerprint hashes the resolver-relevant fields of SkillsConfig.
// Order-insensitive within slice fields.
func configFingerprint(c *skills.SkillsConfig) string {
	if c == nil {
		return ""
	}
	bs := make([]skills.SkillBinding, len(c.Bindings))
	copy(bs, c.Bindings)
	es := append([]string(nil), c.EnabledSkills...)  //nolint:staticcheck // legacy shim
	ds := append([]string(nil), c.DisabledSkills...) //nolint:staticcheck // legacy shim
	sort.Strings(es)
	sort.Strings(ds)
	return index.HashBindings(bs) + "|" +
		joinForFP(es) + "|" + joinForFP(ds) + "|" + boolStr(c.Enabled)
}

func joinForFP(xs []string) string {
	out := ""
	for _, x := range xs {
		out += x + ","
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
