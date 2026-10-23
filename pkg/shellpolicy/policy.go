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

// Package shellpolicy decides which shell commands may run without a person's
// approval. A Policy lists the shell builtins and programs that run freely,
// with per-program argument rules (Profile), and the programs no approval can
// lift. Analyze judges a command string before it runs (the command-policy
// admission hook); CheckLaunch and CheckBuiltin judge each program launch and
// builtin while it runs, on the real expanded arguments (the jailed runner).
// Both halves use the same tables, so a rule cannot exist in one and be
// missing from the other.
//
// The policy decides what starts. It does not contain what a started program
// does: an allowed program runs with the operating-system permissions of the
// process that starts it.
package shellpolicy

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Policy is a named command policy.
type Policy struct {
	// Name identifies the policy in reasons and in hook bindings.
	Name string
	// Builtins are the shell builtins that run without asking.
	Builtins map[string]bool
	// Programs are the programs that run without asking, each with the rules
	// its arguments must satisfy.
	Programs map[string]*Profile
	// Never are programs no approval can lift, matched on the program's base
	// name (so /usr/bin/sudo is sudo).
	Never map[string]bool

	// flat is this policy as additions to Readonly, so a separate process
	// (the jail) can rebuild it from data (FromSpec).
	flat Spec
}

// Roots are the directories commands may read and write. Paths must be
// absolute and symlink-resolved; Read always includes Write.
type Roots struct {
	Read  []string
	Write []string
}

// Grant is what a person approves when they approve a command: the names and
// paths the static check flagged, which are exactly what the command text
// shows. A program in Programs runs for that one call with any arguments.
type Grant struct {
	Programs  []string `json:"programs,omitempty"`
	Builtins  []string `json:"builtins,omitempty"`
	ReadPaths []string `json:"read_paths,omitempty"`
}

// MergeGrants combines the *Grant values among an approved call's admission
// grants (shuttle.AdmissionGrantsFromContext), ignoring anything else. It
// returns nil when there are none.
func MergeGrants(grants []any) *Grant {
	var out *Grant
	for _, g := range grants {
		gr, ok := g.(*Grant)
		if !ok || gr == nil {
			continue
		}
		if out == nil {
			out = &Grant{}
		}
		out.Programs = appendNew(out.Programs, gr.Programs...)
		out.Builtins = appendNew(out.Builtins, gr.Builtins...)
		out.ReadPaths = appendNew(out.ReadPaths, gr.ReadPaths...)
	}
	return out
}

func appendNew(dst []string, items ...string) []string {
	for _, it := range items {
		found := false
		for _, d := range dst {
			if d == it {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, it)
		}
	}
	return dst
}

func (g *Grant) allowsProgram(name string) bool {
	return g != nil && contains(g.Programs, name)
}

func (g *Grant) allowsBuiltin(name string) bool {
	return g != nil && contains(g.Builtins, name)
}

// allowsRead reports whether path lies within a granted read path.
func (g *Grant) allowsRead(path string) bool {
	if g == nil {
		return false
	}
	for _, p := range g.ReadPaths {
		if WithinDir(p, path) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// add records an item into the grant once.
func (g *Grant) add(kind, item string) {
	switch kind {
	case "program":
		g.Programs = appendNew(g.Programs, item)
	case "builtin":
		g.Builtins = appendNew(g.Builtins, item)
	case "read":
		g.ReadPaths = appendNew(g.ReadPaths, item)
	}
}

func (g *Grant) empty() bool {
	return len(g.Programs) == 0 && len(g.Builtins) == 0 && len(g.ReadPaths) == 0
}

// Extend returns a copy of p named name, with extra programs allowed (each
// with path confinement only and no other argument rules) and extra programs
// on the never list. Never always wins: a program on either policy's never
// list is not allowed, whatever allow says.
func (p *Policy) Extend(name string, allow, never []string) *Policy {
	out := &Policy{
		Name:     name,
		Builtins: make(map[string]bool, len(p.Builtins)),
		Programs: make(map[string]*Profile, len(p.Programs)+len(allow)),
		Never:    make(map[string]bool, len(p.Never)+len(never)),
		flat: Spec{
			Name:  name,
			Allow: append(append([]string{}, p.flat.Allow...), allow...),
			Never: append(append([]string{}, p.flat.Never...), never...),
		},
	}
	for k, v := range p.Builtins {
		out.Builtins[k] = v
	}
	for k, v := range p.Programs {
		out.Programs[k] = v
	}
	for k, v := range p.Never {
		out.Never[k] = v
	}
	for _, n := range never {
		out.Never[n] = true
		delete(out.Programs, n)
	}
	for _, n := range allow {
		if _, ok := out.Programs[n]; !ok && !out.Never[n] {
			out.Programs[n] = &Profile{MaxPositional: -1}
		}
	}
	return out
}

// Validate reports a policy that cannot be applied: no name, a program named
// by path (the allowlist holds bare names resolved on a pinned PATH), or a
// program both allowed and never allowed.
func (p *Policy) Validate() error {
	if p == nil || strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("shell policy has no name")
	}
	var bad []string
	for n := range p.Programs {
		if n == "" || strings.ContainsRune(n, '/') || strings.ContainsRune(n, filepath.Separator) {
			bad = append(bad, fmt.Sprintf("%q is not a bare program name", n))
		}
		if p.Never[n] {
			bad = append(bad, fmt.Sprintf("%q is both allowed and never allowed", n))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("shell policy %q: %s", p.Name, strings.Join(bad, "; "))
	}
	return nil
}

// ProgramNames returns the allowlisted program names, sorted.
func (p *Policy) ProgramNames() []string {
	names := make([]string, 0, len(p.Programs))
	for n := range p.Programs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// WithinDir reports whether path is root or lies beneath it, comparing whole
// path components (so "/tmpfoo" is not within "/tmp"). Both are cleaned;
// neither is resolved, so callers pass resolved paths.
func WithinDir(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// inRoots reports whether path lies within any of roots.
func inRoots(path string, roots []string) bool {
	for _, r := range roots {
		if WithinDir(r, path) {
			return true
		}
	}
	return false
}

// ResolvePath returns path made absolute against dir (when relative), with
// symlinks resolved. A path that does not exist has its deepest existing
// ancestor resolved and the rest appended, so a file about to be created
// resolves to where it would be created.
func ResolvePath(dir, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	path = filepath.Clean(path)
	rest := ""
	cur := path
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if rest == "" {
				return resolved
			}
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path
		}
		if rest == "" {
			rest = filepath.Base(cur)
		} else {
			rest = filepath.Join(filepath.Base(cur), rest)
		}
		cur = parent
	}
}
