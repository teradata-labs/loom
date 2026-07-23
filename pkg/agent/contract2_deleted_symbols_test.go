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

// D-1 tree-wide grep gates (LLD Tests item (e); story acceptance criteria
// getcontextwindow-deleted and treewide-zero-refs).
//
// These walk the whole repo's non-test Go source (every *.go file except those ending in
// _test.go) and assert:
//   - GetContextWindow (the string-form second assembler) has zero references left at all —
//     it is fully deleted, not just uncalled.
//   - maxL1Tokens (the SegmentedMemory field that drove per-message/restore compression
//     triggers) has zero references left at all — it is fully deleted.
//   - AggressiveTrim has no CALL site anywhere except inside Agent.ResetSessionContext. Its
//     method declaration (segmented_memory.go) and its TrimableMemory interface signature
//     (recovery.go) are not call sites and are exempt.
//
// Also asserts the call-site closure this story's LLD claims for prepareContext: exactly the
// two LLM-bound sites (the per-turn loop and the max-turns synthesis call) invoke it — the
// static half of "single-writer-both-sites" that a stubbed-dispatch behavioral test can't see.

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

// repoRootFromPkgAgent resolves the repository root relative to this test file's package
// directory (pkg/agent), verified by the presence of the repo's go.mod.
func repoRootFromPkgAgent(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("could not locate repo root (expected go.mod at %s): %v", root, err)
	}
	return root
}

// nonTestGoFile reports whether path is Go source this gate should scan: a *.go file that is
// not a _test.go file, not generated code, and not vendored/version-controlled tooling.
func nonTestGoFile(path string) bool {
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return false
	}
	for _, skip := range []string{
		string(filepath.Separator) + ".git" + string(filepath.Separator),
		string(filepath.Separator) + "gen" + string(filepath.Separator),
		string(filepath.Separator) + "vendor" + string(filepath.Separator),
		string(filepath.Separator) + "node_modules" + string(filepath.Separator),
	} {
		if strings.Contains(path, skip) {
			return false
		}
	}
	return true
}

// funcCallSites finds every call `recv.symbol(...)` or `symbol(...)` in non-test Go source
// under root, returning the enclosing top-level function's name for each (empty string if the
// call is not inside any function body). A cheap substring pre-filter avoids parsing files
// that can't possibly contain the symbol.
func funcCallSites(t *testing.T, root, symbol string) map[string][]string {
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
		if !strings.Contains(string(raw), symbol) {
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
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch fx := call.Fun.(type) {
				case *ast.SelectorExpr:
					name = fx.Sel.Name
				case *ast.Ident:
					name = fx.Name
				}
				if name == symbol {
					sites[path] = append(sites[path], fn.Name.Name)
				}
				return true
			})
		}
		return nil
	})
	require.NoError(t, err)
	return sites
}

// identOccurrences counts every occurrence of the exact identifier name (declaration or use)
// anywhere in non-test Go source under root — used for symbols that must be deleted entirely,
// not merely uncalled.
func identOccurrences(t *testing.T, root, name string) map[string]int {
	t.Helper()
	occurrences := make(map[string]int)

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
		if !strings.Contains(string(raw), name) {
			return nil
		}
		file, err := parser.ParseFile(fset, path, raw, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		count := 0
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == name {
				count++
			}
			return true
		})
		if count > 0 {
			occurrences[path] = count
		}
		return nil
	})
	require.NoError(t, err)
	return occurrences
}

// --- getcontextwindow-deleted ---

func TestGrepGate_GetContextWindow_Deleted(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	occurrences := identOccurrences(t, root, "GetContextWindow")

	assert.Empty(t, occurrences,
		"GetContextWindow (the string-form second assembler) must be fully deleted — zero non-test references, including its own declaration")
}

// --- treewide-zero-refs ---

func TestGrepGate_MaxL1Tokens_Deleted(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	occurrences := identOccurrences(t, root, "maxL1Tokens")

	assert.Empty(t, occurrences,
		"maxL1Tokens (the SegmentedMemory field that drove per-message/restore compression triggers) must be fully deleted — zero non-test references")
}

func TestGrepGate_AggressiveTrim_OnlyCalledFromResetSessionContext(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	sites := funcCallSites(t, root, "AggressiveTrim")

	for file, funcs := range sites {
		for _, fn := range funcs {
			assert.Equal(t, "ResetSessionContext", fn,
				"AggressiveTrim must be reachable only from the user-surfaced reset_context action; found a call inside %s (%s)", fn, file)
		}
	}
}

func TestGrepGate_SegmentedMemoryTrimLastN_OnlyCalledFromResetSessionContext(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	sites := funcCallSites(t, root, "TrimLastN")

	for file, funcs := range sites {
		for _, fn := range funcs {
			assert.Equal(t, "ResetSessionContext", fn,
				"TrimLastN must be reachable only from the user-surfaced reset_context action; found a call inside %s (%s)", fn, file)
		}
	}
}

// --- call-site closure backing single-writer-both-sites ---

func TestGrepGate_PrepareContext_CalledFromExactlyBothLLMBoundSites(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	sites := funcCallSites(t, root, "prepareContext")

	agentGoSites := sites[filepath.Join(root, "pkg", "agent", "agent.go")]
	require.NotEmpty(t, agentGoSites, "prepareContext must be called from agent.go's conversation loop")
	assert.Len(t, agentGoSites, 2,
		"prepareContext must run before the LLM call on exactly both LLM-bound sites (the per-turn loop and the max-turns synthesis call), no more, no fewer")

	for file := range sites {
		assert.Equal(t, filepath.Join(root, "pkg", "agent", "agent.go"), file,
			"prepareContext must only be called from agent.go's conversation loop, found a call in %s", file)
	}
}

func TestGrepGate_ChatWithRetry_HasExactlyTwoCallSites(t *testing.T) {
	root := repoRootFromPkgAgent(t)
	sites := funcCallSites(t, root, "chatWithRetry")

	agentGoSites := sites[filepath.Join(root, "pkg", "agent", "agent.go")]
	assert.Len(t, agentGoSites, 2,
		"chatWithRetry must have exactly the two LLM-bound call sites this story's call-surface closure claims")
}
