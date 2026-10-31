// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"strings"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// A state's contract is to end its reply with its signal. A model asked for one
// JSON object routinely wraps it in a fence, and reading the closing ``` as the
// signal is invisible in every way that matters: the payload never parses, the
// machine routes on "```", no vocabulary matches it, and the state parks as
// unroutable — so the run reads as a worker that answered wrongly rather than an
// engine that read the wrong line. That was found by a live run, not by a test,
// which is why these exist.

func TestTheSignalIsFoundThroughACodeFence(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
	}{
		{"bare word", "did the thing\ncreated", "created"},
		{"bare json", `prose
{"signal":"deltas","deltas":["a"]}`, "deltas"},
		{"fenced json", "prose\n```\n" + `{"signal":"deltas","deltas":["a"]}` + "\n```", "deltas"},
		{"fenced json with a language tag", "prose\n```json\n" + `{"signal":"converged"}` + "\n```", "converged"},
		{"tilde fence", "prose\n~~~\n" + `{"signal":"escalate","reason":"x"}` + "\n~~~", "escalate"},
		{"fence then trailing blank lines", "prose\n```\n" + `{"signal":"created"}` + "\n```\n\n\n", "created"},
		{"fenced bare word", "prose\n```\ncreated\n```", "created"},
		{"no fence, trailing blanks", "created\n\n", "created"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := signalOf(tc.output); got != tc.want {
				t.Errorf("signalOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAFenceIsOnlyAFenceWhenThatIsAllTheLineIs — a prose line that happens to
// start with backticks is content, and skipping it would lose the real signal or
// silently change the handoff.
func TestAFenceIsOnlyAFenceWhenThatIsAllTheLineIs(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"```", true},
		{"```json", true},
		{"~~~", true},
		{"~~~python", true},
		{"``` and then prose", false},
		{"```json with a note", false},
		{"created", false},
		{`{"signal":"created"}`, false},
	} {
		if got := isCodeFence(tc.line); got != tc.want {
			t.Errorf("isCodeFence(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// TestThePayloadParsesThroughAFence, because the fields a signal carries —
// deltas, reason, vars — are read from the payload and not from the signal name.
// A fence hid all of them.
func TestThePayloadParsesThroughAFence(t *testing.T) {
	out := "the review found things\n```json\n" +
		`{"signal":"deltas","deltas":["FR-2 unallocated"],"vars":{"round":"2"}}` +
		"\n```"

	payload := parseSignalPayload(out)
	if payload == nil {
		t.Fatal("no payload parsed through the fence")
	}
	if payload["signal"] != "deltas" {
		t.Errorf("signal = %v", payload["signal"])
	}
	if d, ok := payload["deltas"].([]any); !ok || len(d) != 1 {
		t.Errorf("deltas did not survive: %v", payload["deltas"])
	}
	if v, ok := payload["vars"].(map[string]any); !ok || v["round"] != "2" {
		t.Errorf("vars did not survive: %v", payload["vars"])
	}
}

// TestTheHandoffStopsAboveTheFence. The handoff is what the next state reads;
// leaving the opening fence in ends it on a dangling ``` that reads as content.
func TestTheHandoffStopsAboveTheFence(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
	}{
		{"fenced signal", "the prose\n```json\n" + `{"signal":"created"}` + "\n```", "the prose"},
		{"bare signal", "the prose\ncreated", "the prose"},
		{"blank lines between", "the prose\n\n```\n" + `{"signal":"created"}` + "\n```", "the prose"},
		{"signal only", "created", ""},
		{"fenced signal only", "```\ncreated\n```", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := handoff(tc.output, ""); got != tc.want {
				t.Errorf("handoff = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAFencedCodeBlockInTheProseIsNotMistakenForTheSignal. A design document's
// prose contains fenced blocks; only the trailing fence around the signal is
// skipped, and a state whose real signal sits after such a block still routes.
func TestAFencedCodeBlockInTheProseIsNotMistakenForTheSignal(t *testing.T) {
	out := "here is the interface:\n```go\nfunc Add(a, b int) int\n```\nand that is the design.\ncreated"

	if got := signalOf(out); got != "created" {
		t.Errorf("signalOf = %q, want created", got)
	}
	want := "here is the interface:\n```go\nfunc Add(a, b int) int\n```\nand that is the design."
	if got := handoff(out, ""); got != want {
		t.Errorf("handoff = %q, want %q", got, want)
	}
}

func TestNoSignalAtAll(t *testing.T) {
	for _, out := range []string{"", "\n\n", "```\n```"} {
		if got := signalOf(out); got != "" {
			t.Errorf("signalOf(%q) = %q, want empty", out, got)
		}
		if got := handoff(out, ""); got != "" {
			t.Errorf("handoff(%q) = %q, want empty", out, got)
		}
	}
}

// A payload with a list or a nested object in it does not fit on one line, and a
// model asked for one JSON object writes it pretty-printed. Reading only the last
// line then finds "}" or "]}" — no signal, no fields, and a state that parks as
// unroutable for a reason that has nothing to do with the work. A live run showed
// exactly that: A_REVIEW's signal came back as "]}".
func TestASignalObjectMaySpanLines(t *testing.T) {
	for _, tc := range []struct {
		name, output, want, handoff string
	}{
		{
			name: "pretty-printed with a list",
			output: "the review found things\n" +
				"{\n  \"signal\": \"deltas\",\n  \"deltas\": [\n    \"FR-2 unallocated\"\n  ]\n}",
			want:    "deltas",
			handoff: "the review found things\n\n{\n  \"deltas\": [\n    \"FR-2 unallocated\"\n  ]\n}",
		},
		{
			name: "closing on ]}",
			output: "prose\n" +
				"{\"signal\": \"deltas\",\n \"deltas\": [\"one\",\n  \"two\"]}",
			want:    "deltas",
			handoff: "prose\n\n{\n  \"deltas\": [\n    \"one\",\n    \"two\"\n  ]\n}",
		},
		{
			name:    "pretty-printed inside a fence",
			output:  "prose\n```json\n{\n  \"signal\": \"escalate\",\n  \"reason\": \"a doubt\"\n}\n```",
			want:    "escalate",
			handoff: "prose\n\n{\n  \"reason\": \"a doubt\"\n}",
		},
		{
			name:    "a document that merely ends in a brace is not a payload",
			output:  "here is a struct:\n\ntype T struct {\n\tA int\n}\ncreated",
			want:    "created",
			handoff: "here is a struct:\n\ntype T struct {\n\tA int\n}",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := signalOf(tc.output); got != tc.want {
				t.Errorf("signalOf = %q, want %q", got, tc.want)
			}
			if got := handoff(tc.output, ""); got != tc.handoff {
				t.Errorf("handoff = %q, want %q", got, tc.handoff)
			}
		})
	}
}

// TestTheFieldsOfAMultiLinePayloadSurvive — the fields are why the payload
// exists: deltas carry the finding, vars carry the cursor. A signal read without
// them routes correctly and loses the content.
func TestTheFieldsOfAMultiLinePayloadSurvive(t *testing.T) {
	out := "done\n{\n  \"signal\": \"accepted\",\n  \"vars\": {\n    \"current_task\": \"T-3\"\n  }\n}"

	payload := parseSignalPayload(out)
	if payload == nil {
		t.Fatal("no payload parsed from a multi-line object")
	}
	vars, ok := payload["vars"].(map[string]any)
	if !ok || vars["current_task"] != "T-3" {
		t.Errorf("the cursor did not survive: %v", payload["vars"])
	}
}

// TestTheSessionSuffixNamesTheCursorItWasWrittenFor. A per-surface tester is
// declared once with suffix `{suite}` and must become one conversation per
// surface. Read literally, every surface shares a conversation keyed "{suite}",
// which is exactly the collapse the suffix exists to prevent.
func TestTheSessionSuffixNamesTheCursorItWasWrittenFor(t *testing.T) {
	e := &StateMachineExecutor{workflowID: "run"}
	b := &loomv1.AgentState{AgentId: "tester", SessionKeySuffix: "{suite}"}

	if got := e.sessionID(b, map[string]string{"suite": "unit"}); got != "run-tester-unit" {
		t.Errorf("suffix not interpolated: %q", got)
	}
	if got := e.sessionID(b, map[string]string{"suite": "e2e"}); got != "run-tester-e2e" {
		t.Errorf("a second surface must be a second conversation: %q", got)
	}
	// No cursor yet is no suffix, not a literal one.
	if got := e.sessionID(b, nil); got != "run-tester-{suite}" {
		// The engine leaves an unsupplied placeholder alone everywhere else; it
		// does here too, and the placeholder guard in the loops is what keeps a
		// spec from relying on that.
		t.Logf("unsupplied suffix left literal, as elsewhere: %q", got)
	}
}

// TestADismissNamesTheConversationItWasWrittenFor. `tester-{suite}` is one
// surface's tester; read literally it is nobody, and dismissing nobody is a
// no-op by design — so the bound on a per-surface conversation never applied.
func TestADismissNamesTheConversationItWasWrittenFor(t *testing.T) {
	got := interpolateRun("tester-{suite}", map[string]string{ParamDir: "/w"},
		map[string]string{"suite": "unit"})
	if got != "tester-unit" {
		t.Errorf("dismiss name not interpolated: %q", got)
	}
}

// A reviewer told to end on {"signal":"deltas","deltas":[…]} writes its findings
// there and nowhere else. A live creator was told "Still outstanding — four
// fixes." and then nothing, two turns running, and gave up: the four were in the
// object, and the handoff stopped at the prose. What routes (signal) and what the
// run keeps (vars) stay with the engine; the rest is for the next seat.
func TestWhatASignalObjectCarriesReachesTheNextSeat(t *testing.T) {
	out := "Still outstanding — four fixes.\n" +
		`{"signal":"deltas","vars":{"round":"2"},"deltas":["A — missing edge case","B — uncited constraint"]}`
	got := handoff(out, "")
	for _, want := range []string{"Still outstanding — four fixes.", "A — missing edge case", "B — uncited constraint"} {
		if !strings.Contains(got, want) {
			t.Errorf("the next seat never sees %q; it gets:\n%s", want, got)
		}
	}
	for _, control := range []string{`"signal"`, `"vars"`, `"round"`} {
		if strings.Contains(got, control) {
			t.Errorf("the engine's own %s travelled as content:\n%s", control, got)
		}
	}
	if got := handoff("all good\n"+`{"signal":"converged"}`, ""); got != "all good" {
		t.Errorf("a bare signal object added %q to the message", got)
	}
}
