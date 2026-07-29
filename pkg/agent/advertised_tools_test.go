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
package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// namedStubTool is a shuttle.Tool whose only meaningful property is its name,
// so a test can control exactly which tool names the projection sees.
type namedStubTool struct{ name string }

func (t namedStubTool) Name() string        { return t.name }
func (t namedStubTool) Description() string { return t.name + " stub" }
func (t namedStubTool) Backend() string     { return "" }
func (t namedStubTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (t namedStubTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true}, nil
}

// newAdvertisedToolsAgent builds a minimal Agent holding only the machinery the
// per-session advertised-tool projection touches: the definition Registry and
// the ledger maps. skillOrchestrator and permissionChecker stay nil, so the
// skill-exclusion and permission filters pass every tool through and the test
// observes the projection (per-session scoping + name sort) in isolation. The
// baseTools are registered and frozen as the always-advertised set before any
// session-scoped event, mirroring captureBaseTools() at the top of the loop.
func newAdvertisedToolsAgent(t *testing.T, baseTools ...string) *Agent {
	t.Helper()
	a := &Agent{
		id:                "agent-under-test",
		tools:             shuttle.NewRegistry(),
		sessionToolLedger: make(map[string]map[string]bool),
		scopedToolNames:   make(map[string]bool),
	}
	for _, n := range baseTools {
		a.tools.Register(namedStubTool{name: n})
	}
	a.captureBaseTools()
	return a
}

// registerSessionScopedTool simulates a session-scoped registration event (a
// skill load's required tool or a progressive-disclosure tool): the definition
// enters the shared Registry and the name is recorded into ONE session's ledger
// via the production registerSessionTool API. Because the tool is not in the
// frozen base set, this scopes it to sessionID alone.
func registerSessionScopedTool(a *Agent, sessionID, name string) {
	a.tools.Register(namedStubTool{name: name})
	a.registerSessionTool(sessionID, name)
}

// advertisedNames returns the ordered tool names the agent would advertise to
// session on a provider call.
func advertisedNames(a *Agent, session *Session) []string {
	tools := a.advertisedTools(session)
	names := make([]string, len(tools))
	for i, tl := range tools {
		names[i] = tl.Name()
	}
	return names
}

// AC1: a session's advertised tool list is name-sorted and in the identical
// order on every turn. Base tools are registered out of alphabetical order; the
// projection must return them ascending by name, and re-deriving it repeatedly
// (once per provider call) must yield that same order every time.
func TestAdvertisedTools_NameSortedAndStableEveryTurn(t *testing.T) {
	a := newAdvertisedToolsAgent(t, "zebra", "alpha", "mango", "bravo")
	session := &Session{ID: "session-1"}

	want := []string{"alpha", "bravo", "mango", "zebra"}

	for turn := 0; turn < 5; turn++ {
		got := advertisedNames(a, session)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("turn %d advertised order = %v, want name-sorted %v", turn, got, want)
		}
	}
}

// AC2: the advertised list is byte-identical between two consecutive turns when
// no event occurred for this session, and it changes on the turn this session's
// OWN skill load / first-need disclosure registers a tool.
func TestAdvertisedTools_ByteIdenticalUnlessOwnEvent(t *testing.T) {
	a := newAdvertisedToolsAgent(t, "alpha", "bravo")
	session := &Session{ID: "session-1"}

	// Two consecutive re-derivations with no intervening event: byte-identical.
	first := advertisedNames(a, session)
	second := advertisedNames(a, session)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("advertised list changed with no session event: %v -> %v", first, second)
	}

	// This session's own disclosure registers a tool: the very next derivation
	// differs and now carries the newly disclosed tool, still name-sorted.
	registerSessionScopedTool(a, session.ID, "get_error_details")
	afterOwnEvent := advertisedNames(a, session)
	if reflect.DeepEqual(second, afterOwnEvent) {
		t.Fatalf("advertised list did not change after this session's own disclosure: %v", afterOwnEvent)
	}
	wantAfter := []string{"alpha", "bravo", "get_error_details"}
	if !reflect.DeepEqual(afterOwnEvent, wantAfter) {
		t.Fatalf("after own disclosure advertised = %v, want %v", afterOwnEvent, wantAfter)
	}
}

// AC3: a session advertises the base always-available tools plus only the tools
// ITS OWN events registered — never a tool another session's event registered.
func TestAdvertisedTools_ScopedToOwnSessionEvents(t *testing.T) {
	a := newAdvertisedToolsAgent(t, "alpha", "bravo")
	sessionA := &Session{ID: "session-a"}
	sessionB := &Session{ID: "session-b"}

	registerSessionScopedTool(a, sessionA.ID, "a_only_tool")

	gotA := advertisedNames(a, sessionA)
	wantA := []string{"a_only_tool", "alpha", "bravo"}
	if !reflect.DeepEqual(gotA, wantA) {
		t.Fatalf("session A advertised = %v, want base + own tool %v", gotA, wantA)
	}

	gotB := advertisedNames(a, sessionB)
	wantB := []string{"alpha", "bravo"}
	if !reflect.DeepEqual(gotB, wantB) {
		t.Fatalf("session B advertised = %v, want base only (not A's tool) %v", gotB, wantB)
	}
}

// AC4: another session's skill load or disclosure does not change this
// session's advertised list — it stays byte-identical and never gains the other
// session's scoped tool.
func TestAdvertisedTools_OtherSessionEventDoesNotChangeThisSession(t *testing.T) {
	a := newAdvertisedToolsAgent(t, "alpha", "bravo")
	sessionA := &Session{ID: "session-a"}
	sessionB := &Session{ID: "session-b"}

	before := advertisedNames(a, sessionA)

	// Session B loads a skill / discloses a tool of its own.
	registerSessionScopedTool(a, sessionB.ID, "b_only_tool")

	after := advertisedNames(a, sessionA)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("session A advertised list changed after session B's event: %v -> %v", before, after)
	}
	for _, name := range after {
		if name == "b_only_tool" {
			t.Fatalf("session A advertised session B's scoped tool: %v", after)
		}
	}
}
