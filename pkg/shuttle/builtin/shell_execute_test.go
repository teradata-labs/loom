// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShellExecuteTool_Name(t *testing.T) {
	tool := NewShellExecuteTool("")
	assert.Equal(t, "shell_execute", tool.Name())
}

func TestShellExecuteTool_Backend(t *testing.T) {
	tool := NewShellExecuteTool("")
	assert.Equal(t, "", tool.Backend())
}

func TestShellExecuteTool_Description(t *testing.T) {
	tool := NewShellExecuteTool("")
	desc := tool.Description()
	assert.NotEmpty(t, desc)
	assert.Contains(t, desc, "shell")
}

func TestShellExecuteTool_InputSchema(t *testing.T) {
	tool := NewShellExecuteTool("")
	schema := tool.InputSchema()
	require.NotNil(t, schema)

	// Check required fields
	assert.Empty(t, schema.Required, "either command or commands; neither is required alone")

	// Check properties exist
	assert.NotNil(t, schema.Properties["command"])
	assert.NotNil(t, schema.Properties["commands"])
	assert.NotNil(t, schema.Properties["working_dir"])
	assert.NotNil(t, schema.Properties["env"])
	assert.NotNil(t, schema.Properties["shell"])
	assert.NotNil(t, schema.Properties["max_output_bytes"])

	// timeout_seconds is honoured but never advertised: almost no command needs
	// it, and a caller who raises it can outlive the conversation cache. The
	// tool names it only in the result of a command it just stopped.
	assert.Nil(t, schema.Properties["timeout_seconds"])
	// stop_on_error is gone with sequential batches — dependent steps chain
	// with && inside one command.
	assert.Nil(t, schema.Properties["stop_on_error"])
}

func TestShellExecuteTool_Execute(t *testing.T) {
	// Skip on Windows for now (commands differ)
	if runtime.GOOS == "windows" {
		t.Skip("Skipping Unix-specific tests on Windows")
	}

	tests := []struct {
		name           string
		params         map[string]interface{}
		expectSuccess  bool
		expectError    string
		validateResult func(*testing.T, map[string]interface{})
	}{
		{
			name: "simple echo command",
			params: map[string]interface{}{
				"command": "echo hello",
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				stdout := data["stdout"].(string)
				assert.Contains(t, stdout, "hello")
				assert.Equal(t, 0, data["exit_code"])
				assert.False(t, data["timed_out"].(bool))
			},
		},
		{
			name: "command with stderr",
			params: map[string]interface{}{
				"command": "echo error >&2",
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				stderr := data["stderr"].(string)
				assert.Contains(t, stderr, "error")
				assert.Equal(t, 0, data["exit_code"])
			},
		},
		{
			name: "non-zero exit code",
			params: map[string]interface{}{
				"command": "exit 1",
			},
			expectSuccess: false,
			expectError:   "EXIT_ERROR",
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.Equal(t, 1, data["exit_code"])
			},
		},
		{
			name: "working directory",
			params: map[string]interface{}{
				"command":     "pwd",
				"working_dir": "/tmp",
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				stdout := data["stdout"].(string)
				assert.Contains(t, stdout, "/tmp")
				assert.Equal(t, "/tmp", data["working_dir"])
			},
		},
		{
			name: "environment variables",
			params: map[string]interface{}{
				"command": "echo $TEST_VAR",
				"env": map[string]interface{}{
					"TEST_VAR": "test-value-123",
				},
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				stdout := data["stdout"].(string)
				assert.Contains(t, stdout, "test-value-123")
			},
		},
		// Note: Timeout test is platform-specific and may not work reliably on all systems
		// The timeout mechanism is implemented, but testing it requires careful handling of
		// process signals and goroutine cleanup which varies by OS. In production, the timeout
		// works correctly but may take slightly longer than expected to fully clean up.
		// Skipped for now to avoid flaky tests - the timeout mechanism is tested manually.
		// {
		// 	name: "timeout enforcement",
		// 	params: map[string]interface{}{
		// 		"command":         "sleep 10",
		// 		"timeout_seconds": 1,
		// 	},
		// 	expectSuccess: false,
		// 	expectError:   "TIMEOUT",
		// 	validateResult: func(t *testing.T, data map[string]interface{}) {
		// 		assert.True(t, data["timed_out"].(bool))
		// 	},
		// },
		{
			name: "multiple lines output",
			params: map[string]interface{}{
				"command": "echo line1; echo line2; echo line3",
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				stdout := data["stdout"].(string)
				assert.Contains(t, stdout, "line1")
				assert.Contains(t, stdout, "line2")
				assert.Contains(t, stdout, "line3")
			},
		},
		{
			name: "invalid command",
			params: map[string]interface{}{
				"command": "nonexistentcommand12345",
			},
			expectSuccess: false,
			expectError:   "EXIT_ERROR",
		},
		{
			name: "missing command parameter",
			params: map[string]interface{}{
				"working_dir": "/tmp",
			},
			expectSuccess: false,
			expectError:   "INVALID_PARAMS",
		},
		{
			name: "empty command",
			params: map[string]interface{}{
				"command": "",
			},
			expectSuccess: false,
			expectError:   "INVALID_PARAMS",
		},
		{
			name: "invalid working directory",
			params: map[string]interface{}{
				"command":     "echo test",
				"working_dir": "/nonexistent/directory/path",
			},
			expectSuccess: false,
			expectError:   "INVALID_WORKDIR",
		},
		{
			name: "blocked working directory /etc",
			params: map[string]interface{}{
				"command":     "echo test",
				"working_dir": "/etc",
			},
			expectSuccess: false,
			expectError:   "UNSAFE_PATH",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewShellExecuteTool("")
			ctx := context.Background()

			result, err := tool.Execute(ctx, tt.params)
			require.NoError(t, err, "Execute should not return Go error")
			require.NotNil(t, result)

			if tt.expectSuccess {
				assert.True(t, result.Success, "Expected success=true")
				assert.Nil(t, result.Error, "Expected no error")
			} else {
				assert.False(t, result.Success, "Expected success=false")
				require.NotNil(t, result.Error, "Expected error")
				assert.Equal(t, tt.expectError, result.Error.Code, "Error code mismatch")
			}

			// Validate result data if validator provided
			if tt.validateResult != nil && result.Data != nil {
				data, ok := result.Data.(map[string]interface{})
				require.True(t, ok, "Result.Data should be map")
				tt.validateResult(t, data)
			}

			// Verify execution time is tracked
			assert.GreaterOrEqual(t, result.ExecutionTimeMs, int64(0), "ExecutionTimeMs should be >= 0")
		})
	}
}

func TestShellExecuteTool_Execute_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Skipping Windows-specific tests on non-Windows")
	}

	tests := []struct {
		name           string
		params         map[string]interface{}
		expectSuccess  bool
		validateResult func(*testing.T, map[string]interface{})
	}{
		{
			name: "simple echo command",
			params: map[string]interface{}{
				"command": "echo hello",
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				stdout := data["stdout"].(string)
				assert.Contains(t, stdout, "hello")
			},
		},
		{
			// "default" shell resolution tries PowerShell before cmd, and
			// PowerShell's `cd` (Set-Location) prints nothing for a bare
			// call, unlike cmd.exe's builtin. Pin the shell so the test
			// doesn't depend on which one auto-detection picks.
			name: "working directory",
			params: map[string]interface{}{
				"command":     "cd",
				"working_dir": "C:\\Windows\\Temp",
				"shell":       "cmd",
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				stdout := data["stdout"].(string)
				assert.Contains(t, strings.ToLower(stdout), "temp")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewShellExecuteTool("")
			ctx := context.Background()

			result, err := tool.Execute(ctx, tt.params)
			require.NoError(t, err)
			require.NotNil(t, result)

			if tt.expectSuccess {
				assert.True(t, result.Success)
			}

			if tt.validateResult != nil && result.Data != nil {
				data, ok := result.Data.(map[string]interface{})
				require.True(t, ok)
				tt.validateResult(t, data)
			}
		})
	}
}

func TestShellExecuteTool_ConcurrentExecution(t *testing.T) {

	if runtime.GOOS == "windows" {
		t.Skip("Skipping concurrent test on Windows")
	}

	tool := NewShellExecuteTool("")
	ctx := context.Background()

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errors := make([]error, numGoroutines)
	results := make([]*struct {
		success bool
		output  string
	}, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			params := map[string]interface{}{
				"command": fmt.Sprintf("echo test-%d", idx),
			}

			result, err := tool.Execute(ctx, params)
			errors[idx] = err

			if result != nil {
				results[idx] = &struct {
					success bool
					output  string
				}{
					success: result.Success,
				}
				if result.Data != nil {
					if data, ok := result.Data.(map[string]interface{}); ok {
						if stdout, ok := data["stdout"].(string); ok {
							results[idx].output = stdout
						}
					}
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify all executions completed successfully
	for i := 0; i < numGoroutines; i++ {
		assert.NoError(t, errors[i], "Goroutine %d should not error", i)
		require.NotNil(t, results[i], "Goroutine %d should have result", i)
		if results[i] != nil {
			assert.True(t, results[i].success, "Goroutine %d should succeed", i)
			// Note: Output might be empty if result was populated but data wasn't
			if results[i].output != "" {
				assert.Contains(t, results[i].output, fmt.Sprintf("test-%d", i), "Output should match index")
			}
		}
	}
}

func TestShellExecuteTool_LargeOutput(t *testing.T) {

	if runtime.GOOS == "windows" {
		t.Skip("Skipping large output test on Windows")
	}

	tool := NewShellExecuteTool("")
	ctx := context.Background()

	// Generate output that approaches the limit but doesn't necessarily exceed it
	// The default limit is 1MB, so generate about 500KB of output which should succeed
	params := map[string]interface{}{
		"command": "for i in {1..5000}; do echo line-$i-with-some-padding-text; done",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should succeed since we're under the limit
	assert.True(t, result.Success, "Large output within limits should succeed")

	// Large streams come back bounded by the command budget, head kept, with a
	// footer that says what was cut and how to narrow.
	if result.Data != nil {
		data := result.Data.(map[string]interface{})
		stdout := data["stdout"].(string)
		assert.LessOrEqual(t, len(stdout), defaultCommandBudget+300, "stdout must be bounded by the budget")
		assert.Contains(t, stdout, "bytes cut at a", "the cut must be explicit")
		assert.Contains(t, stdout, "line-1-", "head preserved")
	}
}

func TestShellExecuteTool_ContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping context cancellation test on Windows")
	}

	tool := NewShellExecuteTool("")
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	params := map[string]interface{}{
		"command": "sleep 10",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should fail (either TIMEOUT or EXECUTION_FAILED)
	assert.False(t, result.Success)
}

func TestShellExecuteTool_ShellTypeSelection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping shell type tests on Windows")
	}

	tests := []struct {
		name       string
		shellType  string
		shouldWork bool
	}{
		{
			name:       "default shell",
			shellType:  "default",
			shouldWork: true,
		},
		{
			name:       "explicit bash",
			shellType:  "bash",
			shouldWork: true, // Assuming bash is available
		},
		{
			name:       "explicit sh",
			shellType:  "sh",
			shouldWork: true, // sh should always be available on Unix
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewShellExecuteTool("")
			ctx := context.Background()

			params := map[string]interface{}{
				"command": "echo test",
				"shell":   tt.shellType,
			}

			result, err := tool.Execute(ctx, params)
			require.NoError(t, err)
			require.NotNil(t, result)

			if tt.shouldWork {
				assert.True(t, result.Success, "Shell type %s should work", tt.shellType)
			}
		})
	}
}

func TestShellExecuteTool_SensitiveEnvVarFiltering(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping env var test on Windows")
	}

	tool := NewShellExecuteTool("")
	ctx := context.Background()

	params := map[string]interface{}{
		"command": "env | grep TEST",
		"env": map[string]interface{}{
			"TEST_SAFE":             "safe-value",
			"AWS_SECRET_ACCESS_KEY": "should-be-filtered",
			"GITHUB_TOKEN":          "should-be-filtered",
		},
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	if result.Success && result.Data != nil {
		data := result.Data.(map[string]interface{})
		stdout := data["stdout"].(string)

		// Safe variable should be present
		assert.Contains(t, stdout, "TEST_SAFE")

		// Sensitive variables should be filtered
		assert.NotContains(t, stdout, "AWS_SECRET_ACCESS_KEY")
		assert.NotContains(t, stdout, "GITHUB_TOKEN")
	}
}

func TestShellExecuteTool_RelativeWorkingDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping relative path test on Windows")
	}

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "shell-test-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create subdirectory
	subDir := filepath.Join(tmpDir, "subdir")
	err = os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	tool := NewShellExecuteTool(tmpDir)
	ctx := context.Background()

	params := map[string]interface{}{
		"command":     "pwd",
		"working_dir": "subdir", // Relative path
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.True(t, result.Success)
	data := result.Data.(map[string]interface{})
	stdout := data["stdout"].(string)
	assert.Contains(t, stdout, "subdir")
}

func TestSanitizeCommandForTracing(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected string
	}{
		{
			name:     "simple command",
			command:  "echo hello",
			expected: "echo hello",
		},
		{
			name:     "password redaction",
			command:  "mysql -u user -p password=secret123",
			expected: "mysql -u user -p ***",
		},
		{
			name:     "token redaction",
			command:  "curl -H 'Authorization: token=abc123'",
			expected: "curl -H 'Authorization: ***'",
		},
		{
			name:     "api key redaction",
			command:  "export API_KEY=sk-1234567890",
			expected: "export ***",
		},
		{
			name:     "long command truncation",
			command:  strings.Repeat("a", 250),
			expected: strings.Repeat("a", 197) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeCommandForTracing(tt.command)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDetectShell(t *testing.T) {
	tests := []struct {
		name         string
		shellType    string
		expectError  bool
		checkBinary  bool
		expectedType string
	}{
		{
			name:         "default shell",
			shellType:    "default",
			expectError:  false,
			checkBinary:  true,
			expectedType: "", // OS-dependent
		},
		{
			name:         "invalid shell type",
			shellType:    "invalid",
			expectError:  true,
			checkBinary:  false,
			expectedType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary, args, actualType, err := detectShell(tt.shellType, "echo test")

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkBinary {
					assert.NotEmpty(t, binary)
					assert.NotEmpty(t, args)
					assert.NotEmpty(t, actualType)
				}
			}

			if tt.expectedType != "" {
				assert.Equal(t, tt.expectedType, actualType)
			}
		})
	}
}

func TestIsBlockedWorkingDir(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "etc directory",
			path:     "/etc",
			expected: true,
		},
		{
			name:     "etc subdirectory",
			path:     "/etc/nginx",
			expected: true,
		},
		{
			name:     "tmp directory",
			path:     "/tmp",
			expected: false,
		},
		{
			name:     "home directory",
			path:     "/home/user",
			expected: false,
		},
		{
			name:     "system directory",
			path:     "/System",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isBlockedWorkingDir(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFilterSensitiveEnvVars(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]string
		expected map[string]string
	}{
		{
			name: "filter AWS credentials",
			input: map[string]string{
				"SAFE_VAR":              "safe",
				"AWS_SECRET_ACCESS_KEY": "secret",
				"AWS_SESSION_TOKEN":     "token",
			},
			expected: map[string]string{
				"SAFE_VAR": "safe",
			},
		},
		{
			name: "filter password variables",
			input: map[string]string{
				"USER":              "admin",
				"DATABASE_PASSWORD": "pass123",
				"DB_PASSWORD":       "pass456",
			},
			expected: map[string]string{
				"USER": "admin",
			},
		},
		{
			name: "filter generic secret patterns",
			input: map[string]string{
				"APP_NAME":      "myapp",
				"MY_SECRET":     "secret123",
				"PASSWORD_HASH": "hash456",
			},
			expected: map[string]string{
				"APP_NAME": "myapp",
			},
		},
		{
			name: "allow normal variables",
			input: map[string]string{
				"PATH":     "/usr/bin",
				"HOME":     "/home/user",
				"APP_PORT": "8080",
			},
			expected: map[string]string{
				"PATH":     "/usr/bin",
				"HOME":     "/home/user",
				"APP_PORT": "8080",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterSensitiveEnvVars(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ANSI escapes are stripped from output; oversized streams keep head and tail
// with an explicit elision marker.
// TestBudgetOutput — output is bounded by a per-command budget with a footer
// that says what was cut and how to narrow; small output passes untouched.
func TestBudgetOutput(t *testing.T) {
	small := "10:06:32  WARNING: test line"
	if got, cut := budgetOutput(small, defaultCommandBudget); got != small || cut != 0 {
		t.Fatalf("small output must pass untouched, got %q cut=%d", got, cut)
	}
	big := strings.Repeat("line of output\n", 10000)
	got, cut := budgetOutput(big, 4096)
	if cut == 0 || len(got) > 4096+300 || !strings.Contains(got, "bytes cut at a 4096-byte budget") {
		t.Fatalf("big output must be cut to the budget with a footer, got %d bytes cut=%d", len(got), cut)
	}
}

// TestShellExecute_BatchSections — several commands in one call each run in
// their OWN shell (so a cd applies only inside the command that issued it),
// render as labelled sections in submission order, and a failing command is an
// ERROR section while its siblings still run.
func TestShellExecute_BatchSections(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash batch")
	}
	dir := t.TempDir()
	tool := NewShellExecuteTool(dir)
	tool.SetRestrictWrites(false)
	// A batch renders a labelled section per command, and one command failing
	// leaves the others untouched — they are independent shells, not a chain.
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"working_dir": dir,
		"commands": []interface{}{
			"echo first",
			map[string]interface{}{"command": "echo second", "label": "labelled"},
			"false",
			"echo after-failure",
		},
	})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("batch must run: err=%v res=%+v", err, res)
	}
	data, _ := res.Data.(map[string]interface{})
	text, _ := data["result"].(string)
	for _, want := range []string{"== command 1 ==\nfirst", "== labelled ==\nsecond", "== command 3 ==\nERROR: exit 1", "== command 4 ==\nafter-failure"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if data["failed"] != 1 {
		t.Fatalf("one failure expected, got %v", data["failed"])
	}

	// Each command is its own shell: a cd in one is not visible to another.
	// Dependent steps are expressed by chaining inside a single command.
	res, _ = tool.Execute(context.Background(), map[string]interface{}{
		"working_dir": dir,
		"commands": []interface{}{
			"mkdir -p sub && cd sub && pwd | xargs basename",
			"pwd | xargs basename",
		},
	})
	data, _ = res.Data.(map[string]interface{})
	text, _ = data["result"].(string)
	if !strings.Contains(text, "== command 1 ==\nsub") {
		t.Fatalf("a cd chained inside one command must apply to that command:\n%s", text)
	}
	if strings.Contains(text, "== command 2 ==\nsub") {
		t.Fatalf("a cd must NOT leak into a sibling command:\n%s", text)
	}
}

// Commands in a batch run at the same time, so a batch costs about as long as
// its slowest command rather than the sum. This is what keeps a batch inside
// the conversation-cache lifetime.
func TestShellExecute_BatchRunsInParallel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash batch")
	}
	dir := t.TempDir()
	tool := NewShellExecuteTool(dir)
	tool.SetRestrictWrites(false)
	started := time.Now()
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"working_dir": dir,
		"commands":    []interface{}{"sleep 2; echo a", "sleep 2; echo b", "sleep 2; echo c"},
	})
	elapsed := time.Since(started)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("batch must run: err=%v res=%+v", err, res)
	}
	data, _ := res.Data.(map[string]interface{})
	text, _ := data["result"].(string)
	for _, want := range []string{"a", "b", "c"} {
		if !strings.Contains(text, want) {
			t.Fatalf("every command must still report: missing %q in:\n%s", want, text)
		}
	}
	if elapsed > 5*time.Second {
		t.Fatalf("three 2s commands took %v — they ran in sequence, not in parallel", elapsed)
	}
}

// Sections render in the order the caller listed them, whatever order the
// commands actually finish in. Identical input must produce identical bytes, or
// a cached prefix is invalidated on every call.
func TestShellExecute_BatchRendersInSubmissionOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash batch")
	}
	dir := t.TempDir()
	tool := NewShellExecuteTool(dir)
	tool.SetRestrictWrites(false)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"working_dir": dir,
		"commands": []interface{}{
			map[string]interface{}{"command": "sleep 2; echo slow", "label": "listed-first"},
			map[string]interface{}{"command": "echo fast", "label": "listed-second"},
		},
	})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("batch must run: err=%v res=%+v", err, res)
	}
	data, _ := res.Data.(map[string]interface{})
	text, _ := data["result"].(string)
	first := strings.Index(text, "== listed-first ==")
	second := strings.Index(text, "== listed-second ==")
	if first < 0 || second < 0 {
		t.Fatalf("both sections must render:\n%s", text)
	}
	if first > second {
		t.Fatalf("the command listed first must render first even though it finished last:\n%s", text)
	}
}

// A batch is capped at batchMaxCommands. Commands past the cap simply do not
// run, and the model reads that off the sections it gets back.
func TestShellExecute_BatchCapsCommandCount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash batch")
	}
	dir := t.TempDir()
	tool := NewShellExecuteTool(dir)
	tool.SetRestrictWrites(false)
	var cmds []interface{}
	for i := 1; i <= batchMaxCommands+3; i++ {
		cmds = append(cmds, fmt.Sprintf("echo cmd%d", i))
	}
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"working_dir": dir, "commands": cmds,
	})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("batch must run: err=%v res=%+v", err, res)
	}
	data, _ := res.Data.(map[string]interface{})
	if data["commands"] != batchMaxCommands {
		t.Fatalf("expected %d commands to run, got %v", batchMaxCommands, data["commands"])
	}
	text, _ := data["result"].(string)
	if !strings.Contains(text, fmt.Sprintf("cmd%d", batchMaxCommands)) {
		t.Fatalf("command %d must run:\n%s", batchMaxCommands, text)
	}
	if strings.Contains(text, fmt.Sprintf("cmd%d", batchMaxCommands+1)) {
		t.Fatalf("command %d is past the cap and must not run:\n%s", batchMaxCommands+1, text)
	}
}

// A single command that times out must come back with the advice the model can
// act on, in the rendered payload. Error.Message and Error.Suggestion are not
// part of the rendered tool result, so a payload carrying only timed_out=true
// tells the model nothing and it abandons the step instead of narrowing it.
func TestShellExecute_SingleCommandTimeoutCarriesAdviceAndPartialOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash timeout")
	}
	dir := t.TempDir()
	tool := NewShellExecuteTool(dir)
	tool.SetRestrictWrites(false)

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"working_dir":     dir,
		"command":         "echo produced-before-the-kill; sleep 30",
		"timeout_seconds": float64(2),
	})
	if err != nil {
		t.Fatalf("a timeout is a result, not a transport error: %v", err)
	}
	data, _ := res.Data.(map[string]interface{})
	if to, _ := data["timed_out"].(bool); !to {
		t.Fatalf("the command must be reported as timed out: %+v", data)
	}
	text, _ := data["result"].(string)
	if !strings.Contains(text, "TIMED OUT after 2s") {
		t.Fatalf("the advice must state the budget that actually applied, not a constant:\n%s", text)
	}
	if !strings.Contains(text, "Split it into smaller steps") {
		t.Fatalf("splitting must be offered before raising the limit:\n%s", text)
	}
	if !strings.Contains(text, "produced-before-the-kill") {
		t.Fatalf("output produced before the kill must survive:\n%s", text)
	}
}

// The batch path states its own per-command budget, so the two paths never
// disagree about which limit was hit.
func TestShellExecute_BatchTimeoutStatesPerCommandBudget(t *testing.T) {
	if !strings.Contains(timedOutAdvice(perCommandTimeoutSecs), "TIMED OUT after 270s") {
		t.Fatalf("batch advice must name the per-command budget: %s", timedOutAdvice(perCommandTimeoutSecs))
	}
}

// A shell built with a directory starts its commands there, whatever
// LOOM_SANDBOX_DIR says: the sandbox is one value for the process, and where
// one agent's commands start is that agent's own. A relative working_dir
// resolves against it. Built with none, commands start in the sandbox.
func TestShellExecuteTool_StartsWhereItWasBuilt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pwd")
	}
	sandbox, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	own := filepath.Join(sandbox, "units", "u-1")
	require.NoError(t, os.MkdirAll(filepath.Join(own, "repo"), 0o755))
	t.Setenv("LOOM_SANDBOX_DIR", sandbox)

	pwd := func(tool *ShellExecuteTool, params map[string]interface{}) string {
		params["command"] = "pwd -P"
		res, err := tool.Execute(context.Background(), params)
		require.NoError(t, err)
		require.True(t, res.Success, "%+v", res.Error)
		return strings.TrimSpace(res.Data.(map[string]interface{})["stdout"].(string))
	}

	assert.Equal(t, own, pwd(NewShellExecuteTool(own), map[string]interface{}{}),
		"a placed shell started somewhere other than its own directory")
	assert.Equal(t, filepath.Join(own, "repo"), pwd(NewShellExecuteTool(own), map[string]interface{}{"working_dir": "repo"}),
		"a relative working_dir did not resolve against the shell's own directory")
	assert.Equal(t, sandbox, pwd(NewShellExecuteTool(""), map[string]interface{}{}),
		"an unplaced shell did not start in the sandbox")

	// Built with a relative directory, it is that directory as seen from where
	// the tool was built.
	t.Chdir(sandbox)
	assert.Equal(t, own, pwd(NewShellExecuteTool(filepath.Join("units", "u-1")), map[string]interface{}{}),
		"a shell built with a relative directory started somewhere else")
}

// Containment is tested on whole path segments: a sibling directory whose name
// merely starts the same way is outside, not inside.
func TestPathWithinRejectsSiblingPrefix(t *testing.T) {
	for _, tc := range []struct {
		path, dir string
		want      bool
	}{
		{"/work/proj", "/work/proj", true},
		{"/work/proj/sub/file", "/work/proj", true},
		{"/work/proj-evil", "/work/proj", false},
		{"/work/project", "/work/proj", false},
		{"/work", "/work/proj", false},
		{"/work/proj", "", false},
		{"", "/work/proj", false},
	} {
		if got := pathWithin(tc.path, tc.dir); got != tc.want {
			t.Errorf("pathWithin(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}

// One command's output reaching the cap leaves the others' sections whole: a
// command that finished reports its exit, and a later command's output and
// exit are its own — the budget is per command, not shared.
func TestShellExecute_BatchOneCommandAtItsCapLeavesTheOthersWhole(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash batch")
	}
	dir := t.TempDir()
	tool := NewShellExecuteTool(dir)
	tool.SetRestrictWrites(false)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"working_dir":      dir,
		"max_output_bytes": float64(2000),
		"commands": []interface{}{
			"head -c 5000 /dev/zero | tr '\\0' 'a'; echo; echo one-done",
			"echo second-ran",
		},
	})
	if err != nil || res == nil {
		t.Fatalf("batch must run: err=%v", err)
	}
	data, _ := res.Data.(map[string]interface{})
	text, _ := data["result"].(string)
	if strings.Contains(text, "did not finish") {
		t.Fatalf("a command that finished is reported as not finishing:\n%s", text)
	}
	if !strings.Contains(text, "== command 2 ==\nsecond-ran") {
		t.Fatalf("the second command's output was lost to the first's:\n%s", text)
	}
	if !strings.Contains(text, "cut at a") {
		t.Fatalf("the first command's cut is not stated:\n%s", text)
	}
	if data["failed"] != 0 {
		t.Fatalf("no command failed, got failed=%v:\n%s", data["failed"], text)
	}
}

// A command whose output is multi-byte text cut at its budget still renders
// valid UTF-8: the replay's byte cut never reaches what is shown.
func TestShellExecute_BatchCutKeepsTextValid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash batch")
	}
	dir := t.TempDir()
	tool := NewShellExecuteTool(dir)
	tool.SetRestrictWrites(false)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"working_dir":      dir,
		"max_output_bytes": float64(1500),
		"commands":         []interface{}{"for i in $(seq 1 800); do printf 'é€'; done; echo", "echo after"},
	})
	data, _ := res.Data.(map[string]interface{})
	text, _ := data["result"].(string)
	if !utf8.ValidString(text) {
		t.Fatalf("the rendered batch is not valid UTF-8")
	}
	if !strings.Contains(text, "== command 2 ==\nafter") {
		t.Fatalf("the second command was lost:\n%s", text)
	}
}
