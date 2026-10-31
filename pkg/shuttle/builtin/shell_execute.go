// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/session"
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
}

// testHookAfterWorkingDirCheck, when set by a test, runs after the working
// directory has been checked and before the command starts. Always nil in
// production.
var testHookAfterWorkingDirCheck func()

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
	}
}

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
	return `Executes shell commands on the local system — general-purpose. Supports bash/sh on Unix and PowerShell/cmd on Windows.
Prefer file_read for reading and searching files, and file_write/edit_files for changing them; use shell for everything else: dbt, builds, scripts, OS operations.
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
		// path-component comparison (pathWithin), never a string prefix: a link
		// inside LOOM_DATA_DIR that points elsewhere is elsewhere, and
		// "/tmpfoo" is not inside "/tmp".
		// LOOM_SANDBOX_DIR is the agent execution context (the workspace) and
		// is honored exactly as this guard's Suggestion advertises — without
		// it, an anchored workspace outside the data dir rejects every call.
		isAllowed := pathWithin(resolvedWorkingDir, resolvedAbs(loomDataDir))
		if !isAllowed {
			if sandboxDir := config.GetLoomSandboxDir(); sandboxDir != "" && pathWithin(resolvedWorkingDir, resolvedAbs(sandboxDir)) {
				isAllowed = true
			}
		}
		if !isAllowed {
			// Whitelist /tmp for temporary file operations (common for agent workflows)
			if runtime.GOOS != "windows" && pathWithin(resolvedWorkingDir, resolvedAbs("/tmp")) {
				isAllowed = true
			}
			// Windows temp directory
			if runtime.GOOS == "windows" && os.Getenv("TEMP") != "" {
				if pathWithin(resolvedWorkingDir, resolvedAbs(os.Getenv("TEMP"))) {
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

	if testHookAfterWorkingDirCheck != nil {
		testHookAfterWorkingDirCheck()
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
	cmd := exec.Command(shellBinary, shellArgs...) // #nosec G204 -- shellBinary is validated against allowlist above
	// Run in the directory the checks above approved. cleanWorkingDir may be a
	// symlink, and a link repointed after the check would otherwise carry the
	// command outside LOOM_DATA_DIR.
	cmd.Dir = resolvedWorkingDir
	// Own process group, so a timeout, cancellation or output overflow can kill
	// the shell's whole process group, not only the shell (killProcessTree).
	setProcessGroup(cmd)

	// Set environment variables: the server's environment minus anything that
	// looks like a credential (unless the operator passed it by name), then
	// the call's own variables, filtered the same way.
	cmd.Env = inheritedEnv(os.Environ(), t.passEnv)
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

	// readStream collects one pipe's lines. It counts raw bytes as they
	// arrive, with no per-line limit: a bufio.Scanner fails on a token over
	// its buffer and stops draining, so a newline-free flood blocked the
	// writer until the timeout instead of tripping the overflow.
	readStream := func(pipe io.Reader, lines *[]string) {
		defer wg.Done()
		reader := bufio.NewReaderSize(pipe, 64*1024)
		var line []byte
		for {
			frag, isPrefix, err := reader.ReadLine()
			if err != nil {
				return
			}
			n := int64(len(frag))
			if !isPrefix {
				n++ // the newline
			}
			mu.Lock()
			outputBytes += n
			if outputBytes > maxOutputBytes {
				outputErr = fmt.Errorf("output exceeded maximum size (%d bytes)", maxOutputBytes)
				mu.Unlock()
				signalOverflow()
				return
			}
			line = append(line, frag...)
			if !isPrefix {
				*lines = append(*lines, string(line))
				line = line[:0]
			}
			mu.Unlock()
		}
	}

	wg.Add(2)
	go readStream(stdoutPipe, &stdoutLines)
	go readStream(stderrPipe, &stderrLines)

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
		// One group kill can miss a process the shell forks while the kill is
		// being delivered (the shell wakes from waiting on a killed pipeline and
		// starts the next command first). The straggler holds the pipes open, so
		// the group is killed again until the exit status arrives.
		deadline := time.NewTimer(500 * time.Millisecond)
		defer deadline.Stop()
		rekill := time.NewTicker(25 * time.Millisecond)
		defer rekill.Stop()
	wait:
		for {
			select {
			case waitErr = <-waitDone:
				break wait
			case <-rekill.C:
				rekillProcessGroup(cmd)
			case <-deadline.C:
				break wait
			}
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

	// The scanners may still be running if stop() gave up on them, so every
	// read of what they write happens under their lock.
	mu.Lock()
	overflowErr := outputErr
	stdout := compactShellOutput(strings.Join(stdoutLines, "\n"))
	stderr := compactShellOutput(strings.Join(stderrLines, "\n"))
	totalOutputBytes := outputBytes
	mu.Unlock()

	// Check for output overflow (detected during streaming)
	if overflowErr != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "OUTPUT_OVERFLOW",
				Message:    overflowErr.Error(),
				Suggestion: "Increase max_output_bytes or reduce command output",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
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
		"C:/Windows/System32",
		"C:/Windows/SysWOW64",
		"C:/Windows/WinSxS",
	}

	_, blocked := matchBlockedPath(filepath.Clean(path), blockedDirs)
	return blocked
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

// sensitiveEnvNames are withheld from commands by exact (upper-cased) name.
var sensitiveEnvNames = map[string]bool{
	"DATABASE_PASSWORD": true,
	"DB_PASS":           true,
}

// sensitiveEnvSubstrings are withheld wherever they appear in a name, so
// PASSPHRASE, ZZ_PRIVATEKEY and SESSIONCOOKIE match without underscores.
var sensitiveEnvSubstrings = []string{
	"SECRET", "PASSWORD", "PASSWD", "PASSPHRASE", "PASSCODE", "CREDENTIAL",
	"APIKEY", "PRIVATE_KEY", "PRIVATEKEY", "AUTHTOKEN", "ACCESSKEY", "SIGNINGKEY",
	"BEARER", "COOKIE", "JWT", "WEBHOOK",
}

// sensitiveEnvSegments are withheld when they are a whole underscore-separated
// part of a name, so AWS_ACCESS_KEY_ID and LOOM_OTLP_HEADERS match while
// KEYBOARD_LAYOUT and GIT_AUTHOR_NAME do not.
// HEADERS covers LOOM_OTLP_HEADERS and OTEL_EXPORTER_OTLP_HEADERS, which carry
// "Authorization=Bearer ..." values.
var sensitiveEnvSegments = map[string]bool{
	"KEY": true, "PASS": true, "DSN": true,
	"AUTH": true, "AUTHORIZATION": true, "HEADERS": true,
}

// sensitiveEnvSegmentSuffixes are withheld when an underscore-separated part
// ends with them, so GH_TOKEN, GITHUBTOKEN and ZZ_APITOKEN match while
// TOKENIZERS_PARALLELISM does not.
var sensitiveEnvSegmentSuffixes = []string{"TOKEN"}

// sensitiveEnvNameSuffixes are withheld when the whole name ends with them.
// "_PWD" catches DB_PWD-style passwords while the shell's own PWD and OLDPWD
// still pass.
var sensitiveEnvNameSuffixes = []string{"DATABASE_URL", "_PWD"}

// passedEnvNames match the rules above but are passed on purpose.
// SSH_AUTH_SOCK is a socket path, not a secret; commands that can reach it can
// use the operator's loaded SSH keys (documented in docs/architecture/artifacts.md).
var passedEnvNames = map[string]bool{"SSH_AUTH_SOCK": true}

// isSensitiveEnvName reports whether an environment variable's name looks like
// it holds a credential. Names are compared upper-cased.
func isSensitiveEnvName(name string) bool {
	upper := strings.ToUpper(name)
	if passedEnvNames[upper] {
		return false
	}
	if sensitiveEnvNames[upper] {
		return true
	}
	for _, suffix := range sensitiveEnvNameSuffixes {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	for _, sub := range sensitiveEnvSubstrings {
		if strings.Contains(upper, sub) {
			return true
		}
	}
	for _, seg := range strings.Split(upper, "_") {
		if sensitiveEnvSegments[seg] {
			return true
		}
		for _, suffix := range sensitiveEnvSegmentSuffixes {
			if strings.HasSuffix(seg, suffix) {
				return true
			}
		}
	}
	return false
}

// hasURLCredentials reports whether value is a URL with a password in its
// userinfo (postgres://user:pw@host, http://user:pw@proxy).
func hasURLCredentials(value string) bool {
	if !strings.Contains(value, "://") || !strings.Contains(value, "@") {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.User == nil {
		return false
	}
	_, hasPassword := u.User.Password()
	return hasPassword
}

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

// pathWithin reports whether path is dir itself or sits beneath it. A bare
// strings.HasPrefix accepts a sibling whose name merely starts the same way —
// /work/proj also admitting /work/proj-evil — so containment is tested on
// whole path segments. Neither path is symlink-resolved; callers that need
// that pass resolvedAbs paths.
func pathWithin(path, dir string) bool {
	if dir == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ansiEscape matches CSI/OSC terminal escape sequences — pure rendering bytes
// that inflate build-tool output (dbt colorizes every line) without carrying
// information.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

const (
	// shellOutputCap bounds one stream's bytes in the result.
	shellOutputCap = 12 * 1024
	// shellOutputHead / shellOutputTail split the cap: build tools put
	// failures inline and the summary at the end, so the tail gets the bulk.
	shellOutputHead = 4 * 1024
	shellOutputTail = 8 * 1024
)

// compactShellOutput strips terminal escape sequences and, past the cap,
// keeps the head and tail of the stream with an explicit elision marker. The
// cuts land on rune boundaries: a byte-offset slice through a multi-byte rune
// puts invalid UTF-8 into the result, which every downstream encoder then has
// to repair.
func compactShellOutput(s string) string {
	s = ansiEscape.ReplaceAllString(s, "")
	if len(s) <= shellOutputCap {
		return s
	}
	head := runeBoundaryBefore(s, shellOutputHead)
	tail := runeBoundaryAfter(s, len(s)-shellOutputTail)
	elided := tail - head
	return s[:head] +
		fmt.Sprintf("\n[... %d bytes elided — rerun with a filter (grep/tail) to see the middle ...]\n", elided) +
		s[tail:]
}

// runeBoundaryBefore returns the largest index <= i that starts a rune.
func runeBoundaryBefore(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// runeBoundaryAfter returns the smallest index >= i that starts a rune.
func runeBoundaryAfter(s string, i int) int {
	if i < 0 {
		return 0
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}
