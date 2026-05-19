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

// Package binding evaluates SkillBinding records against a skill library and
// produces a deterministic load-policy decision for each skill an agent
// declaratively attaches. It is the per-agent attachment layer introduced by
// the skills overhaul; see docs/architecture/skills-overhaul.md.
package binding

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/teradata-labs/loom/pkg/skills"
)

// MatchKind describes how a binding hit a skill, used for tie-breaking and
// for the source attribution attached to each ResolvedBinding.
type MatchKind int

const (
	// MatchNone is the zero value used when no match was found.
	MatchNone MatchKind = iota
	// MatchExactName indicates an exact match against either Skill.Name or
	// the fully-qualified path. Exact-name matches outrank glob matches for
	// the same underlying skill.
	MatchExactName
	// MatchGlob indicates a path.Match-style glob match against the
	// fully-qualified path (parent_index_path + "/" + name) or, when
	// parent_index_path is empty, against the bare name.
	MatchGlob
	// MatchLabel indicates a label-only match: the binding has no name set
	// and only label_match is used to select skills.
	MatchLabel
)

// String renders the kind as a stable identifier for telemetry.
func (k MatchKind) String() string {
	switch k {
	case MatchExactName:
		return "exact"
	case MatchGlob:
		return "glob"
	case MatchLabel:
		return "label"
	default:
		return "none"
	}
}

// MatchResult captures whether a binding matched a skill and how.
type MatchResult struct {
	Matched bool
	Kind    MatchKind
	// FQN is the fully-qualified name the matcher considered for this skill,
	// useful for diagnostics. Equal to skill.Name when ParentIndexPath empty.
	FQN string
}

// MatchBinding evaluates a single SkillBinding against a single Skill.
// The decision is deterministic and pure; no I/O.
//
// Match precedence (within a single binding -> single skill comparison):
//  1. Exact name match against Skill.Name (legacy compat).
//  2. Exact name match against the FQN (parent_index_path/name).
//  3. Glob match against the FQN, when the binding name contains *, ?, or [.
//  4. Label-only match when Binding.Name is empty and label_match is set.
//
// Label and version filters apply on top of any name/glob match: a binding
// that names a skill but whose label_match excludes it returns Matched=false.
func MatchBinding(b *skills.SkillBinding, s *skills.Skill) MatchResult {
	if b == nil || s == nil {
		return MatchResult{}
	}

	fqn := fullyQualifiedName(s)
	res := MatchResult{FQN: fqn}

	switch {
	case b.Name == "" && len(b.LabelMatch) > 0:
		// Label-only binding.
		if !labelsMatch(b.LabelMatch, s.Labels) {
			return res
		}
		res.Matched = true
		res.Kind = MatchLabel

	case b.Name == "":
		// No name and no labels means the binding is meaningless; treat as
		// a no-op rather than erroring so legacy YAML with empty entries
		// degrades gracefully.
		return res

	case b.Name == s.Name || b.Name == fqn:
		res.Matched = true
		res.Kind = MatchExactName

	case hasGlobMeta(b.Name):
		// Try FQN first (parent_index_path/name) then bare name. Bare-name
		// fallback lets simple globs like "teradata-*" continue to match
		// skills that gained a parent_index_path after the binding was
		// authored — important since path.Match's "*" doesn't cross "/".
		// path.Match returns ErrBadPattern only on malformed patterns; we
		// swallow the error because the resolver validates patterns at
		// config-load time.
		matched, err := path.Match(b.Name, fqn)
		if (err != nil || !matched) && fqn != s.Name {
			matched, err = path.Match(b.Name, s.Name)
		}
		if err != nil || !matched {
			return res
		}
		res.Matched = true
		res.Kind = MatchGlob

	default:
		return res
	}

	// Apply secondary filters that can downgrade an otherwise-positive match.
	if len(b.LabelMatch) > 0 && res.Kind != MatchLabel {
		if !labelsMatch(b.LabelMatch, s.Labels) {
			return MatchResult{FQN: fqn}
		}
	}
	if b.MinVersion != "" {
		ok, err := versionAtLeast(s.Version, b.MinVersion)
		if err != nil || !ok {
			return MatchResult{FQN: fqn}
		}
	}
	return res
}

// fullyQualifiedName composes the path used for FQN matching. When the skill
// declares no parent_index_path, the FQN is just its bare name.
func fullyQualifiedName(s *skills.Skill) string {
	if s.ParentIndexPath == "" {
		return s.Name
	}
	return strings.TrimRight(s.ParentIndexPath, "/") + "/" + s.Name
}

// hasGlobMeta returns true when the string contains any path.Match
// metacharacter and should be treated as a pattern instead of a literal.
func hasGlobMeta(p string) bool {
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '*', '?', '[':
			return true
		}
	}
	return false
}

// labelsMatch returns true when every key/value pair in want is present in
// have with equal value (AND semantics across keys).
func labelsMatch(want, have map[string]string) bool {
	if len(want) == 0 {
		return true
	}
	if len(have) == 0 {
		return false
	}
	for k, v := range want {
		got, ok := have[k]
		if !ok || got != v {
			return false
		}
	}
	return true
}

// versionAtLeast returns true when actual >= minimum. Both are parsed as
// dot-separated numeric segments; missing trailing segments treat as zero
// (so "1.2" >= "1.2.0"). Non-numeric segments cause an error.
//
// We intentionally avoid pulling golang.org/x/mod/semver for this single
// >= comparison; skill versions in this codebase are simple numeric semver.
func versionAtLeast(actual, minimum string) (bool, error) {
	if actual == "" || minimum == "" {
		return true, nil
	}
	a, err := parseVersion(actual)
	if err != nil {
		return false, fmt.Errorf("actual version %q: %w", actual, err)
	}
	m, err := parseVersion(minimum)
	if err != nil {
		return false, fmt.Errorf("min version %q: %w", minimum, err)
	}
	for i := 0; i < len(a) || i < len(m); i++ {
		var av, mv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(m) {
			mv = m[i]
		}
		if av != mv {
			return av > mv, nil
		}
	}
	return true, nil
}

func parseVersion(v string) ([]int, error) {
	v = strings.TrimPrefix(v, "v")
	// Drop a trailing pre-release / build-metadata suffix; we treat them as
	// equal-or-greater than the bare version for binding purposes.
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("empty segment")
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("non-numeric segment %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

// ValidatePattern returns an error when the binding's name uses an invalid
// path.Match pattern. Resolvers should call this at config-load time.
func ValidatePattern(name string) error {
	if !hasGlobMeta(name) {
		return nil
	}
	if _, err := path.Match(name, ""); err != nil {
		return fmt.Errorf("invalid glob pattern %q: %w", name, err)
	}
	return nil
}
