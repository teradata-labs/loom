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

// D-2 (Contract 1 core: closed-world assembler + deletion manifest 1 + tail note)
// tree-wide grep gates (LLD Seam 2/5; story acceptance criteria
// injection-channels-removed-graph-extract-unchanged, no-system-role-outside-assembler,
// treewide-zero-refs).
//
// repoRootFromPkgAgent, nonTestGoFile, funcCallSites, and identOccurrences are defined in
// contract2_deleted_symbols_test.go (D-1, same package) and reused here unchanged.
//
// These assert:
//   - InjectSkills, InjectPattern, findingsCache, and promotedContext (the SegmentedMemory
//     injection channels and their backing state) have zero non-test references left,
//     tree-wide — fully deleted, not just uncalled.
//   - FormatActiveSkillsForLLM (pkg/skills/orchestrator.go, the skill-body-into-context
//     formatter D-2 deletes once its sole call site in agent.go's discovery block is
//     removed) has zero non-test references left anywhere in the tree, including
//     pkg/skills itself.
//   - The only `Message{Role: "system", ...}` (or equivalent composite literal) constructed
//     anywhere in pkg/agent's non-test source reaching the LLM path lives inside
//     SegmentedMemory.GetMessagesForLLM — the sole assembler (O-ASM-2). This gate is scoped
//     to pkg/agent (D-2's component scope), not tree-wide: wire-format serialization of an
//     already-assembled message elsewhere (e.g. an LLM provider client marshaling for its
//     own wire protocol) is not context injection and is out of scope. The soft-reminder
//     mutation, the graph-memory inject, and the four deleted injection renders were the
//     only other offenders within pkg/agent; once Seam 1/2 land, GetMessagesForLLM's own
//     ROM and fold-residue lines are the last two sites.
//   - extractGraphMemoryAsync (background graph extraction) keeps every one of its call
//     sites — Seam 2 deletes injectGraphMemoryContext's persist-into-session behavior, but
//     explicitly leaves background extraction untouched (C-006).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- treewide-zero-refs (D-2's five symbols) ---

func TestGrepGate_InjectPattern_Deleted(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	occurrences := identOccurrences(t, root, "InjectPattern")

	assert.Empty(t, occurrences,
		"InjectPattern (the pattern-into-context injection channel) must be fully deleted — zero non-test references")
}

func TestGrepGate_InjectSkills_Deleted(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	occurrences := identOccurrences(t, root, "InjectSkills")

	assert.Empty(t, occurrences,
		"InjectSkills (the skill-body-into-context injection channel) must be fully deleted — zero non-test references")
}

func TestGrepGate_FormatActiveSkillsForLLM_Deleted(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	occurrences := identOccurrences(t, root, "FormatActiveSkillsForLLM")

	assert.Empty(t, occurrences,
		"FormatActiveSkillsForLLM (pkg/skills/orchestrator.go) must be fully deleted tree-wide — zero non-test references, "+
			"including its declaration and any caller in pkg/agent or pkg/skills")
}

func TestGrepGate_FindingsCache_Deleted(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	occurrences := identOccurrences(t, root, "findingsCache")

	assert.Empty(t, occurrences,
		"findingsCache (the verified-findings working-memory channel) must be fully deleted — zero non-test references")
}

func TestGrepGate_PromotedContext_Deleted(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	occurrences := identOccurrences(t, root, "promotedContext")

	assert.Empty(t, occurrences,
		"promotedContext (the swap-retrieval-into-context injection channel) must be fully deleted — zero non-test references")
}

// --- no-system-role-outside-assembler ---

// systemRoleConstructSites finds every composite literal of the form
// `Message{..., Role: "system", ...}` (any field order, any selector prefix such as
// types.Message{...}) in non-test Go source under root, returning the enclosing top-level
// function's name for each. A cheap substring pre-filter avoids parsing files that can't
// possibly contain a system-role construction.
func systemRoleConstructSites(t *testing.T, root string) map[string][]string {
	t.Helper()
	sites := make(map[string][]string) // file -> enclosing func names

	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !nonTestGoFile(path) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(raw), "Role") || !strings.Contains(string(raw), `"system"`) {
			return nil
		}
		file, err := parser.ParseFile(fset, path, raw, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok || key.Name != "Role" {
						continue
					}
					val, ok := kv.Value.(*ast.BasicLit)
					if !ok || val.Kind != token.STRING {
						continue
					}
					if val.Value == `"system"` {
						sites[path] = append(sites[path], fn.Name.Name)
					}
				}
				return true
			})
		}
		return nil
	})
	require.NoError(t, err)
	return sites
}

func TestGrepGate_SystemRoleConstruction_OnlyInsideAssembler(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	// Scoped to pkg/agent (D-2's component scope), not tree-wide: wire-format serialization
	// of already-assembled messages elsewhere (e.g. an LLM provider client marshaling a
	// message for transmission over its own wire protocol) is not injection into context
	// and is out of scope for this gate — only construction sites within the surface that
	// owns the LLM path's context assembly are checked.
	pkgAgentDir := filepath.Join(root, "pkg", "agent")
	sites := systemRoleConstructSites(t, pkgAgentDir)

	assemblerFile := filepath.Join(pkgAgentDir, "segmented_memory.go")
	for file, funcs := range sites {
		for _, fn := range funcs {
			assert.Equal(t, assemblerFile, file,
				"a Role:\"system\" message must only be constructed inside segmented_memory.go's assembler, found one in %s (%s)", file, fn)
			if file == assemblerFile {
				assert.Equal(t, "GetMessagesForLLM", fn,
					"a Role:\"system\" message inside segmented_memory.go must only be constructed by GetMessagesForLLM, found one in %s", fn)
			}
		}
	}
}

// --- injection-channels-removed-graph-extract-unchanged (background half) ---

// TestGrepGate_ExtractGraphMemoryAsync_CallSitesUnchanged asserts extractGraphMemoryAsync
// (background graph extraction, C-006) keeps being called from the conversation loop.
// Seam 2 deletes injectGraphMemoryContext's persist-into-session inject; it must not touch
// the separate, already-async, already-backgrounded extraction pathway.
func TestGrepGate_ExtractGraphMemoryAsync_CallSitesUnchanged(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	sites := funcCallSites(t, root, "extractGraphMemoryAsync")

	agentGoSites := sites[filepath.Join(root, "pkg", "agent", "agent.go")]
	assert.NotEmpty(t, agentGoSites,
		"extractGraphMemoryAsync must still be called from agent.go's conversation loop after D-2 — background extraction is untouched (C-006)")
}

// The graph-memory inject's persist-into-session behavior (agent.go previously constructed
// a Role:"system" "[Graph Memory Context]" message via session.AddMessage) is covered by
// TestGrepGate_SystemRoleConstruction_OnlyInsideAssembler above, which catches that
// construction site directly — the ticket mandates deleting the persist behavior (Seam 2),
// not any particular function name, so no separate identifier-deletion gate is pinned here.
