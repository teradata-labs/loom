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

package luasandbox

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// Trust says who wrote the script being run. Policies can deny more tools to
// scripts the runner did not write.
type Trust int

// Trust tiers.
const (
	// TrustInline is source the model wrote in this call.
	TrustInline Trust = iota
	// TrustOwn is a saved script owned by the runner.
	TrustOwn
	// TrustShared is a saved script another user shared with the runner.
	TrustShared
	// TrustPublished is a saved script published site-wide.
	TrustPublished
)

func (t Trust) String() string {
	switch t {
	case TrustInline:
		return "inline"
	case TrustOwn:
		return "own"
	case TrustShared:
		return "shared"
	case TrustPublished:
		return "published"
	}
	return fmt.Sprintf("trust(%d)", int(t))
}

// foreign reports whether the runner did not write the script.
func (t Trust) foreign() bool { return t == TrustShared || t == TrustPublished }

// hardDeny lists tools a script may never call, whatever the policy says.
// Each one re-enters the agent loop or a model, waits on a human, or changes
// the session's tool set while the script runs.
var hardDeny = []string{
	"activate_tool",
	"contact_human",
	"delegate_to_agent",
	"manage_ephemeral_agents",
	"manage_skills",
	"query_tool_result", // scripts use turn.result instead
	"tool_search",
}

// HardDeny returns a copy of the tools no script may call.
func HardDeny() []string { return slices.Clone(hardDeny) }

// Policy decides which tools a script may call and with what limits.
//
// Entries in Allow, Deny and DenyForShared are exact tool names or path.Match
// patterns ("web_*").
type Policy struct {
	// Limits are the defaults and ceilings for runs under this policy.
	Limits Limits
	// Allow, when non-empty, restricts scripts to these tools.
	Allow []string
	// Deny removes tools for every script.
	Deny []string
	// DenyForShared removes tools for scripts the runner did not write.
	DenyForShared []string
	// Reserved names the host's own script tools. They are always denied so
	// a script can never start another script.
	Reserved []string
	// ReservedPrefixes are name prefixes that are always denied, for example
	// the prefix of tools made from saved scripts.
	ReservedPrefixes []string
}

// Validate reports malformed patterns.
func (p Policy) Validate() error {
	for _, list := range [][]string{p.Allow, p.Deny, p.DenyForShared, p.Reserved} {
		for _, pat := range list {
			if strings.TrimSpace(pat) == "" {
				return fmt.Errorf("luasandbox: empty tool pattern")
			}
			if _, err := path.Match(pat, ""); err != nil {
				return fmt.Errorf("luasandbox: bad tool pattern %q: %w", pat, err)
			}
		}
	}
	for _, pfx := range p.ReservedPrefixes {
		if pfx == "" {
			return fmt.Errorf("luasandbox: empty reserved prefix")
		}
	}
	return nil
}

func matchesAny(name string, patterns []string) bool {
	for _, pat := range patterns {
		if pat == name {
			return true
		}
		if ok, err := path.Match(pat, name); err == nil && ok {
			return true
		}
	}
	return false
}

// Check reports whether a script with the given trust may call name. When it
// may not, code is CodeHardDenied for names no policy can allow and
// CodeToolNotVisible otherwise.
func (p Policy) Check(name string, trust Trust) (code string, ok bool) {
	if slices.Contains(hardDeny, name) || matchesAny(name, p.Reserved) {
		return CodeHardDenied, false
	}
	for _, pfx := range p.ReservedPrefixes {
		if pfx != "" && strings.HasPrefix(name, pfx) {
			return CodeHardDenied, false
		}
	}
	if matchesAny(name, p.Deny) {
		return CodeToolNotVisible, false
	}
	if trust.foreign() && matchesAny(name, p.DenyForShared) {
		return CodeToolNotVisible, false
	}
	if len(p.Allow) > 0 && !matchesAny(name, p.Allow) {
		return CodeToolNotVisible, false
	}
	return "", true
}

// Visible filters the tools the model can see down to those a script with the
// given trust may call. The result is sorted and has no duplicates.
func (p Policy) Visible(advertised []string, trust Trust) []string {
	out := make([]string, 0, len(advertised))
	for _, name := range advertised {
		if _, ok := p.Check(name, trust); ok {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
