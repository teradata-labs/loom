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

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// D-1 acceptance tests for the admission hook chain at the tool-admit seam.
// Every case drives the shuttle Executor's real Execute/ExecuteWithTool interface
// with a chain attached (SE-1 harness) and asserts the decision's observable
// effect on the returned Result and on whether the tool body ran.

// -- test hooks -------------------------------------------------------------

// fixedHook matches by an optional predicate (nil ⇒ matches every call) and
// returns a fixed decision. It is the building block for the decision cases.
type fixedHook struct {
	match    func(AdmissionRequest) bool
	decision Decision
}

func (h fixedHook) Matches(req AdmissionRequest) bool {
	if h.match == nil {
		return true
	}
	return h.match(req)
}
func (h fixedHook) Evaluate(req AdmissionRequest) Decision { return h.decision }

// denyToolNamed matches only the given tool name and denies it.
func denyToolNamed(name, reason string) fixedHook {
	return fixedHook{
		match:    func(req AdmissionRequest) bool { return req.ToolName == name },
		decision: Decision{Kind: Deny, Reason: reason},
	}
}

// panicHook matches every call and panics from Evaluate, exercising the
// chain's fail-closed recovery.
type panicHook struct{}

func (panicHook) Matches(req AdmissionRequest) bool { return true }
func (panicHook) Evaluate(req AdmissionRequest) Decision {
	panic("boom")
}

// resolveTo is an AskResolver that turns any Ask verdict into a fixed decision.
type resolveTo struct{ decision Decision }

func (r resolveTo) Resolve(req AdmissionRequest, d Decision) Decision { return r.decision }

// recordingHook appends "decision" to a shared order log when it evaluates,
// so a test can assert the decision was produced before the tool body ran.
type recordingHook struct {
	rec      *orderLog
	decision Decision
}

func (h recordingHook) Matches(req AdmissionRequest) bool { return true }
func (h recordingHook) Evaluate(req AdmissionRequest) Decision {
	h.rec.add("decision")
	return h.decision
}

// orderLog records the order of admission/body events across objects.
type orderLog struct {
	mu     sync.Mutex
	events []string
}

func (l *orderLog) add(s string) {
	l.mu.Lock()
	l.events = append(l.events, s)
	l.mu.Unlock()
}

// allowChain builds a chain whose single hook matches every call and allows it.
func allowChain() *Chain {
	return NewChain([]Hook{fixedHook{decision: Decision{Kind: Allow}}}, nil, nil)
}

// countingTool is a MockTool that records how many times its body ran; a body
// count of zero after a Deny proves the tool never executed.
func countingTool(name string) *MockTool {
	return &MockTool{MockName: name}
}

// ac1: a deny decision returns a permission_denied Result and the tool body
// never runs.
func TestAdmission_Deny_ReturnsPermissionDeniedAndSkipsBody(t *testing.T) {
	reg := NewRegistry()
	tool := countingTool("blocked")
	reg.Register(tool)

	exec := NewExecutor(reg)
	exec.SetAdmissionChain(NewChain(
		[]Hook{denyToolNamed("blocked", "blocked by policy")},
		nil, nil,
	))

	result, err := exec.Execute(context.Background(), "blocked", map[string]interface{}{"k": "v"})
	require.NoError(t, err, "a policy deny is a Result, not a transport error")
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.NotNil(t, result.Error)
	require.Equal(t, "permission_denied", result.Error.Code)
	require.Equal(t, "blocked by policy", result.Error.Message)
	require.False(t, result.Error.Retryable)
	require.Equal(t, 0, tool.ExecuteCount, "denied tool body must not run")
}

// ac2: an allow decision runs the tool and returns its normal result.
func TestAdmission_Allow_RunsToolAndReturnsResult(t *testing.T) {
	reg := NewRegistry()
	tool := countingTool("greet")
	reg.Register(tool)

	exec := NewExecutor(reg)
	exec.SetAdmissionChain(allowChain())

	result, err := exec.Execute(context.Background(), "greet", map[string]interface{}{"k": "v"})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Nil(t, result.Error)
	require.Equal(t, "mock result", result.Data)
	require.Equal(t, 1, tool.ExecuteCount, "allowed tool body runs exactly once")
	require.Equal(t, map[string]interface{}{"k": "v"}, tool.LastParams)
}

// ac3: a tool call matched by no hook behaves exactly as with no chain attached
// — the §8-R2 byte-for-byte pass-through guard. The no-chain baseline and a
// chain whose only hook never matches must yield identical Results.
func TestAdmission_Unmatched_EqualsNoChainBaseline(t *testing.T) {
	run := func(attachChain bool) *Result {
		reg := NewRegistry()
		reg.Register(countingTool("passthru"))
		exec := NewExecutor(reg)
		if attachChain {
			// A hook that matches nothing ⇒ NoDecision ⇒ pass-through.
			exec.SetAdmissionChain(NewChain(
				[]Hook{fixedHook{
					match:    func(req AdmissionRequest) bool { return false },
					decision: Decision{Kind: Deny, Reason: "unreachable"},
				}},
				nil, nil,
			))
		}
		res, err := exec.Execute(context.Background(), "passthru", map[string]interface{}{"k": "v"})
		require.NoError(t, err)
		return res
	}

	baseline := run(false)
	withNonMatchingChain := run(true)

	// Success, Data, Error, and Metadata must match byte-for-byte; only timing
	// (ExecutionTimeMs) may differ between runs.
	require.Equal(t, baseline.Success, withNonMatchingChain.Success)
	require.Equal(t, baseline.Data, withNonMatchingChain.Data)
	require.Equal(t, baseline.Error, withNonMatchingChain.Error)
	require.Equal(t, baseline.Metadata, withNonMatchingChain.Metadata)
	// The pass-through path stamps no admission metadata.
	require.NotContains(t, withNonMatchingChain.Metadata, "admission.decision")
}

// ac4: for every tool call a decision is produced before the tool body runs;
// the ordering is observable on an allow, and on a deny the body never runs
// (SC-007).
func TestAdmission_DecisionPrecedesBody(t *testing.T) {
	t.Run("allow orders decision before body", func(t *testing.T) {
		rec := &orderLog{}
		reg := NewRegistry()
		tool := &MockTool{MockName: "ordered", MockExecute: func(ctx context.Context, p map[string]interface{}) (*Result, error) {
			rec.add("body")
			return &Result{Success: true}, nil
		}}
		reg.Register(tool)

		exec := NewExecutor(reg)
		exec.SetAdmissionChain(NewChain(
			[]Hook{recordingHook{rec: rec, decision: Decision{Kind: Allow}}},
			nil, nil,
		))

		_, err := exec.Execute(context.Background(), "ordered", nil)
		require.NoError(t, err)
		require.Equal(t, []string{"decision", "body"}, rec.events,
			"admission decision must be produced before the tool body runs")
	})

	t.Run("deny reaches a decision and never runs the body", func(t *testing.T) {
		rec := &orderLog{}
		reg := NewRegistry()
		tool := &MockTool{MockName: "ordered", MockExecute: func(ctx context.Context, p map[string]interface{}) (*Result, error) {
			rec.add("body")
			return &Result{Success: true}, nil
		}}
		reg.Register(tool)

		exec := NewExecutor(reg)
		exec.SetAdmissionChain(NewChain(
			[]Hook{recordingHook{rec: rec, decision: Decision{Kind: Deny, Reason: "no"}}},
			nil, nil,
		))

		_, err := exec.Execute(context.Background(), "ordered", nil)
		require.NoError(t, err)
		require.Equal(t, []string{"decision"}, rec.events,
			"a decision is produced but the denied body never runs")
	})
}

// ac5: when ≥2 hooks return differing decisions, the outcome is the most
// restrictive under Deny > Ask > Allow.
func TestAdmission_MostRestrictiveWins(t *testing.T) {
	t.Run("deny outranks allow", func(t *testing.T) {
		reg := NewRegistry()
		tool := countingTool("multi")
		reg.Register(tool)
		exec := NewExecutor(reg)
		exec.SetAdmissionChain(NewChain([]Hook{
			fixedHook{decision: Decision{Kind: Allow}},
			fixedHook{decision: Decision{Kind: Deny, Reason: "hard no"}},
		}, nil, nil))

		result, err := exec.Execute(context.Background(), "multi", nil)
		require.NoError(t, err)
		require.Equal(t, "permission_denied", result.Error.Code)
		require.Equal(t, 0, tool.ExecuteCount)
	})

	t.Run("ask outranks allow then resolves", func(t *testing.T) {
		// Combined pre-resolver decision is Ask (Ask > Allow). The resolver is
		// consulted and denies — proving Ask, not Allow, was the combined verdict.
		reg := NewRegistry()
		tool := countingTool("multi")
		reg.Register(tool)
		exec := NewExecutor(reg)
		exec.SetAdmissionChain(NewChain([]Hook{
			fixedHook{decision: Decision{Kind: Allow}},
			fixedHook{decision: Decision{Kind: Ask}},
		}, nil, resolveTo{decision: Decision{Kind: Deny, Reason: "approval refused"}}))

		result, err := exec.Execute(context.Background(), "multi", nil)
		require.NoError(t, err)
		require.Equal(t, "permission_denied", result.Error.Code,
			"Ask was selected over Allow; the resolver then denied")
		require.Equal(t, 0, tool.ExecuteCount)
	})

	t.Run("deny outranks both ask and allow", func(t *testing.T) {
		reg := NewRegistry()
		tool := countingTool("multi")
		reg.Register(tool)
		exec := NewExecutor(reg)
		exec.SetAdmissionChain(NewChain([]Hook{
			fixedHook{decision: Decision{Kind: Allow}},
			fixedHook{decision: Decision{Kind: Ask}},
			fixedHook{decision: Decision{Kind: Deny, Reason: "hard no"}},
		}, nil, resolveTo{decision: Decision{Kind: Allow}}))

		result, err := exec.Execute(context.Background(), "multi", nil)
		require.NoError(t, err)
		require.Equal(t, "permission_denied", result.Error.Code)
		require.Equal(t, 0, tool.ExecuteCount)
	})
}

// ac6: a hook that panics yields a deny and the tool body does not run
// (C-014 fail-closed).
func TestAdmission_HookPanic_FailsClosed(t *testing.T) {
	reg := NewRegistry()
	tool := countingTool("risky")
	reg.Register(tool)

	exec := NewExecutor(reg)
	exec.SetAdmissionChain(NewChain([]Hook{panicHook{}}, nil, nil))

	result, err := exec.Execute(context.Background(), "risky", nil)
	require.NoError(t, err, "a panicking hook must not surface as a transport error")
	require.NotNil(t, result)
	require.Equal(t, "permission_denied", result.Error.Code)
	require.Equal(t, 0, tool.ExecuteCount, "fail-closed: body never runs on hook panic")
}

// ac6 (Ask fail-closed): an Ask verdict with no resolver wired fails closed to
// Deny and the body does not run.
func TestAdmission_AskWithoutResolver_FailsClosed(t *testing.T) {
	reg := NewRegistry()
	tool := countingTool("needs_approval")
	reg.Register(tool)

	exec := NewExecutor(reg)
	exec.SetAdmissionChain(NewChain(
		[]Hook{fixedHook{decision: Decision{Kind: Ask}}},
		nil, nil, // no AskResolver
	))

	result, err := exec.Execute(context.Background(), "needs_approval", nil)
	require.NoError(t, err)
	require.Equal(t, "permission_denied", result.Error.Code)
	require.Equal(t, 0, tool.ExecuteCount)
}

// ac7: a deny holds unchanged after a prior tool call errored — the admission
// decision is independent of any prior tool's success/failure (FR-017, SC-008).
func TestAdmission_DenyIndependentOfPriorToolError(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&errorTool{name: "flaky"}) // no hook matches ⇒ runs and errors
	blocked := countingTool("blocked")
	reg.Register(blocked)

	exec := NewExecutor(reg)
	exec.SetAdmissionChain(NewChain(
		[]Hook{denyToolNamed("blocked", "blocked by policy")},
		nil, nil,
	))

	// A prior tool call runs and fails at the body.
	first, err := exec.Execute(context.Background(), "flaky", nil)
	require.NoError(t, err)
	require.Equal(t, "execution_failed", first.Error.Code)

	// The subsequent denied call is unaffected by the prior failure.
	second, err := exec.Execute(context.Background(), "blocked", nil)
	require.NoError(t, err)
	require.Equal(t, "permission_denied", second.Error.Code)
	require.Equal(t, 0, blocked.ExecuteCount, "prior tool error opens no bypass")
}

// -- ac8 fakes: external/dynamic tool origin ------------------------------

// fakeMCPClient is an MCP client double whose CallTool records whether the tool
// body ran. Its method signature matches the mcpClient interface the executor's
// mcpToolWrapper asserts against.
type fakeMCPClient struct {
	mu     sync.Mutex
	called bool
}

func (c *fakeMCPClient) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (interface{}, error) {
	c.mu.Lock()
	c.called = true
	c.mu.Unlock()
	return map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": "ok"},
		},
	}, nil
}

func (c *fakeMCPClient) wasCalled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.called
}

// fakeMCPManager returns a fixed MCP client for any server.
type fakeMCPManager struct{ client interface{} }

func (m *fakeMCPManager) GetClient(serverName string) (interface{}, error) { return m.client, nil }

// fakeToolRegistry resolves one externally-provided MCP tool name via Search,
// standing in for a real tool registry so the executor's dynamic-registration
// path acquires the tool before the admission check.
type fakeToolRegistry struct{ toolName string }

func (r *fakeToolRegistry) Search(ctx context.Context, req *loomv1.SearchToolsRequest) (*loomv1.SearchToolsResponse, error) {
	return &loomv1.SearchToolsResponse{
		Results: []*loomv1.ToolSearchResult{
			{Tool: &loomv1.IndexedTool{
				Name:      r.toolName,
				Source:    loomv1.ToolSource_TOOL_SOURCE_MCP,
				McpServer: "fake_server",
			}},
		},
	}, nil
}

// ac8: an externally/dynamically-resolved tool name is governed by the same
// admission check as a locally-registered tool — the decision landing after
// acquisition and before the tool body runs.
func TestAdmission_ExternalTool_GovernedAtSameSeam(t *testing.T) {
	t.Run("deny after acquisition, before body", func(t *testing.T) {
		reg := NewRegistry()
		client := &fakeMCPClient{}
		exec := NewExecutor(reg)
		exec.SetToolRegistry(&fakeToolRegistry{toolName: "ext_tool"})
		exec.SetMCPManager(&fakeMCPManager{client: client})
		exec.SetAdmissionChain(NewChain(
			[]Hook{denyToolNamed("ext_tool", "external tool blocked")},
			nil, nil,
		))

		_, ok := reg.Get("ext_tool")
		require.False(t, ok, "ext_tool is not locally registered up front")

		result, err := exec.Execute(context.Background(), "ext_tool", nil)
		require.NoError(t, err)
		require.Equal(t, "permission_denied", result.Error.Code)
		require.False(t, client.wasCalled(), "denied external tool body must not run")

		// The tool was acquired (dynamically registered) before the deny — proving
		// the check lands after acquisition, at the same seam as a local tool.
		_, ok = reg.Get("ext_tool")
		require.True(t, ok, "external tool is acquired before the admission check")
	})

	t.Run("allow runs the acquired external tool", func(t *testing.T) {
		reg := NewRegistry()
		client := &fakeMCPClient{}
		exec := NewExecutor(reg)
		exec.SetToolRegistry(&fakeToolRegistry{toolName: "ext_tool"})
		exec.SetMCPManager(&fakeMCPManager{client: client})
		exec.SetAdmissionChain(allowChain())

		result, err := exec.Execute(context.Background(), "ext_tool", nil)
		require.NoError(t, err)
		require.True(t, result.Success)
		require.True(t, client.wasCalled(), "allowed external tool body runs")
	})
}

// ac9: the existing name-level behavior (tools.permissions
// allow/deny/disabled/yolo) is preserved as the chain's first hook (permHook).
func TestAdmission_PermHook_PreservesNameLevelPermissions(t *testing.T) {
	t.Run("disabled tool is denied through the chain", func(t *testing.T) {
		reg := NewRegistry()
		tool := countingTool("dangerous")
		reg.Register(tool)

		pc := NewPermissionChecker(PermissionConfig{DisabledTools: []string{"dangerous"}})
		exec := NewExecutor(reg)
		exec.SetAdmissionChain(NewChain([]Hook{newPermHook(pc)}, nil, nil))

		result, err := exec.Execute(context.Background(), "dangerous", nil)
		require.NoError(t, err)
		require.Equal(t, "permission_denied", result.Error.Code)
		require.Contains(t, result.Error.Message, "disabled by configuration")
		require.Equal(t, 0, tool.ExecuteCount)
	})

	t.Run("require_approval denies a non-allowlisted tool", func(t *testing.T) {
		reg := NewRegistry()
		tool := countingTool("unlisted")
		reg.Register(tool)

		pc := NewPermissionChecker(PermissionConfig{RequireApproval: true, DefaultAction: "deny"})
		exec := NewExecutor(reg)
		exec.SetAdmissionChain(NewChain([]Hook{newPermHook(pc)}, nil, nil))

		result, err := exec.Execute(context.Background(), "unlisted", nil)
		require.NoError(t, err)
		require.Equal(t, "permission_denied", result.Error.Code)
		require.Equal(t, 0, tool.ExecuteCount)
	})

	t.Run("allowlisted tool runs through the chain", func(t *testing.T) {
		reg := NewRegistry()
		tool := countingTool("trusted")
		reg.Register(tool)

		pc := NewPermissionChecker(PermissionConfig{RequireApproval: true, AllowedTools: []string{"trusted"}})
		exec := NewExecutor(reg)
		exec.SetAdmissionChain(NewChain([]Hook{newPermHook(pc)}, nil, nil))

		result, err := exec.Execute(context.Background(), "trusted", nil)
		require.NoError(t, err)
		require.True(t, result.Success)
		require.Equal(t, 1, tool.ExecuteCount)
	})

	t.Run("yolo bypasses the name-level check", func(t *testing.T) {
		reg := NewRegistry()
		tool := countingTool("anything")
		reg.Register(tool)

		pc := NewPermissionChecker(PermissionConfig{YOLO: true, DisabledTools: []string{"anything"}})
		exec := NewExecutor(reg)
		exec.SetAdmissionChain(NewChain([]Hook{newPermHook(pc)}, nil, nil))

		result, err := exec.Execute(context.Background(), "anything", nil)
		require.NoError(t, err)
		require.True(t, result.Success, "yolo short-circuit is preserved inside the hook")
		require.Equal(t, 1, tool.ExecuteCount)
	})
}

// ExecuteWithTool carries the identical admission block as Execute: a deny
// skips the body and a matched hook governs the passed-in tool instance.
func TestAdmission_ExecuteWithTool_GovernedIdentically(t *testing.T) {
	t.Run("deny skips body", func(t *testing.T) {
		tool := countingTool("direct")
		exec := NewExecutor(NewRegistry())
		exec.SetAdmissionChain(NewChain(
			[]Hook{denyToolNamed("direct", "blocked")},
			nil, nil,
		))

		result, err := exec.ExecuteWithTool(context.Background(), tool, nil)
		require.NoError(t, err)
		require.Equal(t, "permission_denied", result.Error.Code)
		require.Equal(t, 0, tool.ExecuteCount)
	})

	t.Run("allow runs body", func(t *testing.T) {
		tool := countingTool("direct")
		exec := NewExecutor(NewRegistry())
		exec.SetAdmissionChain(allowChain())

		result, err := exec.ExecuteWithTool(context.Background(), tool, nil)
		require.NoError(t, err)
		require.True(t, result.Success)
		require.Equal(t, 1, tool.ExecuteCount)
	})
}
