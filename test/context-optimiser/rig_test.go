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

// Package contextoptimiser drives a real Agent through scripted conversations
// and captures the exact context handed to the provider at every call, so the
// resulting stages can be read against what the context OUGHT to be.
//
// It is an instrument, not a gate: nothing in CI depends on it, and it skips
// unless LOOM_CONTEXT_OPTIMISER=1 is set. It still compiles in CI so the routes
// cannot rot silently when an API they drive changes.
//
//	LOOM_CONTEXT_OPTIMISER=1 go test -tags fts5 ./test/context-optimiser/
//
// The suite drives the agent from OUTSIDE pkg/agent — only exported API — so a
// route sees what a real consumer sees.
package contextoptimiser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/types"
)

const (
	envGate = "LOOM_CONTEXT_OPTIMISER"
)

// requireGate skips unless the suite was asked for explicitly. Keeps the routes
// out of every ordinary `go test ./...` while leaving them compiled.
func requireGate(t *testing.T) {
	t.Helper()
	if os.Getenv(envGate) == "" {
		t.Skipf("context-optimiser is an instrument, not a gate: set %s=1 to run it", envGate)
	}
}

// ---------------------------------------------------------------------------
// Fixtures — skills and patterns on disk, loaded by the real library.
// ---------------------------------------------------------------------------

const (
	skillGrantReview = "grant-review" // plain skill, references a pattern
	skillAuditTrail  = "audit-trail"  // declares a required tool
	skillProdDelete  = "prod-delete"  // HIGH risk — load must be refused
	patternJoinRef   = "grant-join"   // pattern the grant-review skill references
	requiredToolName = "web_search"   // audit-trail's required tool (scoped builtin)
	emitToolName     = "emit"         // the scriptable payload tool
	skillBodyMarker  = "GRANT REVIEW PROCEDURE"
	auditBodyMarker  = "AUDIT TRAIL PROCEDURE"
)

var skillFixtures = map[string]string{
	skillGrantReview + ".yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: ` + skillGrantReview + `
  title: Grant Review
  description: Review a grant request end to end.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    ` + skillBodyMarker + `
    1. Read the request.
    2. Check the passenger table scope.
    3. State the decision with its scope.
patterns:
  pattern_refs:
    - ` + patternJoinRef + `
`,
	skillAuditTrail + ".yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: ` + skillAuditTrail + `
  title: Audit Trail
  description: Produce an audit trail for a decision.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    ` + auditBodyMarker + `
    Record every decision with its referent.
tools:
  required_tools:
    - ` + requiredToolName + `
`,
	skillProdDelete + ".yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: ` + skillProdDelete + `
  title: Prod Delete
  description: Destructive production operation.
  domain: ops
  risk_level: HIGH
trigger:
  mode: MANUAL
prompt:
  instructions: |
    DESTRUCTIVE PROCEDURE — must never reach the context without approval.
`,
}

var patternFixtures = map[string]string{
	patternJoinRef + ".yaml": `apiVersion: loom/v1
kind: Pattern
metadata:
  name: ` + patternJoinRef + `
  description: How to join grant records to passenger scope.
spec:
  guidance: |
    GRANT JOIN PATTERN
    Join on passenger_id, filter by scope, never widen.
`,
}

// writeFixtures materialises the skill and pattern libraries under dir.
func writeFixtures(t *testing.T, dir string) (skillsDir, patternsDir string) {
	t.Helper()
	skillsDir = filepath.Join(dir, "skills")
	patternsDir = filepath.Join(dir, "patterns")
	for path, files := range map[string]map[string]string{
		skillsDir:   skillFixtures,
		patternsDir: patternFixtures,
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create fixture dir %s: %v", path, err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(path, name), []byte(body), 0o644); err != nil {
				t.Fatalf("write fixture %s: %v", name, err)
			}
		}
	}
	return skillsDir, patternsDir
}

// ---------------------------------------------------------------------------
// The scripted provider — deterministic, offline, no credentials.
// ---------------------------------------------------------------------------

// scriptedTurn is one provider response: plain text, tool calls, or a reactive
// call derived from the context the provider was just handed. `derive` keeps the
// route deterministic — it is a pure function of the context — while letting a
// turn use a value the run itself produced, such as a stored-result handle.
type scriptedTurn struct {
	text      string
	toolCalls []types.ToolCall
	derive    func([]types.Message) []types.ToolCall
}

// sayText scripts a terminal assistant turn (no tool calls), which ends a Chat.
func sayText(s string) scriptedTurn { return scriptedTurn{text: s} }

// callTool scripts one tool call.
func callTool(id, name string, input map[string]interface{}) scriptedTurn {
	return scriptedTurn{toolCalls: []types.ToolCall{{ID: id, Name: name, Input: input}}}
}

// callTools scripts a parallel batch — several tool calls in one assistant turn.
func callTools(calls ...types.ToolCall) scriptedTurn {
	return scriptedTurn{toolCalls: calls}
}

// emit scripts an emit call returning a payload of the requested size and shape.
func emit(id string, bytes int, shape string) types.ToolCall {
	return types.ToolCall{ID: id, Name: emitToolName, Input: map[string]interface{}{
		"bytes": float64(bytes), "shape": shape,
	}}
}

// scriptedLLM replays a fixed list of turns. Exhausting the script is a fault,
// not a fallback: a route that runs past its script is no longer deterministic
// and its stages mean nothing.
type scriptedLLM struct {
	mu      sync.Mutex
	turns   []scriptedTurn
	idx     int
	over    bool // set when the script was exhausted
	refuseN int  // next N Chat calls return the typed context-too-long error
}

// refuse makes the next n Chat calls return llm.ErrContextTooLong — the typed
// provider refusal that is the ONLY relief trigger (HLD §5.2 step 12). This is
// how relief is exercised deterministically: no token stuffing, no marks.
func (m *scriptedLLM) refuse(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refuseN = n
}

func (m *scriptedLLM) Name() string  { return "context-optimiser" }
func (m *scriptedLLM) Model() string { return "scripted" }

func (m *scriptedLLM) Chat(_ context.Context, messages []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refuseN > 0 {
		m.refuseN--
		return nil, fmt.Errorf("API error (status 400): context window exceeded: %w", llm.ErrContextTooLong)
	}
	if m.idx >= len(m.turns) {
		m.over = true
		return &types.LLMResponse{Content: "script exhausted"}, nil
	}
	turn := m.turns[m.idx]
	m.idx++
	calls := turn.toolCalls
	if turn.derive != nil {
		calls = turn.derive(messages)
	}
	return &types.LLMResponse{
		Content:   turn.text,
		ToolCalls: calls,
		Usage:     types.Usage{InputTokens: 10, OutputTokens: 10},
	}, nil
}

// ---------------------------------------------------------------------------
// The emit tool — the raw material for size and shape scenarios.
// ---------------------------------------------------------------------------

type emitTool struct{}

func (e *emitTool) Name() string        { return emitToolName }
func (e *emitTool) Description() string { return "Emit a payload of a given size and shape." }
func (e *emitTool) Backend() string     { return "" }
func (e *emitTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"bytes": {Type: "integer", Description: "approximate payload size"},
			"shape": {Type: "string", Description: "string | composite"},
		},
	}
}

func (e *emitTool) Execute(_ context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	size := 512
	if b, ok := params["bytes"].(float64); ok && b > 0 {
		size = int(b)
	}
	shape, _ := params["shape"].(string)
	payload := strings.Repeat("scan-row ", (size/9)+1)
	if len(payload) > size {
		payload = payload[:size]
	}
	if shape == "composite" {
		return &shuttle.Result{Success: true, Data: map[string]interface{}{
			"rows": []interface{}{map[string]interface{}{"id": 1, "body": payload}},
			"n":    1,
		}}, nil
	}
	if shape == "table" {
		// A genuine tabular result — {columns, rows} — the shape the query index
		// (tool-result-storage-design §5) admits. Rows are split so SELECTs have
		// something to count and filter.
		chunk := 64
		rows := make([]interface{}, 0, (len(payload)/chunk)+1)
		for i := 0; i < len(payload); i += chunk {
			end := i + chunk
			if end > len(payload) {
				end = len(payload)
			}
			rows = append(rows, []interface{}{len(rows) + 1, payload[i:end]})
		}
		return &shuttle.Result{Success: true, Data: map[string]interface{}{
			"columns": []interface{}{"id", "body"},
			"rows":    rows,
		}}, nil
	}
	return &shuttle.Result{Success: true, Data: payload}, nil
}

// ---------------------------------------------------------------------------
// The rig — a real Agent, driven only through exported API.
// ---------------------------------------------------------------------------

type rig struct {
	agent  *agent.Agent
	orch   *skills.Orchestrator
	store  *agent.SessionStore
	llm    *scriptedLLM
	mem    *agent.Memory
	outDir string

	skillsDir   string
	patternsDir string
	dbPath      string
}

// newRig builds the agent under audit: real skills library and orchestrator,
// real pattern library, durable session store, context dump ON, small budget so
// pressure is reachable in a scripted route.
func newRig(t *testing.T, outDir string, script []scriptedTurn, maxContext, reservedOutput, offloadThreshold int) *rig {
	t.Helper()

	work := t.TempDir()
	skillsDir, patternsDir := writeFixtures(t, work)
	dbPath := filepath.Join(work, "sessions.db")

	// The dump sink is keyed off LOOM_DEBUG_DIR; point it at this run's out dir.
	t.Setenv("LOOM_DEBUG_DIR", outDir)
	// Isolate loom's data dir: without this, a route that produces a tabular
	// result writes loom.db into the operator's real ~/.loom. It also gives the
	// storage suite a fence to watch — nothing tool-result-shaped may appear here.
	// The dir is created eagerly because NewAgent SILENTLY skips its SQL result
	// store when the open fails (missing dir) — without this line the store's
	// existence depends on which test ran first in the process.
	loomData := filepath.Join(work, "loom-data")
	if err := os.MkdirAll(loomData, 0o755); err != nil {
		t.Fatalf("create loom data dir: %v", err)
	}
	t.Setenv("LOOM_DATA_DIR", loomData)

	store, err := agent.NewSessionStore(dbPath, observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("session store: %v", err)
	}

	llm := &scriptedLLM{turns: script}
	a, orch, mem := buildAgentWithMemory(t, llm, store, skillsDir, patternsDir,
		maxContext, reservedOutput, offloadThreshold, true)

	return &rig{
		agent: a, orch: orch, store: store, llm: llm, mem: mem,
		outDir: outDir, skillsDir: skillsDir, patternsDir: patternsDir, dbPath: dbPath,
	}
}

// buildAgentWithMemory wires one agent instance and hands back its Memory, so a
// restore twin can replay a session through the same durable store.
// Only the live agent dumps: a restore twin exists to replay messages, and its
// own dump would interleave with the run under audit.
func buildAgentWithMemory(t *testing.T, llm agent.LLMProvider, store *agent.SessionStore, skillsDir, patternsDir string,
	maxContext, reservedOutput, offloadThreshold int, dump bool) (*agent.Agent, *skills.Orchestrator, *agent.Memory) {
	t.Helper()

	lib := skills.NewLibrary(skills.WithSearchPaths(skillsDir))
	orch := skills.NewOrchestrator(lib)

	cfg := agent.DefaultConfig()
	cfg.Debug.ContextDump = dump
	cfg.MaxContextTokens = maxContext
	cfg.ReservedOutputTokens = reservedOutput
	// The bound set renders the static ROM menu (names + descriptions). Binding
	// mode is a search-path input only — nothing here auto-loads; a skill reaches
	// the session solely through manage_skills(load).
	cfg.SkillsConfig = &skills.SkillsConfig{
		Enabled: true,
		Bindings: []skills.SkillBinding{
			{Name: skillGrantReview, Mode: skills.BindingLazy},
			{Name: skillAuditTrail, Mode: skills.BindingLazy},
			{Name: skillProdDelete, Mode: skills.BindingLazy},
		},
	}
	if pc := cfg.PatternConfig; pc != nil {
		pc.UseLLMClassifier = false
	}

	// A permission checker that is NOT in YOLO mode: without one the high-risk
	// gate is disabled by design, and a HIGH skill would load body and all.
	checker := shuttle.NewPermissionChecker(shuttle.PermissionConfig{
		RequireApproval: true,
		YOLO:            false,
		DefaultAction:   "allow", // ordinary tools still run; only the skill gate matters here
	})

	mem := agent.NewMemoryWithStore(store)
	a := agent.NewAgent(nil, llm,
		agent.WithConfig(cfg),
		agent.WithSkillOrchestrator(orch),
		agent.WithMemory(mem),
		agent.WithPermissionChecker(checker),
		agent.WithoutSelfCorrection(),
	)
	a.RegisterTool(&emitTool{})
	if offloadThreshold > 0 {
		a.SetSharedMemoryThreshold(int64(offloadThreshold))
	}
	return a, orch, mem
}

// ---------------------------------------------------------------------------
// Reading the dump — the record shape is JSON on disk; mirror it here so the
// suite stays outside pkg/agent.
// ---------------------------------------------------------------------------

type dumpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Backend     string `json:"backend,omitempty"`
}

// dumpMessage mirrors types.Message as it lands on disk. That type carries no
// json tags, so it marshals under its Go field names — these must match exactly.
type dumpMessage struct {
	Role       string
	Content    string
	ToolUseID  string
	ToolCalls  []types.ToolCall
	ToolResult *json.RawMessage
	AgentID    string
}

// stage is one provider call: the exact context dispatched at that moment.
type stage struct {
	SessionID string        `json:"session_id"`
	Turn      int           `json:"turn"`
	Messages  []dumpMessage `json:"messages"`
	Tools     []dumpTool    `json:"tools"`
}

// readStages returns every provider call captured so far, in order.
func (r *rig) readStages(t *testing.T) []stage {
	t.Helper()
	dir := filepath.Join(r.outDir, "context-dumps")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dump dir %s: %v", dir, err)
	}
	var stages []stage
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read dump %s: %v", e.Name(), err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var s stage
			if err := json.Unmarshal([]byte(line), &s); err != nil {
				t.Fatalf("parse dump line: %v", err)
			}
			stages = append(stages, s)
		}
	}
	return stages
}
