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

package shellpolicy

import (
	"fmt"
	"sort"
	"strings"
)

// Spec declares a policy in configuration: a name, the policy it extends
// ("readonly" when empty), and the programs it adds to the allowlist or to the
// never list.
type Spec struct {
	Name    string   `json:"name" mapstructure:"name"`
	Extends string   `json:"extends,omitempty" mapstructure:"extends"`
	Allow   []string `json:"allow,omitempty" mapstructure:"allow"`
	Never   []string `json:"never,omitempty" mapstructure:"never"`
}

// Build resolves specs into policies, keyed by name. The built-in readonly
// policy is always present; a spec may extend it or another spec. A spec named
// readonly, an unknown or looping extends, a duplicate name, or a policy that
// fails Validate is an error.
func Build(specs []Spec) (map[string]*Policy, error) {
	byName := make(map[string]Spec, len(specs))
	for _, s := range specs {
		name := strings.TrimSpace(s.Name)
		switch {
		case name == "":
			return nil, fmt.Errorf("shell policy with no name")
		case name == "readonly":
			return nil, fmt.Errorf("shell policy %q is built in and cannot be redefined; extend it under another name", name)
		case byName[name].Name != "":
			return nil, fmt.Errorf("shell policy %q is defined twice", name)
		}
		s.Name = name
		byName[name] = s
	}
	out := map[string]*Policy{"readonly": Readonly()}
	var resolve func(name string, seen []string) (*Policy, error)
	resolve = func(name string, seen []string) (*Policy, error) {
		if p, ok := out[name]; ok {
			return p, nil
		}
		for _, s := range seen {
			if s == name {
				return nil, fmt.Errorf("shell policy extends loop: %s -> %s", strings.Join(seen, " -> "), name)
			}
		}
		s, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("shell policy %q extends unknown policy %q", seen[len(seen)-1], name)
		}
		base := s.Extends
		if base == "" {
			base = "readonly"
		}
		parent, err := resolve(base, append(seen, name))
		if err != nil {
			return nil, err
		}
		p := parent.Extend(name, s.Allow, s.Never)
		if err := p.Validate(); err != nil {
			return nil, err
		}
		out[name] = p
		return p, nil
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := resolve(n, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// FlatSpec returns p as additions to the readonly policy, for handing to a
// separate process; FromSpec(p.FlatSpec()) behaves exactly like p.
func (p *Policy) FlatSpec() Spec {
	return Spec{
		Name:  p.flat.Name,
		Allow: append([]string{}, p.flat.Allow...),
		Never: append([]string{}, p.flat.Never...),
	}
}

// FromSpec rebuilds a policy from its FlatSpec.
func FromSpec(s Spec) *Policy {
	ro := Readonly()
	if s.Name == "readonly" && len(s.Allow) == 0 && len(s.Never) == 0 {
		return ro
	}
	return ro.Extend(s.Name, s.Allow, s.Never)
}
