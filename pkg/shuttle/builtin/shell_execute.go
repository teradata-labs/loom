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
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/validation"
)

const (
	// DefaultShellTimeout is the default execution timeout (5 minutes).
	DefaultShellTimeout = 300

	// MaxShellTimeout is the maximum allowed timeout (10 minutes).
	MaxShellTimeout = 600

	// DefaultMaxOutputBytes limits output size to prevent memory issues (1MB).
	DefaultMaxOutputBytes = 1024 * 1024
)

// ShellExecuteTool provides cross-platform shell command execution.
// Supports Unix (bash/sh) and Windows (PowerShell/cmd).
// With session-based path restrictions for security.
type ShellExecuteTool struct {
	baseDir        string // Base directory for resolving relative paths
	loomDataDir    string // LOOM_DATA_DIR for boundary checking
	restrictWrites bool   // Enforce write restrictions (default: true)
	restrictReads  string // Read restriction level: "session" or "all_sessions"
}

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
	}
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
	return `Executes shell commands on the local system with real-time output streaming.
Supports bash/sh on Unix and PowerShell/cmd on Windows.

Use this tool to:
- Run system commands for automation tasks
- Execute build commands, tests, linters
- Run data processing scripts
- Perform system operations

Security: Validates working directories, filters sensitive environment variables, enforces timeouts.`
}

func (t *ShellExecuteTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for shell command execution",
		map[string]*shuttle.JSONSchema{
			"command": shuttle.NewStringSchema("Shell command to execute (required)"),
			"working_dir": shuttle.NewStringSchema(
				"Working directory for command execution (default: current directory)",
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
				"Shell to use (default: auto-detect, bash/sh on Unix, powershell/cmd on Windows)",
			).WithEnum("default", "bash", "sh", "powershell", "cmd").
				WithDefault("default"),
			"max_output_bytes": shuttle.NewNumberSchema(
				"Maximum output size in bytes (default: 1048576 = 1MB)",
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

		// Check if path is within LOOM_DATA_DIR or a whitelisted directory
		isAllowed := strings.HasPrefix(absWorkingDir, absLoomDataDir)
		if !isAllowed {
			// Whitelist /tmp for temporary file operations (common for agent workflows)
			if runtime.GOOS != "windows" && strings.HasPrefix(absWorkingDir, "/tmp") {
				isAllowed = true
			}
			// Windows temp directory
			if runtime.GOOS == "windows" && os.Getenv("TEMP") != "" {
				absTempDir, _ := filepath.Abs(os.Getenv("TEMP"))
				if strings.HasPrefix(absWorkingDir, absTempDir) {
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

		// If write restrictions enabled, ensure working directory is in session
		if t.restrictWrites {
			sessionDir := filepath.Join(loomDataDir, "artifacts", "sessions", sessionID)
			if !strings.HasPrefix(absWorkingDir, sessionDir) {
				return &shuttle.Result{
					Success: false,
					Error: &shuttle.Error{
						Code:       "WRITE_RESTRICTED",
						Message:    fmt.Sprintf("Write operations restricted to session directories: %s", cleanWorkingDir),
						Suggestion: fmt.Sprintf("Use working directory within LOOM_SANDBOX_DIR: %s", sessionDir),
					},
					ExecutionTimeMs: time.Since(start).Milliseconds(),
				}, nil
			}
		}
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
	cmd := exec.Command(shellBinary, shellArgs...)
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

	// Stream output concurrently
	var stdoutLines, stderrLines []string
	var outputBytes int64
	var outputErr error
	var mu sync.Mutex
	var wg sync.WaitGroup

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
				break
			}
			stderrLines = append(stderrLines, line)
			mu.Unlock()
		}
	}()

	// Wait for command completion in a goroutine
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	// Wait for either completion or timeout
	var waitErr error
	timedOut := false

	timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
	defer timer.Stop()

	select {
	case waitErr = <-waitDone:
		// Command completed before timeout
		wg.Wait() // Wait for output streams to finish
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

	// Check for output overflow (detected during streaming)
	if outputErr != nil {
		// Kill the process if still running
		if cmd.Process != nil {
			if err := cmd.Process.Kill(); err != nil {
				// Log kill failure but continue with error handling
				// Process may have already exited
			}
		}
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "OUTPUT_OVERFLOW",
				Message:    outputErr.Error(),
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
				"stdout":      strings.Join(stdoutLines, "\n"),
				"stderr":      strings.Join(stderrLines, "\n"),
				"exit_code":   -1,
				"shell":       actualShellType,
				"working_dir": cleanWorkingDir,
				"timed_out":   true,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Build result
	stdout := strings.Join(stdoutLines, "\n")
	stderr := strings.Join(stderrLines, "\n")
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

	cleanPath := filepath.Clean(path)

	// Check exact match or prefix
	for _, blocked := range blockedDirs {
		if cleanPath == blocked || strings.HasPrefix(cleanPath, blocked+string(filepath.Separator)) {
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

// intPtr returns a pointer to an int (helper for schema ranges).
func intPtr(i int) *float64 {
	f := float64(i)
	return &f
}

// autoValidateConfigFiles detects and validates YAML files written to $LOOM_DATA_DIR/agents/ or $LOOM_DATA_DIR/workflows/
// Returns formatted validation output to append to command result, or empty string if no validation needed.
func autoValidateConfigFiles(command, stdout, workingDir string) string {
	// Extract file paths from command
	filePaths := extractFilePaths(command, stdout, workingDir)

	if len(filePaths) == 0 {
		return ""
	}

	var validationOutputs []string

	for _, filePath := range filePaths {
		// Only validate files in .loom directories
		if !validation.ShouldValidate(filePath) {
			continue
		}

		// Check if file exists and was recently modified (within last 5 seconds)
		info, err := os.Stat(filePath)
		if err != nil || time.Since(info.ModTime()) > 5*time.Second {
			continue
		}

		// Validate the file
		result := validation.ValidateYAMLFile(filePath)

		// Only include validation output if there are errors or warnings
		if !result.Valid || result.HasWarnings() {
			validationOutputs = append(validationOutputs, result.FormatForWeaver())
		} else {
			// File is valid - add success message
			validationOutputs = append(validationOutputs, fmt.Sprintf("✅ %s validated successfully\n", filepath.Base(filePath)))
		}
	}

	if len(validationOutputs) == 0 {
		return ""
	}

	// Combine all validation outputs
	return strings.Join(validationOutputs, "\n")
}

// extractFilePaths extracts file paths from shell command and output.
// Looks for common patterns like:
// - "cat > file.yaml"
// - "echo ... > file.yaml"
// - Output like "Created: /path/to/file.yaml"
func extractFilePaths(command, stdout, workingDir string) []string {
	var paths []string
	seenPaths := make(map[string]bool)

	// Pattern 1: Redirect operators (>, >>)
	// Matches: cat > file.yaml, echo "content" > file.yaml
	redirectPattern := regexp.MustCompile(`>\s*([~\/]?[\w\/.\_\-]+\.ya?ml)`)
	matches := redirectPattern.FindAllStringSubmatch(command, -1)
	for _, match := range matches {
		if len(match) > 1 {
			path := resolvePath(match[1], workingDir)
			if path != "" && !seenPaths[path] {
				paths = append(paths, path)
				seenPaths[path] = true
			}
		}
	}

	// Pattern 2: tee command
	// Matches: echo "content" | tee file.yaml
	teePattern := regexp.MustCompile(`tee\s+([~\/]?[\w\/.\_\-]+\.ya?ml)`)
	matches = teePattern.FindAllStringSubmatch(command, -1)
	for _, match := range matches {
		if len(match) > 1 {
			path := resolvePath(match[1], workingDir)
			if path != "" && !seenPaths[path] {
				paths = append(paths, path)
				seenPaths[path] = true
			}
		}
	}

	// Pattern 3: Output mentions (Created:, Written:, Saved:)
	outputPattern := regexp.MustCompile(`(?i)(created|written|saved|wrote):\s*([~\/]?[\w\/.\_\-]+\.ya?ml)`)
	matches = outputPattern.FindAllStringSubmatch(stdout, -1)
	for _, match := range matches {
		if len(match) > 2 {
			path := resolvePath(match[2], workingDir)
			if path != "" && !seenPaths[path] {
				paths = append(paths, path)
				seenPaths[path] = true
			}
		}
	}

	// Pattern 4: Direct file paths in .loom directories
	loomPathPattern := regexp.MustCompile(`([~\/]?[\w\/\.\-]+\.loom\/(?:agents|workflows)\/[\w\-]+\.ya?ml)`)
	matches = loomPathPattern.FindAllStringSubmatch(command+" "+stdout, -1)
	for _, match := range matches {
		if len(match) > 1 {
			path := resolvePath(match[1], workingDir)
			if path != "" && !seenPaths[path] {
				paths = append(paths, path)
				seenPaths[path] = true
			}
		}
	}

	return paths
}

// resolvePath resolves a file path (handles ~, relative paths, etc.)
func resolvePath(path, workingDir string) string {
	// Expand tilde
	if strings.HasPrefix(path, "~") {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			path = strings.Replace(path, "~", homeDir, 1)
		}
	}

	// Make absolute if relative
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}

	// Clean the path
	return filepath.Clean(path)
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
			"Command is too large: %d characters (~%d tokens). Maximum: %d characters (~%d tokens).\n\n"+
				"Large commands often fail due to output token limits. Consider:\n"+
				"1. Breaking large file writes into smaller sections\n"+
				"2. Using multiple append operations instead of one large write\n"+
				"3. Writing data incrementally rather than all at once\n\n"+
				"Example: Instead of 'cat <<EOF > file.json' with 50KB JSON, "+
				"create the file in sections using multiple echo/append commands.",
			commandLength, estimatedTokens, maxCommandChars, maxCommandTokens,
		)
	}

	return nil
}
