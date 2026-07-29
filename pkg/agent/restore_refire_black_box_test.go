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
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/task"
)

// These tests drive story D-restore's acceptance criteria on the loom.component
// surface: a real Agent over a scripted mockToolCallingLLM with a real skill
// library/orchestrator, a durable SQLite SessionStore, and the M9 context dump.
//
// The scenario (runRestoreScenario) plays S1 on a LIVE agent whose durable
// messages come to hold every record the re-fire keys on — a compacted skill
// load, a stored-error record, and a >=64 KiB large-result record — then
// DISCARDS the in-memory Agent/Memory and builds a NEW agent over the SAME
// durable SessionStore. GetOrCreateSessionWithAgent replays the durable
// messages (memory.go:259) and the re-fire pass runs after Memory.mu releases
// (memory.go:273). The four ACs are asserted against the reloaded durable
// record, the restored session's advertised tools + active skills (compared to
// the untouched live session), the post-restart dump + manage_skills(list), and
// the message/task deltas across the re-fire.
//
// The two probes below (flaky_probe, bulk_scan) are the only way to make the
// live agent produce authentic error / large-result durable records through the
// real Chat pipeline: the agent's own formatToolResult stores the error and
// offloads the >=64 KiB result. They are registered on BOTH the live and the
// restored agent so the globally-advertised registry is identical on each side;
// the ONLY advertised-tool difference between them is the session-scoped set the
// re-fire reconstructs.

// --- probes -----------------------------------------------------------------

// flakyProbeName is a non-builtin tool that always fails, so the agent's
// formatToolResult stores the error and renders a get_error_details reference
// into the durable tool message.
const flakyProbeName = "flaky_probe"

// bulkScanName is a non-builtin tool that returns a >=64 KiB string, so the
// agent's formatToolResult offloads it to shared memory and renders a "stored
// in memory" reference into the durable tool message.
const bulkScanName = "bulk_scan"

// largeResultBytes is the probe payload size: strictly above the agent's 64 KiB
// (storage.DefaultSharedMemoryThreshold) offload threshold so the result is
// stored by reference rather than kept inline.
const largeResultBytes = 80 * 1024

// refireErroringTool always returns a tool-execution failure (Success=false,
// Error set) so the agent stores the error and advertises get_error_details.
type refireErroringTool struct{}

func (t *refireErroringTool) Name() string        { return flakyProbeName }
func (t *refireErroringTool) Description() string { return "probe that always fails" }
func (t *refireErroringTool) Backend() string     { return "" }
func (t *refireErroringTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (t *refireErroringTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{
		Success: false,
		Error: &shuttle.Error{
			Code:    "probe_failed",
			Message: "flaky_probe deliberately failed for the restore scenario",
		},
	}, nil
}

// refireLargeResultTool returns a payload above the offload threshold so the
// agent stores it by reference and advertises query_tool_result.
type refireLargeResultTool struct{ payload string }

func (t *refireLargeResultTool) Name() string        { return bulkScanName }
func (t *refireLargeResultTool) Description() string { return "probe that returns a large result" }
func (t *refireLargeResultTool) Backend() string     { return "" }
func (t *refireLargeResultTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (t *refireLargeResultTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: t.payload}, nil
}

// rawToolCall scripts one assistant turn that calls an arbitrary tool by name.
func rawToolCall(id, name string, input map[string]interface{}) mockLLMResponse {
	return mockLLMResponse{toolCalls: []llmtypes.ToolCall{{ID: id, Name: name, Input: input}}}
}

// --- scenario ---------------------------------------------------------------

// restoreScenario captures the two agents (LIVE and RESTORED) and the durable
// state observed across the restart, so each AC test asserts its own slice.
type restoreScenario struct {
	sessionID string
	store     *SessionStore

	liveAgent   *Agent
	liveOrch    *skills.Orchestrator
	liveSession *Session

	restoredAgent   *Agent
	restoredOrch    *skills.Orchestrator
	restoredSession *Session

	// The skill load whose in-memory marker was compacted into the LIVE
	// session's opaque L2 summary (AC1).
	compactedSkill string
	// The other loaded skill, which declares web_search as a required tool.
	tooledSkill string

	// Durable message records read straight from the store after S1 (used to
	// assert marker persistence), and message/task counts measured immediately
	// after the re-fire but before any further turn (used to assert the re-fire
	// emitted nothing).
	durableAfterS1         []Message
	durableCountAfterS1    int
	durableCountPostRefire int
	restoredMsgCount       int
	tasksPostRefire        int

	// The context-dump sink the restored agent's post-restart turn wrote to.
	dumpSinkDir string
}

// buildRestoreAgent constructs an Agent over the shared durable store with a
// real fixture skill library/orchestrator, self-correction off (deterministic
// tool flow), and the two probes registered. dump toggles the M9 context dump.
func buildRestoreAgent(t *testing.T, llm LLMProvider, store *SessionStore, skillsDir string, errStore ErrorStore, dump bool, extra ...Option) (*Agent, *skills.Orchestrator) {
	t.Helper()

	lib := skills.NewLibrary(skills.WithSearchPaths(skillsDir))
	orch := skills.NewOrchestrator(lib)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	// Pull-only: no legacy per-turn skill push, so a skill reaches the session
	// solely through a manage_skills(load) tool-result and its durable marker.
	cfg.SkillsConfig = &skills.SkillsConfig{Enabled: false}
	cfg.Debug.ContextDump = dump

	opts := []Option{WithConfig(cfg), WithSkillOrchestrator(orch), WithMemory(NewMemoryWithStore(store)), WithoutSelfCorrection()}
	if errStore != nil {
		opts = append(opts, WithErrorStore(errStore))
	}
	opts = append(opts, extra...)

	a := NewAgent(&mockBackend{}, llm, opts...)

	// The probes are not builtins; the re-fire never re-registers them. Register
	// them on every agent so the globally-advertised registry matches across the
	// restart and only the session-scoped re-fired tools differ.
	a.tools.Register(&refireErroringTool{})
	a.tools.Register(&refireLargeResultTool{payload: strings.Repeat("D", largeResultBytes)})

	return a, orch
}

// runRestoreScenario plays S1 on a live agent, forces a load into L2, then
// rebuilds a fresh agent over the same durable store so the restore re-fire
// runs, and finally drives one post-restart turn (dump on) so the M9 record is
// available. It returns everything the AC tests read.
func runRestoreScenario(t *testing.T) *restoreScenario {
	t.Helper()

	ctx := context.Background()
	sinkDir := redirectCtxDumpSink(t)
	// Force the env dump switch off so ONLY the restored agent (config dump on)
	// writes a sink file — the live agent's provider calls must not also dump,
	// or readDumpRecords would see more than one file.
	t.Setenv(envContextDumpVar, "")
	resetCtxDumpEnvOnce()
	skillsDir := writeSkillFixtures(t)

	store, err := NewSessionStore(filepath.Join(t.TempDir(), "sessions.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	const sessionID = "sess-restore"

	// --- S1: the live agent ---
	liveLLM := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c-alpha", "alpha-skill"),                             // marker + no-evict activation
		loadCall("c-tooled", "tooled-skill"),                           // marker + required web_search
		rawToolCall("c-err", flakyProbeName, map[string]interface{}{}), // stored-error record
		rawToolCall("c-big", bulkScanName, map[string]interface{}{}),   // large-result record
	}}
	liveAgent, liveOrch := buildRestoreAgent(t, liveLLM, store, skillsDir, newTestErrorStore(t), false)

	_, err = liveAgent.Chat(ctx, sessionID, "load the skills and run both probes")
	require.NoError(t, err)
	// A second turn adds a trailing user message, so CompactMemory (which keeps
	// the last user message and compresses everything before it) folds S1's
	// loads into the opaque L2 summary.
	_, err = liveAgent.Chat(ctx, sessionID, "continue")
	require.NoError(t, err)

	liveSession := liveAgent.memory.GetOrCreateSessionWithAgent(ctx, sessionID, liveAgent.config.Name, "")
	require.NotNil(t, liveSession)
	liveSegMem, ok := liveSession.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "live session carries a *SegmentedMemory")
	liveSegMem.CompactMemory(ctx)

	durableAfterS1, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)

	// --- Restart: a fresh agent over the SAME durable store ---
	_, mgr, dec := newTaskSubsystem(t)
	restoredLLM := &mockToolCallingLLM{} // no scripted tools; the post-restart turn just ends
	restoredAgent, restoredOrch := buildRestoreAgent(t, restoredLLM, store, skillsDir, newTestErrorStore(t), true,
		WithTaskBoard(mgr, dec, nil))

	// GetOrCreateSessionWithAgent replays the durable messages and runs the
	// re-fire pass. Measure the message/task deltas across exactly this call.
	restoredSession := restoredAgent.memory.GetOrCreateSessionWithAgent(ctx, sessionID, restoredAgent.config.Name, "")
	require.NotNil(t, restoredSession)

	durablePostRefire, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	_, tasksTotalPostRefire, err := mgr.ListTasks(ctx, task.ListTasksOpts{Limit: 100})
	require.NoError(t, err)

	sc := &restoreScenario{
		sessionID:              sessionID,
		store:                  store,
		liveAgent:              liveAgent,
		liveOrch:               liveOrch,
		liveSession:            liveSession,
		restoredAgent:          restoredAgent,
		restoredOrch:           restoredOrch,
		restoredSession:        restoredSession,
		compactedSkill:         "alpha-skill",
		tooledSkill:            "tooled-skill",
		durableAfterS1:         durableAfterS1,
		durableCountAfterS1:    len(durableAfterS1),
		durableCountPostRefire: len(durablePostRefire),
		restoredMsgCount:       len(restoredSession.Messages),
		tasksPostRefire:        tasksTotalPostRefire,
		dumpSinkDir:            sinkDir,
	}

	// One post-restart provider call so the M9 dump captures the restored
	// session's advertised tools. Done last, after the re-fire deltas are read.
	_, err = restoredAgent.Chat(ctx, sessionID, "status")
	require.NoError(t, err)

	return sc
}

// loadMarkerFor returns the durable load-result marker (ToolResult.Metadata)
// for the named skill, or nil if no durable tool message carries it.
func loadMarkerFor(msgs []Message, skillName string) map[string]interface{} {
	for i := range msgs {
		m := msgs[i]
		if m.Role != "tool" || m.ToolResult == nil || m.ToolResult.Metadata == nil {
			continue
		}
		if name, _ := m.ToolResult.Metadata["skill"].(string); name == skillName {
			return m.ToolResult.Metadata
		}
	}
	return nil
}

// invokeManageSkillsList calls the registered manage_skills(list) directly for a
// session and returns the parsed JSON — the tool's own Result, read before any
// large-result paging.
func invokeManageSkillsList(t *testing.T, a *Agent, sessionID string) skillListResultJSON {
	t.Helper()
	tool := findRegisteredTool(a, "manage_skills")
	require.NotNil(t, tool, "manage_skills is registered")
	res, err := tool.Execute(session.WithSessionID(context.Background(), sessionID), map[string]interface{}{"action": "list"})
	require.NoError(t, err)
	require.True(t, res.Success)
	var parsed skillListResultJSON
	require.NoError(t, json.Unmarshal([]byte(res.Data.(string)), &parsed))
	return parsed
}

// activeFromList returns the set of skill names list() marks active.
func activeFromList(parsed skillListResultJSON) map[string]bool {
	out := map[string]bool{}
	for _, e := range parsed.Skills {
		if e.Active {
			out[e.Name] = true
		}
	}
	return out
}

// --- AC1: the load marker is durably persisted, even for a compacted load ----

// TestRestore_AC1_LoadMarkerDurableEvenWhenCompacted asserts the load-result
// marker round-trips through the durable SessionStore with its full
// {skill, source_path, risk, activated_at} shape — including for a load whose
// in-memory marker the live session has already compacted into its opaque L2
// summary. The durable message list, not the compacted L1/L2, is what feeds the
// replay, so the marker must survive there.
func TestRestore_AC1_LoadMarkerDurableEvenWhenCompacted(t *testing.T) {
	sc := runRestoreScenario(t)

	// The live session compacted alpha-skill's load into L2: L2 has content and
	// no L1 message still carries alpha-skill's structured marker.
	liveSegMem := sc.liveSession.SegmentedMem.(*SegmentedMemory)
	require.True(t, liveSegMem.HasL2Content(), "the live session compacted a batch into L2")
	assert.Nil(t, loadMarkerFor(liveSegMem.GetMessages(), sc.compactedSkill),
		"the compacted load's structured marker is gone from the live L1 (folded into opaque L2)")

	// The durable store still carries the marker for that same compacted load,
	// with every field intact.
	md := loadMarkerFor(sc.durableAfterS1, sc.compactedSkill)
	require.NotNil(t, md, "durable store retains the compacted load's marker")

	skill, err := sc.liveOrch.GetLibrary().Load(sc.compactedSkill)
	require.NoError(t, err)

	assert.Equal(t, sc.compactedSkill, md["skill"])
	assert.Equal(t, sc.compactedSkill, md["source_path"])
	assert.Equal(t, skill.RiskLevel, md["risk"])
	activatedAt, ok := md["activated_at"].(string)
	require.True(t, ok, "activated_at survived as a string")
	_, perr := time.Parse(time.RFC3339, activatedAt)
	assert.NoError(t, perr, "activated_at is an RFC3339 timestamp after the store round-trip")

	// The reloaded restored session sees the same durable marker — the precise
	// input the re-fire walks.
	restoredMD := loadMarkerFor(sc.restoredSession.Messages, sc.compactedSkill)
	require.NotNil(t, restoredMD, "reloaded session.Messages retains the compacted load's marker")
	assert.Equal(t, sc.compactedSkill, restoredMD["skill"])
}

// --- AC2: restored session ≡ live for tools and active skills ----------------

// TestRestore_AC2_RestoredMatchesLiveToolsAndSkills asserts that after
// restart-and-replay the restored session advertises the same tools and reports
// the same active skills as the untouched live session at the same point —
// including the skill whose load had compacted into L2. The equality is read
// three ways: the advertised-tool projection, the post-restart M9 dump, and
// manage_skills(list).
func TestRestore_AC2_RestoredMatchesLiveToolsAndSkills(t *testing.T) {
	sc := runRestoreScenario(t)

	// Advertised tools: restored == live, byte-for-byte on the name-sorted list.
	liveTools := advertisedNames(sc.liveAgent, sc.liveSession)
	restoredTools := advertisedNames(sc.restoredAgent, sc.restoredSession)
	assert.Equal(t, liveTools, restoredTools,
		"restored session advertises the same tools as the live session")

	// Active skills: restored orchestrator == live orchestrator, including the
	// compacted-load skill.
	liveActive := activeSkillNames(sc.liveOrch, sc.sessionID)
	restoredActive := activeSkillNames(sc.restoredOrch, sc.sessionID)
	assert.Equal(t, liveActive, restoredActive,
		"restored active-skill set equals the live one")
	assert.True(t, restoredActive[sc.compactedSkill],
		"the compacted-load skill is active again after restore")
	assert.True(t, restoredActive[sc.tooledSkill],
		"the tool-declaring skill is active again after restore")

	// manage_skills(list) on the restored session reports the same active set.
	restoredListActive := activeFromList(invokeManageSkillsList(t, sc.restoredAgent, sc.sessionID))
	liveListActive := activeFromList(invokeManageSkillsList(t, sc.liveAgent, sc.sessionID))
	assert.Equal(t, liveListActive, restoredListActive,
		"manage_skills(list) reports the same active skills after restore")
	assert.Equal(t, restoredActive, restoredListActive,
		"list() active set agrees with the orchestrator active set")

	// The post-restart M9 dump — the real provider payload — carries the restored
	// advertised tools, including the re-fired ones.
	recs := readDumpRecords(t, sc.dumpSinkDir)
	require.NotEmpty(t, recs, "the post-restart turn wrote a dump record")
	last := toolNamesInRecord(recs[len(recs)-1])
	assert.True(t, last["manage_skills"], "dump advertises manage_skills")
	assert.True(t, last[requiredToolName], "dump advertises the re-fired required tool")
	assert.True(t, last["get_error_details"], "dump advertises the re-fired error-disclosure tool")
	assert.True(t, last["query_tool_result"], "dump advertises the re-fired large-result tool")
}

// --- AC3: re-fire walks the durable messages, re-firing loads + disclosures ---

// TestRestore_AC3_RefireReconstructsFromDurableRecords asserts the restored
// advertised set carries the tools the re-fire reconstructs from the durable
// message records: the load marker's required tool (ActivateSkill +
// per-session registration), plus the disclosure tools implied by the durable
// error record (get_error_details) and large-result record (query_tool_result).
// It could only have these if it walked the durable session.Messages — the
// compacted L1/L2 strip the markers.
func TestRestore_AC3_RefireReconstructsFromDurableRecords(t *testing.T) {
	sc := runRestoreScenario(t)

	// Preconditions: the durable records the re-fire keys on are present.
	require.NotNil(t, loadMarkerFor(sc.durableAfterS1, sc.tooledSkill),
		"a durable load marker exists for the tool-declaring skill")
	sawErrorRecord, sawLargeResult := false, false
	for _, m := range sc.durableAfterS1 {
		if m.Role != "tool" {
			continue
		}
		if strings.Contains(m.Content, "get_error_details") {
			sawErrorRecord = true
		}
		// The re-fire keys a large-result record on either a stored DataReference
		// or a "stored in memory" content marker; assert the same disjunction.
		if (m.ToolResult != nil && m.ToolResult.DataReference != nil) ||
			strings.Contains(m.Content, "stored in memory") {
			sawLargeResult = true
		}
	}
	require.True(t, sawErrorRecord, "a durable stored-error record exists")
	require.True(t, sawLargeResult, "a durable large-result record exists")

	// The restored advertised set carries every re-fired tool.
	restored := map[string]bool{}
	for _, n := range advertisedNames(sc.restoredAgent, sc.restoredSession) {
		restored[n] = true
	}
	assert.True(t, restored[requiredToolName],
		"re-fire re-registered the load marker's required tool (ActivateSkill + per-session registration)")
	assert.True(t, restored["get_error_details"],
		"re-fire re-registered the disclosure tool implied by the durable error record")
	assert.True(t, restored["query_tool_result"],
		"re-fire re-registered the disclosure tool implied by the durable large-result record")

	// And the restored per-session ledger holds those scoped tools directly.
	sc.restoredAgent.mu.RLock()
	ledger := sc.restoredAgent.sessionToolLedger[sc.sessionID]
	sc.restoredAgent.mu.RUnlock()
	assert.True(t, ledger[requiredToolName], "required tool scoped to the restored session")
	assert.True(t, ledger["get_error_details"], "get_error_details scoped to the restored session")
	assert.True(t, ledger["query_tool_result"], "query_tool_result scoped to the restored session")
}

// --- AC4: the re-fire emits no new message and no task -----------------------

// TestRestore_AC4_RefireEmitsNoMessageOrTask asserts the restore re-fire
// re-activates and re-registers only: across the GetOrCreateSessionWithAgent
// call that replays and re-fires, the durable message count is unchanged, the
// reloaded session.Messages gains no synthetic entry, and no task is emitted —
// even though the re-fire re-activated the durable loads.
func TestRestore_AC4_RefireEmitsNoMessageOrTask(t *testing.T) {
	sc := runRestoreScenario(t)

	// The re-fire re-activated the durable loads (precondition: it did real work).
	require.NotEmpty(t, activeSkillNames(sc.restoredOrch, sc.sessionID),
		"the re-fire re-activated at least one skill")

	// No new conversation message: the durable count is unchanged across the
	// re-fire, and the reloaded in-memory list matches it exactly.
	assert.Equal(t, sc.durableCountAfterS1, sc.durableCountPostRefire,
		"the re-fire persisted no new durable message")
	assert.Equal(t, sc.durableCountAfterS1, sc.restoredMsgCount,
		"the reloaded session.Messages gained no synthetic entry")

	// No task: despite re-activating skills, the re-fire emitted nothing to the
	// restored agent's task store.
	assert.Equal(t, 0, sc.tasksPostRefire, "the re-fire emitted no task")
}
