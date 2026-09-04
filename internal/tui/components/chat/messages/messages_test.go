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
