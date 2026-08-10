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

//go:build integration

package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// D-2 story, integration surface, AC6 (test-plan.yaml): a malformed/unresolvable
// tools.hooks binding must fail `looms serve` startup LOUDLY — the process aborts
// non-zero and logs the failure, never coming up silently ungoverned (fail-closed,
// C-007, L-Cfg-1). The builder-level error return is proven at the component surface;
// only a running process can show that error becomes a startup abort, which is what
// this leg asserts.

// repoRoot returns the loom module root (two levels above this test/e2e file), so a
// freshly built looms binary and `serve`'s relative asset dirs (docs/, examples/)
// resolve against the repo under test rather than an ambient checkout.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed to locate this test file")
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	require.NoError(t, err)
	return root
}

// buildLoomsBinary compiles the looms server from the repo under test with the same
// tags/cgo settings the pack prepare uses (fts5, CGO disabled, GOWORK off), so the
// serve path this AC exercises is the one shipped. The binary lands in the test's
// temp dir and is removed with it.
func buildLoomsBinary(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "looms-ac6")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-tags", "fts5", "-o", bin, "./cmd/looms")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "building looms failed: %s", out)
	return bin
}

// TestE2E_AdmissionServe_MalformedHookBindingFailsClosed asserts AC6 at the serve
// process boundary: a tools.hooks binding with an unknown kind makes `looms serve`
// abort with a non-zero exit and a logged admission-chain build failure — it never
// reaches the point of accepting connections. The config is otherwise valid and
// self-contained (sqlite backend; the admission chain is built before any port bind
// or LLM preflight, so no external service is required), which pins the abort to the
// malformed binding rather than an ambient startup failure.
func TestE2E_AdmissionServe_MalformedHookBindingFailsClosed(t *testing.T) {
	root := repoRoot(t)
	bin := buildLoomsBinary(t, root)

	home := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "loom.db")

	// Valid server config EXCEPT the single tools.hooks binding, whose kind is not a
	// registered policy. BuildChainFromConfig rejects it, so createAdmissionChain
	// returns an error and serve fatals. The tools.hooks list nests under tools.hooks
	// (Tools.Hooks -> HooksConfig.Bindings), matching how serve unmarshals config.
	config := `
server:
  port: 61099
  host: 127.0.0.1
llm:
  provider: ollama
  model: stub
storage:
  backend: sqlite
  sqlite:
    path: ` + dbPath + `
  migration:
    auto_migrate: true
observability:
  enabled: false
logging:
  level: info
  format: text
tools:
  hooks:
    hooks:
      - kind: not-a-real-kind
        scope: "sql_execute"
`
	cfgPath := filepath.Join(t.TempDir(), "looms-malformed-hook.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(config), 0o600))

	// A generous timeout is a safety net only: serve fatals on the bad binding well
	// before it would bind the gRPC port, so a healthy run exits fast. A timeout kill
	// would surface as a non-ExitError below and fail the test.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "serve", "--config", cfgPath)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "HOME="+home)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()

	logs := out.String()

	// Fail-closed: serve exited non-zero (a clean process exit, not a timeout signal
	// kill) rather than coming up.
	var exitErr *exec.ExitError
	require.ErrorAsf(t, err, &exitErr, "serve should exit non-zero on a malformed binding; output:\n%s", logs)
	require.Positivef(t, exitErr.ExitCode(), "serve should exit with a non-zero code, got %d; output:\n%s", exitErr.ExitCode(), logs)

	// Loud, not silent: the malformed binding was seen and the admission-chain build
	// failure is logged — the abort is tied to that binding, never a silent ungoverned
	// startup.
	require.Containsf(t, logs, "Admission hook binding", "serve should log the binding it tried to build; output:\n%s", logs)
	require.Containsf(t, logs, "Failed to build admission chain", "serve should log the admission-chain build failure; output:\n%s", logs)
}
