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

// askRule is the config-built policy for an "ask" binding. Its selection is the
// scope ∧ matcher contract shared by every library policy; its decision body asks
// a human about any call that selection governs, deferring to the chain's
// AskResolver (fail-closed to Deny when none is wired).
type askRule struct {
	scope   ToolScope
	matcher Matcher
}

// Matches reports whether the binding governs this call: its tool is in scope
// and its params satisfy the ask matcher.
func (h askRule) Matches(req AdmissionRequest) bool {
	return h.scope.MatchesTool(req.ToolName) && h.matcher.MatchesParams(req.Params)
}

// Evaluate holds a governed call for a human: the Ask verdict defers to the
// chain's AskResolver, and the Reason reaches the raised request's context
// verbatim, so it is written to be read by the approver.
func (h askRule) Evaluate(req AdmissionRequest) Decision {
	return Decision{Kind: Ask, Reason: "approval required by ask rule"}
}
