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
package message

import "testing"

// The persisted-role string a producer writes (pkg/agent) and the TUI's
// typed Role must be the same literal, or the bare cast in
// internal/tui/adapter/messages.go:ProtoToMessage silently produces a role
// the TUI never explicitly handles.
func TestSyntheticRoles_MatchPersistedStrings(t *testing.T) {
	if SkillBody != "skill_body" {
		t.Fatalf("SkillBody = %q, want %q", SkillBody, "skill_body")
	}
	if HygieneInjection != "hygiene_injection" {
		t.Fatalf("HygieneInjection = %q, want %q", HygieneInjection, "hygiene_injection")
	}
	if EmptyResponseRetry != "empty_response_retry" {
		t.Fatalf("EmptyResponseRetry = %q, want %q", EmptyResponseRetry, "empty_response_retry")
	}
	if SynthesisPrompt != "synthesis_prompt" {
		t.Fatalf("SynthesisPrompt = %q, want %q", SynthesisPrompt, "synthesis_prompt")
	}
}
