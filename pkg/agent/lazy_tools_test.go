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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// newTestAgent creates a minimal agent suitable for lazy-tool tests.
func newTestAgent() *Agent {
	return &Agent{
		tools: shuttle.NewRegistry(),
	}
}

// mockTool creates a named MockTool for testing.
func mockTool(name string) shuttle.Tool {
	return &shuttle.MockTool{MockName: name}
}

// alwaysTrigger returns true for any message.
func alwaysTrigger(_ string) bool { return true }

// neverTrigger returns false for any message.
func neverTrigger(_ string) bool { return false }

// TestRegisterLazyTools_TriggerFalse verifies tools are NOT registered when trigger returns false.
func TestRegisterLazyTools_TriggerFalse(t *testing.T) {
	a := newTestAgent()
	tools := []shuttle.Tool{mockTool("tool_a")}

	a.RegisterLazyTools(tools, neverTrigger)
	a.evaluateLazyTools("some message that never triggers")

	assert.False(t, a.tools.IsRegistered("tool_a"), "tool should not be registered when trigger returns false")
}

// TestRegisterLazyTools_TriggerTrue verifies tools ARE registered when trigger returns true.
func TestRegisterLazyTools_TriggerTrue(t *testing.T) {
	a := newTestAgent()
	tools := []shuttle.Tool{mockTool("tool_b")}

	a.RegisterLazyTools(tools, alwaysTrigger)
	a.evaluateLazyTools("trigger message")

	assert.True(t, a.tools.IsRegistered("tool_b"), "tool should be registered when trigger returns true")
}

// TestRegisterLazyTools_Idempotent verifies calling evaluateLazyTools multiple times only registers once.
func TestRegisterLazyTools_Idempotent(t *testing.T) {
	a := newTestAgent()
	tools := []shuttle.Tool{mockTool("tool_c")}

	a.RegisterLazyTools(tools, alwaysTrigger)
	a.evaluateLazyTools("trigger 1")
	a.evaluateLazyTools("trigger 2")
	a.evaluateLazyTools("trigger 3")

	assert.True(t, a.tools.IsRegistered("tool_c"))
	assert.Equal(t, 1, a.tools.Count())
}

// TestRegisterLazyTools_MultipleSets verifies independent lazy sets are evaluated independently.
func TestRegisterLazyTools_MultipleSets(t *testing.T) {
	a := newTestAgent()

	uiTools := []shuttle.Tool{mockTool("create_ui_app"), mockTool("list_component_types")}
	debugTools := []shuttle.Tool{mockTool("debug_tool")}

	uiTrigger := func(msg string) bool { return msg == "show me a dashboard" }
	debugTrigger := func(msg string) bool { return msg == "debug this" }

	a.RegisterLazyTools(uiTools, uiTrigger)
	a.RegisterLazyTools(debugTools, debugTrigger)

	// Neither fires yet.
	a.evaluateLazyTools("just a normal query")
	assert.False(t, a.tools.IsRegistered("create_ui_app"))
	assert.False(t, a.tools.IsRegistered("debug_tool"))

	// UI trigger fires.
	a.evaluateLazyTools("show me a dashboard")
	assert.True(t, a.tools.IsRegistered("create_ui_app"))
	assert.True(t, a.tools.IsRegistered("list_component_types"))
	assert.False(t, a.tools.IsRegistered("debug_tool"))

	// Debug trigger fires.
	a.evaluateLazyTools("debug this")
	assert.True(t, a.tools.IsRegistered("debug_tool"))
}

// TestRegisterLazyTools_EmptyToolsOrNilTrigger verifies no-op for degenerate inputs.
func TestRegisterLazyTools_EmptyToolsOrNilTrigger(t *testing.T) {
	a := newTestAgent()
	// Empty tools slice — should not panic.
	a.RegisterLazyTools(nil, alwaysTrigger)
	a.RegisterLazyTools([]shuttle.Tool{}, alwaysTrigger)
	// Nil trigger — should not panic.
	a.RegisterLazyTools([]shuttle.Tool{mockTool("orphan")}, nil)

	a.evaluateLazyTools("any message")
	assert.Equal(t, 0, a.tools.Count(), "no tools should be registered for degenerate inputs")
}

// TestRegisterLazyTools_Race verifies concurrent RegisterLazyTools + evaluateLazyTools is race-free.
func TestRegisterLazyTools_Race(t *testing.T) {
	a := newTestAgent()

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			tools := []shuttle.Tool{mockTool("race_tool")}
			trigger := func(_ string) bool { return i%2 == 0 }
			a.RegisterLazyTools(tools, trigger)
			a.evaluateLazyTools("trigger message")
		}()
	}

	wg.Wait()
	// No race condition should have occurred — validated by -race detector.
	require.NotNil(t, a.tools)
}

// TestRegisterLazyTools_KeywordTrigger verifies a realistic UI keyword-based trigger.
func TestRegisterLazyTools_KeywordTrigger(t *testing.T) {
	a := newTestAgent()
	uiTools := []shuttle.Tool{mockTool("create_ui_app")}

	// Simplified ContainsUIIntent-style trigger.
	uiTrigger := func(msg string) bool {
		for _, kw := range []string{"dashboard", "chart", "visualization"} {
			if len(msg) >= len(kw) {
				for i := 0; i <= len(msg)-len(kw); i++ {
					if msg[i:i+len(kw)] == kw {
						return true
					}
				}
			}
		}
		return false
	}

	a.RegisterLazyTools(uiTools, uiTrigger)

	// Non-UI message — no registration.
	a.evaluateLazyTools("query the database for sales data")
	assert.False(t, a.tools.IsRegistered("create_ui_app"))

	// UI message — registration happens.
	a.evaluateLazyTools("create a dashboard for the sales data")
	assert.True(t, a.tools.IsRegistered("create_ui_app"))
}
