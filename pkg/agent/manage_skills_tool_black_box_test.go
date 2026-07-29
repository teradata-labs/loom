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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
)

// These tests drive the manage_skills builtin through the real Agent.Chat loop
// with a scripted, deterministic LLM (mockToolCallingLLM), a real skill
// Library/Orchestrator loaded from on-disk YAML fixtures, and the M9 context
// dump switched ON. They assert story D-A's acceptance criteria against the
// tool's own Result and the per-provider-call dump records — the pull-event
// surface (25-test-arch.md Entry 3 + Entry 8).
//
// The legacy per-turn push (skill matching + system-prompt injection) is turned
// off (SkillsConfig.Enabled=false) so a skill body reaches the conversation ONLY
// via a manage_skills(load) tool-result, which is precisely the pull semantics
// under test. The manage_skills tool is registered whenever the orchestrator is
// wired, independent of that switch, and its required-tool wiring runs on the
// load event itself.

// --- fixtures ---------------------------------------------------------------

// skillFixtures is the on-disk skill library every test loads. Names are
// kebab-case and domains are drawn from the loader's allow-list. Risk levels,
// pattern refs, and required tools are chosen to exercise each AC:
//   - alpha/beta/gamma-skill: plain low-risk skills (body, marker, no-evict).
//   - pattern-skill: declares pattern_refs (FormatForLLM omits them).
//   - tooled-skill: declares required_tools: [web_search] (a builtin).
//   - danger-skill: HIGH risk (the approval gate).
var skillFixtures = map[string]string{
	"alpha-skill.yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: alpha-skill
  title: Alpha Skill
  description: First plain test skill.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Alpha skill instructions body.
  constraints:
    - Keep alpha responses terse.
`,
	"beta-skill.yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: beta-skill
  title: Beta Skill
  description: Second plain test skill.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Beta skill instructions body.
`,
	"gamma-skill.yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: gamma-skill
  title: Gamma Skill
  description: Third plain test skill.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Gamma skill instructions body.
`,
	"pattern-skill.yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: pattern-skill
  title: Pattern Skill
  description: Declares pattern references.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Pattern skill instructions body.
pattern_refs:
  - sql-optimization
  - join-strategy
`,
	"tooled-skill.yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: tooled-skill
  title: Tooled Skill
  description: Declares a required builtin tool.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Tooled skill instructions body.
tools:
  required_tools:
    - web_search
`,
	// base-tooled-skill requires a tool the AGENT itself registers at
	// construction (a base tool), not a builtin the load has to disclose. It
	// exists to prove that re-firing this skill on restore never marks a base
	// tool as session-scoped and hides it from other sessions.
	"base-tooled-skill.yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: base-tooled-skill
  title: Base Tooled Skill
  description: Declares a required tool that is part of the agent's base set.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Base tooled skill instructions body.
tools:
  required_tools:
    - base_probe
`,
	"danger-skill.yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: danger-skill
  title: Danger Skill
  description: High-risk skill guarded by the approval gate.
  domain: ops
  risk_level: HIGH
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Danger skill instructions body.
`,
}

// requiredToolName is the builtin tool tooled-skill declares as required. It is
// resolvable via builtin.ByName, so a load wires it into the session ledger.
const requiredToolName = "web_search"

// writeSkillFixtures writes the fixture library to a fresh temp dir and returns
// the directory path for skills.WithSearchPaths.
func writeSkillFixtures(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "skills")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, body := range skillFixtures {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

// --- rig --------------------------------------------------------------------

// manageSkillsRig bundles the driven agent with the live library and
// orchestrator so a test can read the same active set the tool mutates.
type manageSkillsRig struct {
	agent   *Agent
	orch    *skills.Orchestrator
	library *skills.Library
}

// buildManageSkillsRig constructs an agent over the scripted LLM with the fixture
// library/orchestrator wired. checker may be nil (no permission checker). dump
// toggles the M9 context dump. orchOpts tune the orchestrator (e.g. the eviction
// cap) — the load path ignores that cap, which L16 asserts.
func buildManageSkillsRig(t *testing.T, llm LLMProvider, checker *shuttle.PermissionChecker, dump bool, orchOpts ...skills.OrchestratorOption) *manageSkillsRig {
	t.Helper()

	dir := writeSkillFixtures(t)
	lib := skills.NewLibrary(skills.WithSearchPaths(dir))
	orch := skills.NewOrchestrator(lib, orchOpts...)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false // deterministic scripted LLM
	// Pull-only: disable the legacy per-turn match+inject push so a body reaches
	// the conversation solely through a manage_skills(load) tool-result.
	cfg.SkillsConfig = &skills.SkillsConfig{Enabled: false}
	cfg.Debug.ContextDump = dump

	opts := []Option{WithConfig(cfg), WithSkillOrchestrator(orch)}
	if checker != nil {
		opts = append(opts, WithPermissionChecker(checker))
	}

	return &manageSkillsRig{
		agent:   NewAgent(&mockBackend{}, llm, opts...),
		orch:    orch,
		library: lib,
	}
}

// loadCall scripts one assistant turn that calls manage_skills(load, name).
func loadCall(id, name string) mockLLMResponse {
	return mockLLMResponse{toolCalls: []llmtypes.ToolCall{{
		ID:    id,
		Name:  "manage_skills",
		Input: map[string]interface{}{"action": "load", "name": name},
	}}}
}

// finalTurn scripts the assistant's terminal text turn (no tool calls) so Chat
// returns.
func finalTurn() mockLLMResponse {
	return mockLLMResponse{content: "done"}
}

// invokeManageSkills calls the registered manage_skills tool directly with the
// session in context and returns the tool's OWN Result. This is the surface the
// ticket scopes ("§5.1 manage_skills tool … body-as-string" / list JSON), read
// before the agent executor's generic large-result paging (Executor.handleLargeResult)
// can re-home Data into shared memory — that clean-render exemption for this
// tool is downstream D-E's contract, not D-A's.
func invokeManageSkills(t *testing.T, rig *manageSkillsRig, sessionID string, params map[string]interface{}) *shuttle.Result {
	t.Helper()
	tool := findRegisteredTool(rig.agent, "manage_skills")
	require.NotNil(t, tool, "manage_skills is registered")
	res, err := tool.Execute(session.WithSessionID(context.Background(), sessionID), params)
	require.NoError(t, err)
	require.NotNil(t, res)
	return res
}

// manageSkillsResults returns, in order, the Result of every manage_skills
// execution in a response.
func manageSkillsResults(resp *Response) []*shuttle.Result {
	var out []*shuttle.Result
	for _, e := range resp.ToolExecutions {
		if e.ToolName == "manage_skills" {
			out = append(out, e.Result)
		}
	}
	return out
}

// activeSkillNames returns the set of skill names active for a session.
func activeSkillNames(orch *skills.Orchestrator, sessionID string) map[string]bool {
	out := map[string]bool{}
	for _, as := range orch.GetActiveSkills(sessionID) {
		if as != nil && as.Skill != nil {
			out[as.Skill.Name] = true
		}
	}
	return out
}

// findRegisteredTool returns the registered tool with the given name, or nil.
func findRegisteredTool(a *Agent, name string) shuttle.Tool {
	for _, tl := range a.RegisteredTools() {
		if tl.Name() == name {
			return tl
		}
	}
	return nil
}

// toolNamesInRecord returns the set of tool names captured in a dump record.
func toolNamesInRecord(rec contextDumpRecord) map[string]bool {
	out := map[string]bool{}
	for _, tl := range rec.Tools {
		out[tl.Name] = true
	}
	return out
}

// skillListEntryJSON mirrors the JSON list() renders: the library annotated with
// per-session active flags.
type skillListEntryJSON struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type skillListResultJSON struct {
	SessionID   string               `json:"session_id"`
	ActiveCount int                  `json:"active_count"`
	Skills      []skillListEntryJSON `json:"skills"`
}

// --- L6: body appears only as a load tool-result, prefixed, verbatim body ----

// TestManageSkills_L6_LoadBodyIsToolResultOnly asserts the manage_skills tool's
// own load Result carries the confirmation as Data (tool_result payload) and
// the skill body verbatim as Metadata["text_body"] (sidecar the agent executor
// appends as a user-role message immediately after the tool_result). Under the
// Chat drive the confirmation marker lands as a tool-role message; the body
// sidecar lands as a user-role message following it — both are downstream of
// the model's explicit manage_skills(load) call (pull semantics preserved).
func TestManageSkills_L6_LoadBodyIsToolResultOnly(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "alpha-skill"),
		finalTurn(),
	}}
	rig := buildManageSkillsRig(t, llm, nil, true)

	skill, err := rig.library.Load("alpha-skill")
	require.NoError(t, err)

	// AC core: read the tool's OWN Result. Under the split contract, Data is
	// the short confirmation and the body rides in Metadata["text_body"]
	// verbatim. The agent executor appends that body as its own Role="user"
	// Message immediately after the tool_result Message, so the body lands in
	// the user-instruction slot rather than the tool-result data slot.
	loadRes := invokeManageSkills(t, rig, "sess-l6-direct",
		map[string]interface{}{"action": "load", "name": "alpha-skill"})
	require.True(t, loadRes.Success)
	data, ok := loadRes.Data.(string)
	require.True(t, ok, "load Data is a string confirmation")
	assert.Equal(t, "Skill loaded: alpha-skill", data,
		"load Data is the short confirmation payload for the tool_result")
	require.NotNil(t, loadRes.Metadata)
	textBody, ok := loadRes.Metadata["text_body"].(string)
	require.True(t, ok, "load Metadata carries the text_body sidecar as a string")
	assert.Equal(t, skill.FormatForLLM(), textBody,
		"text_body sidecar is the verbatim FormatForLLM body")

	// Pull semantics: driving the same load through Chat, the confirmation
	// marker appears as a tool-role message and the body sidecar appears as a
	// user-role message immediately after it — both downstream of the model's
	// explicit load call, never pushed via a system message ahead of any turn.
	_, err = rig.agent.Chat(context.Background(), "sess-l6", "load alpha")
	require.NoError(t, err)

	const marker = "Skill loaded: alpha-skill"
	recs := readDumpRecords(t, sinkDir)
	sawInToolMessage := false
	for _, rec := range recs {
		for _, m := range rec.Messages {
			if strings.Contains(m.Content, marker) {
				assert.Equal(t, "tool", m.Role,
					"the confirmation marker may appear only in a tool-role message, not role %q", m.Role)
				if m.Role == "tool" {
					sawInToolMessage = true
				}
			}
		}
	}
	assert.True(t, sawInToolMessage, "the confirmation marker appears as a tool-result message")

	// The body sidecar arrives as a user-role message following the tool_result
	// — never in a system or assistant slot, and never pushed ahead of the turn.
	sawBodyInUserMessage := false
	for _, rec := range recs {
		for _, m := range rec.Messages {
			if strings.Contains(m.Content, skill.FormatForLLM()) {
				assert.NotEqual(t, "system", m.Role,
					"skill body must not land in a system message (pull semantics)")
				assert.NotEqual(t, "assistant", m.Role,
					"skill body must not land in an assistant message")
				if m.Role == "user" {
					sawBodyInUserMessage = true
				}
			}
		}
	}
	assert.True(t, sawBodyInUserMessage,
		"the skill body sidecar appears as a user-role message in the conversation")
}

// --- L8: load Result carries the structured activation marker ----------------

// TestManageSkills_L8_LoadMarkerMetadata asserts the load Result.Metadata carries
// the off-band {skill, source_path, risk, activated_at} marker.
func TestManageSkills_L8_LoadMarkerMetadata(t *testing.T) {
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "alpha-skill"),
		finalTurn(),
	}}
	rig := buildManageSkillsRig(t, llm, nil, false)

	resp, err := rig.agent.Chat(context.Background(), "sess-l8", "load alpha")
	require.NoError(t, err)

	results := manageSkillsResults(resp)
	require.Len(t, results, 1)
	require.True(t, results[0].Success)

	skill, err := rig.library.Load("alpha-skill")
	require.NoError(t, err)

	md := results[0].Metadata
	require.NotNil(t, md, "load Result carries a metadata marker")
	assert.Equal(t, "alpha-skill", md["skill"])
	assert.Equal(t, "alpha-skill", md["source_path"])
	assert.Equal(t, skill.RiskLevel, md["risk"])

	activatedAt, ok := md["activated_at"].(string)
	require.True(t, ok, "activated_at is present")
	_, perr := time.Parse(time.RFC3339, activatedAt)
	assert.NoError(t, perr, "activated_at is an RFC3339 timestamp")
}

// --- L10: load surfaces the skill's pattern refs (FormatForLLM omits them) ----

// TestManageSkills_L10_LoadSurfacesPatternRefs asserts the load result surfaces
// pattern_refs — which the rendered body omits — as the model's only reference
// to pass on to load_pattern. Under the split contract, pattern refs are
// appended to the text_body sidecar (which the agent executor emits as a
// user-role message), not to the short tool_result confirmation Data.
func TestManageSkills_L10_LoadSurfacesPatternRefs(t *testing.T) {
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "pattern-skill"),
		finalTurn(),
	}}
	rig := buildManageSkillsRig(t, llm, nil, false)

	resp, err := rig.agent.Chat(context.Background(), "sess-l10", "load pattern")
	require.NoError(t, err)

	results := manageSkillsResults(resp)
	require.Len(t, results, 1)
	require.True(t, results[0].Success)

	skill, err := rig.library.Load("pattern-skill")
	require.NoError(t, err)
	require.NotEmpty(t, skill.PatternRefs)

	require.NotNil(t, results[0].Metadata)
	textBody, ok := results[0].Metadata["text_body"].(string)
	require.True(t, ok, "load Metadata carries the text_body sidecar as a string")

	// The rendered body omits pattern refs...
	assert.NotContains(t, skill.FormatForLLM(), "sql-optimization",
		"FormatForLLM omits pattern refs")
	// ...so the text_body sidecar is where they reach the model.
	assert.Contains(t, textBody, "Referenced patterns (load via load_pattern): ")
	for _, ref := range skill.PatternRefs {
		assert.Contains(t, textBody, ref, "pattern ref %q surfaced for load_pattern", ref)
	}
}

// --- L12: list() renders the library annotated with active skills as JSON -----

// TestManageSkills_L12_ListAnnotatesActiveAsJSON asserts list() returns the full
// library rendered as JSON, each skill annotated with whether it is active for
// THIS session.
func TestManageSkills_L12_ListAnnotatesActiveAsJSON(t *testing.T) {
	const sessionID = "sess-l12"
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "alpha-skill"),
		finalTurn(),
	}}
	rig := buildManageSkillsRig(t, llm, nil, false)

	// Establish this session's active set via a real load through the Chat drive.
	_, err := rig.agent.Chat(context.Background(), sessionID, "load alpha")
	require.NoError(t, err)

	// Read list()'s OWN Result — the tool's raw JSON — directly, before the agent
	// executor's large-result paging (D-E's exemption, out of D-A scope) would
	// re-home Data into a shared-memory reference.
	listResult := invokeManageSkills(t, rig, sessionID, map[string]interface{}{"action": "list"})
	require.True(t, listResult.Success)

	raw, ok := listResult.Data.(string)
	require.True(t, ok)

	var parsed skillListResultJSON
	require.NoError(t, json.Unmarshal([]byte(raw), &parsed), "list Data is valid JSON")

	assert.Equal(t, sessionID, parsed.SessionID)
	assert.Equal(t, 1, parsed.ActiveCount)

	byName := map[string]bool{}
	for _, e := range parsed.Skills {
		byName[e.Name] = e.Active
	}
	// The full library is present...
	for f := range skillFixtures {
		name := strings.TrimSuffix(f, ".yaml")
		_, present := byName[name]
		assert.True(t, present, "list includes library skill %q", name)
	}
	// ...annotated with this session's active set.
	assert.True(t, byName["alpha-skill"], "loaded skill is annotated active")
	assert.False(t, byName["beta-skill"], "unloaded skill is annotated inactive")
	assert.False(t, byName["gamma-skill"], "unloaded skill is annotated inactive")
}

// --- L16: a load never evicts and is not bounded by MaxConcurrentSkills -------

// TestManageSkills_L16_LoadNeverEvictsPastCap asserts loading more skills than
// the orchestrator's eviction cap leaves every loaded skill active — the load
// path has no count cap and evicts nothing.
func TestManageSkills_L16_LoadNeverEvictsPastCap(t *testing.T) {
	const sessionID = "sess-l16"
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "alpha-skill"),
		loadCall("c2", "beta-skill"),
		loadCall("c3", "gamma-skill"),
		finalTurn(),
	}}
	// Eviction cap of 2, yet three loads must all remain active.
	rig := buildManageSkillsRig(t, llm, nil, false, skills.WithMaxConcurrentSkills(2))

	resp, err := rig.agent.Chat(context.Background(), sessionID, "load three skills")
	require.NoError(t, err)

	results := manageSkillsResults(resp)
	require.Len(t, results, 3)
	for i, r := range results {
		require.Truef(t, r.Success, "load %d succeeded", i)
	}

	active := activeSkillNames(rig.orch, sessionID)
	assert.Len(t, active, 3, "three loads past the cap of 2 stay active (no eviction)")
	assert.True(t, active["alpha-skill"], "first-loaded skill not evicted")
	assert.True(t, active["beta-skill"])
	assert.True(t, active["gamma-skill"])
}

// --- L18: no unload verb -----------------------------------------------------

// TestManageSkills_L18_NoUnloadVerb asserts the action enum is exactly
// {load, list} — there is no unload — and an "unload" action is rejected as
// invalid input rather than performing any removal.
func TestManageSkills_L18_NoUnloadVerb(t *testing.T) {
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{
			ID:    "c1",
			Name:  "manage_skills",
			Input: map[string]interface{}{"action": "unload", "name": "alpha-skill"},
		}}},
		finalTurn(),
	}}
	rig := buildManageSkillsRig(t, llm, nil, false)

	tool := findRegisteredTool(rig.agent, "manage_skills")
	require.NotNil(t, tool, "manage_skills is registered")
	actionProp := tool.InputSchema().Properties["action"]
	require.NotNil(t, actionProp)
	assert.Equal(t, []interface{}{"load", "list"}, actionProp.Enum,
		"action enum admits only load and list — no unload")

	resp, err := rig.agent.Chat(context.Background(), "sess-l18", "try to unload")
	require.NoError(t, err)

	results := manageSkillsResults(resp)
	require.Len(t, results, 1)
	require.False(t, results[0].Success, "unload is not a valid action")
	require.NotNil(t, results[0].Error)
	assert.Equal(t, "invalid_input", results[0].Error.Code)
}

// --- L19: list() and tool-wiring agree with the orchestrator active set -------

// TestManageSkills_L19_SingleSourceActiveSet asserts list()'s active annotation
// and the advertised tool set both derive from the orchestrator active set: the
// names list() marks active match the orchestrator's, and the loaded skill's
// required tool (derived from that same active skill) is advertised.
func TestManageSkills_L19_SingleSourceActiveSet(t *testing.T) {
	const sessionID = "sess-l19"
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "tooled-skill"),
		finalTurn(),
	}}
	rig := buildManageSkillsRig(t, llm, nil, true)

	_, err := rig.agent.Chat(context.Background(), sessionID, "load tooled skill")
	require.NoError(t, err)

	// Orchestrator active set is the reference.
	orchActive := activeSkillNames(rig.orch, sessionID)
	assert.Equal(t, map[string]bool{"tooled-skill": true}, orchActive)

	// list() agrees with the orchestrator active set — not re-derived elsewhere.
	// Read list()'s OWN Result directly, before the executor's large-result paging
	// (D-E's exemption, out of D-A scope) would re-home its JSON into a reference.
	listRes := invokeManageSkills(t, rig, sessionID, map[string]interface{}{"action": "list"})
	require.True(t, listRes.Success)
	var parsed skillListResultJSON
	require.NoError(t, json.Unmarshal([]byte(listRes.Data.(string)), &parsed))
	listActive := map[string]bool{}
	for _, e := range parsed.Skills {
		if e.Active {
			listActive[e.Name] = true
		}
	}
	assert.Equal(t, orchActive, listActive, "list() active set == orchestrator active set")

	// Tool-wiring agrees too: the active skill's required tool is advertised.
	recs := readDumpRecords(t, sinkDir)
	advertisedRequired := false
	for _, rec := range recs {
		if toolNamesInRecord(rec)[requiredToolName] {
			advertisedRequired = true
		}
	}
	assert.True(t, advertisedRequired,
		"advertised tools include the active skill's required tool (same active-set source)")
}

// --- L14 (component half): required tools surface same-turn; ledger write -----

// TestManageSkills_L14_RequiredToolsSurfaceSameTurn asserts the structural half
// of L14 locally: the load writes the skill's required tools into THIS session's
// ledger (registerSessionTool), and the M9 dump shows those tools appear in the
// advertised set only AFTER the load — on the provider call that follows within
// the same Chat — never mutating the load turn's own frozen tool snapshot.
func TestManageSkills_L14_RequiredToolsSurfaceSameTurn(t *testing.T) {
	const sessionID = "sess-l14"
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "tooled-skill"),
		finalTurn(),
	}}
	rig := buildManageSkillsRig(t, llm, nil, true)

	resp, err := rig.agent.Chat(context.Background(), sessionID, "load tooled skill")
	require.NoError(t, err)
	require.Len(t, manageSkillsResults(resp), 1)

	recs := readDumpRecords(t, sinkDir)
	require.GreaterOrEqual(t, len(recs), 2, "at least the load turn and the follow-up turn")

	// The load-turn snapshot (first provider call) advertises manage_skills but
	// not the required tool: the tool did not mutate the frozen slice.
	first := toolNamesInRecord(recs[0])
	assert.True(t, first["manage_skills"], "manage_skills is advertised from the first turn")
	assert.False(t, first[requiredToolName],
		"required tool absent on the load turn's frozen snapshot")

	// A later provider call in the same Chat re-projects the tool set and shows
	// the just-registered required tool.
	surfacedLater := false
	for _, rec := range recs[1:] {
		if toolNamesInRecord(rec)[requiredToolName] {
			surfacedLater = true
		}
	}
	assert.True(t, surfacedLater,
		"required tool surfaces on the provider call after the load (same-turn re-projection)")

	// The tool's own contract: the required tool was written to this session's
	// ledger by registerSessionTool.
	rig.agent.mu.RLock()
	ledger := rig.agent.sessionToolLedger[sessionID]
	inLedger := ledger[requiredToolName]
	rig.agent.mu.RUnlock()
	assert.True(t, inLedger, "load wrote the required tool into this session's ledger")
}

// --- L21: high-risk load without approval is blocked, not activated ----------

// TestManageSkills_L21_HighRiskBlockedWithoutApproval asserts that loading a
// HIGH-risk skill with a permission checker present and non-YOLO returns an
// explicit approval-required Result and does NOT activate the skill — a returned
// verdict on every attempt, never a silent skip.
func TestManageSkills_L21_HighRiskBlockedWithoutApproval(t *testing.T) {
	const sessionID = "sess-l21"
	checker := shuttle.NewPermissionChecker(shuttle.PermissionConfig{}) // present, non-YOLO
	require.False(t, checker.IsYOLOMode())

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "danger-skill"),
		finalTurn(),
	}}
	rig := buildManageSkillsRig(t, llm, checker, false)

	resp, err := rig.agent.Chat(context.Background(), sessionID, "load danger")
	require.NoError(t, err)

	results := manageSkillsResults(resp)
	require.Len(t, results, 1)

	r := results[0]
	require.False(t, r.Success, "high-risk load without approval fails")
	require.NotNil(t, r.Error)
	assert.Equal(t, "approval_required", r.Error.Code,
		"explicit approval-required verdict, never a silent skip")
	assert.Equal(t, false, r.Metadata["activated"], "marker records the skill was not activated")

	assert.NotContains(t, activeSkillNames(rig.orch, sessionID), "danger-skill",
		"blocked high-risk skill is not in the active set")
}

// --- L23: high-risk load with nil checker or YOLO activates ------------------

// TestManageSkills_L23_HighRiskActivatesWithoutGate asserts a HIGH-risk skill
// loads and activates when there is no permission checker, and when the checker
// is in YOLO mode.
func TestManageSkills_L23_HighRiskActivatesWithoutGate(t *testing.T) {
	cases := []struct {
		name    string
		checker *shuttle.PermissionChecker
	}{
		{"nil-checker", nil},
		{"yolo-mode", shuttle.NewPermissionChecker(shuttle.PermissionConfig{YOLO: true})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := "sess-l23-" + tc.name
			llm := &mockToolCallingLLM{responses: []mockLLMResponse{
				loadCall("c1", "danger-skill"),
				finalTurn(),
			}}
			rig := buildManageSkillsRig(t, llm, tc.checker, false)

			resp, err := rig.agent.Chat(context.Background(), sessionID, "load danger")
			require.NoError(t, err)

			results := manageSkillsResults(resp)
			require.Len(t, results, 1)
			require.True(t, results[0].Success, "high-risk load activates without an enforcing gate")

			assert.True(t, activeSkillNames(rig.orch, sessionID)["danger-skill"],
				"high-risk skill is in the active set")
		})
	}
}

// --- L24: manage_skills registered as a base advertised tool -----------------

// TestManageSkills_L24_RegisteredWhenOrchestratorSet asserts manage_skills is
// registered and advertised from the first turn whenever the orchestrator is
// wired, is callable in the scripted drive, and is absent when no orchestrator
// is set.
func TestManageSkills_L24_RegisteredWhenOrchestratorSet(t *testing.T) {
	t.Run("registered-and-callable", func(t *testing.T) {
		sinkDir := redirectCtxDumpSink(t)
		llm := &mockToolCallingLLM{responses: []mockLLMResponse{
			loadCall("c1", "alpha-skill"),
			finalTurn(),
		}}
		rig := buildManageSkillsRig(t, llm, nil, true)

		require.NotNil(t, findRegisteredTool(rig.agent, "manage_skills"),
			"manage_skills registered when the orchestrator is set")

		resp, err := rig.agent.Chat(context.Background(), "sess-l24", "load alpha")
		require.NoError(t, err)

		results := manageSkillsResults(resp)
		require.Len(t, results, 1)
		assert.True(t, results[0].Success, "manage_skills is callable")

		// Advertised as a base tool from the very first provider call, before any
		// session-scoped registration.
		recs := readDumpRecords(t, sinkDir)
		require.NotEmpty(t, recs)
		assert.True(t, toolNamesInRecord(recs[0])["manage_skills"],
			"manage_skills advertised from the first turn (base tool)")
	})

	t.Run("absent-without-orchestrator", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.PatternConfig = DefaultPatternConfig()
		cfg.PatternConfig.UseLLMClassifier = false
		ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg))
		assert.Nil(t, findRegisteredTool(ag, "manage_skills"),
			"manage_skills is not registered without a skill orchestrator")
	})
}
