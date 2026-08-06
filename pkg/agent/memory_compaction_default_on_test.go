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
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Story D-D — component suite. Covers:
//   - ac-1 (partial): the live agent-init path wires an enabled LLM compressor
//     (default-on), and CompressConversation sources its compaction prompt from
//     the FileRegistry key "memory.compaction" — never a string literal — whose
//     content instructs preservation of decisions/approvals-with-scope, open
//     commitments, and reference/reload pointers while compressing reasoning and
//     reproducible data.
//   - ac-2: the heuristic summariser is used only when no enabled compressor is
//     present.
//
// The behavioural outcome that a REAL LLM compaction emits the pre-compaction
// approval with its scope into the L2 summary is deferred to the e2e LLM
// surface; here the terminal LLM is a recording stub so the local surface can
// still assert the wiring, prompt source, and routing.

// recordingLLM is an LLMProvider that captures every Chat invocation and returns
// a fixed summary. Tests inspect its calls to see whether — and under which
// system prompt — the compaction path reached the LLM.
type recordingLLM struct {
	mu      sync.Mutex
	summary string
	calls   [][]Message
}

func (r *recordingLLM) Chat(_ context.Context, msgs []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	captured := make([]Message, len(msgs))
	copy(captured, msgs)
	r.calls = append(r.calls, captured)
	return &LLMResponse{Content: r.summary}, nil
}

func (r *recordingLLM) Name() string  { return "recording" }
func (r *recordingLLM) Model() string { return "recording-model" }

func (r *recordingLLM) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// callWithSystem returns the first captured Chat whose system message equals
// want, along with that call's user content.
func (r *recordingLLM) callWithSystem(want string) (userContent string, found bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, msgs := range r.calls {
		if len(msgs) >= 2 && msgs[0].Role == "system" && msgs[0].Content == want && msgs[1].Role == "user" {
			return msgs[1].Content, true
		}
	}
	return "", false
}

// loadRepoPromptRegistry loads the repo's on-disk prompt files (the same
// prompts/ tree the live path reads) and asserts the compaction prompt is
// registered, so tests exercise the real registry content rather than a fixture.
func loadRepoPromptRegistry(t *testing.T) *prompts.FileRegistry {
	t.Helper()
	dir := filepath.Join("..", "..", "prompts")
	reg := prompts.NewFileRegistry(dir)
	require.NoError(t, reg.Reload(context.Background()), "load repo prompt registry from %s", dir)

	got, err := reg.Get(context.Background(), compactionPromptKey, nil)
	require.NoErrorf(t, err, "%q must be registered in %s", compactionPromptKey, dir)
	require.NotEmpty(t, got)
	return reg
}

// ac-1 (default-on): NewAgent with an LLM and a prompt registry wires an enabled
// LLM compressor, so a compaction routes through the LLM under the registry's
// compaction prompt — not the heuristic. The approval stated before compaction,
// with its scope, is what reaches the compressor's input.
func TestNewAgent_DefaultOn_CompactionUsesRegistryPrompt(t *testing.T) {
	ctx := context.Background()
	reg := loadRepoPromptRegistry(t)
	wantPrompt, err := reg.Get(ctx, compactionPromptKey, nil)
	require.NoError(t, err)

	const l2Sentinel = "LLM-L2-SUMMARY-SENTINEL"
	llm := &recordingLLM{summary: l2Sentinel}

	ag := NewAgent(nil, llm, WithPrompts(reg))

	sess := ag.memory.GetOrCreateSession(ctx, "sess-default-on")
	segMem, ok := sess.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "session must hold a *SegmentedMemory")

	const approval = "Approved deploying build 42 to staging only."
	segMem.AddMessage(ctx, Message{Role: "user", Content: approval})
	segMem.AddMessage(ctx, Message{Role: "assistant", Content: "Acknowledged."})
	segMem.AddMessage(ctx, Message{Role: "user", Content: "continue"})

	segMem.CompactMemory(ctx)

	require.GreaterOrEqual(t, llm.callCount(), 1,
		"default-on wiring must route compaction through the LLM compressor")

	userContent, found := llm.callWithSystem(wantPrompt)
	require.True(t, found,
		"compaction must call the LLM with the registry compaction prompt as the system message")
	assert.Contains(t, userContent, approval,
		"the pre-compaction approval with its scope must reach the compressor input")

	l2 := segMem.GetL2Summary()
	assert.Contains(t, l2, l2Sentinel, "the LLM compressor output must populate L2")
	assert.NotContains(t, l2, "User asked about:",
		"with an enabled compressor the heuristic summariser must not run")
}

// ac-1 (registry-sourced): CompressConversation sends the registry's
// memory.compaction content as the system instruction and the conversation as
// user content — the prompt is sourced from the registry, not embedded.
func TestCompressConversation_UsesRegistrySystemPrompt(t *testing.T) {
	ctx := context.Background()
	reg := loadRepoPromptRegistry(t)
	wantPrompt, err := reg.Get(ctx, compactionPromptKey, nil)
	require.NoError(t, err)

	llm := &recordingLLM{summary: "ok"}
	caller := newPromptRegistryCompressor(llm, reg)

	const conversation = "[user]: Approved schema change for the orders table only."
	out, err := caller.CompressConversation(ctx, conversation)
	require.NoError(t, err)
	assert.Equal(t, "ok", out)

	userContent, found := llm.callWithSystem(wantPrompt)
	require.True(t, found, "system message must be the registry compaction prompt verbatim")
	assert.Equal(t, conversation, userContent, "conversation must be passed as user content")
}

// ac-1 (not a string literal): with no memory.compaction in the registry there
// is no baked-in prompt to fall back to, so CompressConversation fails and never
// calls the LLM — proving the prompt is registry-sourced, not hardcoded.
func TestCompressConversation_NoHardcodedFallbackWhenPromptMissing(t *testing.T) {
	ctx := context.Background()

	emptyReg := prompts.NewFileRegistry(t.TempDir())
	require.NoError(t, emptyReg.Reload(ctx))

	llm := &recordingLLM{summary: "should-not-be-used"}
	caller := newPromptRegistryCompressor(llm, emptyReg)

	_, err := caller.CompressConversation(ctx, "[user]: hello")
	require.Error(t, err, "missing registry prompt must be an error, not a hardcoded fallback")
	assert.Contains(t, err.Error(), compactionPromptKey)
	assert.Equal(t, 0, llm.callCount(), "no LLM call is made without a registry-sourced prompt")
}

// ac-1 (prompt content): the registered memory.compaction prompt instructs
// preservation of decisions and approvals with their exact scope, open
// commitments, and reference/reload pointers, while compressing reasoning and
// reproducible data.
func TestCompactionPrompt_InstructsDecisionAndScopePreservation(t *testing.T) {
	ctx := context.Background()
	reg := loadRepoPromptRegistry(t)

	prompt, err := reg.Get(ctx, compactionPromptKey, nil)
	require.NoError(t, err)
	low := strings.ToLower(prompt)

	// Decisions and approvals WITH their exact scope.
	assert.Contains(t, low, "decision")
	assert.Contains(t, low, "approval")
	assert.Contains(t, low, "scope")
	// Open commitments.
	assert.Contains(t, low, "commitment")
	// Reference and reload pointers.
	assert.Contains(t, low, "reference")
	assert.Contains(t, low, "reload")
	// Compress reasoning and reproducible data.
	assert.Contains(t, low, "reasoning")
	assert.Contains(t, low, "reproduc")
}

// ac-2: the heuristic summariser runs only when no enabled compressor is
// present. Absent or disabled → heuristic ("User asked about:"); enabled →
// compressor output, heuristic suppressed.
func TestCompaction_HeuristicUsedOnlyWhenNoEnabledCompressor(t *testing.T) {
	const sentinel = "COMPRESSOR-OUTPUT-SENTINEL"

	// compressToL2 keeps the last user message in L1 and compresses what
	// precedes it, so a trailing user message forces the approval into the
	// compressed batch.
	fill := func(sm *SegmentedMemory) {
		ctx := context.Background()
		sm.AddMessage(ctx, Message{Role: "user", Content: "Approved rollback of the orders service."})
		sm.AddMessage(ctx, Message{Role: "assistant", Content: "Noted."})
		sm.AddMessage(ctx, Message{Role: "user", Content: "next"})
	}

	tests := []struct {
		name          string
		compressor    MemoryCompressor
		wantHeuristic bool
	}{
		{
			name:          "no compressor present",
			compressor:    nil,
			wantHeuristic: true,
		},
		{
			name:          "present but disabled compressor is treated as absent",
			compressor:    &mockCompressor{enabled: false, compressFn: func([]Message) string { return sentinel }},
			wantHeuristic: true,
		},
		{
			name:          "enabled compressor suppresses the heuristic",
			compressor:    &mockCompressor{enabled: true, compressFn: func([]Message) string { return sentinel }},
			wantHeuristic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewSegmentedMemory("rom", 0, 0)
			if tt.compressor != nil {
				sm.SetCompressor(tt.compressor)
			}
			fill(sm)
			sm.CompactMemory(context.Background())

			l2 := sm.GetL2Summary()
			require.NotEmpty(t, l2, "compaction must produce an L2 summary")
			if tt.wantHeuristic {
				assert.Contains(t, l2, "User asked about:", "heuristic summariser must be used")
				assert.NotContains(t, l2, sentinel, "the compressor must not run")
			} else {
				assert.Contains(t, l2, sentinel, "the enabled compressor must produce the summary")
				assert.NotContains(t, l2, "User asked about:", "heuristic must not run when a compressor is present")
			}
		})
	}
}
