// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shelljail"
	"github.com/teradata-labs/loom/pkg/shellpolicy"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

const (
	// DefaultShellTimeout is the default execution timeout (5 minutes).
	DefaultShellTimeout = 300

	// MaxShellTimeout is the maximum allowed timeout (10 minutes).
	MaxShellTimeout = 600

	// DefaultMaxOutputBytes limits output size to prevent memory issues (1MB).
	DefaultMaxOutputBytes = 1024 * 1024

	// PassEnvVar names the environment variable holding a comma-separated list
	// of server variables passed to commands even though their names look
	// sensitive (for example GH_TOKEN for the gh CLI). Every other sensitive
	// server variable is withheld from commands.
	PassEnvVar = "LOOM_SHELL_PASS_ENV" // #nosec G101 -- an environment variable name, not a credential
)

// ShellExecuteTool provides cross-platform shell command execution.
// Supports Unix (bash/sh) and Windows (PowerShell/cmd).
// With session-based path restrictions for security.
type ShellExecuteTool struct {
	baseDir        string // Base directory for resolving relative paths
	loomDataDir    string // LOOM_DATA_DIR for boundary checking
	restrictWrites bool   // Enforce write restrictions (default: true)
	restrictReads  string // Read restriction level: "session" or "all_sessions"
	// passEnv names sensitive-looking server variables that commands still
	// receive; set from LOOM_SHELL_PASS_ENV or SetPassEnv.
	passEnv map[string]bool
	// jail, when set, runs commands in the jailed runner instead of bash
	// (tools.shell_execute.mode: jailed).
	jail *shelljail.Runner
}

// defaultJail is the runner every ShellExecuteTool created after SetShellJail
// uses. serve sets it once at startup, before any agent is built.
var defaultJail atomic.Pointer[shelljail.Runner]

// SetShellJail makes every ShellExecuteTool created from now on run commands
// in r (jailed mode); nil restores bash. Tools already created keep the
// runner they were created with.
func SetShellJail(r *shelljail.Runner) { defaultJail.Store(r) }

// ShellJail returns the runner SetShellJail installed, or nil.
func ShellJail() *shelljail.Runner { return defaultJail.Load() }

// NewShellExecuteTool creates a new shell execution tool.
// If baseDir is empty, uses current working directory.
// Defaults: restrictWrites=true, restrictReads="session"
func NewShellExecuteTool(baseDir string) *ShellExecuteTool {
	if baseDir == "" {
		baseDir, _ = os.Getwd()
	}
	return &ShellExecuteTool{
		baseDir:        baseDir,
		loomDataDir:    os.Getenv("LOOM_DATA_DIR"), // Will be set from config in agent initialization
		restrictWrites: true,                       // Default to restricted writes
		restrictReads:  "session",                  // Default to session-only reads
		passEnv:        parsePassEnv(os.Getenv(PassEnvVar)),
		jail:           defaultJail.Load(),
	}
}

// SetJail makes this tool run commands in r; nil means bash.
func (t *ShellExecuteTool) SetJail(r *shelljail.Runner) { t.jail = r }

// SetPassEnv replaces the list of sensitive-looking server variables that
// commands still receive. Names match exactly.
func (t *ShellExecuteTool) SetPassEnv(names []string) {
	t.passEnv = parsePassEnv(strings.Join(names, ","))
}

// parsePassEnv turns a comma-separated list into a set, skipping blanks.
func parsePassEnv(list string) map[string]bool {
	set := make(map[string]bool)
	for _, name := range strings.Split(list, ",") {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	return set
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
	return `Executes shell commands on the local system. Supports bash/sh on Unix and PowerShell/cmd on Windows.
Use for automation, builds, tests, data processing, and system operations.
Security: validates working directories, filters sensitive env vars, enforces timeouts.`
}

func (t *ShellExecuteTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for shell command execution",
		map[string]*shuttle.JSONSchema{
			"command": shuttle.NewStringSchema("Shell command to execute (required)"),
			"working_dir": shuttle.NewStringSchema(
				"Working directory for command execution.",
			),
			"env": shuttle.NewObjectSchema(
				"Environment variables to set (merged with system environment)",
				nil,
				nil,
			),
			"timeout_seconds": shuttle.NewNumberSchema(
				"Maximum execution time in seconds (default: 300, max: 600)",
			).WithDefault(DefaultShellTimeout).
				WithRange(intPtr(1), intPtr(MaxShellTimeout)),
			"shell": shuttle.NewStringSchema(
				"Shell to use.",
			).WithEnum("default", "bash", "sh", "powershell", "cmd").
				WithDefault("default"),
			"max_output_bytes": shuttle.NewNumberSchema(
				"Maximum output size in bytes.",
			).WithDefault(DefaultMaxOutputBytes),
		},
		[]string{"command"},
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
	// Priority: 1) explicit working_dir param, 2) LOOM_SANDBOX_DIR (agent execution context)
	// Note: LOOM_SANDBOX_DIR defaults to LOOM_DATA_DIR (see config.GetLoomSandboxDir)
	workingDir := config.GetLoomSandboxDir()
	if wd, ok := params["working_dir"].(string); ok && wd != "" {
		workingDir = wd // Explicit override always wins
	}

	// Jailed mode resolves the working directory and the read/write roots the
	// same way the command-policy hook did when it admitted the call: the
	// session scratchpad by default, relative paths against it.
	var jailOpt shellpolicy.Options
	if t.jail != nil {
		if st, ok := params["shell"].(string); ok && st != "" && st != "default" && st != "bash" && st != "sh" {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:       "INVALID_PARAMS",
					Message:    fmt.Sprintf("shell %q is not available: commands run in the jailed bash-compatible interpreter", st),
					Suggestion: "Omit the shell parameter",
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}
		wd, _ := params["working_dir"].(string)
		opt, err := t.jail.Options(sessionID, wd)
		if err != nil {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:       "JAIL_UNAVAILABLE",
					Message:    fmt.Sprintf("the jailed shell cannot run this call: %v", err),
					Suggestion: "Run the command inside a session",
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}
		jailOpt = opt
		workingDir = opt.WorkingDir
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
	cleanWorkingDir, err := resolveWorkingDir(workingDir, t.baseDir)
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

	// Security: Block execution in sensitive system directories, judged on both
	// the path as given and where its symlinks lead.
	resolvedWorkingDir := resolvedAbs(cleanWorkingDir)
	if isBlockedWorkingDir(cleanWorkingDir) || isBlockedWorkingDir(resolvedWorkingDir) {
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
		// Ensure working directory is within LOOM_DATA_DIR or whitelisted
		// directories. Containment is judged on symlink-resolved paths, with a
		// path-component comparison (withinDir), never a string prefix: a link
		// inside LOOM_DATA_DIR that points elsewhere is elsewhere, and
		// "/tmpfoo" is not inside "/tmp".
		isAllowed := withinDir(resolvedAbs(loomDataDir), resolvedWorkingDir)
		if !isAllowed {
			// Whitelist /tmp for temporary file operations (common for agent workflows)
			if runtime.GOOS != "windows" && withinDir(resolvedAbs("/tmp"), resolvedWorkingDir) {
				isAllowed = true
			}
			// Windows temp directory
			if runtime.GOOS == "windows" && os.Getenv("TEMP") != "" {
				if withinDir(resolvedAbs(os.Getenv("TEMP")), resolvedWorkingDir) {
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

	var cmd *exec.Cmd
	var child *shelljail.Child
	var jailEnvDropped []string
	actualShellType := "jailed"
	if t.jail != nil {
		// The jail child: the interpreter in a process of its own, judging
		// every launch against the policy and what a person approved for this
		// call (the admission grants), with an environment built from scratch.
		sessionVars := map[string]string{"LOOM_DATA_DIR": loomDataDir, "SESSION_ID": sessionID}
		if d, err := artifacts.GetArtifactDir(sessionID, artifacts.SourceAgent); err == nil {
			sessionVars["SESSION_ARTIFACT_DIR"] = d
		}
		if d, err := artifacts.GetScratchpadDir(sessionID); err == nil {
			sessionVars["SESSION_SCRATCHPAD_DIR"] = d
		}
		env, dropped := t.jail.BaseEnv(jailOpt.WorkingDir, sessionVars, envVars)
		jailEnvDropped = dropped
		grant := shellpolicy.MergeGrants(shuttle.AdmissionGrantsFromContext(ctx))
		spec := t.jail.Spec(command, jailOpt, grant, env, time.Duration(timeoutSeconds)*time.Second)
		c, err := t.jail.Command(spec)
		if err != nil {
			return &shuttle.Result{
				Success:         false,
				Error:           &shuttle.Error{Code: "JAIL_UNAVAILABLE", Message: fmt.Sprintf("cannot start the jailed shell: %v", err)},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}
		child = c
		cmd = c.Cmd
		defer child.Started() // idempotent safety net; Started is also called right after Start
	} else {
		// Detect shell binary
		shellBinary, shellArgs, detected, err := detectShell(shellType, command)
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
		actualShellType = detected

		// Create command (we'll handle timeout manually for better control)
		cmd = exec.Command(shellBinary, shellArgs...) // #nosec G204 -- shellBinary is validated against allowlist above
		cmd.Dir = cleanWorkingDir
		// Own process group, so a timeout, cancellation or output overflow can kill
		// everything the shell started, not only the shell (killProcessTree).
		setProcessGroup(cmd)

		// Set environment variables: the server's environment minus anything that
		// looks like a credential (unless the operator passed it by name), then
		// the call's own variables, filtered the same way.
		cmd.Env = inheritedEnv(os.Environ(), t.passEnv)
		filteredEnv := filterSensitiveEnvVars(envVars)
		for k, v := range filteredEnv {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Add session-specific environment variables if session exists (bash mode;
	// the jail built its own above)
	if child == nil && sessionID != "" && loomDataDir != "" {
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
	startErr := cmd.Start()
	if child != nil {
		child.Started()
	}
	if err := startErr; err != nil {
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

	// Stream output concurrently
	var stdoutLines, stderrLines []string
	var outputBytes int64
	var outputErr error
	var mu sync.Mutex
	var wg sync.WaitGroup
	// overflow is closed (once) when output passes maxOutputBytes, so the
	// command is stopped at once: a reader that stops reading leaves the writer
	// blocked on a full pipe until the timeout.
	overflow := make(chan struct{})
	var overflowOnce sync.Once
	signalOverflow := func() { overflowOnce.Do(func() { close(overflow) }) }

	wg.Add(2)

	// Read stdout
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdoutPipe)
		buf := make([]byte, 64*1024)   // 64KB line buffer
		scanner.Buffer(buf, 1024*1024) // 1MB max line size

		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			outputBytes += int64(len(line)) + 1 // +1 for newline
			if outputBytes > maxOutputBytes {
				outputErr = fmt.Errorf("output exceeded maximum size (%d bytes)", maxOutputBytes)
				mu.Unlock()
				signalOverflow()
				break
			}
			stdoutLines = append(stdoutLines, line)
			mu.Unlock()
		}
	}()

	// Read stderr
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		buf := make([]byte, 64*1024)   // 64KB line buffer
		scanner.Buffer(buf, 1024*1024) // 1MB max line size

		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			outputBytes += int64(len(line)) + 1 // +1 for newline
			if outputBytes > maxOutputBytes {
				outputErr = fmt.Errorf("output exceeded maximum size (%d bytes)", maxOutputBytes)
				mu.Unlock()
				signalOverflow()
				break
			}
			stderrLines = append(stderrLines, line)
			mu.Unlock()
		}
	}()

	// Wait for pipe scanners to finish, then call cmd.Wait() to get the exit code.
	// IMPORTANT: Per Go docs, all reads from StdoutPipe/StderrPipe must complete
	// BEFORE calling cmd.Wait(), because Wait closes the pipes. Calling Wait first
	// can cause scanners to see early EOF and miss buffered output.
	waitDone := make(chan error, 1)
	go func() {
		wg.Wait()              // Scanners finish when pipes close at process exit
		waitDone <- cmd.Wait() // Then collect exit status
	}()

	// Wait for completion, timeout, cancellation or output overflow.
	var waitErr error
	timedOut := false

	timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
	defer timer.Stop()

	// stop kills the whole process group, then waits briefly for the exit
	// status and the output streams. It never blocks for long: a process that
	// escaped the group (setsid) may keep a pipe open.
	stop := func() {
		killProcessTree(cmd)
		select {
		case waitErr = <-waitDone:
		case <-time.After(500 * time.Millisecond):
		}
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
		}
	}

	select {
	case waitErr = <-waitDone:
		// Command completed — scanners already finished above
	case <-timer.C:
		timedOut = true
		stop()
	case <-ctx.Done():
		// Parent context cancelled
		timedOut = true
		stop()
	case <-overflow:
		stop()
	}

	// The jailed shell leaves nothing running: whatever the command started in
	// the background dies with the call. Then collect what the jail reported.
	var jailResult *shelljail.Result
	if child != nil {
		killProcessTree(cmd)
		res := child.Result()
		res.EnvDropped = mergeNames(jailEnvDropped, res.EnvDropped)
		if timedOut && res.Limit == "" {
			res.Limit = "timeout"
		}
		jailResult = &res
	}

	// The scanners may still be running if stop() gave up on them, so every
	// read of what they write happens under their lock.
	mu.Lock()
	overflowErr := outputErr
	stdout := strings.Join(stdoutLines, "\n")
	stderr := strings.Join(stderrLines, "\n")
	totalOutputBytes := outputBytes
	mu.Unlock()

	// Check for output overflow (detected during streaming)
	if overflowErr != nil {
		overflow := &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "OUTPUT_OVERFLOW",
				Message:    overflowErr.Error(),
				Suggestion: "Increase max_output_bytes or reduce command output",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}
		if jailResult != nil {
			overflow.Data = map[string]interface{}{"jail": jailData(jailResult)}
		}
		return overflow, nil
	}

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

	// Handle timeout
	if timedOut {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "TIMEOUT",
				Message:    fmt.Sprintf("Command execution timeout after %d seconds", timeoutSeconds),
				Suggestion: "Increase timeout_seconds or optimize the command",
				Retryable:  false,
			},
			Data: map[string]interface{}{
				"stdout":      stdout,
				"stderr":      stderr,
				"exit_code":   -1,
				"shell":       actualShellType,
				"working_dir": cleanWorkingDir,
				"timed_out":   true,
				"jail":        jailData(jailResult),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Build result
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
			"output_bytes": totalOutputBytes,
			"exit_code":    exitCode,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}
	if jailResult != nil {
		result.Data.(map[string]interface{})["jail"] = jailData(jailResult)
	}

	// Add error for non-zero exit codes
	if !success {
		result.Error = &shuttle.Error{
			Code:       "EXIT_ERROR",
			Message:    fmt.Sprintf("Command exited with code %d", exitCode),
			Suggestion: "Check stderr output for details",
			Retryable:  true, // Non-zero exit might be transient
		}
		if jailResult != nil && len(jailResult.Blocked) > 0 {
			result.Error.Message += "; the shell policy blocked: " + describeBlocked(jailResult.Blocked)
			result.Error.Suggestion = "A program that is not on the shell allowlist runs only with a person's approval, and only when the command names it literally. Call again with it written out (not computed through a variable or substitution) so it can be approved, or use an allowed program."
		}
		if jailResult != nil && jailResult.Limit != "" {
			result.Error.Message += "; the command hit the jail's " + jailResult.Limit + " limit"
			result.Error.Retryable = false
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

// jailData renders a jail result for the tool's Data.
func jailData(r *shelljail.Result) map[string]interface{} {
	if r == nil {
		return nil
	}
	blocked := make([]map[string]interface{}, 0, len(r.Blocked))
	for _, b := range r.Blocked {
		blocked = append(blocked, map[string]interface{}{"name": b.Name, "reason": b.Reason, "pos": b.Pos})
	}
	out := map[string]interface{}{"blocked": blocked, "env_dropped": r.EnvDropped, "limit": r.Limit}
	if r.Error != "" {
		out["error"] = r.Error
	}
	return out
}

// describeBlocked lists blocked items for an error message, at most five.
func describeBlocked(blocked []shelljail.Blocked) string {
	parts := make([]string, 0, 5)
	for i, b := range blocked {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("and %d more", len(blocked)-5))
			break
		}
		at := ""
		if b.Pos != "" {
			at = " (" + b.Pos + ")"
		}
		parts = append(parts, b.Name+at+": "+b.Reason)
	}
	return strings.Join(parts, "; ")
}

// mergeNames returns the sorted union of two name lists.
func mergeNames(a, b []string) []string {
	set := map[string]bool{}
	for _, n := range append(append([]string{}, a...), b...) {
		set[n] = true
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
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
	filtered := make(map[string]string)
	for k, v := range envVars {
		if !isSensitiveEnvName(k) && !hasURLCredentials(v) {
			filtered[k] = v
		}
	}
	return filtered
}

// inheritedEnv returns the server environment a command receives: every
// "NAME=value" entry except those whose name looks like a credential or whose
// value is a URL carrying a password, unless passEnv names the variable.
func inheritedEnv(environ []string, passEnv map[string]bool) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if passEnv[name] || (!isSensitiveEnvName(name) && !hasURLCredentials(value)) {
			out = append(out, kv)
		}
	}
	return out
}

// isSensitiveEnvName reports whether an environment variable's name looks like
// it holds a credential. The rules live in shelljail so bash mode and jailed
// mode withhold the same names.
func isSensitiveEnvName(name string) bool { return shelljail.SensitiveEnvName(name) }

// hasURLCredentials reports whether value is a URL with a password in its
// userinfo (postgres://user:pw@host, http://user:pw@proxy).
func hasURLCredentials(value string) bool { return shelljail.URLHasPassword(value) }

// resolvedAbs returns path made absolute and with its symlinks resolved. When
// resolution fails (the path does not exist yet), it returns the cleaned
// absolute path.
func resolvedAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// withinDir reports whether path is root or lies beneath it, comparing whole
// path components (so "/tmpfoo" is not within "/tmp"). Both are cleaned first;
// neither is resolved, so callers pass resolvedAbs paths.
func withinDir(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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

// intPtr returns a pointer to an int (helper for schema ranges).
func intPtr(i int) *float64 {
	f := float64(i)
	return &f
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
