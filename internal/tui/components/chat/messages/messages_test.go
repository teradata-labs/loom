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
package messages

import (
	"strings"
	"testing"

	"github.com/teradata-labs/loom/internal/message"
)

func newTestMessage(t *testing.T, role message.Role, content string) message.Message {
	t.Helper()
	m := message.NewMessage("m-1", "sess-1", role)
	m.AddPart(message.ContentText{Text: content})
	return m
}

// A skill body must render as a muted note, not the human bubble
// (renderUserMessage) or the assistant content block (renderAssistantMessage)
// — rendering it as either would misrepresent who "said" it.
func TestMessageCmp_SkillBody_RendersAsSyntheticNote(t *testing.T) {
	msg := newTestMessage(t, message.SkillBody, "## Skill: SQL Optimization")
	cmp := NewMessageCmp(msg)
	cmp.SetSize(80, 20)

	out := cmp.View()
	if !strings.Contains(out, "Skill loaded") {
		t.Errorf("expected a skill-loaded label, got: %s", out)
	}
	if !strings.Contains(out, "SQL Optimization") {
		t.Errorf("expected the skill body content, got: %s", out)
	}
}

func TestMessageCmp_HygieneInjection_RendersAsSyntheticNote(t *testing.T) {
	msg := newTestMessage(t, message.HygieneInjection, "Task-board hygiene check found violations")
	cmp := NewMessageCmp(msg)
	cmp.SetSize(80, 20)

	out := cmp.View()
	if !strings.Contains(out, "Hygiene retry requested") {
		t.Errorf("expected a hygiene-retry label, got: %s", out)
	}
	if !strings.Contains(out, "hygiene check found violations") {
		t.Errorf("expected the injection content, got: %s", out)
	}
}

func TestMessageCmp_EmptyResponseRetry_RendersAsSyntheticNote(t *testing.T) {
	msg := newTestMessage(t, message.EmptyResponseRetry, "Your previous response was empty. Please provide a response.")
	cmp := NewMessageCmp(msg)
	cmp.SetSize(80, 20)

	out := cmp.View()
	if !strings.Contains(out, "Retry requested") {
		t.Errorf("expected a retry-requested label, got: %s", out)
	}
	if !strings.Contains(out, "previous response was empty") {
		t.Errorf("expected the empty-response retry content, got: %s", out)
	}
}

func TestMessageCmp_SynthesisPrompt_RendersAsSyntheticNote(t *testing.T) {
	msg := newTestMessage(t, message.SynthesisPrompt, "You must provide your final answer NOW with whatever information you have gathered so far.")
	cmp := NewMessageCmp(msg)
	cmp.SetSize(80, 20)

	out := cmp.View()
	if !strings.Contains(out, "Synthesis requested") {
		t.Errorf("expected a synthesis-requested label, got: %s", out)
	}
	if !strings.Contains(out, "final answer NOW") {
		t.Errorf("expected the synthesis prompt content, got: %s", out)
	}
}

// A synthetic note is never a focus target — unlike the user bubble and the
// assistant content block, focusing it must not change its rendering.
func TestMessageCmp_SkillBody_FocusHasNoEffect(t *testing.T) {
	msg := newTestMessage(t, message.SkillBody, "## Skill: SQL Optimization")
	cmp := NewMessageCmp(msg)
	cmp.SetSize(80, 20)

	unfocused := cmp.View()
	cmp.Focus()
	focused := cmp.View()

	if unfocused != focused {
		t.Errorf("expected synthetic note rendering to be focus-independent, got different output:\nunfocused: %q\nfocused:   %q", unfocused, focused)
	}
}
