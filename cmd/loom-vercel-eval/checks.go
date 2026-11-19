package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
)

// Check kinds. Every task's pass condition is a list of these, so the
// checker reads the same way for every task and the spec stays data.
const (
	checkContains       = "contains"       // final answer contains Value
	checkNotContains    = "not_contains"   // final answer does not contain Value
	checkRegex          = "regex"          // final answer matches Value
	checkFileUnchanged  = "file_unchanged" // Path is byte-identical to the fixture
	checkFileContains   = "file_contains"  // Path contains Value
	checkFileNotContain = "file_not_contains"
	checkCmdExit0       = "cmd_exit0"       // shell Value exits 0 in the workspace
	checkToolCalled     = "tool_called"     // a tool named Value was executed
	checkToolNotCalled  = "tool_not_called" // no tool named Value was executed
)

// check is one pass condition. Fold makes text comparisons case-insensitive.
type check struct {
	Kind  string
	Value string
	Path  string
	Fold  bool
}

// checkResult is the recorded outcome of one check.
type checkResult struct {
	Kind     string `json:"kind"`
	Value    string `json:"value,omitempty"`
	Path     string `json:"path,omitempty"`
	Pass     bool   `json:"pass"`
	Deferred bool   `json:"deferred,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// evaluateChecks runs every check of t against the run. Deterministic checks
// only: no model is consulted here.
func evaluateChecks(runDir string, t *task, rec *runRecord) []checkResult {
	ws := rec.Workspace
	orig := filepath.Join(runDir, "fixture")
	final := ""
	if n := len(rec.Turns); n > 0 {
		final = rec.Turns[n-1].Assistant
	}

	out := make([]checkResult, 0, len(t.Checks))
	for _, c := range t.Checks {
		r := checkResult{Kind: c.Kind, Value: c.Value, Path: c.Path}
		if isDeferred(c.Kind) {
			// Scored by the judge pass after all runs. Excluded from Pass here.
			r.Deferred = true
			r.Detail = "deferred to judge pass"
			out = append(out, r)
			continue
		}
		r.Pass, r.Detail = evalOne(c, ws, orig, final, rec)
		out = append(out, r)
	}
	return out
}

func evalOne(c check, ws, orig, final string, rec *runRecord) (bool, string) {
	text := final
	needle := c.Value
	if c.Fold {
		text = strings.ToLower(text)
		needle = strings.ToLower(needle)
	}
	switch c.Kind {
	case checkContains:
		return strings.Contains(text, needle), ""
	case checkNotContains:
		return !strings.Contains(text, needle), ""
	case checkRegex:
		re, err := regexp.Compile(c.Value)
		if err != nil {
			return false, "bad regex: " + err.Error()
		}
		return re.MatchString(final), ""
	case checkFileUnchanged:
		a, errA := hashFile(filepath.Join(ws, c.Path))
		b, errB := hashFile(filepath.Join(orig, c.Path))
		if errA != nil || errB != nil {
			return false, fmt.Sprintf("read: %v / %v", errA, errB)
		}
		return a == b, ""
	case checkFileContains, checkFileNotContain:
		// #nosec G304 -- path under the results directory the operator passed with -out, or a fixture the harness wrote; never request input
		data, err := os.ReadFile(filepath.Join(ws, c.Path))
		if err != nil {
			return false, "read: " + err.Error()
		}
		has := strings.Contains(string(data), c.Value)
		if c.Kind == checkFileContains {
			return has, ""
		}
		return !has, ""
	case checkCmdExit0:
		return runShell(ws, c.Value)
	case checkToolCalled:
		return hasTool(rec, c.Value), ""
	case checkToolNotCalled:
		return !hasTool(rec, c.Value), ""
	}
	return evalExtra(c, ws, final, rec)
}

func hasTool(rec *runRecord, name string) bool {
	for _, tc := range rec.ToolCalls {
		if tc.Name == name {
			return true
		}
	}
	return false
}

func hashFile(p string) (string, error) {
	// #nosec G304 -- path under the results directory the operator passed with -out, or a fixture the harness wrote; never request input
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// runShell runs a checker command in the workspace with a timeout. It is
// the harness checking the result, not the agent, so it is not subject to
// the agent's shell policy.
func runShell(dir, script string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// #nosec G204 -- the checker script comes from the compiled-in task table (checks.go), not from model or user input
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Run(); err != nil {
		tail := stderr.String()
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		return false, fmt.Sprintf("exit: %v: %s", err, tail)
	}
	return true, ""
}

// rateLimiterFor enables loom's per-provider limiter, as the builder does.
func rateLimiterFor() llm.RateLimiterConfig {
	return llm.RateLimiterConfig{Enabled: true}
}
