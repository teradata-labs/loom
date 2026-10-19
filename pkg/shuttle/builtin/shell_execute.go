// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

const (
	// DefaultShellTimeout is a command's own wall clock (4:30). Sized to stay
	// inside the conversation-cache lifetime: a call that outlives the cache
	// costs a full context re-send at write price, far more than the call
	// itself saved. A batch gives every command this budget in parallel, so
	// the batch as a whole still lands inside it.
	DefaultShellTimeout = perCommandTimeoutSecs

	// batchDriverSlack is the head-room the driver needs on top of a command's
	// own budget to replay the finished sections. Only reached if a command
	// ignores its own timeout.
	batchDriverSlack = 20

	// MaxShellTimeout is the ceiling a caller may ask for. Reachable only by
	// passing timeout_seconds, which is not advertised: the tool names it in
	// the timed-out result, where it is the right answer for a step that
	// genuinely cannot be split.
	MaxShellTimeout = 600

	// DefaultMaxOutputBytes limits output size to prevent memory issues (1MB).
	DefaultMaxOutputBytes = 1024 * 1024
)

// ShellExecuteTool provides cross-platform shell command execution.
// Supports Unix (bash/sh) and Windows (PowerShell/cmd).
// With session-based path restrictions for security.
type ShellExecuteTool struct {
	baseDir        string   // Base directory for resolving relative paths
	placed         bool     // Built with a base directory: commands start there
	loomDataDir    string   // LOOM_DATA_DIR for boundary checking
	restrictWrites bool     // Enforce write restrictions (default: true)
	restrictReads  string   // Read restriction level: "session" or "all_sessions"
	also           []string // Folders beyond its own a placed tool may run in
}

// NewShellExecuteTool creates a new shell execution tool.
// A tool built with a baseDir starts its commands there: whoever builds the
// tool says where its agent works. Built with none, commands start in
// LOOM_SANDBOX_DIR and relative paths resolve against the current directory.
// Defaults: restrictWrites=true, restrictReads="session"
func NewShellExecuteTool(baseDir string) *ShellExecuteTool {
	placed := baseDir != ""
	if placed {
		// Absolute, so it is the same place wherever it is resolved from.
		if abs, err := filepath.Abs(baseDir); err == nil {
			baseDir = abs
		}
	} else {
		baseDir, _ = os.Getwd()
	}
	return &ShellExecuteTool{
		baseDir:        baseDir,
		placed:         placed,
		loomDataDir:    os.Getenv("LOOM_DATA_DIR"), // Will be set from config in agent initialization
		restrictWrites: true,                       // Default to restricted writes
		restrictReads:  "session",                  // Default to session-only reads
	}
}

// Allow lets a placed tool run commands in more folders than its own.
func (t *ShellExecuteTool) Allow(dirs ...string) {
	for _, d := range dirs {
		if abs, err := filepath.Abs(d); err == nil {
			t.also = append(t.also, abs)
		}
	}
}

// pathWithin reports whether path is dir itself or sits beneath it. A bare
// strings.HasPrefix accepts a sibling whose name merely starts the same way —
// /work/proj also admitting /work/proj-evil — so containment is tested on
// whole path segments.
func pathWithin(path, dir string) bool {
	if dir == "" || path == "" {
		return false
	}
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// SetLoomDataDir sets the LOOM_DATA_DIR for path validation.
// This is typically called after tool creation to configure it.
func (t *ShellExecuteTool) SetLoomDataDir(dir string) {
	t.loomDataDir = dir
}

// SetRestrictWrites enables or disables write restrictions.
func (t *ShellExecuteTool) SetRestrictWrites(restrict bool) {
	t.restrictWrites = restrict
}

// SetRestrictReads sets the read restriction level ("session" or "all_sessions").
func (t *ShellExecuteTool) SetRestrictReads(level string) {
	t.restrictReads = level
}

func (t *ShellExecuteTool) Name() string {
	return "shell_execute"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/shell_execute.yaml).
// This fallback is used only when prompts are not configured.
func (t *ShellExecuteTool) Description() string {
	return `Run shell commands. One call can run up to 5: pass "commands" (an array) and they run in PARALLEL, each in its own shell — cd and variables do NOT carry between them, so use working_dir, absolute paths, or chain with && inside a single command. The result is one text with a "== label ==" section per command, always in the order you listed them. A failing command is an "ERROR: exit N" section and the others still run.

Reading code is a batch. Locate, then read the regions you need, across several files, in one call:
  commands: [
    {"command": "grep -n 'def run_handlers\\|_pending_handler' lib/ansible/plugins/strategy/__init__.py", "label": "locate"},
    {"command": "sed -n '300,420p' lib/ansible/executor/play_iterator.py", "label": "play_iterator 300-420"},
    {"command": "sed -n '40,120p' lib/ansible/playbook/handler.py", "label": "handler 40-120"}
  ]
Read a region rather than a whole file unless the file is small; what you read stays in context while you work on it.

Each command's output is guaranteed a byte budget (default 64K, or its own max_output_bytes). A cut section says how many bytes were cut and how to narrow.

Independent work is a batch: several greps, git status and diff. Dependent steps chain with && inside one command: build && test.

Single command: "command". Working directory: "working_dir".`
}

func (t *ShellExecuteTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for shell command execution",
		map[string]*shuttle.JSONSchema{
			"command": shuttle.NewStringSchema("One shell command."),
			"commands": shuttle.NewArraySchema(
				fmt.Sprintf("Up to %d commands, run in parallel, one section per command in the order you list them. Items: string or {command, label?, max_output_bytes?}.", batchMaxCommands),
				shuttle.NewStringSchema("shell command"),
			),
			"working_dir": shuttle.NewStringSchema("Working directory."),
			"env":         shuttle.NewObjectSchema("Extra environment variables.", nil, nil),
			"shell":       shuttle.NewStringSchema("Shell.").WithEnum("default", "bash", "sh", "powershell", "cmd").WithDefault("default"),
			"max_output_bytes": shuttle.NewNumberSchema(
				"Output budget per command in bytes (default 65536).",
			).WithDefault(defaultCommandBudget),
		},
		nil,
	)
}

func (t *ShellExecuteTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Extract session ID from context for path restrictions
	sessionID := session.SessionIDFromContext(ctx)

	// Determine LOOM_DATA_DIR (from environment or config)
	loomDataDir := t.loomDataDir
	if loomDataDir == "" {
		loomDataDir = config.GetLoomDataDir()
	}

	// Output budget per command: the rendered guarantee, with a self-describing
	// footer when cut. Replaces the old head/tail elision.
	commandBudget := defaultCommandBudget
	if mob, ok := params["max_output_bytes"].(float64); ok && mob > 0 {
		commandBudget = int(mob)
	}

	// Batch mode: 'commands' → one driver program running them in parallel.
	batchCmds, batchMode, batchErr := parseBatchCommands(params, commandBudget)
	if batchMode && batchErr != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    batchErr.Error(),
				Suggestion: "commands: [\"grep -n handler lib/a.py\", {\"command\": \"sed -n '40,120p' lib/a.py\", \"label\": \"a.py 40-120\"}]",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
	if batchMode {
		params["command"] = buildBatchDriver(batchCmds)
		// Each command polices its own clock inside the driver. The call-level
		// timer is only a backstop for a command that ignores its own, so it
		// sits just above one command's budget — never at a multiple of it.
		if _, asked := params["timeout_seconds"]; !asked {
			params["timeout_seconds"] = float64(perCommandTimeoutSecs + batchDriverSlack)
		}
	}

	// Extract and validate command
	command, ok := params["command"].(string)
	if !ok || command == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "command is required",
				Suggestion: "Provide a shell command to execute (e.g., 'ls -la' or 'echo hello')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Determine working directory
	// Priority: 1) explicit working_dir param, 2) the turn's working
	// directory, 3) the directory the tool was built with,
	// 4) LOOM_SANDBOX_DIR (agent execution context).
	// LOOM_SANDBOX_DIR is one value for the process; where one agent's
	// commands start is that agent's own, so a placed tool does not take it.
	// A relative working_dir is beneath whichever of 2 and 3 applies.
	// Note: LOOM_SANDBOX_DIR defaults to LOOM_DATA_DIR (see config.GetLoomSandboxDir)
	baseDir := t.baseDir
	workingDir := config.GetLoomSandboxDir()
	if t.placed {
		workingDir = baseDir
	}
	if dir := shuttle.WorkDirFromContext(ctx); dir != "" {
		baseDir, workingDir = dir, dir
	}
	if wd, ok := params["working_dir"].(string); ok && wd != "" {
		workingDir = wd // Explicit override always wins
	}

	timeoutSeconds := DefaultShellTimeout
	if ts, ok := params["timeout_seconds"].(float64); ok {
		timeoutSeconds = int(ts)
		if timeoutSeconds < 1 {
			timeoutSeconds = 1
		}
		if timeoutSeconds > MaxShellTimeout {
			timeoutSeconds = MaxShellTimeout
		}
	}

	shellType := "default"
	if st, ok := params["shell"].(string); ok && st != "" {
		shellType = st
	}

	maxOutputBytes := int64(DefaultMaxOutputBytes)
	if mob, ok := params["max_output_bytes"].(float64); ok && mob > 0 {
		maxOutputBytes = int64(mob)
	}

	// Extract environment variables
	envVars := make(map[string]string)
	if env, ok := params["env"].(map[string]interface{}); ok {
		for k, v := range env {
			if vStr, ok := v.(string); ok {
				envVars[k] = vStr
			}
		}
	}

	// Validate and resolve working directory
	cleanWorkingDir, err := resolveWorkingDir(workingDir, baseDir)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_WORKDIR",
				Message:    fmt.Sprintf("Invalid working directory: %v", err),
				Suggestion: "Provide a valid, accessible directory path",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// TOKEN SIZE CHECK: Prevent commands with huge content from executing
	// This is critical for preventing output token exhaustion from large file writes
	if err := checkCommandTokenSize(command); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "COMMAND_TOO_LARGE",
				Message:    err.Error(),
				Suggestion: "Break the operation into smaller chunks. Instead of writing a 10MB file at once, create sections separately and append them.",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Security: Block execution in sensitive system directories
	if isBlockedWorkingDir(cleanWorkingDir) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "UNSAFE_PATH",
				Message:    fmt.Sprintf("Cannot execute commands in system directory: %s", cleanWorkingDir),
				Suggestion: "Execute commands in your project directory or user data directories",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Session-based path restrictions
	if loomDataDir != "" && sessionID != "" {
		// Ensure working directory is within LOOM_DATA_DIR or whitelisted directories
		absWorkingDir, _ := filepath.Abs(cleanWorkingDir)
		absLoomDataDir, _ := filepath.Abs(loomDataDir)

		// Check if path is within LOOM_DATA_DIR or a whitelisted directory.
		// A placed tool runs where it was placed, where the turn it serves
		// was placed — a loop's step works where its spec says — and in what
		// it was allowed besides: all of it its agent's own, which nothing
		// else in the process can widen or narrow. A tool built with no place
		// runs in LOOM_SANDBOX_DIR, the agent execution context, exactly as
		// this guard's Suggestion advertises.
		isAllowed := pathWithin(absWorkingDir, absLoomDataDir)
		if !isAllowed && t.placed {
			isAllowed = pathWithin(absWorkingDir, t.baseDir)
			if turn := shuttle.WorkDirFromContext(ctx); turn != "" {
				absTurn, _ := filepath.Abs(turn)
				isAllowed = isAllowed || pathWithin(absWorkingDir, absTurn)
			}
			for _, d := range t.also {
				isAllowed = isAllowed || pathWithin(absWorkingDir, d)
			}
		}
		if !isAllowed && !t.placed {
			absSandboxDir, _ := filepath.Abs(config.GetLoomSandboxDir())
			if pathWithin(absWorkingDir, absSandboxDir) {
				isAllowed = true
			}
			// The temp directory is open to an unplaced tool (common for agent
			// workflows); a placed tool's folders are only the ones it was given.
			if runtime.GOOS != "windows" && pathWithin(absWorkingDir, "/tmp") {
				isAllowed = true
			}
			if runtime.GOOS == "windows" && os.Getenv("TEMP") != "" {
				absTempDir, _ := filepath.Abs(os.Getenv("TEMP"))
				if pathWithin(absWorkingDir, absTempDir) {
					isAllowed = true
				}
			}
		}

		if !isAllowed {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:       "PATH_RESTRICTED",
					Message:    fmt.Sprintf("Working directory outside LOOM_DATA_DIR: %s", cleanWorkingDir),
					Suggestion: "Execute commands within LOOM_SANDBOX_DIR, LOOM_DATA_DIR, or /tmp",
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}

		// Note: restrictWrites check removed - PATH_RESTRICTED check above is sufficient
		// The PATH_RESTRICTED validation already ensures working_dir is in safe locations:
		// - LOOM_DATA_DIR (includes agents/, workflows/, examples/, artifacts/)
		// - /tmp (temporary operations)
		// - LOOM_SANDBOX_DIR (if configured)
		// No additional restriction needed - agents can read from LOOM_DATA_DIR as intended
	}

	// Detect shell binary
	shellBinary, shellArgs, actualShellType, err := detectShell(shellType, command)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "SHELL_NOT_FOUND",
				Message:    fmt.Sprintf("Shell not found: %v", err),
				Suggestion: "Ensure bash/sh (Unix) or PowerShell/cmd (Windows) is installed",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Create command (we'll handle timeout manually for better control)
	if batchMode && actualShellType != "bash" && actualShellType != "sh" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    fmt.Sprintf("batch mode ('commands') needs bash or sh; shell is %s", actualShellType),
				Suggestion: "Use 'command' for a single command on this shell",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	cmd := exec.Command(shellBinary, shellArgs...) // #nosec G204 -- shellBinary is validated against allowlist above
	cmd.Dir = cleanWorkingDir

	// Set environment variables (merge with system env, filter sensitive ones)
	cmd.Env = os.Environ()
	filteredEnv := filterSensitiveEnvVars(envVars)
	for k, v := range filteredEnv {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// Add session-specific environment variables if session exists
	if sessionID != "" && loomDataDir != "" {
		cmd.Env = append(cmd.Env,
			fmt.Sprintf("LOOM_DATA_DIR=%s", loomDataDir),
			fmt.Sprintf("SESSION_ID=%s", sessionID),
		)

		// Add session artifact and scratchpad directories
		if sessionArtifactDir, err := artifacts.GetArtifactDir(sessionID, artifacts.SourceAgent); err == nil {
			cmd.Env = append(cmd.Env, fmt.Sprintf("SESSION_ARTIFACT_DIR=%s", sessionArtifactDir))
		}
		if scratchpadDir, err := artifacts.GetScratchpadDir(sessionID); err == nil {
			cmd.Env = append(cmd.Env, fmt.Sprintf("SESSION_SCRATCHPAD_DIR=%s", scratchpadDir))
		}
	}

	// Capture stdout and stderr
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "EXECUTION_FAILED",
				Message: fmt.Sprintf("Failed to create stdout pipe: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "EXECUTION_FAILED",
				Message: fmt.Sprintf("Failed to create stderr pipe: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Start command
	if err := cmd.Start(); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "EXECUTION_FAILED",
				Message:    fmt.Sprintf("Failed to start command: %v", err),
				Suggestion: "Check command syntax and ensure required executables are available",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Stream stdout/stderr concurrently. Each pipe is drained to EOF via
	// io.Copy into a capturedOutput sink that keeps only the first
	// maxOutputBytes and silently consumes the rest. Because the sink NEVER
	// stops reading (Write always returns len(p), nil), the child can never
	// block on a full pipe — so a command that emits a huge single line
	// (e.g. grep matching a 1.6MB one-line manifest.json) is truncated here,
	// not deadlocked until the wall-clock timeout kills it.
	//
	// A line-scanner cannot give that guarantee: it stops reading once the
	// budget is spent, and its max-line-size bounds how much of a single line
	// it will take at all. Lines are recovered from the captured text after
	// the drain, so batch parsing still sees the per-command sections.
	var stdoutLines, stderrLines []string
	var outputBytes int64
	var wg sync.WaitGroup

	stdoutCap := &capturedOutput{max: maxOutputBytes}
	stderrCap := &capturedOutput{max: maxOutputBytes}

	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(stdoutCap, stdoutPipe) }()
	go func() { defer wg.Done(); _, _ = io.Copy(stderrCap, stderrPipe) }()

	// Wait for pipe scanners to finish, then call cmd.Wait() to get the exit code.
	// IMPORTANT: Per Go docs, all reads from StdoutPipe/StderrPipe must complete
	// BEFORE calling cmd.Wait(), because Wait closes the pipes. Calling Wait first
	// can cause scanners to see early EOF and miss buffered output.
	waitDone := make(chan error, 1)
	go func() {
		wg.Wait()              // Scanners finish when pipes close at process exit
		waitDone <- cmd.Wait() // Then collect exit status
	}()

	// Wait for either completion or timeout
	var waitErr error
	timedOut := false

	timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
	defer timer.Stop()

	select {
	case waitErr = <-waitDone:
		// Command completed — scanners already finished above
	case <-timer.C:
		// Timeout - kill the process forcefully
		timedOut = true
		if cmd.Process != nil {
			// Try SIGKILL for forceful termination
			_ = cmd.Process.Signal(os.Kill) // Ignore error - process may have already exited
			// Also call Kill() as backup
			_ = cmd.Process.Kill() // Ignore error - process may have already exited
		}
		// Wait for Wait() to return after kill (brief timeout)
		select {
		case waitErr = <-waitDone:
			// Got it
		case <-time.After(500 * time.Millisecond):
			// If it takes too long, continue anyway
		}
		// Wait briefly for output streams (they should close after process dies)
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			// Streams finished
		case <-time.After(100 * time.Millisecond):
			// Don't wait too long for streams after timeout
		}
	case <-ctx.Done():
		// Parent context cancelled
		timedOut = true
		if cmd.Process != nil {
			// Try SIGKILL for forceful termination
			_ = cmd.Process.Signal(os.Kill) // Ignore error - process may have already exited
			// Also call Kill() as backup
			_ = cmd.Process.Kill() // Ignore error - process may have already exited
		}
		// Wait for Wait() to return after kill (brief timeout)
		select {
		case waitErr = <-waitDone:
			// Got it
		case <-time.After(500 * time.Millisecond):
			// If it takes too long, continue anyway
		}
		// Wait briefly for output streams
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			// Streams finished
		case <-time.After(100 * time.Millisecond):
			// Don't wait too long for streams after cancellation
		}
	}

	// Recover lines from the captured text now that both pipes are drained.
	// Oversize output is truncated by the sink and reported as such, rather
	// than failing the call: what a command did manage to print is the most
	// useful thing to hand back, and the old OUTPUT_OVERFLOW path threw all
	// of it away.
	stdoutLines = splitCapturedLines(stdoutCap.String())
	stderrLines = splitCapturedLines(stderrCap.String())
	outputBytes = stdoutCap.Total() + stderrCap.Total()

	// Determine exit code
	exitCode := 0

	// If not timed out, check for other errors
	if !timedOut && waitErr != nil {
		// Check for exit error (non-zero exit code)
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			// Other error (e.g., signal termination, failed to start)
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "EXECUTION_FAILED",
					Message: fmt.Sprintf("Command execution error: %v", waitErr),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}
	}

	// Handle timeout. This is the one moment the tool talks about time, so the
	// advice has to reach the model — which means it goes in Data: Error.Message
	// and Error.Suggestion are not part of the rendered tool result, so a payload
	// carrying only timed_out=true tells the model nothing and it abandons the
	// step instead of narrowing it.
	if timedOut {
		partialOut := firstOf(budgetOutput(strings.Join(stdoutLines, "\n"), commandBudget))
		partialErr := firstOf(budgetOutput(strings.Join(stderrLines, "\n"), commandBudget))
		text := timedOutAdvice(timeoutSeconds)
		if partialOut != "" {
			text = partialOut + "\n" + text
		}
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "TIMEOUT",
				Message: fmt.Sprintf("Command execution timeout after %d seconds", timeoutSeconds),
				// Split first; raising the limit is the last resort, not the first.
				Suggestion: "Split the command into smaller steps and send them as one parallel batch; raise timeout_seconds only if the step genuinely cannot be split",
				Retryable:  false,
			},
			Data: map[string]interface{}{
				"result":      text,
				"stdout":      partialOut,
				"stderr":      partialErr,
				"exit_code":   -1,
				"shell":       actualShellType,
				"working_dir": cleanWorkingDir,
				"timed_out":   true,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Build result
	if batchMode {
		secs := parseBatchOutput(stdoutLines, stderrLines, len(batchCmds))
		text, failed, firstExit := renderBatch(batchCmds, secs, false)
		return &shuttle.Result{
			Success: true, // errors are sections; the batch itself ran
			Data: map[string]interface{}{
				"result":      text,
				"commands":    len(batchCmds),
				"failed":      failed,
				"exit_code":   firstExit,
				"shell":       actualShellType,
				"working_dir": cleanWorkingDir,
				"timed_out":   false,
			},
			Metadata: map[string]interface{}{
				"command":      fmt.Sprintf("batch of %d", len(batchCmds)),
				"shell_type":   actualShellType,
				"shell_os":     runtime.GOOS,
				"output_bytes": outputBytes,
				"exit_code":    firstExit,
				"failed":       failed,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	stdout, _ := budgetOutput(strings.Join(stdoutLines, "\n"), commandBudget)
	stderr, _ := budgetOutput(strings.Join(stderrLines, "\n"), commandBudget)
	success := exitCode == 0

	result := &shuttle.Result{
		Success: success,
		Data: map[string]interface{}{
			"stdout":      stdout,
			"stderr":      stderr,
			"exit_code":   exitCode,
			"shell":       actualShellType,
			"working_dir": cleanWorkingDir,
			"timed_out":   false,
		},
		Metadata: map[string]interface{}{
			"command":      sanitizeCommandForTracing(command),
			"shell_type":   actualShellType,
			"shell_os":     runtime.GOOS,
			"output_bytes": outputBytes,
			"exit_code":    exitCode,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}

	// Add error for non-zero exit codes
	if !success {
		result.Error = &shuttle.Error{
			Code:       "EXIT_ERROR",
			Message:    fmt.Sprintf("Command exited with code %d", exitCode),
			Suggestion: "Check stderr output for details",
			Retryable:  true, // Non-zero exit might be transient
		}
	}

	// NOTE: Auto-validation removed - agent_management tool now handles validation upfront
	// The agent_management tool validates YAML before writing files, providing immediate feedback.
	// This is more user-friendly than post-hoc validation via shell_execute.
	//
	// If you need validation for files written via shell_execute (discouraged), consider:
	// 1. Using agent_management tool instead (recommended for weaver)
	// 2. Calling validation explicitly after writing
	//
	// Old auto-validation code (autoValidateConfigFiles) is preserved below for reference.

	return result, nil
}

func (t *ShellExecuteTool) Backend() string {
	return "" // Backend-agnostic
}

// detectShell determines which shell to use based on OS and user preference.
func detectShell(shellType, command string) (binary string, args []string, actualType string, err error) {
	switch shellType {
	case "bash":
		binary, err = exec.LookPath("bash")
		if err != nil {
			return "", nil, "", fmt.Errorf("bash not found")
		}
		return binary, []string{"-c", command}, "bash", nil

	case "sh":
		binary, err = exec.LookPath("sh")
		if err != nil {
			return "", nil, "", fmt.Errorf("sh not found")
		}
		return binary, []string{"-c", command}, "sh", nil

	case "powershell":
		binary, err = exec.LookPath("powershell.exe")
		if err != nil {
			binary, err = exec.LookPath("powershell")
		}
		if err != nil {
			return "", nil, "", fmt.Errorf("powershell not found")
		}
		return binary, []string{"-NoProfile", "-NonInteractive", "-Command", command}, "powershell", nil

	case "cmd":
		binary, err = exec.LookPath("cmd.exe")
		if err != nil {
			binary, err = exec.LookPath("cmd")
		}
		if err != nil {
			return "", nil, "", fmt.Errorf("cmd not found")
		}
		return binary, []string{"/C", command}, "cmd", nil

	case "default":
		// Auto-detect based on OS
		switch runtime.GOOS {
		case "windows":
			// Try PowerShell first, fallback to cmd
			if binary, err = exec.LookPath("powershell.exe"); err == nil {
				return binary, []string{"-NoProfile", "-NonInteractive", "-Command", command}, "powershell", nil
			}
			if binary, err = exec.LookPath("powershell"); err == nil {
				return binary, []string{"-NoProfile", "-NonInteractive", "-Command", command}, "powershell", nil
			}
			if binary, err = exec.LookPath("cmd.exe"); err == nil {
				return binary, []string{"/C", command}, "cmd", nil
			}
			if binary, err = exec.LookPath("cmd"); err == nil {
				return binary, []string{"/C", command}, "cmd", nil
			}
			return "", nil, "", fmt.Errorf("no shell found (tried powershell, cmd)")

		default:
			// Unix: Try bash first, fallback to sh
			if binary, err = exec.LookPath("bash"); err == nil {
				return binary, []string{"-c", command}, "bash", nil
			}
			if binary, err = exec.LookPath("sh"); err == nil {
				return binary, []string{"-c", command}, "sh", nil
			}
			return "", nil, "", fmt.Errorf("no shell found (tried bash, sh)")
		}

	default:
		return "", nil, "", fmt.Errorf("unknown shell type: %s", shellType)
	}
}

// resolveWorkingDir resolves and validates the working directory.
func resolveWorkingDir(workingDir, baseDir string) (string, error) {
	if workingDir == "" {
		return baseDir, nil
	}

	// Clean the path
	cleanDir := filepath.Clean(workingDir)

	// If relative, make it relative to baseDir
	if !filepath.IsAbs(cleanDir) {
		cleanDir = filepath.Join(baseDir, cleanDir)
	}

	// Check if directory exists
	info, err := os.Stat(cleanDir)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("directory does not exist: %s", cleanDir)
	}
	if err != nil {
		return "", fmt.Errorf("cannot access directory: %v", err)
	}

	// Ensure it's a directory
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", cleanDir)
	}

	return cleanDir, nil
}

// isBlockedWorkingDir checks if a working directory is in a sensitive system location.
func isBlockedWorkingDir(path string) bool {
	// System-critical directories to block
	blockedDirs := []string{
		"/etc",
		"/bin",
		"/sbin",
		"/boot",
		"/sys",
		"/proc",
		"/private/etc",
		"/System",
		"/Library",
		"C:\\Windows\\System32",
		"C:\\Windows\\SysWOW64",
		"C:\\Windows\\WinSxS",
	}

	// filepath.Clean renders the OS-native separator (backslash on Windows), so
	// compare on a slash-normalized form; otherwise the Unix entries above never
	// match a cleaned Windows path and vice versa.
	cleanPath := filepath.ToSlash(filepath.Clean(path))

	// Check exact match or prefix
	for _, blocked := range blockedDirs {
		blockedSlash := filepath.ToSlash(blocked)
		if cleanPath == blockedSlash || strings.HasPrefix(cleanPath, blockedSlash+"/") {
			return true
		}
	}

	return false
}

// filterSensitiveEnvVars removes sensitive environment variables from user input.
func filterSensitiveEnvVars(envVars map[string]string) map[string]string {
	// Sensitive environment variables to block
	blockedVars := map[string]bool{
		"AWS_SECRET_ACCESS_KEY": true,
		"AWS_SESSION_TOKEN":     true,
		"GITHUB_TOKEN":          true,
		"ANTHROPIC_API_KEY":     true,
		"OPENAI_API_KEY":        true,
		"DATABASE_PASSWORD":     true,
		"DB_PASSWORD":           true,
		"DB_PASS":               true,
		"MYSQL_PASSWORD":        true,
		"POSTGRES_PASSWORD":     true,
	}

	filtered := make(map[string]string)
	for k, v := range envVars {
		keyUpper := strings.ToUpper(k)
		if !blockedVars[keyUpper] && !strings.Contains(keyUpper, "SECRET") && !strings.Contains(keyUpper, "PASSWORD") {
			filtered[k] = v
		}
	}

	return filtered
}

// sanitizeCommandForTracing redacts sensitive information from commands for tracing.
func sanitizeCommandForTracing(command string) string {
	// Patterns to redact (order matters - more specific first)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(api[_-]?key)[=\s:]+[^\s'";]+`), // API_KEY=xxx, api-key: xxx
		regexp.MustCompile(`(?i)(password)[=\s:]+[^\s'";]+`),    // password=xxx, password: xxx
		regexp.MustCompile(`(?i)(token)[=\s:]+[^\s'";]+`),       // token=xxx, token: xxx
		regexp.MustCompile(`(?i)(secret)[=\s:]+[^\s'";]+`),      // secret=xxx, secret: xxx
		regexp.MustCompile(`(?i)(key)[=\s:]+[^\s'";]+`),         // key=xxx, key: xxx
	}

	sanitized := command
	for _, pattern := range patterns {
		sanitized = pattern.ReplaceAllString(sanitized, "***")
	}

	// Truncate if too long
	if len(sanitized) > 200 {
		return sanitized[:197] + "..."
	}

	return sanitized
}

// checkCommandTokenSize validates that a command isn't too large to execute safely.
// Large commands (especially heredocs) can cause output token exhaustion and infinite error loops.
//
// Token estimation: ~4 characters per token (conservative estimate)
// Threshold: 10,000 tokens (~40KB) - allows reasonable commands while blocking giant file writes
//
// This check prevents scenarios like:
// - Agent attempts to write 10MB file via heredoc
// - LLM output hits 8,192 token limit mid-generation
// - Tool call gets truncated with empty parameters
// - Agent retries same failed command 59+ times
func checkCommandTokenSize(command string) error {
	const (
		maxCommandTokens = 10000                            // Maximum tokens in a single command
		charsPerToken    = 4                                // Conservative estimate
		maxCommandChars  = maxCommandTokens * charsPerToken // 40,000 characters
	)

	commandLength := len(command)
	estimatedTokens := commandLength / charsPerToken

	if commandLength > maxCommandChars {
		return fmt.Errorf(
			"command is too large: %d characters (~%d tokens), maximum: %d characters (~%d tokens); "+
				"large commands often fail due to output token limits - consider breaking large file writes into smaller sections, "+
				"using multiple append operations instead of one large write, "+
				"or writing data incrementally rather than all at once",
			commandLength, estimatedTokens, maxCommandChars, maxCommandTokens,
		)
	}

	return nil
}

// firstOf drops the byte count from budgetOutput for callers that only want the text.
func firstOf(s string, _ int) string { return s }

// capturedOutput is an io.Writer that keeps the first max bytes and counts the
// rest. Write ALWAYS reports the full length as accepted, so a reader copying
// into it never stops: that is what keeps the child process from blocking on a
// full pipe when it emits more than the budget, which a line scanner cannot
// promise because it stops reading once the budget is spent.
type capturedOutput struct {
	mu        sync.Mutex
	buf       []byte
	total     int64
	max       int64
	truncated bool
}

func (c *capturedOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(p))
	if room := c.max - int64(len(c.buf)); room > 0 {
		if room > int64(len(p)) {
			room = int64(len(p))
		}
		c.buf = append(c.buf, p[:room]...)
	}
	if c.total > c.max {
		c.truncated = true
	}
	return len(p), nil
}

func (c *capturedOutput) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.truncated {
		return string(c.buf) + fmt.Sprintf("\n[output truncated at %d bytes]", c.max)
	}
	return string(c.buf)
}

// Total is how many bytes the stream actually produced, truncation included —
// what the caller reports as output_bytes.
func (c *capturedOutput) Total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// splitCapturedLines turns captured text back into the lines the batch parser
// and the budget helpers expect. Empty input yields no lines rather than one
// empty line, so an empty stream renders as absent instead of blank.
func splitCapturedLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
