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
package shuttle

// ToolScope is the set of tools a binding governs, derived from the binding's
// Scope selector. It reuses the exact/prefix semantics of tools.permissions: a
// bare tool name matches exactly, a trailing "*" matches by prefix (e.g.
// "opendata:*" governs every "opendata:" tool), and a bare "*" governs all.
type ToolScope struct {
	exact    map[string]bool
	prefixes []string
}

// NewToolScope compiles a single Scope selector into a ToolScope.
func NewToolScope(scope string) ToolScope {
	exact, prefixes := splitPatterns([]string{scope})
	return ToolScope{exact: exact, prefixes: prefixes}
}

// MatchesTool reports whether the named tool is governed by this scope.
func (s ToolScope) MatchesTool(name string) bool {
	return matchPattern(name, s.exact, s.prefixes)
}
