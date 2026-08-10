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

// M12 — EPIC ASSEMBLY: the assembled admission guarantee driven through the real
// in-process conversation loop. The mockToolCallingLLM scripts the tool-call
// sequence; NewAgent + WithAdmissionHooks attaches a real admission chain over
// the real Executor and the executor's approved-set store; fake
// render_grant_read_sql and execute_sql tools stand in for the render/execute
// pair. These read what the consumer receives — resp.ToolExecutions, the §5.4
// approved-set (contains), and the audit decision the executor stamps — over the
// same machine the producer tiers (M1–M8) build, with only the LLM mocked.

import (
	"context"
	"sync"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Tool names and the statement param the gated-allowlist and its recorder key on.
const (
	admRenderTool = "render_grant_read_sql"
	admExecTool   = "execute_sql"
	admStmtParam  = "sql"
	admResultPath = "statements"
)

// The statements the render tool emits (the approved view + read grant) and the
// broad self-GRANT the model may attempt without rendering it first. The self-
// GRANT is never a render output, so it never enters any approved set.
const (
	admViewSQL      = "CREATE VIEW orders_ro AS SELECT * FROM orders"
	admGrantReadSQL = "GRANT SELECT ON orders_ro TO read_role"
	admSelfGrantSQL = "GRANT ALL ON orders TO PUBLIC"
)

// fakeRenderGrantReadTool is the trusted render/stage tool: it returns a fixed
// list of statements under admResultPath, which the chain's recorder captures
// into the approved set. It cannot emit a self-GRANT.
type fakeRenderGrantReadTool struct {
	stmts []string
}

func (t *fakeRenderGrantReadTool) Name() string { return admRenderTool }
func (t *fakeRenderGrantReadTool) Description() string {
	return "Returns the view and read-grant SQL to be approved"
}
func (t *fakeRenderGrantReadTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"object": {Type: "string"}},
	}
}
func (t *fakeRenderGrantReadTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	stmts := make([]string, len(t.stmts))
	copy(stmts, t.stmts)
	return &shuttle.Result{
		Success: true,
		Data:    map[string]interface{}{admResultPath: stmts},
	}, nil
}
func (t *fakeRenderGrantReadTool) Backend() string { return "" }

// fakeExecuteSQLTool is the execute tool the gated-allowlist governs. Its body
// records every statement it actually runs, so a test can prove a denied call's
// statement never reached execution.
type fakeExecuteSQLTool struct {
	mu       sync.Mutex
	executed []string
}

func (t *fakeExecuteSQLTool) Name() string        { return admExecTool }
func (t *fakeExecuteSQLTool) Description() string { return "Runs a SQL statement" }
func (t *fakeExecuteSQLTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{admStmtParam: {Type: "string"}},
	}
}
func (t *fakeExecuteSQLTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	sql, _ := params[admStmtParam].(string)
	t.mu.Lock()
	t.executed = append(t.executed, sql)
	t.mu.Unlock()
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}
func (t *fakeExecuteSQLTool) Backend() string { return "" }

// ran reports whether the tool body ran sql, comparing on the same call-identity
// canonicalization the approved set matches on.
func (t *fakeExecuteSQLTool) ran(sql string) bool {
	want := shuttle.Canonicalize(admExecTool, map[string]interface{}{admStmtParam: sql}, admStmtParam)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.executed {
		if shuttle.Canonicalize(admExecTool, map[string]interface{}{admStmtParam: s}, admStmtParam) == want {
			return true
		}
	}
	return false
}

// buildGrantAdmissionChain assembles the chain for stateKey purely from config:
// a gated-allowlist that admits an execute_sql call only when its statement was
// captured from admRenderTool into the approved set (a recorder is contributed
// for it by the builder), plus an audit binding so the executor stamps the
// admission decision the loop surfaces. Both are wired with no source change —
// the same path serve boots.
func buildGrantAdmissionChain(t *testing.T, stateKey string) *shuttle.Chain {
	t.Helper()
	cfg := shuttle.HooksConfig{Bindings: []shuttle.HookBinding{
		{
			Kind:       "gated-allowlist",
			Scope:      admExecTool,
			StateKey:   stateKey,
			SourceTool: admRenderTool,
			StmtParam:  admStmtParam,
			ResultPath: admResultPath,
		},
		{
			Kind:  "audit",
			Scope: admExecTool,
		},
	}}
	chain, err := shuttle.BuildChainFromConfig(cfg, shuttle.ChainDeps{})
	if err != nil {
		t.Fatalf("BuildChainFromConfig: %v", err)
	}
	return chain
}

// startAdmissionAgent builds an agent over the real executor with the mock LLM
// and (optionally) the admission chain attached, and registers the render and
// execute tools. A nil chain leaves the executor an ungoverned pass-through.
func startAdmissionAgent(t *testing.T, llm LLMProvider, chain *shuttle.Chain) (*Agent, *fakeExecuteSQLTool) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	opts := []Option{WithConfig(cfg)}
	if chain != nil {
		opts = append(opts, WithAdmissionHooks(chain))
	}
	ag := NewAgent(&mockBackend{}, llm, opts...)
	ag.RegisterTool(&fakeRenderGrantReadTool{stmts: []string{admViewSQL, admGrantReadSQL}})
	exec := &fakeExecuteSQLTool{}
	ag.RegisterTool(exec)
	return ag, exec
}

// grantSetContains reads the §5.4 approved set through the accessor the agent's
// executor actually wired — the set is per-accessor state, so membership must be
// asked of the same instance the recorder wrote — from the same session the loop
// ran under, and reports whether sql's call identity is a member.
func grantSetContains(t *testing.T, ag *Agent, sessionID, stateKey, sql string) bool {
	t.Helper()
	acc := ag.executor.ApprovedSet()
	if acc == nil {
		t.Fatal("agent's executor has no approved-set accessor wired")
	}
	ctx := session.WithSessionID(context.Background(), sessionID)
	ok, err := acc.Contains(ctx, stateKey, shuttle.Canonicalize(admExecTool, map[string]interface{}{admStmtParam: sql}, admStmtParam))
	if err != nil {
		t.Fatalf("approved-set Contains(%q): %v", sql, err)
	}
	return ok
}

// execExecutionFor returns the execute_sql ToolExecution whose statement is sql,
// or nil if the loop recorded no such call.
func execExecutionFor(resp *Response, sql string) *ToolExecution {
	for i := range resp.ToolExecutions {
		te := &resp.ToolExecutions[i]
		if te.ToolName != admExecTool {
			continue
		}
		if s, _ := te.Input[admStmtParam].(string); s == sql {
			return te
		}
	}
	return nil
}

// execToolCall scripts an execute_sql tool call for the given statement.
func execToolCall(id, sql string) llmtypes.ToolCall {
	return llmtypes.ToolCall{ID: id, Name: admExecTool, Input: map[string]interface{}{admStmtParam: sql}}
}

// renderToolCall scripts the render_grant_read_sql tool call.
func renderToolCall(id string) llmtypes.ToolCall {
	return llmtypes.ToolCall{ID: id, Name: admRenderTool, Input: map[string]interface{}{"object": "orders"}}
}

// TestAgent_AdmissionLoop_RenderThenGrantRead_Admitted drives render → execute
// through the real loop: the recorder captures the render output into the
// approved set before any execute admit, and the view + read-grant, being in the
// set, are admitted and run (M3 → M2/M1 assembled).
func TestAgent_AdmissionLoop_RenderThenGrantRead_Admitted(t *testing.T) {
	const sessionID = "adm-loop-render-then-grant"
	const stateKey = "grants/render-then-grant"

	mockLLM := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{renderToolCall("call_render")}},
		{toolCalls: []llmtypes.ToolCall{execToolCall("call_view", admViewSQL), execToolCall("call_grant", admGrantReadSQL)}},
		{content: "granted read access"},
	}}

	ag, exec := startAdmissionAgent(t, mockLLM, buildGrantAdmissionChain(t, stateKey))

	resp, err := ag.Chat(context.Background(), sessionID, "grant read access to orders")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	// The render output is in the approved set — recorded before either execute
	// was admitted, which their admission proves.
	if !grantSetContains(t, ag, sessionID, stateKey, admViewSQL) {
		t.Error("expected the rendered CREATE VIEW in the approved set")
	}
	if !grantSetContains(t, ag, sessionID, stateKey, admGrantReadSQL) {
		t.Error("expected the rendered read GRANT in the approved set")
	}

	for _, sql := range []string{admViewSQL, admGrantReadSQL} {
		te := execExecutionFor(resp, sql)
		if te == nil {
			t.Fatalf("no execute_sql ToolExecution for %q", sql)
		}
		if te.Result == nil || !te.Result.Success {
			t.Errorf("expected execute_sql(%q) to succeed, got %+v", sql, te.Result)
		}
		if !exec.ran(sql) {
			t.Errorf("expected execute_sql body to run %q", sql)
		}
	}
}

// TestAgent_AdmissionLoop_UnrenderedSelfGrant_HardDenied drives render →
// execute(view+grant) → execute(self-GRANT) where the self-GRANT was never
// rendered/approved and no approval step ran. The rendered pair is admitted; the
// self-GRANT is a hard permission_denied and its body never runs — the guarantee
// holds with the approval step skipped (SC-001/SC-002, C-003/FR-005).
func TestAgent_AdmissionLoop_UnrenderedSelfGrant_HardDenied(t *testing.T) {
	const sessionID = "adm-loop-unrendered-self-grant"
	const stateKey = "grants/unrendered-self-grant"

	mockLLM := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{renderToolCall("call_render")}},
		{toolCalls: []llmtypes.ToolCall{execToolCall("call_view", admViewSQL), execToolCall("call_grant", admGrantReadSQL)}},
		{toolCalls: []llmtypes.ToolCall{execToolCall("call_self_grant", admSelfGrantSQL)}},
		{content: "done"},
	}}

	ag, exec := startAdmissionAgent(t, mockLLM, buildGrantAdmissionChain(t, stateKey))

	resp, err := ag.Chat(context.Background(), sessionID, "grant read access, then grant everything")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	// The rendered pair still admits and runs.
	for _, sql := range []string{admViewSQL, admGrantReadSQL} {
		te := execExecutionFor(resp, sql)
		if te == nil || te.Result == nil || !te.Result.Success {
			t.Fatalf("expected execute_sql(%q) to succeed, got %+v", sql, te)
		}
	}

	// The unrendered self-GRANT is a hard deny: permission_denied, not a
	// pending/ask, and it never entered the approved set.
	if grantSetContains(t, ag, sessionID, stateKey, admSelfGrantSQL) {
		t.Fatal("the self-GRANT must never be in the approved set")
	}
	te := execExecutionFor(resp, admSelfGrantSQL)
	if te == nil {
		t.Fatal("no execute_sql ToolExecution for the self-GRANT")
	}
	if te.Result == nil || te.Result.Success {
		t.Fatalf("expected the self-GRANT denied, got %+v", te.Result)
	}
	if te.Result.Error == nil || te.Result.Error.Code != "permission_denied" {
		t.Errorf("expected permission_denied on the self-GRANT, got %+v", te.Result.Error)
	}
	if exec.ran(admSelfGrantSQL) {
		t.Error("execute_sql body must never have run the self-GRANT")
	}
}

// TestAgent_AdmissionLoop_ModelSkipsRender_SelfGrantDenied drives a self-GRANT
// with no render call at all. The deny follows from the empty approved set, not
// from call ordering or narration: with nothing rendered, the self-GRANT is
// denied and never runs.
func TestAgent_AdmissionLoop_ModelSkipsRender_SelfGrantDenied(t *testing.T) {
	const sessionID = "adm-loop-model-skips-render"
	const stateKey = "grants/model-skips-render"

	mockLLM := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{execToolCall("call_self_grant", admSelfGrantSQL)}},
		{content: "done"},
	}}

	ag, exec := startAdmissionAgent(t, mockLLM, buildGrantAdmissionChain(t, stateKey))

	resp, err := ag.Chat(context.Background(), sessionID, "just grant everything")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	// No render ran, so the set is empty for every statement.
	if grantSetContains(t, ag, sessionID, stateKey, admSelfGrantSQL) {
		t.Fatal("the self-GRANT must never be in the approved set")
	}
	if grantSetContains(t, ag, sessionID, stateKey, admGrantReadSQL) {
		t.Fatal("the approved set must be empty when render is skipped")
	}

	te := execExecutionFor(resp, admSelfGrantSQL)
	if te == nil {
		t.Fatal("no execute_sql ToolExecution for the self-GRANT")
	}
	if te.Result == nil || te.Result.Success {
		t.Fatalf("expected the self-GRANT denied, got %+v", te.Result)
	}
	if te.Result.Error == nil || te.Result.Error.Code != "permission_denied" {
		t.Errorf("expected permission_denied on the self-GRANT, got %+v", te.Result.Error)
	}
	if exec.ran(admSelfGrantSQL) {
		t.Error("execute_sql body must never have run the self-GRANT")
	}
}

// TestAgent_AdmissionLoop_HarnessNotSkillEnforces runs the identical scripted
// sequence twice — once with the chain attached, once without — proving it is
// the attached chain, not skill prose, that blocks the self-GRANT (C-001). The
// governed run also reads the audit decision the executor stamped: deny.
func TestAgent_AdmissionLoop_HarnessNotSkillEnforces(t *testing.T) {
	script := func() []mockLLMResponse {
		return []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{renderToolCall("call_render")}},
			{toolCalls: []llmtypes.ToolCall{execToolCall("call_view", admViewSQL), execToolCall("call_grant", admGrantReadSQL)}},
			{toolCalls: []llmtypes.ToolCall{execToolCall("call_self_grant", admSelfGrantSQL)}},
			{content: "done"},
		}
	}

	// Governed: the chain is attached — the self-GRANT is denied and the audit
	// decision on that run reads "deny".
	const govSession = "adm-loop-harness-governed"
	const stateKey = "grants/harness-governed"
	govLLM := &mockToolCallingLLM{responses: script()}
	govAgent, govExec := startAdmissionAgent(t, govLLM, buildGrantAdmissionChain(t, stateKey))

	govResp, err := govAgent.Chat(context.Background(), govSession, "grant read, then grant everything")
	if err != nil {
		t.Fatalf("governed Chat failed: %v", err)
	}
	govTE := execExecutionFor(govResp, admSelfGrantSQL)
	if govTE == nil {
		t.Fatal("no execute_sql ToolExecution for the governed self-GRANT")
	}
	if govTE.Result == nil || govTE.Result.Success {
		t.Fatalf("expected the governed self-GRANT denied, got %+v", govTE.Result)
	}
	if govTE.Result.Error == nil || govTE.Result.Error.Code != "permission_denied" {
		t.Errorf("expected permission_denied on the governed self-GRANT, got %+v", govTE.Result.Error)
	}
	if govExec.ran(admSelfGrantSQL) {
		t.Error("with the chain attached, execute_sql must never have run the self-GRANT")
	}
	if govTE.AdmissionDecision != "deny" {
		t.Errorf("expected audit decision \"deny\" on the governed self-GRANT, got %q", govTE.AdmissionDecision)
	}

	// Ungoverned: no chain attached — the identical script runs the self-GRANT,
	// showing the skill prose alone never blocked it; the chain is the enforcer.
	const ungovSession = "adm-loop-harness-ungoverned"
	ungovLLM := &mockToolCallingLLM{responses: script()}
	ungovAgent, ungovExec := startAdmissionAgent(t, ungovLLM, nil)

	ungovResp, err := ungovAgent.Chat(context.Background(), ungovSession, "grant read, then grant everything")
	if err != nil {
		t.Fatalf("ungoverned Chat failed: %v", err)
	}
	ungovTE := execExecutionFor(ungovResp, admSelfGrantSQL)
	if ungovTE == nil {
		t.Fatal("no execute_sql ToolExecution for the ungoverned self-GRANT")
	}
	if ungovTE.Result == nil || !ungovTE.Result.Success {
		t.Fatalf("expected the ungoverned self-GRANT to run, got %+v", ungovTE.Result)
	}
	if !ungovExec.ran(admSelfGrantSQL) {
		t.Error("without the chain, execute_sql should have run the self-GRANT")
	}
	if ungovTE.AdmissionDecision != "" {
		t.Errorf("expected no audit decision on an ungoverned run, got %q", ungovTE.AdmissionDecision)
	}
}
