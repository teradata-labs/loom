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

package shelljail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/shellpolicy"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// DefaultSearchPath is where allowlisted programs are looked up.
var DefaultSearchPath = []string{"/usr/local/bin", "/usr/bin", "/bin"}

// Default limits for one jailed command.
const (
	DefaultMemoryBytes = 512 << 20
	DefaultFileBytes   = 256 << 20
)

// selfTestTimeout bounds the startup check that the host binary runs as a jail.
const selfTestTimeout = 5 * time.Second

// Config configures a Runner.
type Config struct {
	// Policy is the policy every jailed command runs under. A command-policy
	// binding on the same tool must name it.
	Policy *shellpolicy.Policy
	// SearchPath lists the directories programs are looked up in. Empty
	// means DefaultSearchPath.
	SearchPath []string
	// Limits for each command. Zero MemoryBytes or FileBytes means the
	// default.
	Limits Limits
	// Strict runs every command with set -u (design decision D10).
	Strict bool
	// LoomDataDir is the data directory sessions live in; read access is
	// confined to it, /tmp and ExtraRead.
	LoomDataDir string
	// ExtraRead and ExtraWrite add roots for every session.
	ExtraRead, ExtraWrite []string
	// Executable is the host binary to run as the jail; empty means
	// os.Executable().
	Executable string
}

// Runner starts jailed commands. It is safe for concurrent use.
type Runner struct {
	exe        string
	policy     *shellpolicy.Policy
	search     []string
	pinned     map[string]string
	limits     Limits
	strict     bool
	dataDir    string
	extraRead  []string
	extraWrite []string
}

// NewRunner validates cfg, pins every allowlisted program to the absolute path
// it resolves to now, and checks that git accepts the hardening flags (git is
// left unpinned, so calls to it need approval, when it does not). It returns
// warnings for programs it left out. It does not run the self-test.
func NewRunner(cfg Config) (*Runner, []string, error) {
	if !supported {
		return nil, nil, fmt.Errorf("jailed shell mode is not supported on %s", runtime.GOOS)
	}
	if cfg.Policy == nil {
		return nil, nil, errors.New("jailed shell needs a policy")
	}
	if err := cfg.Policy.Validate(); err != nil {
		return nil, nil, err
	}
	exe := cfg.Executable
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return nil, nil, fmt.Errorf("cannot find the host binary to run as the jail: %w", err)
		}
	}
	search := cfg.SearchPath
	if len(search) == 0 {
		search = DefaultSearchPath
	}
	for _, d := range search {
		if !filepath.IsAbs(d) {
			return nil, nil, fmt.Errorf("search path entry %q is not absolute", d)
		}
	}
	if cfg.LoomDataDir == "" {
		return nil, nil, errors.New("jailed shell needs the loom data directory")
	}
	r := &Runner{
		exe:        exe,
		policy:     cfg.Policy,
		search:     append([]string{}, search...),
		pinned:     map[string]string{},
		limits:     cfg.Limits,
		strict:     cfg.Strict,
		dataDir:    shellpolicy.ResolvePath("/", cfg.LoomDataDir),
		extraRead:  resolveAll(cfg.ExtraRead),
		extraWrite: resolveAll(cfg.ExtraWrite),
	}
	if r.limits.MemoryBytes == 0 {
		r.limits.MemoryBytes = DefaultMemoryBytes
	}
	if r.limits.FileBytes == 0 {
		r.limits.FileBytes = DefaultFileBytes
	}
	var warnings []string
	for _, name := range cfg.Policy.ProgramNames() {
		p := LookPath(name, r.search)
		if p == "" {
			warnings = append(warnings, fmt.Sprintf("%s: not found on the search path; calls to it fail with exit 127", name))
			continue
		}
		r.pinned[name] = p
	}
	if git, ok := r.pinned["git"]; ok && !gitHasAttrSource(git) {
		delete(r.pinned, "git")
		warnings = append(warnings, "git: does not accept --attr-source, so its hardening cannot be applied; git now needs approval")
	}
	sort.Strings(warnings)
	return r, warnings, nil
}

func resolveAll(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) != "" {
			out = append(out, shellpolicy.ResolvePath("/", p))
		}
	}
	return out
}

// gitHasAttrSource reports whether git accepts --attr-source, the flag that
// stops repository filters and textconv drivers (design 06, Probe 9).
func gitHasAttrSource(git string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "--attr-source="+shellpolicy.EmptyTreeHash, "version") // #nosec G204 -- the pinned git, fixed flags
	cmd.Env = []string{"PATH=" + strings.Join(DefaultSearchPath, ":"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	return cmd.Run() == nil
}

// Policy returns the policy jailed commands run under.
func (r *Runner) Policy() *shellpolicy.Policy { return r.policy }

// Pinned returns a copy of the pinned program table.
func (r *Runner) Pinned() map[string]string {
	out := make(map[string]string, len(r.pinned))
	for k, v := range r.pinned {
		out[k] = v
	}
	return out
}

// SessionDirs returns the session's scratchpad and artifact directories,
// creating them. The scratchpad is a jailed command's default working
// directory and HOME.
func (r *Runner) SessionDirs(sessionID string) (scratch, artifactDir string, err error) {
	if sessionID == "" {
		return "", "", errors.New("the jailed shell runs only inside a session")
	}
	if scratch, err = artifacts.GetScratchpadDir(sessionID); err != nil {
		return "", "", err
	}
	if artifactDir, err = artifacts.GetArtifactDir(sessionID, artifacts.SourceAgent); err != nil {
		return "", "", err
	}
	for _, d := range []string{scratch, artifactDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return "", "", err
		}
	}
	return shellpolicy.ResolvePath("/", scratch), shellpolicy.ResolvePath("/", artifactDir), nil
}

// Options returns the working directory and roots for a call: the call's
// working_dir (relative paths resolve against the session scratchpad), or the
// scratchpad when none is given. Reads are confined to the loom data
// directory, /tmp and the configured extra roots; writes to the session's
// scratchpad and artifact directories and the extra write roots. The hook and
// the tool both use it, so they judge the same paths.
func (r *Runner) Options(sessionID, workingDir string) (shellpolicy.Options, error) {
	scratch, artifactDir, err := r.SessionDirs(sessionID)
	if err != nil {
		return shellpolicy.Options{}, err
	}
	dir := scratch
	if workingDir != "" {
		dir = shellpolicy.ResolvePath(scratch, workingDir)
	}
	write := append([]string{scratch, artifactDir}, r.extraWrite...)
	read := append([]string{r.dataDir}, r.extraRead...)
	if runtime.GOOS != "windows" {
		read = append(read, shellpolicy.ResolvePath("/", "/tmp"))
	}
	read = append(read, write...)
	return shellpolicy.Options{WorkingDir: dir, Roots: shellpolicy.Roots{Read: read, Write: write}}, nil
}

// OptionsFunc adapts Options for the command-policy hook.
func (r *Runner) OptionsFunc() shellpolicy.OptionsFunc {
	return func(req shuttle.AdmissionRequest) (shellpolicy.Options, error) {
		wd, _ := req.Params["working_dir"].(string)
		return r.Options(req.SessionID, wd)
	}
}

// BaseEnv is a jailed command's starting environment: fixed entries, the
// session's variables, and the call's own variables after FilterEnv. It
// returns the names FilterEnv dropped.
func (r *Runner) BaseEnv(home string, session map[string]string, callEnv map[string]string) ([]string, []string) {
	env := []string{
		"PATH=" + strings.Join(r.search, string(os.PathListSeparator)),
		"HOME=" + home,
		"TERM=dumb",
		"NO_COLOR=1",
	}
	for _, name := range []string{"LANG", "LC_ALL", "TZ"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	keys := make([]string, 0, len(session))
	for k := range session {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+session[k])
	}
	callKeys := make([]string, 0, len(callEnv))
	for k := range callEnv {
		callKeys = append(callKeys, k)
	}
	sort.Strings(callKeys)
	var entries []string
	for _, k := range callKeys {
		if k == "PATH" || k == "HOME" {
			continue
		}
		entries = append(entries, k+"="+callEnv[k])
	}
	kept, dropped := FilterEnv(entries)
	return append(env, kept...), dropped
}

// Spec assembles a command's spec.
func (r *Runner) Spec(command string, opt shellpolicy.Options, grant *shellpolicy.Grant, env []string, timeout time.Duration) Spec {
	limits := r.limits
	if timeout > 0 && limits.CPUSeconds == 0 {
		limits.CPUSeconds = uint64(timeout/time.Second) + 5 // #nosec G115 -- a positive timeout
	}
	return Spec{
		Command:    command,
		Dir:        opt.WorkingDir,
		Roots:      opt.Roots,
		Policy:     r.policy.FlatSpec(),
		Grant:      grant,
		Env:        env,
		Pinned:     r.Pinned(),
		SearchPath: append([]string{}, r.search...),
		Limits:     limits,
		Strict:     r.strict,
	}
}

// Child is a jail child process, prepared but not started.
type Child struct {
	Cmd *exec.Cmd
	res *os.File
	wr  *os.File
}

// Command prepares the child for spec. The caller attaches stdout and stderr
// (directly or with StdoutPipe/StderrPipe), calls Start, then Started, waits,
// and calls Result. The child leads its own process group, so the caller can
// kill everything it started at once.
func (r *Runner) Command(spec Spec) (*Child, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSpecBytes {
		return nil, fmt.Errorf("jail spec too large (%d bytes)", len(raw))
	}
	rd, wr, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(r.exe) // #nosec G204 -- the host's own binary, run as its jail
	cmd.Env = []string{
		MarkerEnv + "=" + markerValue,
		"PATH=" + strings.Join(r.search, string(os.PathListSeparator)),
		"HOME=" + spec.Dir,
		"TMPDIR=" + spec.Dir,
	}
	cmd.Dir = spec.Dir
	cmd.Stdin = bytes.NewReader(raw)
	cmd.ExtraFiles = []*os.File{wr}
	newProcessGroup(cmd)
	return &Child{Cmd: cmd, res: rd, wr: wr}, nil
}

// Started closes the host's copy of the result pipe's write end; call it
// right after a successful Start (or a failed one).
func (c *Child) Started() {
	_ = c.wr.Close()
}

// Result reads what the child reported, after it exited. It never blocks
// past a short deadline: a child killed before it reported yields an empty
// Result.
func (c *Child) Result() Result {
	defer func() { _ = c.res.Close() }()
	_ = c.res.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, err := io.ReadAll(io.LimitReader(c.res, 1<<20))
	var res Result
	if err != nil && len(raw) == 0 {
		return res
	}
	_ = json.Unmarshal(raw, &res)
	return res
}

// SelfTest runs "echo" through the jail and requires its answer. A host that
// forgot to call Main runs its normal main instead and never answers, which
// is what this catches.
func (r *Runner) SelfTest(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "loom-shelljail-selftest-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	dir = shellpolicy.ResolvePath("/", dir)
	opt := shellpolicy.Options{WorkingDir: dir, Roots: shellpolicy.Roots{Read: []string{dir}, Write: []string{dir}}}
	env, _ := r.BaseEnv(dir, nil, nil)
	child, err := r.Command(r.Spec("echo shelljail-ok", opt, nil, env, selfTestTimeout))
	if err != nil {
		return err
	}
	var out, errOut bytes.Buffer
	child.Cmd.Stdout = &out
	child.Cmd.Stderr = &errOut
	ctx, cancel := context.WithTimeout(ctx, selfTestTimeout)
	defer cancel()
	if err := child.Cmd.Start(); err != nil {
		child.Started()
		return fmt.Errorf("jail self-test could not start %s: %w", r.exe, err)
	}
	child.Started()
	done := make(chan error, 1)
	go func() { done <- child.Cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		KillGroup(child.Cmd)
		<-done
		return fmt.Errorf("jail self-test: %s did not answer within %s; the binary must call shelljail.Main() first thing in main()", r.exe, selfTestTimeout)
	}
	res := child.Result()
	if err != nil || strings.TrimSpace(out.String()) != "shelljail-ok" {
		return fmt.Errorf("jail self-test failed (err=%v, stdout %q, stderr %q, jail error %q); the binary must call shelljail.Main() first thing in main()",
			err, truncate(out.String(), 200), truncate(errOut.String(), 200), res.Error)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
