// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package shuttle

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// grantSeen records the admission grants each run of a tool body received.
type grantSeen struct {
	mu   sync.Mutex
	runs [][]any
}

func (g *grantSeen) tool(name string) *MockTool {
	return &MockTool{MockName: name, MockExecute: func(ctx context.Context, _ map[string]interface{}) (*Result, error) {
		g.mu.Lock()
		g.runs = append(g.runs, AdmissionGrantsFromContext(ctx))
		g.mu.Unlock()
		return &Result{Success: true}, nil
	}}
}

func (g *grantSeen) only(t *testing.T) []any {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	require.Len(t, g.runs, 1, "the tool body runs exactly once")
	return g.runs[0]
}

func askWith(grant any) fixedHook {
	return fixedHook{decision: Decision{Kind: Ask, Reason: "needs approval", Grant: grant}}
}

func executorWith(tool Tool, hooks []Hook, resolver AskResolver) *Executor {
	reg := NewRegistry()
	reg.Register(tool)
	exec := NewExecutor(reg)
	exec.SetAdmissionChain(NewChain(hooks, nil, resolver))
	return exec
}

func TestAdmissionGrants(t *testing.T) {
	approve := resolveTo{decision: Decision{Kind: Allow}}
	reject := resolveTo{decision: Decision{Kind: Deny, Reason: "no"}}
	allow := fixedHook{decision: Decision{Kind: Allow}}

	for _, tc := range []struct {
		name     string
		hooks    []Hook
		resolver AskResolver
		ctx      func(context.Context) context.Context
		ran      bool
		want     []any
	}{
		{name: "allowed outright carries none", hooks: []Hook{allow}, resolver: approve, ran: true},
		{name: "no chain match carries none", hooks: nil, resolver: approve, ran: true},
		{name: "resolver approval carries the grant", hooks: []Hook{askWith("g1")}, resolver: approve, ran: true, want: []any{"g1"}},
		{name: "resolver rejection runs nothing", hooks: []Hook{askWith("g1")}, resolver: reject},
		{name: "no resolver denies", hooks: []Hook{askWith("g1")}},
		{name: "every Ask's grant survives the fold", hooks: []Hook{askWith("g1"), allow, askWith("g2")}, resolver: approve, ran: true, want: []any{"g1", "g2"}},
		{name: "an Ask without a grant adds nothing", hooks: []Hook{askWith(nil), askWith("g2")}, resolver: approve, ran: true, want: []any{"g2"}},
		{name: "approved with no grants at all carries none", hooks: []Hook{askWith(nil)}, resolver: approve, ran: true},
		{name: "a Deny beats an approved Ask", hooks: []Hook{askWith("g1"), denyToolNamed("t", "blocked")}, resolver: approve},
		{
			name: "an approved AskGrant (park resume) carries the grant", hooks: []Hook{askWith("g1")}, ran: true, want: []any{"g1"},
			ctx: func(ctx context.Context) context.Context { return ContextWithAskGrant(ctx, &AskGrant{Approved: true}) },
		},
		{
			name: "a rejected AskGrant runs nothing", hooks: []Hook{askWith("g1")}, resolver: approve,
			ctx: func(ctx context.Context) context.Context { return ContextWithAskGrant(ctx, &AskGrant{Approved: false}) },
		},
		{
			name: "grants already on the context never pass through an outright Allow", hooks: []Hook{allow}, ran: true,
			ctx: func(ctx context.Context) context.Context { return ContextWithAdmissionGrants(ctx, []any{"stale"}) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, entry := range []string{"Execute", "ExecuteWithTool"} {
				seen := &grantSeen{}
				tool := seen.tool("t")
				exec := executorWith(tool, tc.hooks, tc.resolver)
				ctx := context.Background()
				if tc.ctx != nil {
					ctx = tc.ctx(ctx)
				}
				var res *Result
				var err error
				if entry == "Execute" {
					res, err = exec.Execute(ctx, "t", map[string]interface{}{})
				} else {
					res, err = exec.ExecuteWithTool(ctx, tool, map[string]interface{}{})
				}
				require.NoError(t, err, entry)
				if !tc.ran {
					assert.False(t, res.Success, entry)
					assert.Empty(t, seen.runs, "%s: the body must not run", entry)
					continue
				}
				assert.Equal(t, tc.want, seen.only(t), entry)
			}
		})
	}
}

func TestAdmissionResult_ApprovedAndGrants(t *testing.T) {
	chain := NewChain([]Hook{askWith("g1")}, nil, resolveTo{decision: Decision{Kind: Allow}})
	res := chain.Admit(AdmissionRequest{Ctx: context.Background(), ToolName: "t"})
	assert.Equal(t, Allow, res.Decision.Kind)
	assert.True(t, res.Approved)
	assert.Equal(t, []any{"g1"}, res.Grants)

	plain := NewChain([]Hook{fixedHook{decision: Decision{Kind: Allow}}}, nil, nil).Admit(AdmissionRequest{Ctx: context.Background(), ToolName: "t"})
	assert.False(t, plain.Approved)
	assert.Nil(t, plain.Grants)
}

// Preflight decides without resolving, so it never reports an approval.
func TestPreflight_NeverApproves(t *testing.T) {
	chain := NewChain([]Hook{askWith("g1")}, nil, resolveTo{decision: Decision{Kind: Allow}})
	d := chain.Preflight(AdmissionRequest{Ctx: context.Background(), ToolName: "t"})
	assert.Equal(t, Ask, d.Kind)
}

// A tool approved with grants that calls another tool through the executor
// must not hand its grants to that nested call.
func TestAdmissionGrants_NotInheritedByNestedCalls(t *testing.T) {
	inner := &grantSeen{}
	innerTool := inner.tool("inner")
	reg := NewRegistry()
	reg.Register(innerTool)
	exec := NewExecutor(reg)

	var outerSaw []any
	outerTool := &MockTool{MockName: "outer", MockExecute: func(ctx context.Context, _ map[string]interface{}) (*Result, error) {
		outerSaw = AdmissionGrantsFromContext(ctx)
		return exec.ExecuteWithTool(ctx, innerTool, map[string]interface{}{})
	}}
	reg.Register(outerTool)
	exec.SetAdmissionChain(NewChain([]Hook{
		fixedHook{match: func(r AdmissionRequest) bool { return r.ToolName == "outer" }, decision: Decision{Kind: Ask, Grant: "outer-grant"}},
		fixedHook{match: func(r AdmissionRequest) bool { return r.ToolName == "inner" }, decision: Decision{Kind: Allow}},
	}, nil, resolveTo{decision: Decision{Kind: Allow}}))

	res, err := exec.Execute(context.Background(), "outer", map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, res.Success)
	assert.Equal(t, []any{"outer-grant"}, outerSaw)
	assert.Nil(t, inner.only(t), "the nested call was allowed outright and carries no grants")
}

func TestAdmissionGrants_ConcurrentCalls(t *testing.T) {
	seen := &grantSeen{}
	tool := seen.tool("t")
	reg := NewRegistry()
	reg.Register(tool)
	exec := NewExecutor(reg)
	exec.SetAdmissionChain(NewChain([]Hook{grantFromParam{}}, nil, resolveTo{decision: Decision{Kind: Allow}}))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := exec.Execute(context.Background(), "t", map[string]interface{}{"n": fmt.Sprint(i)})
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()
	seen.mu.Lock()
	defer seen.mu.Unlock()
	require.Len(t, seen.runs, 50)
	got := map[any]bool{}
	for _, run := range seen.runs {
		require.Len(t, run, 1)
		got[run[0]] = true
	}
	assert.Len(t, got, 50, "each call carries its own grant")
}

// grantFromParam asks on every call, granting the call's own "n" param.
type grantFromParam struct{}

func (grantFromParam) Matches(AdmissionRequest) bool { return true }
func (grantFromParam) Evaluate(req AdmissionRequest) Decision {
	return Decision{Kind: Ask, Grant: req.Params["n"]}
}

func TestApprovalParamsMaxBytes(t *testing.T) {
	assert.Equal(t, paramsMaxBytes, ApprovalParamsMaxBytes)
}

// An AskGrant (the park resume's approval) lifts the Ask of the call it was
// installed for, and nothing nested inside that call's body: a tool that
// re-enters the executor must not borrow the approval for calls nobody saw.
func TestAskGrant_NotInheritedByNestedCalls(t *testing.T) {
	inner := &grantSeen{}
	innerTool := inner.tool("inner")
	reg := NewRegistry()
	reg.Register(innerTool)
	exec := NewExecutor(reg)

	var nested *Result
	var outerSawGrant bool
	outerTool := &MockTool{MockName: "outer", MockExecute: func(ctx context.Context, _ map[string]interface{}) (*Result, error) {
		outerSawGrant = AskGrantFromContext(ctx) != nil
		r, err := exec.ExecuteWithTool(ctx, innerTool, map[string]interface{}{})
		nested = r
		return r, err
	}}
	reg.Register(outerTool)
	exec.SetAdmissionChain(NewChain([]Hook{
		fixedHook{decision: Decision{Kind: Ask, Reason: "every call asks"}},
	}, nil, resolveTo{decision: Decision{Kind: Deny, Reason: "the resolver rejects"}}))

	ctx := ContextWithAskGrant(context.Background(), &AskGrant{Approved: true})
	_, err := exec.Execute(ctx, "outer", map[string]interface{}{})
	require.NoError(t, err)
	assert.False(t, outerSawGrant, "the tool body does not receive the AskGrant")
	require.NotNil(t, nested)
	assert.False(t, nested.Success, "the nested Ask went to the resolver, which rejected it")
	assert.Equal(t, "the resolver rejects", nested.Error.Message)
	assert.Empty(t, inner.runs)
}
