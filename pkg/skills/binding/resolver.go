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

package binding

import (
	"fmt"
	"sort"

	"github.com/teradata-labs/loom/pkg/skills"
)

// SkillSource holds the library handle the resolver consults. The interface
// keeps the package testable without a real Library instance.
type SkillSource interface {
	List() []*skills.Skill
	Load(name string) (*skills.Skill, error)
}

// ResolvedBinding is the per-skill load decision the resolver emits.
type ResolvedBinding struct {
	// Skill is the resolved library entry.
	Skill *skills.Skill
	// Mode is the effective load policy after defaulting (LAZY when the
	// binding leaves Mode unset).
	Mode skills.SkillBindingMode
	// Priority is the binding's declared priority (higher wins under tight
	// budget; defaults to zero).
	Priority int32
	// MatchKind is how the binding matched this skill (exact / glob / label).
	MatchKind MatchKind
	// Source describes which input produced the binding, for telemetry:
	//   "explicit"        - bindings list, exact name
	//   "glob"            - bindings list, glob match
	//   "label"           - bindings list, label-only match
	//   "legacy_enabled"  - synthesized from EnabledSkills shim
	//   "legacy_default"  - synthesized from "all minus DisabledSkills" shim
	Source string
}

// Resolver runs binding -> skill resolution against a library.
type Resolver struct {
	source SkillSource
}

// NewResolver constructs a Resolver bound to the given library.
func NewResolver(src SkillSource) *Resolver {
	return &Resolver{source: src}
}

// Resolve walks the SkillsConfig bindings (or the legacy shim when bindings
// is empty) and produces the per-skill load decision. The result is sorted
// by skill name for stable iteration order.
//
// Tie-breaking for one skill matched by multiple bindings:
//  1. Exact-name binding wins over glob/label binding.
//  2. Among same-kind matches, higher Priority wins.
//  3. Mode hierarchy ALWAYS > EAGER > LAZY breaks remaining ties.
func (r *Resolver) Resolve(config *skills.SkillsConfig) ([]ResolvedBinding, error) {
	if config == nil || !config.Enabled {
		return nil, nil
	}

	bindings, source, err := selectBindings(config, r.source)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, nil
	}

	all := r.source.List()
	if len(all) == 0 {
		return nil, nil
	}

	// Per-skill candidate list keyed by skill name.
	candidates := map[string][]ResolvedBinding{}
	for i := range bindings {
		b := &bindings[i]
		for _, s := range all {
			res := MatchBinding(b, s)
			if !res.Matched {
				continue
			}
			candidates[s.Name] = append(candidates[s.Name], ResolvedBinding{
				Skill:     s,
				Mode:      defaultMode(b.Mode),
				Priority:  b.Priority,
				MatchKind: res.Kind,
				Source:    sourceForKind(source, res.Kind),
			})
		}
	}

	// Tie-break per skill.
	out := make([]ResolvedBinding, 0, len(candidates))
	for _, group := range candidates {
		out = append(out, pickBest(group))
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Skill.Name < out[j].Skill.Name
	})
	return out, nil
}

// selectBindings returns the effective bindings list and a label describing
// which input produced it (for source attribution). When config.Bindings is
// non-empty it is returned verbatim; otherwise the legacy shim synthesizes
// bindings from EnabledSkills (preferred) or from the library minus
// DisabledSkills.
func selectBindings(config *skills.SkillsConfig, src SkillSource) ([]skills.SkillBinding, string, error) {
	if len(config.Bindings) > 0 {
		// Validate any glob patterns up front so we surface bad patterns at
		// resolve time rather than per-skill comparison.
		for _, b := range config.Bindings {
			if err := ValidatePattern(b.Name); err != nil {
				return nil, "", fmt.Errorf("binding %q: %w", b.Name, err)
			}
		}
		return config.Bindings, "explicit", nil
	}

	// Legacy shim: synthesize from EnabledSkills (EAGER) when set.
	// SA1019 is suppressed because this IS the legitimate consumer — the
	// resolver is the only place legacy fields should be read; everywhere
	// else should use Bindings.
	if len(config.EnabledSkills) > 0 { //nolint:staticcheck // SA1019: resolver is the legacy shim consumer
		out := make([]skills.SkillBinding, 0, len(config.EnabledSkills)) //nolint:staticcheck // SA1019: legacy shim
		for _, name := range config.EnabledSkills {                      //nolint:staticcheck // SA1019: legacy shim
			out = append(out, skills.SkillBinding{
				Name: name,
				Mode: skills.BindingEager,
			})
		}
		return out, "legacy_enabled", nil
	}

	// Final fallback: synthesize LAZY bindings for the entire library minus
	// DisabledSkills. Mirrors the v1.2.0 behavior where any discovered skill
	// could trigger; the new default is lazy load on trigger.
	disabled := stringSet(config.DisabledSkills) //nolint:staticcheck // SA1019: resolver is the legacy shim consumer
	all := src.List()
	out := make([]skills.SkillBinding, 0, len(all))
	for _, s := range all {
		if disabled[s.Name] {
			continue
		}
		out = append(out, skills.SkillBinding{
			Name: s.Name,
			Mode: skills.BindingLazy,
		})
	}
	return out, "legacy_default", nil
}

// pickBest selects the winning ResolvedBinding from a list of candidates
// for the same underlying skill, applying the tie-break rules.
func pickBest(group []ResolvedBinding) ResolvedBinding {
	best := group[0]
	for i := 1; i < len(group); i++ {
		c := group[i]
		if betterThan(c, best) {
			best = c
		}
	}
	return best
}

// betterThan implements the tie-break ordering. Returns true when a should
// outrank b for the same skill.
func betterThan(a, b ResolvedBinding) bool {
	// 1. Exact name beats glob beats label.
	if kindRank(a.MatchKind) != kindRank(b.MatchKind) {
		return kindRank(a.MatchKind) > kindRank(b.MatchKind)
	}
	// 2. Higher priority wins.
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	// 3. ALWAYS > EAGER > LAZY when nothing else differs.
	return modeRank(a.Mode) > modeRank(b.Mode)
}

func kindRank(k MatchKind) int {
	switch k {
	case MatchExactName:
		return 3
	case MatchGlob:
		return 2
	case MatchLabel:
		return 1
	default:
		return 0
	}
}

func modeRank(m skills.SkillBindingMode) int {
	switch m {
	case skills.BindingAlways:
		return 3
	case skills.BindingEager:
		return 2
	case skills.BindingLazy:
		return 1
	default:
		return 0
	}
}

// defaultMode maps an unset binding mode to LAZY (the default new-style
// behavior; eager load was the implicit v1.2.0 default and remains opt-in).
func defaultMode(m skills.SkillBindingMode) skills.SkillBindingMode {
	if m == "" {
		return skills.BindingLazy
	}
	return m
}

func sourceForKind(prefix string, kind MatchKind) string {
	if prefix == "explicit" {
		switch kind {
		case MatchExactName:
			return "explicit"
		case MatchGlob:
			return "glob"
		case MatchLabel:
			return "label"
		}
	}
	return prefix
}

func stringSet(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}
