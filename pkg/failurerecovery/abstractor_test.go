// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package failurerecovery

import (
	"context"
	"testing"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// fakeLessonModel is a scripted LessonModel for testing the LLM abstractor.
type fakeLessonModel struct {
	content string
	err     error
	calls   int
}

func (f *fakeLessonModel) Chat(_ context.Context, _ []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &types.LLMResponse{Content: f.content}, nil
}

func (f *fakeLessonModel) Name() string  { return "fake" }
func (f *fakeLessonModel) Model() string { return "fake-model" }

// llmConfig returns an enabled config with LLM abstraction on.
func llmConfig() Config {
	c := enabledConfig()
	c.LLMAbstraction = LLMAbstractionConfig{Enabled: true}
	return c
}

func TestLLMAbstractor_UsesModelOutputWhenClean(t *testing.T) {
	model := &fakeLessonModel{content: `{"problem":"reserved keyword used as an unquoted identifier","solution":"wrap the identifier in double quotes"}`}
	a := newLLMAbstractor(model, ruleAbstractor{}, 0)

	problem, solution := a.Abstract(context.Background(), ClassGuardrail, `[Error 3706] Syntax error near "order"`)

	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls)
	}
	if problem != "reserved keyword used as an unquoted identifier" {
		t.Errorf("problem = %q", problem)
	}
	if solution != "wrap the identifier in double quotes" {
		t.Errorf("solution = %q", solution)
	}
}

func TestLLMAbstractor_FallsBackOnModelError(t *testing.T) {
	model := &fakeLessonModel{err: errBoom}
	a := newLLMAbstractor(model, ruleAbstractor{}, 0)

	problem, solution := a.Abstract(context.Background(), ClassGuardrail, "syntax error near token")
	ruleP, ruleS := abstract(ClassGuardrail, "syntax error near token")

	if problem != ruleP || solution != ruleS {
		t.Errorf("expected rule fallback (%q/%q), got (%q/%q)", ruleP, ruleS, problem, solution)
	}
}

func TestLLMAbstractor_FallsBackOnUnparseable(t *testing.T) {
	model := &fakeLessonModel{content: "sorry, I cannot help with that"}
	a := newLLMAbstractor(model, ruleAbstractor{}, 0)

	problem, solution := a.Abstract(context.Background(), ClassToolExec, "connection refused")
	ruleP, ruleS := abstract(ClassToolExec, "connection refused")

	if problem != ruleP || solution != ruleS {
		t.Errorf("expected rule fallback, got (%q/%q)", problem, solution)
	}
}

func TestLLMAbstractor_FallsBackOnEmptyFields(t *testing.T) {
	model := &fakeLessonModel{content: `{"problem":"","solution":""}`}
	a := newLLMAbstractor(model, ruleAbstractor{}, 0)

	problem, solution := a.Abstract(context.Background(), ClassToolExec, "timeout exceeded")
	ruleP, ruleS := abstract(ClassToolExec, "timeout exceeded")

	if problem != ruleP || solution != ruleS {
		t.Errorf("expected rule fallback, got (%q/%q)", problem, solution)
	}
}

func TestLLMAbstractor_LeakGateRejectsEchoedLiteral(t *testing.T) {
	// The model echoes a schema-qualified identifier from the raw error.
	raw := `[Error 3706] Syntax error: SELECT order FROM agentic_demo.demo_fr_orders`
	model := &fakeLessonModel{content: `{"problem":"query against agentic_demo.demo_fr_orders failed","solution":"quote the identifier"}`}
	a := newLLMAbstractor(model, ruleAbstractor{}, 0)

	problem, solution := a.Abstract(context.Background(), ClassGuardrail, raw)
	ruleP, ruleS := abstract(ClassGuardrail, raw)

	if problem != ruleP || solution != ruleS {
		t.Errorf("leaked literal must trigger fallback; got (%q/%q)", problem, solution)
	}
}

func TestLLMAbstractor_ReservedKeywordNotTreatedAsLeak(t *testing.T) {
	// A reserved SQL keyword quoted in the error ("order") is language, not
	// user data: a good lesson may reference it, so the gate must NOT reject it.
	raw := `[Error 3706] Syntax error near the keyword "order"`
	model := &fakeLessonModel{content: `{"problem":"a reserved keyword such as order was used as an unquoted identifier","solution":"quote reserved keywords like order when used as identifiers"}`}
	a := newLLMAbstractor(model, ruleAbstractor{}, 0)

	problem, solution := a.Abstract(context.Background(), ClassGuardrail, raw)
	if problem != "a reserved keyword such as order was used as an unquoted identifier" {
		t.Errorf("reserved keyword must not trip the leak gate; problem=%q", problem)
	}
	if solution != "quote reserved keywords like order when used as identifiers" {
		t.Errorf("reserved keyword must not trip the leak gate; solution=%q", solution)
	}
}

func TestLLMAbstractor_PreserveDetailsKeepsIdentifier(t *testing.T) {
	// With detail preservation on, an echoed schema-qualified identifier is
	// allowed through — the lesson stays precise instead of falling back to the
	// generic rule text.
	raw := `[Error 3706] Syntax error: SELECT order FROM agentic_demo.demo_fr_orders`
	model := &fakeLessonModel{content: `{"problem":"query against agentic_demo.demo_fr_orders used reserved keyword order unquoted","solution":"quote order when selecting from agentic_demo.demo_fr_orders"}`}
	a := newLLMAbstractor(model, ruleAbstractor{}, 0)
	a.preserveDetails = true

	problem, solution := a.Abstract(context.Background(), ClassGuardrail, raw)
	if problem != "query against agentic_demo.demo_fr_orders used reserved keyword order unquoted" {
		t.Errorf("preserve mode must keep the identifier; problem=%q", problem)
	}
	if solution != "quote order when selecting from agentic_demo.demo_fr_orders" {
		t.Errorf("preserve mode must keep the identifier; solution=%q", solution)
	}
}

func TestLLMAbstractor_PreserveDetailsOffStillGates(t *testing.T) {
	// Same echoed identifier, but preservation off: the leak gate must still
	// reject it and fall back to the generic rule text.
	raw := `[Error 3706] Syntax error: SELECT order FROM agentic_demo.demo_fr_orders`
	model := &fakeLessonModel{content: `{"problem":"query against agentic_demo.demo_fr_orders failed","solution":"quote the identifier"}`}
	a := newLLMAbstractor(model, ruleAbstractor{}, 0)
	// a.preserveDetails defaults false

	problem, solution := a.Abstract(context.Background(), ClassGuardrail, raw)
	ruleP, ruleS := abstract(ClassGuardrail, raw)
	if problem != ruleP || solution != ruleS {
		t.Errorf("gate must still reject when preservation off; got (%q/%q)", problem, solution)
	}
}

func TestLLMAbstractor_ParsesFencedJSON(t *testing.T) {
	model := &fakeLessonModel{content: "Here is the lesson:\n```json\n{\"problem\":\"a required argument was missing\",\"solution\":\"include every required argument\"}\n```\n"}
	a := newLLMAbstractor(model, ruleAbstractor{}, 0)

	problem, solution := a.Abstract(context.Background(), ClassToolArgs, "missing required parameter x")
	if problem != "a required argument was missing" || solution != "include every required argument" {
		t.Errorf("failed to parse fenced JSON: (%q/%q)", problem, solution)
	}
}

func TestWithLLMAbstractor_NoOpWhenConfigDisabled(t *testing.T) {
	store := &fakeStore{}
	model := &fakeLessonModel{content: `{"problem":"p","solution":"s"}`}
	// Feature enabled, but LLM abstraction NOT enabled in config.
	e := NewEngine(store, nil, enabledConfig(), WithLLMAbstractor(model))

	e.OnToolFailure(testSession, ClassToolExec, "run_sql", `syntax error at or near "from"`)
	e.OnToolSuccess(context.Background(), testAgent, testSession, "run_sql", nil)

	if model.calls != 0 {
		t.Fatalf("model must not be called when LLMAbstraction disabled; calls=%d", model.calls)
	}
	if store.count() != 1 {
		t.Fatalf("want 1 lesson, got %d", store.count())
	}
}

func TestWithLLMAbstractor_UsedWhenConfigEnabled(t *testing.T) {
	store := &fakeStore{}
	model := &fakeLessonModel{content: `{"problem":"reserved keyword used unquoted","solution":"wrap the identifier in double quotes"}`}
	e := NewEngine(store, nil, llmConfig(), WithLLMAbstractor(model))

	e.OnToolFailure(testSession, ClassGuardrail, "run_sql", `syntax error at or near "order"`)
	e.OnToolSuccess(context.Background(), testAgent, testSession, "run_sql", nil)

	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls)
	}
	if store.count() != 1 {
		t.Fatalf("want 1 lesson, got %d", store.count())
	}
	m := store.mems[0]
	if m.Content != "reserved keyword used unquoted" {
		t.Errorf("stored problem = %q, want LLM output", m.Content)
	}
	if m.Summary != "wrap the identifier in double quotes" {
		t.Errorf("stored solution = %q, want LLM output", m.Summary)
	}
}

// TestPreserveDetails_RequiresUserScoped verifies the engine only enables detail
// preservation when BOTH PreserveDetails and UserScoped are set. With an echoed
// identifier in the drafted lesson: user-scoped keeps it, agent-wide abstracts it.
func TestPreserveDetails_RequiresUserScoped(t *testing.T) {
	raw := `[Error 3706] Syntax error: SELECT order FROM agentic_demo.demo_fr_orders`
	draft := `{"problem":"reserved keyword order unquoted in agentic_demo.demo_fr_orders","solution":"quote order in agentic_demo.demo_fr_orders"}`

	newCfg := func(userScoped bool) Config {
		c := llmConfig()
		c.PreserveDetails = true
		c.UserScoped = userScoped
		return c
	}

	t.Run("user-scoped keeps the identifier", func(t *testing.T) {
		store := &fakeStore{}
		model := &fakeLessonModel{content: draft}
		e := NewEngine(store, nil, newCfg(true), WithLLMAbstractor(model))
		ctx := types.ContextWithUserID(context.Background(), "user-a")
		e.OnToolFailure(testSession, ClassGuardrail, "run_sql", raw)
		e.OnToolSuccess(ctx, testAgent, testSession, "run_sql", nil)

		if store.count() != 1 {
			t.Fatalf("want 1 lesson, got %d", store.count())
		}
		if store.mems[0].Content != "reserved keyword order unquoted in agentic_demo.demo_fr_orders" {
			t.Errorf("user-scoped preserve must keep identifier; got %q", store.mems[0].Content)
		}
	})

	t.Run("agent-wide abstracts the identifier", func(t *testing.T) {
		store := &fakeStore{}
		model := &fakeLessonModel{content: draft}
		e := NewEngine(store, nil, newCfg(false), WithLLMAbstractor(model))
		e.OnToolFailure(testSession, ClassGuardrail, "run_sql", raw)
		e.OnToolSuccess(context.Background(), testAgent, testSession, "run_sql", nil)

		if store.count() != 1 {
			t.Fatalf("want 1 lesson, got %d", store.count())
		}
		ruleP, _ := abstract(ClassGuardrail, raw)
		if store.mems[0].Content != ruleP {
			t.Errorf("agent-wide must fall back to generic rule text; got %q", store.mems[0].Content)
		}
	})
}

func TestLeaksRawLiteral(t *testing.T) {
	raw := `[Error 3706] Syntax error: SELECT order FROM agentic_demo.demo_fr_orders WHERE id = '42abc'`
	tests := []struct {
		name      string
		candidate string
		want      bool
	}{
		{"clean generic lesson", "reserved keyword used unquoted; quote the identifier", false},
		{"echoes dotted identifier", "table agentic_demo.demo_fr_orders not usable", true},
		{"echoes quoted value", "the value 42abc is invalid", true},
		{"echoes error code", "error 3706 occurred", true},
		{"generic words only", "correct the statement syntax and retry", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := leaksRawLiteral(raw, tt.candidate); got != tt.want {
				t.Errorf("leaksRawLiteral(%q) = %v, want %v", tt.candidate, got, tt.want)
			}
		})
	}
}

func TestParseLesson(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantOK  bool
	}{
		{"plain json", `{"problem":"p","solution":"s"}`, true},
		{"prose wrapped", `The lesson is {"problem":"p","solution":"s"} okay?`, true},
		{"missing solution", `{"problem":"p"}`, false},
		{"empty fields", `{"problem":"","solution":""}`, false},
		{"no json", `just text`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ok := parseLesson(tt.content)
			if ok != tt.wantOK {
				t.Errorf("parseLesson ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}
