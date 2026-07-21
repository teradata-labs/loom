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

// D-4 (Admission + valve eviction + recall, Part B) — Seam 1 (admission gate) component
// acceptance test.
//
// Drives the real external interface a caller uses to run a tool — Executor.Execute (by
// name, via the registry) and Executor.ExecuteWithTool (with a tool handle directly) — the
// two call sites Seam 1 makes class-aware. Asserts ballast-admission-preview-charter-ledger-
// whole: a ballast-class tool result >= the 4096-byte admission threshold is wrapped into a
// preview + shared-memory reference; below the threshold, or for any non-ballast (charter,
// ledger, or unclassified/mutating) tool result of any size, the result enters context whole.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/storage"
)

// sizedTool returns a fixed string as its Data payload on Execute, letting tests pin the
// exact admission-check payload size — json.Marshal of a plain ASCII string adds exactly 2
// bytes (the surrounding quotes), so a data length of threshold-2 lands exactly at the
// threshold after serialization.
type sizedTool struct {
	toolName string
	data     string
}

func (s *sizedTool) Name() string             { return s.toolName }
func (s *sizedTool) Description() string      { return "sized tool for admission gate tests" }
func (s *sizedTool) Backend() string          { return "" }
func (s *sizedTool) InputSchema() *JSONSchema { return NewObjectSchema("sized", nil, nil) }
func (s *sizedTool) Execute(ctx context.Context, params map[string]interface{}) (*Result, error) {
	return &Result{Success: true, Data: s.data}, nil
}

var _ Tool = (*sizedTool)(nil)

// hintedSizedTool additionally opts into ballast-class context retention via
// ContextClassHinter, the whitelist mechanism a read-only data tool uses to become
// wrappable at admission (D-3/D-4).
type hintedSizedTool struct {
	sizedTool
	hint string
}

func (h *hintedSizedTool) ContextClassHint() string { return h.hint }

var _ Tool = (*hintedSizedTool)(nil)
var _ ContextClassHinter = (*hintedSizedTool)(nil)

// newAdmissionExecutor builds an Executor wired with a real SharedMemoryStore at the default
// 4096-byte admission threshold, mirroring production wiring (config_loader.go/registry.go)
// without needing full agent config plumbing.
func newAdmissionExecutor() (*Executor, *Registry) {
	reg := NewRegistry()
	exec := NewExecutor(reg)
	sharedMem := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       1 * 1024 * 1024,
		CompressionThreshold: 1 * 1024 * 1024,
		TTLSeconds:           3600,
	})
	exec.SetSharedMemory(sharedMem, DefaultAdmissionThresholdBytes)
	return exec, reg
}

// runViaExecute and runViaExecuteWithTool exercise the two admission call sites Seam 1 makes
// class-aware (executor.go Execute at :181-183, ExecuteWithTool at :262), so every scenario
// below is checked against both — a regression net for "extend BOTH guards".
func runViaExecute(t *testing.T, exec *Executor, reg *Registry, tool Tool, params map[string]interface{}) *Result {
	t.Helper()
	reg.Register(tool)
	result, err := exec.Execute(context.Background(), tool.Name(), params)
	require.NoError(t, err)
	return result
}

func runViaExecuteWithTool(t *testing.T, exec *Executor, _ *Registry, tool Tool, params map[string]interface{}) *Result {
	t.Helper()
	result, err := exec.ExecuteWithTool(context.Background(), tool, params)
	require.NoError(t, err)
	return result
}

var admissionEntryPoints = map[string]func(t *testing.T, exec *Executor, reg *Registry, tool Tool, params map[string]interface{}) *Result{
	"Execute":         runViaExecute,
	"ExecuteWithTool": runViaExecuteWithTool,
}

// TestAdmission_BallastAtOrAboveThresholdWraps: a ballast-class tool result whose serialized
// size is exactly at the 4096-byte threshold is admitted as a preview + reference, at both
// admission call sites.
func TestAdmission_BallastAtOrAboveThresholdWraps(t *testing.T) {
	for name, run := range admissionEntryPoints {
		t.Run(name, func(t *testing.T) {
			exec, reg := newAdmissionExecutor()
			original := strings.Repeat("a", 4094) // json.Marshal -> 4096 bytes, exactly at threshold
			tool := &hintedSizedTool{sizedTool: sizedTool{toolName: "get_customer_data", data: original}, hint: ClassBallast}

			result := run(t, exec, reg, tool, nil)

			require.NotNil(t, result.DataReference, "a ballast result at/above the threshold must be wrapped into a shared-memory reference")
			assert.NotEqual(t, original, result.Data, "wrapped Data must be replaced with a preview/summary, not the raw content")
		})
	}
}

// TestAdmission_BallastBelowThresholdStaysInline: the same ballast-class tool, one byte under
// the threshold after serialization, must enter context whole (unwrapped).
func TestAdmission_BallastBelowThresholdStaysInline(t *testing.T) {
	for name, run := range admissionEntryPoints {
		t.Run(name, func(t *testing.T) {
			exec, reg := newAdmissionExecutor()
			original := strings.Repeat("a", 4093) // json.Marshal -> 4095 bytes, one under threshold
			tool := &hintedSizedTool{sizedTool: sizedTool{toolName: "get_customer_data_small", data: original}, hint: ClassBallast}

			result := run(t, exec, reg, tool, nil)

			assert.Nil(t, result.DataReference, "a ballast result below the threshold must not be wrapped")
			assert.Equal(t, original, result.Data, "Data must be returned unchanged when below the threshold")
		})
	}
}

// TestAdmission_CharterToolNeverWrappedRegardlessOfSize: a named charter/ledger builtin
// (manage_skills) must enter whole even for a large body (the 24KB skill-body edge case) —
// the name-based exemption applies regardless of ContextClassHinter.
func TestAdmission_CharterToolNeverWrappedRegardlessOfSize(t *testing.T) {
	for name, run := range admissionEntryPoints {
		t.Run(name, func(t *testing.T) {
			exec, reg := newAdmissionExecutor()
			original := strings.Repeat("skill body content. ", 1200) // ~24KB, well over threshold
			tool := &sizedTool{toolName: "manage_skills", data: original}

			result := run(t, exec, reg, tool, nil)

			assert.Nil(t, result.DataReference, "manage_skills is a named charter exemption and must never be wrapped")
			assert.Equal(t, original, result.Data, "charter tool result must enter context whole, unmodified")
		})
	}
}

// TestAdmission_LedgerToolNeverWrappedRegardlessOfSize: a tool with no ballast hint at all
// (the fail-safe ledger default) must never be wrapped, even for a large result and even
// without appearing on the named exemption list — Wrappable requires IsBallast to be true.
func TestAdmission_LedgerToolNeverWrappedRegardlessOfSize(t *testing.T) {
	for name, run := range admissionEntryPoints {
		t.Run(name, func(t *testing.T) {
			exec, reg := newAdmissionExecutor()
			original := strings.Repeat("migration log line. ", 1500) // ~30KB, well over threshold
			tool := &sizedTool{toolName: "run_sql_migration", data: original}

			result := run(t, exec, reg, tool, nil)

			assert.Nil(t, result.DataReference, "a tool without a ballast hint (ledger default) must never be wrapped")
			assert.Equal(t, original, result.Data, "ledger tool result must enter context whole, unmodified")
		})
	}
}

// TestAdmission_ExemptNamesOverrideBallastHint: the named exemption set (manage_skills,
// manage_patterns, contact_human, recall_context, get_tool_result, query_tool_result) is a
// belt-and-suspenders skip — a tool that both names itself in the exemption set AND opts into
// a ballast hint (an inconsistent configuration) must still never be wrapped.
func TestAdmission_ExemptNamesOverrideBallastHint(t *testing.T) {
	for _, exemptName := range []string{"manage_skills", "manage_patterns", "contact_human", "recall_context", "get_tool_result", "query_tool_result"} {
		t.Run(exemptName, func(t *testing.T) {
			for name, run := range admissionEntryPoints {
				t.Run(name, func(t *testing.T) {
					exec, reg := newAdmissionExecutor()
					original := strings.Repeat("b", 8192)
					tool := &hintedSizedTool{sizedTool: sizedTool{toolName: exemptName, data: original}, hint: ClassBallast}

					result := run(t, exec, reg, tool, nil)

					assert.Nil(t, result.DataReference, "%s is named-exempt and must never be wrapped even with a ballast hint", exemptName)
					assert.Equal(t, original, result.Data)
				})
			}
		})
	}
}
