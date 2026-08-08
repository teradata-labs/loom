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

// denylist is the config-built policy for a "denylist" binding. Its selection is
// the scope ∧ matcher contract shared by every library policy; its decision body
// denies any call that selection governs. The matcher is D-2's single source of
// truth for param/nested matching, so a governed call is by construction one
// whose params meet the deny selector.
type denylist struct {
	scope   ToolScope
	matcher Matcher
}

// Matches reports whether the binding governs this call: its tool is in scope
// and its params satisfy the deny matcher.
func (h denylist) Matches(req AdmissionRequest) bool {
	return h.scope.MatchesTool(req.ToolName) && h.matcher.MatchesParams(req.Params)
}

// Evaluate denies a governed call so the tool body does not run; a non-matching
// call is not governed (Matches is false) and never reaches here, combining to
// today's behavior. The executor returns a permission_denied Result for the Deny.
func (h denylist) Evaluate(req AdmissionRequest) Decision {
	return Decision{Kind: Deny, Reason: "denied by denylist"}
}
