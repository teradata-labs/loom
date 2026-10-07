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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/metrics"
	"strings"
	"sync"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/teradata-labs/loom/pkg/shellpolicy"
)

// Main runs the jail when this process was started as one (MarkerEnv set) and
// exits; otherwise it returns at once. Every host binary that runs
// shell_execute in jailed mode calls it first thing in main(), before any
// other initialization.
func Main() {
	if os.Getenv(MarkerEnv) != markerValue {
		return
	}
	os.Exit(runChild(os.Stdin, os.Stdout, os.Stderr, resultFile()))
}

// runChild is the whole life of a jail child; it returns the exit status.
func runChild(in io.Reader, stdout, stderr io.Writer, resultOut io.Writer) int {
	rep := &reporter{out: resultOut}
	raw, err := io.ReadAll(io.LimitReader(in, maxSpecBytes+1))
	if err != nil || len(raw) > maxSpecBytes {
		rep.fail("cannot read the jail spec")
		return 125
	}
	var spec Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		rep.fail("cannot decode the jail spec: " + err.Error())
		return 125
	}
	if err := applyLimits(spec.Limits); err != nil {
		rep.fail("cannot apply limits: " + err.Error())
		return 125
	}
	stopWatchdog := startWatchdog(spec.Limits.MemoryBytes/3, 2*time.Millisecond, func() {
		rep.limit("memory")
		_, _ = fmt.Fprintln(stderr, "jail: the shell passed its memory limit and was stopped")
		os.Exit(137)
	})
	defer stopWatchdog()

	j := &jail{spec: spec, pol: shellpolicy.FromSpec(spec.Policy), rep: rep, stderr: stderr}
	code := j.run(context.Background(), stdout, stderr)
	rep.finish()
	return code
}

// reporter collects the Result and writes it exactly once.
type reporter struct {
	mu      sync.Mutex
	out     io.Writer
	res     Result
	dropped map[string]bool
	done    bool
}

func (r *reporter) block(b Blocked) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.res.Blocked) < 100 {
		r.res.Blocked = append(r.res.Blocked, b)
	}
}

func (r *reporter) drop(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dropped == nil {
		r.dropped = map[string]bool{}
	}
	for _, n := range names {
		if !r.dropped[n] {
			r.dropped[n] = true
			r.res.EnvDropped = append(r.res.EnvDropped, n)
		}
	}
}

func (r *reporter) fail(msg string) {
	r.mu.Lock()
	r.res.Error = msg
	r.mu.Unlock()
	r.finish()
}

func (r *reporter) limit(name string) {
	r.mu.Lock()
	r.res.Limit = name
	r.mu.Unlock()
	r.finish()
}

func (r *reporter) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done || r.out == nil {
		return
	}
	r.done = true
	_ = json.NewEncoder(r.out).Encode(r.res)
}

type jail struct {
	spec   Spec
	pol    *shellpolicy.Policy
	rep    *reporter
	stderr io.Writer
}

func (j *jail) run(ctx context.Context, stdout, stderr io.Writer) int {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(j.spec.Command), "")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "jail: the command does not parse as bash: %v\n", err)
		return 2
	}
	opts := []interp.RunnerOption{
		interp.Dir(j.spec.Dir),
		interp.Env(expand.ListEnviron(j.spec.Env...)),
		interp.StdIO(strings.NewReader(""), stdout, stderr),
		interp.CallHandler(j.call),
		interp.ExecHandlers(func(interp.ExecHandlerFunc) interp.ExecHandlerFunc { return j.exec }),
		interp.OpenHandler(j.open),
		interp.ReadDirHandler2(j.readDir),
	}
	if j.spec.Strict {
		opts = append(opts, interp.Params("-u"))
	}
	r, err := interp.New(opts...)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "jail: %v\n", err)
		return 125
	}
	err = r.Run(ctx, file)
	var status interp.ExitStatus
	switch {
	case err == nil:
		return 0
	case errors.As(err, &status):
		return int(status)
	default:
		_, _ = fmt.Fprintf(stderr, "jail: %v\n", err)
		return 1
	}
}

func position(ctx context.Context) string {
	p := interp.HandlerCtx(ctx).Pos
	if !p.IsValid() {
		return ""
	}
	return fmt.Sprintf("%d:%d", p.Line(), p.Col())
}

// refuse records a refusal and prints it where the command's errors go.
func (j *jail) refuse(ctx context.Context, w io.Writer, name, reason string) {
	pos := position(ctx)
	j.rep.block(Blocked{Name: name, Reason: reason, Pos: pos})
	at := ""
	if pos != "" {
		at = " (" + pos + ")"
	}
	hint := ""
	if strings.HasPrefix(reason, "not on the shell allowlist") {
		hint = " Write the command out literally so it can be approved."
	}
	_, _ = fmt.Fprintf(w, "jail: blocked %s%s: %s.%s\n", name, at, reason, hint)
}

// prefixBuiltins only route to the next word; the word they route to is what
// gets checked.
var prefixBuiltins = map[string]bool{"exec": true, "command": true, "builtin": true}

// call runs before every simple command, builtins included. A refused builtin
// is swapped for blockedMarker so exec fails just that command.
func (j *jail) call(ctx context.Context, args []string) ([]string, error) {
	i := 0
	for i < len(args)-1 && prefixBuiltins[args[i]] {
		if args[i] == "command" && (args[i+1] == "-v" || args[i+1] == "-V") {
			return args, nil
		}
		i++
	}
	name := args[i]
	if !interp.IsBuiltin(name) || prefixBuiltins[name] {
		return args, nil
	}
	if err := j.pol.CheckBuiltin(name, j.spec.Grant); err != nil {
		var le *shellpolicy.LaunchError
		reason := err.Error()
		if errors.As(err, &le) {
			reason = le.Reason
		}
		return []string{blockedMarker, name, reason}, nil
	}
	return args, nil
}

// exec runs before every program launch, with the expanded argv.
func (j *jail) exec(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	if args[0] == blockedMarker && len(args) == 3 {
		j.refuse(ctx, hc.Stderr, args[1], args[2])
		return interp.ExitStatus(126)
	}
	if err := j.pol.CheckLaunch(args, hc.Dir, j.spec.Roots, j.spec.Grant); err != nil {
		var le *shellpolicy.LaunchError
		if errors.As(err, &le) {
			j.refuse(ctx, hc.Stderr, le.Name, le.Reason)
		} else {
			j.refuse(ctx, hc.Stderr, args[0], err.Error())
		}
		return interp.ExitStatus(126)
	}
	path := j.locate(args[0], hc.Dir)
	if path == "" {
		_, _ = fmt.Fprintf(hc.Stderr, "jail: %s: command not found\n", args[0])
		return interp.ExitStatus(127)
	}
	argv := args
	var extraEnv []string
	if prof := j.pol.Programs[args[0]]; prof != nil && !strings.ContainsRune(args[0], '/') {
		if prof.Harden != nil {
			argv = append([]string{args[0]}, prof.Harden(args[1:])...)
		}
		extraEnv = prof.Env
	}
	cmd := &exec.Cmd{
		Path:   path,
		Args:   argv,
		Env:    j.programEnv(hc.Env, extraEnv),
		Dir:    hc.Dir,
		Stdin:  hc.Stdin,
		Stdout: hc.Stdout,
		Stderr: hc.Stderr,
	}
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(hc.Stderr, "jail: %s: %v\n", args[0], err)
		return interp.ExitStatus(126)
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exitErr):
		code := exitErr.ExitCode()
		if code < 0 {
			code = 128 + signalNumber(exitErr)
		}
		return interp.ExitStatus(uint8(code)) // #nosec G115 -- exit statuses are 0-255
	default:
		_, _ = fmt.Fprintf(hc.Stderr, "jail: %s: %v\n", args[0], err)
		return interp.ExitStatus(126)
	}
}

// locate returns the file to execute for name: the pinned path of an
// allowlisted name, a granted name looked up on the search path, or a path as
// written (relative to dir). "" means not found.
func (j *jail) locate(name, dir string) string {
	if strings.ContainsRune(name, '/') {
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		return name
	}
	if p, ok := j.spec.Pinned[name]; ok {
		return p
	}
	return LookPath(name, j.spec.SearchPath)
}

// programEnv builds a program's environment from the interpreter's exported
// variables: filtered (FilterEnv), PATH forced to the search path, then the
// profile's own entries.
func (j *jail) programEnv(env expand.Environ, extra []string) []string {
	var entries []string
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.Kind == expand.String && name != "PATH" {
			entries = append(entries, name+"="+vr.Str)
		}
		return true
	})
	kept, dropped := FilterEnv(entries)
	j.rep.drop(dropped)
	kept = append(kept, "PATH="+strings.Join(j.spec.SearchPath, string(os.PathListSeparator)))
	return append(kept, extra...)
}

// open runs before every file the shell itself opens (redirects, source).
func (j *jail) open(ctx context.Context, path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	hc := interp.HandlerCtx(ctx)
	write := flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0
	if err := j.pol.CheckOpen(path, hc.Dir, write, j.spec.Roots, j.spec.Grant); err != nil {
		var le *shellpolicy.LaunchError
		if errors.As(err, &le) {
			j.refuse(ctx, hc.Stderr, le.Name, le.Reason)
		}
		return nil, err
	}
	return interp.DefaultOpenHandler()(ctx, path, flag, perm)
}

// readDir runs before every directory a glob lists.
func (j *jail) readDir(ctx context.Context, path string) ([]os.DirEntry, error) {
	hc := interp.HandlerCtx(ctx)
	if !filepath.IsAbs(path) {
		path = filepath.Join(hc.Dir, path)
	}
	if err := j.pol.CheckListDir(path, j.spec.Roots, j.spec.Grant); err != nil {
		var le *shellpolicy.LaunchError
		if errors.As(err, &le) {
			j.refuse(ctx, hc.Stderr, le.Name, le.Reason)
		}
		return nil, err
	}
	return os.ReadDir(path)
}

// LookPath finds an executable regular file named name in dirs, in order,
// ignoring the process's own PATH. It returns "" when there is none.
func LookPath(name string, dirs []string) string {
	if name == "" || strings.ContainsRune(name, '/') {
		return ""
	}
	for _, d := range dirs {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// startWatchdog polls the heap and calls onExceed once when it passes limit.
// A zero limit disables it. runtime/metrics is read rather than
// runtime.ReadMemStats, which would stop the world every poll.
func startWatchdog(limit uint64, every time.Duration, onExceed func()) (stop func()) {
	if limit == 0 {
		return func() {}
	}
	done := make(chan struct{})
	samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				metrics.Read(samples)
				if samples[0].Value.Kind() == metrics.KindUint64 && samples[0].Value.Uint64() > limit {
					onExceed()
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
