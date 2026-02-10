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

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected zapcore.Level
	}{
		{"debug", zap.DebugLevel},
		{"info", zap.InfoLevel},
		{"warn", zap.WarnLevel},
		{"error", zap.ErrorLevel},
		{"", zap.InfoLevel},        // default
		{"unknown", zap.InfoLevel}, // unrecognized falls back to info
		{"DEBUG", zap.InfoLevel},   // case-sensitive, falls back to info
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseLogLevel(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestBuildLogger_ToFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	logger, err := buildLogger(logPath, "info")
	require.NoError(t, err)
	require.NotNil(t, logger)

	// Write a log entry and verify it lands in the file
	logger.Info("hello from test")
	_ = logger.Sync()

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "hello from test")
}

func TestBuildLogger_ToStderr(t *testing.T) {
	// When logFile is empty, buildLogger should succeed and not panic.
	// It writes to stderr, which we can't easily capture, but we can
	// verify the logger is usable.
	logger, err := buildLogger("", "debug")
	require.NoError(t, err)
	require.NotNil(t, logger)

	// Should not panic
	logger.Debug("stderr test")
}

func TestBuildLogger_InvalidPath(t *testing.T) {
	// A path inside a non-existent directory should return an error.
	_, err := buildLogger("/no/such/directory/test.log", "info")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open log file")
}

func TestBuildLogger_NeverUsesStdout(t *testing.T) {
	// Verify that a file-based logger writes ONLY to the file, not stdout.
	// This is critical because stdout is the MCP stdio transport.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	// Capture stdout by replacing it temporarily
	origStdout := os.Stdout
	stdoutR, stdoutW, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = stdoutW

	logger, err := buildLogger(logPath, "info")
	require.NoError(t, err)

	logger.Info("should go to file only")
	logger.Error("error should also go to file only")
	_ = logger.Sync()

	// Close the write end and read what was captured
	stdoutW.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := stdoutR.Read(buf)
	stdoutR.Close()

	assert.Equal(t, 0, n, "nothing should be written to stdout; got: %s", string(buf[:n]))
}

func TestBuildLogger_LevelFiltering(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		logFunc   func(*zap.Logger)
		shouldLog bool
	}{
		{
			name:      "info logger accepts info",
			level:     "info",
			logFunc:   func(l *zap.Logger) { l.Info("test") },
			shouldLog: true,
		},
		{
			name:      "info logger rejects debug",
			level:     "info",
			logFunc:   func(l *zap.Logger) { l.Debug("test") },
			shouldLog: false,
		},
		{
			name:      "error logger rejects warn",
			level:     "error",
			logFunc:   func(l *zap.Logger) { l.Warn("test") },
			shouldLog: false,
		},
		{
			name:      "error logger accepts error",
			level:     "error",
			logFunc:   func(l *zap.Logger) { l.Error("test") },
			shouldLog: true,
		},
		{
			name:      "debug logger accepts debug",
			level:     "debug",
			logFunc:   func(l *zap.Logger) { l.Debug("test") },
			shouldLog: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "test.log")

			logger, err := buildLogger(logPath, tt.level)
			require.NoError(t, err)

			tt.logFunc(logger)
			_ = logger.Sync()

			data, err := os.ReadFile(logPath)
			require.NoError(t, err)

			if tt.shouldLog {
				assert.NotEmpty(t, string(data), "expected log entry to be written")
			} else {
				assert.Empty(t, string(data), "expected no log entry to be written")
			}
		})
	}
}
