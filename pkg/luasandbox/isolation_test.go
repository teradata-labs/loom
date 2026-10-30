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

package luasandbox

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoGoroutineOutlivesARun runs many scripts of every kind, including
// ones that try coroutines, get cancelled, or blow a budget, and checks that
// the goroutine count returns to where it started.
func TestNoGoroutineOutlivesARun(t *testing.T) {
	scripts := []string{
		`return 1`,
		`return coroutine and coroutine.create(function() end)`,
		`while true do end`,
		`local t = {} for i = 1, 1e8 do t[i] = i end`,
		`tools.call("echo", {x = 1}) time.sleep(1) return json.encode({a = 1})`,
		`pcall(error, "x") return tools.list()`,
		`error("boom")`,
	}
	h := newFakeHost().with("echo", echo)
	lim := small()
	lim.CPUTicks = 2_000_000
	lim.MemoryBytes = 4 << 20
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := range 300 {
		ctx, cancel := context.WithCancel(context.Background())
		if i%5 == 0 {
			cancel()
		}
		_ = Run(ctx, Program{Source: scripts[i%len(scripts)]}, lim, h)
		cancel()
	}
	// Timer goroutines from context.WithTimeout exit asynchronously.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), before)
}

// TestConcurrentRunsShareNothing runs scripts in parallel against one host.
// Under -race this proves runs share no interpreter state (string interning,
// random generators, library tables).
func TestConcurrentRunsShareNothing(t *testing.T) {
	h := newFakeHost().with("echo", echo)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src := fmt.Sprintf(`
				math.randomseed(%d)
				local acc = {}
				for j = 1, 200 do
					acc[#acc + 1] = string.format("%%d:%%s", math.random(1, 1000), ("x"):rep(j %% 7))
				end
				table.sort(acc)
				local r = tools.call("echo", {id = %d, n = #acc})
				shared_global = %d
				return {id = r.data.id, n = r.data.n, g = shared_global, j = json.decode(json.encode({k = %d})).k}`, i, i, i, i)
			res := Run(context.Background(), Program{Source: src}, small(), h)
			want := map[string]any{"id": int64(i), "n": int64(200), "g": int64(i), "j": int64(i)}
			if res.Outcome != OutcomeOK || fmt.Sprint(res.Value) != fmt.Sprint(want) {
				errs <- fmt.Errorf("run %d: %s %v %s", i, res.Outcome, res.Value, res.Error)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestForbiddenAPIs keeps the package away from the golua calls and
// libraries the design rules out. Each one was measured to hang, leak or
// open I/O (docs/architecture/lua-script-engine.md).
func TestForbiddenAPIs(t *testing.T) {
	forbiddenCalls := []string{"SetStopLevel", "KillContext"}
	forbiddenImports := []string{
		"github.com/teradata-labs/loom/third_party/golua/lib/coroutine",
		"github.com/teradata-labs/loom/third_party/golua/lib/iolib",
		"github.com/teradata-labs/loom/third_party/golua/lib/oslib",
		"github.com/teradata-labs/loom/third_party/golua/lib/debuglib",
		"github.com/teradata-labs/loom/third_party/golua/lib/golib",
		"github.com/teradata-labs/loom/third_party/golua/lib/runtimelib",
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		file, err := parser.ParseFile(fset, f, src, parser.ImportsOnly|parser.ParseComments)
		require.NoError(t, err)
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			assert.NotContains(t, forbiddenImports, path, "%s imports %s", f, path)
			if path == "github.com/teradata-labs/loom/third_party/golua/lib" {
				// LoadAll would pull in every library.
				assert.NotContains(t, string(src), "lib.LoadAll", "%s calls lib.LoadAll", f)
			}
		}
		full, err := parser.ParseFile(fset, f, src, 0)
		require.NoError(t, err)
		ast.Inspect(full, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				assert.NotContains(t, forbiddenCalls, sel.Sel.Name, "%s uses %s", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}
