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

//go:build !windows

package shelljail

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shellpolicy"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// The test binary is its own jail: re-executed with MarkerEnv set, it runs
// the child instead of the tests.
func TestMain(m *testing.M) {
	Main()
	os.Exit(m.Run())
}

const testSession = "sess-jail-test"

type fixture struct {
	r       *Runner
	opt     shellpolicy.Options
	scratch string
}

func newFixture(t *testing.T, mutate ...func(*Config)) *fixture {
	t.Helper()
	data, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("LOOM_DATA_DIR", data)
	cfg := Config{Policy: shellpolicy.Readonly(), LoomDataDir: data, Strict: true}
	for _, m := range mutate {
		m(&cfg)
	}
	r, _, err := NewRunner(cfg)
	require.NoError(t, err)
	opt, err := r.Options(testSession, "")
	require.NoError(t, err)
	return &fixture{r: r, opt: opt, scratch: opt.WorkingDir}
}

type run struct {
	stdout, stderr string
	code           int
	res            Result
	took           time.Duration
}

func (r run) blockedNames() []string {
	var out []string
	for _, b := range r.res.Blocked {
		out = append(out, b.Name)
	}
	return out
}

// jailed runs command through a real child process, as shell_execute does.
func (f *fixture) jailed(t *testing.T, command string, grant *shellpolicy.Grant, timeout time.Duration) run {
	t.Helper()
	env, _ := f.r.BaseEnv(f.scratch, nil, nil)
	child, err := f.r.Command(f.r.Spec(command, f.opt, grant, env, timeout))
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	child.Cmd.Stdout = &stdout
	child.Cmd.Stderr = &stderr
	start := time.Now()
	require.NoError(t, child.Cmd.Start())
	child.Started()
	done := make(chan error, 1)
	go func() { done <- child.Cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(timeout):
		KillGroup(child.Cmd)
		waitErr = <-done
	}
	KillGroup(child.Cmd)
	out := run{stdout: stdout.String(), stderr: stderr.String(), res: child.Result(), took: time.Since(start)}
	if ee, ok := waitErr.(*exec.ExitError); ok {
		out.code = ee.ExitCode()
	} else if waitErr != nil {
		out.code = -1
	}
	return out
}

// lockedBuffer is a bytes.Buffer safe for the concurrent writers a shell
// produces (pipeline stages, background jobs). The real child writes to OS
// pipes, where concurrent writes are already safe.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// inProcess runs the child's logic in this process (no limits, no
// watchdog), so the race detector sees the handlers.
func (f *fixture) inProcess(t *testing.T, command string, grant *shellpolicy.Grant) run {
	t.Helper()
	env, _ := f.r.BaseEnv(f.scratch, nil, nil)
	spec := f.r.Spec(command, f.opt, grant, env, 0)
	spec.Limits = Limits{}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	var stdout, stderr lockedBuffer
	var result bytes.Buffer
	code := runChild(bytes.NewReader(raw), &stdout, &stderr, &result)
	var res Result
	require.NoError(t, json.Unmarshal(result.Bytes(), &res), result.String())
	return run{stdout: stdout.String(), stderr: stderr.String(), code: code, res: res}
}

func TestSelfTest(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.r.SelfTest(context.Background()))
}

func TestSelfTest_FailsWhenTheBinaryIsNotAJail(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Executable = "/bin/sleep" })
	err := f.r.SelfTest(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shelljail.Main()")
}

type corpusCase struct {
	cmd     string
	grant   *shellpolicy.Grant
	blocked string // a blocked name the result must list; "" = none blocked
	out     string // a substring stdout must contain
}

// corpus is the design's corpus (06 §11, the runner column).
func corpus(t *testing.T, f *fixture) []corpusCase {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.scratch, "notes.txt"), []byte("alpha\nbeta\n"), 0o600))
	hosts, err := filepath.EvalSymlinks("/etc/hosts")
	require.NoError(t, err)
	return []corpusCase{
		{cmd: `ls`, out: "notes.txt"},
		{cmd: `cat notes.txt | grep beta | wc -l`, out: "1"},
		{cmd: `ls *.txt`, out: "notes.txt"},
		{cmd: `command -v ls; builtin echo via-builtin`, out: "via-builtin"},
		{cmd: `x=rm; $x -rf nope`, blocked: "rm"},
		{cmd: `$(echo rm) -rf nope`, blocked: "rm"},
		{cmd: "`echo rm` nope", blocked: "rm"},
		{cmd: `eval "rm nope"`, blocked: "rm"},
		{cmd: `exec rm nope`, blocked: "rm"},
		{cmd: `command rm nope`, blocked: "rm"},
		{cmd: `rm nope 2>/dev/null; echo after-rm`, grant: &shellpolicy.Grant{Programs: []string{"rm"}}, out: "after-rm"},
		{cmd: `rm() { echo fake; }; rm nope`, out: "fake"},
		{cmd: `shopt -s expand_aliases; alias ll='rm'; ll x`, blocked: "shopt"},
		{cmd: `echo 'rm nope' > s.sh; source ./s.sh`, blocked: "source"},
		{cmd: `trap 'rm nope' EXIT; true`, blocked: "rm"},
		{cmd: `(rm nope) & wait`, blocked: "rm"},
		{cmd: `cat <(echo from-procsubst)`, out: "from-procsubst"},
		{cmd: `/bin/rm nope`, blocked: "/bin/rm"},
		{cmd: `./rm`, blocked: "./rm"},
		{cmd: `find . -exec rm {} \;`, blocked: "find"},
		{cmd: `X='-exec rm {} ;'; find . $X`, blocked: "find"},
		{cmd: `git -c core.pager=sh log`, blocked: "git"},
		{cmd: `echo x > /etc/jail-test`, blocked: "/etc/jail-test"},
		{cmd: `echo written > out.txt; cat out.txt`, out: "written"},
		{cmd: `cat < /etc/hosts`, blocked: "/etc/hosts"},
		{cmd: `cat /etc/hosts`, blocked: "cat"},
		{cmd: `cat /etc/hosts >/dev/null && echo read-ok`, grant: &shellpolicy.Grant{ReadPaths: []string{hosts}}, out: "read-ok"},
		{cmd: `cd /; ls`, blocked: "ls"},
		{cmd: `d=/; cd $d; ls`, blocked: "ls"},
		{cmd: `ls /etc/pass*`, blocked: "/etc"},
		{cmd: `env`, blocked: "env"},
		{cmd: `echo "key:${ANTHROPIC_API_KEY:-unset}"`, out: "key:unset"},
		{cmd: `sudo ls`, grant: &shellpolicy.Grant{Programs: []string{"sudo"}}, blocked: "sudo"},
		{cmd: `kill -9 $$`, blocked: "kill"},
		{cmd: `no-such-program-xyz`, grant: &shellpolicy.Grant{Programs: []string{"no-such-program-xyz"}}},
	}
}

func checkCorpus(t *testing.T, how string, tc corpusCase, got run) {
	t.Helper()
	if tc.blocked == "" {
		assert.Empty(t, got.res.Blocked, "%s %q: %s", how, tc.cmd, got.stderr)
	} else {
		assert.Contains(t, got.blockedNames(), tc.blocked, "%s %q: stderr %q", how, tc.cmd, got.stderr)
		assert.Contains(t, got.stderr, "jail: blocked", "%s %q", how, tc.cmd)
	}
	if tc.out != "" {
		assert.Contains(t, got.stdout, tc.out, "%s %q: stderr %q", how, tc.cmd, got.stderr)
	}
}

// The corpus through a real child, as shell_execute runs it.
func TestJail_Corpus(t *testing.T) {
	f := newFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-reach-the-jail")
	for _, tc := range corpus(t, f) {
		checkCorpus(t, "child", tc, f.jailed(t, tc.cmd, tc.grant, 10*time.Second))
	}
}

// The same corpus in this process, so the race detector and coverage see the
// handlers.
func TestJail_CorpusInProcess(t *testing.T) {
	f := newFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-reach-the-jail")
	// The real child runs with TMPDIR set to the scratchpad (Runner.Command),
	// which is where the interpreter makes process-substitution FIFOs.
	t.Setenv("TMPDIR", f.scratch)
	for _, tc := range corpus(t, f) {
		checkCorpus(t, "in-process", tc, f.inProcess(t, tc.cmd, tc.grant))
	}
}

func TestJail_ProgramsByPathAndSignals(t *testing.T) {
	f := newFixture(t)
	script := filepath.Join(f.scratch, "hello.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho hello-from-script\n"), 0o700)) // #nosec G306 -- an executable test script
	got := f.inProcess(t, `./hello.sh`, &shellpolicy.Grant{Programs: []string{"./hello.sh"}})
	assert.Contains(t, got.stdout, "hello-from-script", got.stderr)

	got = f.inProcess(t, `/bin/sh -c 'kill -9 $$'`, &shellpolicy.Grant{Programs: []string{"/bin/sh"}})
	assert.Equal(t, 137, got.code, "a program ended by a signal reports 128+signal")

	got = f.inProcess(t, `no-such-program-xyz`, &shellpolicy.Grant{Programs: []string{"no-such-program-xyz"}})
	assert.Equal(t, 127, got.code)
	assert.Contains(t, got.stderr, "command not found")
}

func TestReporter(t *testing.T) {
	var out bytes.Buffer
	r := &reporter{out: &out}
	r.drop([]string{"A", "B"})
	r.drop([]string{"A"})
	for i := 0; i < 150; i++ {
		r.block(Blocked{Name: "x"})
	}
	r.limit("memory")
	r.finish() // a second write is a no-op
	var res Result
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	assert.Equal(t, "memory", res.Limit)
	assert.Equal(t, []string{"A", "B"}, res.EnvDropped)
	assert.Len(t, res.Blocked, 100, "blocked entries are capped")
	assert.Equal(t, 1, strings.Count(out.String(), "\n"), "written exactly once")
}

func TestOptionsFunc(t *testing.T) {
	f := newFixture(t)
	opt, err := f.r.OptionsFunc()(shuttle.AdmissionRequest{SessionID: testSession, Params: map[string]interface{}{"working_dir": "sub"}})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(f.scratch, "sub"), opt.WorkingDir)
	_, err = f.r.OptionsFunc()(shuttle.AdmissionRequest{})
	assert.Error(t, err)
}

func TestJail_StrictAndExitStatus(t *testing.T) {
	f := newFixture(t)
	got := f.jailed(t, `echo "${NOPE}/x"; echo reached`, nil, 10*time.Second)
	assert.Equal(t, 1, got.code)
	assert.Contains(t, got.stderr, "NOPE: unbound variable")
	assert.NotContains(t, got.stdout, "reached")

	got = f.jailed(t, `exit 7`, nil, 10*time.Second)
	assert.Equal(t, 7, got.code)

	got = f.jailed(t, `rm nope`, nil, 10*time.Second)
	assert.Equal(t, 126, got.code, "a blocked launch is exit 126")
	assert.Contains(t, got.stderr, "Write the command out literally so it can be approved.")

	got = f.jailed(t, `coproc cat`, nil, 10*time.Second)
	assert.NotEqual(t, 0, got.code)
}

func TestJail_EnvIsBuiltFromScratch(t *testing.T) {
	f := newFixture(t)
	t.Setenv("LOOMTEST_SERVER_ONLY", "server-value")
	got := f.jailed(t, `export LD_PRELOAD=/x.so GIT_PAGER=less KEEP_ME=yes; ls >/dev/null; echo "$HOME|$PATH|${LOOMTEST_SERVER_ONLY:-absent}"`, nil, 10*time.Second)
	require.Empty(t, got.res.Blocked, got.stderr)
	assert.Contains(t, got.stdout, f.scratch+"|"+strings.Join(DefaultSearchPath, ":")+"|absent")
	assert.Equal(t, []string{"GIT_PAGER", "LD_PRELOAD"}, got.res.EnvDropped)
}

// Repository config cannot make git run anything (design Probe 9).
func TestJail_GitRunsNothingFromRepoConfig(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	f := newFixture(t)
	if _, ok := f.r.Pinned()["git"]; !ok {
		t.Skip("this git does not accept --attr-source")
	}
	repo := filepath.Join(f.scratch, "repo")
	marks := filepath.Join(f.scratch, "marks")
	require.NoError(t, os.MkdirAll(marks, 0o700))
	sh := func(args ...string) {
		c := exec.Command(git, args...) // #nosec G204 -- test setup
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := c.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	require.NoError(t, os.MkdirAll(repo, 0o700))
	sh("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "g.dat"), []byte("b\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=evil\n*.dat diff=evil\n"), 0o600))
	sh("add", ".")
	sh("-c", "user.email=x@y", "-c", "user.name=x", "commit", "-qm", "init")
	for key, mark := range map[string]string{
		"core.fsmonitor": "fsmonitor", "core.pager": "pager", "diff.external": "extdiff",
		"filter.evil.clean": "clean", "diff.evil.textconv": "textconv",
	} {
		sh("config", key, "touch "+filepath.Join(marks, mark)+"; cat")
	}
	time.Sleep(1100 * time.Millisecond) // make the index stat-dirty, so git re-reads the files
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a2\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "g.dat"), []byte("b2\n"), 0o600))

	got := f.jailed(t, `cd repo && git status && git diff && git log -p -1 && git show HEAD >/dev/null && echo git-ok`, nil, 30*time.Second)
	assert.Contains(t, got.stdout, "git-ok", got.stderr)
	entries, err := os.ReadDir(marks)
	require.NoError(t, err)
	var ran []string
	for _, e := range entries {
		ran = append(ran, e.Name())
	}
	assert.Empty(t, ran, "repository config ran programs under the jail")
}

// sleepers returns the PIDs of sleep processes whose argument is arg.
func sleepers(t *testing.T, arg string) []int {
	t.Helper()
	out, _ := exec.Command("pgrep", "-f", "sleep "+arg).Output() // #nosec G204 -- test helper
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func TestJail_TimeoutKillsEverything(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not installed")
	}
	f := newFixture(t)
	arg := strconv.Itoa(1000+os.Getpid()%1000) + ".5" // unique to this run
	t.Cleanup(func() {
		for _, pid := range sleepers(t, arg) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	got := f.jailed(t, "sleep "+arg+" & sleep "+arg, nil, 2*time.Second)
	assert.Less(t, got.took, 6*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(sleepers(t, arg)) > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	assert.Empty(t, sleepers(t, arg), "a sleep the command started survived the timeout")
}

// The interpreter's own memory bomb is stopped in the child, never in the
// server (design F3).
func TestJail_MemoryBombIsStopped(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Limits.MemoryBytes = 192 << 20 })
	got := f.jailed(t, `x=a; while :; do x=$x$x; done`, nil, 30*time.Second)
	assert.NotEqual(t, 0, got.code)
	assert.Less(t, got.took, 20*time.Second)
	stopped := got.res.Limit == "memory" || strings.Contains(got.stderr, "out of memory")
	assert.True(t, stopped, "limit %q, exit %d, stderr %q", got.res.Limit, got.code, truncate(got.stderr, 300))
}

func TestJail_InProcessHandlersUnderRace(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.scratch, "a.txt"), []byte("a\nb\na\n"), 0o600))
	got := f.inProcess(t, `for i in 1 2 3; do cat a.txt | sort | uniq -c | wc -l; done; rm x & rm y & wait; ls *.txt`, nil)
	assert.Contains(t, got.stdout, "a.txt")
	assert.Equal(t, 3, strings.Count(got.stdout, "2\n"), got.stdout)
	assert.ElementsMatch(t, []string{"rm", "rm"}, got.blockedNames())
}

// For a command whose every word is literal, the static check and the runner
// must agree: what the hook allows, the jail runs without a block, and what
// the jail blocks, the hook did not allow (design 06 §11).
func TestPolicyAndJailAgreeOnLiteralCommands(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.scratch, "n.txt"), []byte("x\n"), 0o600))
	pol := f.r.Policy()
	for _, cmd := range []string{
		"ls", "ls -la", "cat n.txt", "cat /etc/hosts", "rm n.txt", "find . -name n.txt", "find . -delete",
		"sort -o out n.txt", "uniq n.txt", "uniq n.txt out", "git -c a=b status", "env", "/bin/ls",
		"echo hi > out.txt", "echo hi > /etc/x", "cat < /etc/hosts", "date +%s", "date 202601010000",
		"sudo ls", "wc -l n.txt | sort", "echo a; rm b; ls", "base64 -o x n.txt", "file -C n.txt",
		"grep x n.txt", "head -1 n.txt && tail -1 n.txt", "sleep 0", "which ls", "true || rm z",
	} {
		v := pol.Analyze(cmd, f.opt)
		got := f.inProcess(t, cmd, nil)
		if v.Kind == shellpolicy.Allow {
			assert.Empty(t, got.res.Blocked, "%q: the hook allowed it but the jail blocked %v", cmd, got.blockedNames())
		}
		if len(got.res.Blocked) > 0 {
			assert.NotEqual(t, shellpolicy.Allow, v.Kind, "%q: the jail blocked it but the hook allowed it", cmd)
		}
	}
}

func TestNewRunner_Validation(t *testing.T) {
	_, _, err := NewRunner(Config{LoomDataDir: "/x"})
	assert.ErrorContains(t, err, "needs a policy")
	_, _, err = NewRunner(Config{Policy: shellpolicy.Readonly()})
	assert.ErrorContains(t, err, "data directory")
	_, _, err = NewRunner(Config{Policy: shellpolicy.Readonly(), LoomDataDir: "/x", SearchPath: []string{"bin"}})
	assert.ErrorContains(t, err, "not absolute")

	r, warnings, err := NewRunner(Config{Policy: shellpolicy.Readonly().Extend("p", []string{"no-such-program-xyz"}, nil), LoomDataDir: "/x"})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(warnings, "\n"), "no-such-program-xyz: not found")
	assert.NotContains(t, r.Pinned(), "no-such-program-xyz")
	for name, path := range r.Pinned() {
		assert.True(t, filepath.IsAbs(path), name)
	}
}

func TestOptionsAndBaseEnv(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.ExtraRead = []string{"/usr/share"} })
	_, err := f.r.Options("", "")
	assert.ErrorContains(t, err, "inside a session")
	opt, err := f.r.Options(testSession, "sub")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(f.scratch, "sub"), opt.WorkingDir)
	assert.Contains(t, opt.Roots.Read, shellpolicy.ResolvePath("/", "/usr/share"))
	assert.Subset(t, opt.Roots.Read, opt.Roots.Write, "every write root is readable")

	env, dropped := f.r.BaseEnv("/home/x", map[string]string{"SESSION_ID": "s"}, map[string]string{
		"FOO": "1", "GH_TOKEN": "t", "PATH": "/evil", "LD_PRELOAD": "x", "DB_URL": "postgres://u:p@h/d",
	})
	assert.Contains(t, env, "HOME=/home/x")
	assert.Contains(t, env, "SESSION_ID=s")
	assert.Contains(t, env, "FOO=1")
	assert.NotContains(t, strings.Join(env, "\n"), "/evil")
	assert.Equal(t, []string{"DB_URL", "GH_TOKEN", "LD_PRELOAD"}, dropped)
}

func TestFilterEnvAndNames(t *testing.T) {
	kept, dropped := FilterEnv([]string{"A=1", "LD_LIBRARY_PATH=/x", "GIT_DIR=/y", "HF_TOKEN=z", "PYTHONPATH=/p", "BASH_FUNC_f%%=() { :; }", "A=2"})
	assert.Equal(t, []string{"A=1", "A=2"}, kept)
	assert.Equal(t, []string{"BASH_FUNC_f%%", "GIT_DIR", "HF_TOKEN", "LD_LIBRARY_PATH", "PYTHONPATH"}, dropped)
	assert.True(t, SensitiveEnvName("aws_secret_access_key"))
	assert.False(t, SensitiveEnvName("TOKENIZERS_PARALLELISM"))
	assert.True(t, URLHasPassword("https://u:p@h"))
	assert.False(t, URLHasPassword("https://u@h"))
}

func TestLookPath(t *testing.T) {
	assert.NotEmpty(t, LookPath("ls", DefaultSearchPath))
	assert.Empty(t, LookPath("ls", []string{"relative/bin"}))
	assert.Empty(t, LookPath("../ls", DefaultSearchPath))
	assert.Empty(t, LookPath("", DefaultSearchPath))
}

func TestWatchdog(t *testing.T) {
	fired := make(chan struct{})
	stop := startWatchdog(1, time.Millisecond, func() { close(fired) })
	defer stop()
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("the watchdog never fired over a 1-byte limit")
	}
	stop()
	noop := startWatchdog(0, time.Millisecond, func() { t.Error("a zero limit must never fire") })
	noop()
}

func TestChildRejectsABadSpec(t *testing.T) {
	var stdout, stderr, result bytes.Buffer
	code := runChild(strings.NewReader("{not json"), &stdout, &stderr, &result)
	assert.Equal(t, 125, code)
	var res Result
	require.NoError(t, json.Unmarshal(result.Bytes(), &res))
	assert.Contains(t, res.Error, "cannot decode")
}
