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
// Package transport implements stdio transport for MCP servers.
package transport

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"go.uber.org/zap"
)

// StdioTransport implements Transport over stdin/stdout for subprocess communication
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	reader *bufio.Reader
	mu     sync.Mutex
	closed bool
	logger *zap.Logger
}

// StdioConfig configures the stdio transport
type StdioConfig struct {
	Command string            // Command to execute
	Args    []string          // Command arguments
	Env     map[string]string // Environment variables
	Dir     string            // Working directory
	Logger  *zap.Logger       // Logger for stderr output
}

// NewStdioTransport creates a new stdio transport for subprocess communication
func NewStdioTransport(config StdioConfig) (*StdioTransport, error) {
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	// Create command
	// #nosec G204 -- Intentional: MCP transport spawns server processes from trusted config
	cmd := exec.Command(config.Command, config.Args...)

	// Set working directory
	if config.Dir != "" {
		cmd.Dir = config.Dir
	}

	// Set environment
	cmd.Env = os.Environ()
	for k, v := range config.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// Create pipes
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start command
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Use bufio.Reader instead of Scanner to avoid buffer size limits
	// MCP servers can return arbitrarily large responses (e.g., extensive table lists, large JSON)
	reader := bufio.NewReader(stdout)

	t := &StdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		reader: reader,
		logger: config.Logger,
	}

	// Monitor stderr in background
	go t.monitorStderr()

	config.Logger.Info("MCP server started",
		zap.String("command", config.Command),
		zap.Strings("args", config.Args),
		zap.Int("pid", cmd.Process.Pid),
	)

	return t, nil
}

// monitorStderr reads and logs stderr output from the subprocess
func (s *StdioTransport) monitorStderr() {
	reader := bufio.NewReader(s.stderr)
	for {
		// Read until newline (no size limit)
		_, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				s.logger.Error("error reading stderr", zap.Error(err))
			}
			return
		}
		// Discard stderr output (MCP servers already log to their own files)
		// If debugging is needed, stderr can be monitored separately
	}
}

// Send implements Transport by writing to stdin
func (s *StdioTransport) Send(ctx context.Context, message []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("transport closed")
	}

	// Check context
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Write message followed by newline
	if _, err := s.stdin.Write(message); err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	if _, err := s.stdin.Write([]byte("\n")); err != nil {
		return fmt.Errorf("failed to write newline: %w", err)
	}

	return nil
}

// Receive implements Transport by reading from stdout
func (s *StdioTransport) Receive(ctx context.Context) ([]byte, error) {
	// Channel to receive read result
	type readResult struct {
		data []byte
		err  error
	}
	resultChan := make(chan readResult, 1)

	// Read in goroutine to respect context
	go func() {
		s.mu.Lock()
		if s.closed {
			resultChan <- readResult{nil, fmt.Errorf("transport closed")}
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		// Read until newline (no size limit with bufio.Reader)
		data, err := s.reader.ReadBytes('\n')
		if err != nil {
			resultChan <- readResult{nil, err}
			return
		}

		// Trim the trailing newline
		if len(data) > 0 && data[len(data)-1] == '\n' {
			data = data[:len(data)-1]
		}
		// Trim trailing carriage return (Windows)
		if len(data) > 0 && data[len(data)-1] == '\r' {
			data = data[:len(data)-1]
		}

		resultChan <- readResult{data, nil}
	}()

	// Wait for result or context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultChan:
		return result.data, result.err
	}
}

// Close implements Transport by closing pipes and waiting for process
func (s *StdioTransport) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	s.logger.Info("closing MCP server", zap.Int("pid", s.cmd.Process.Pid))

	// Close stdin to signal server to shutdown
	s.stdin.Close()

	// Wait for process to exit (with timeout)
	done := make(chan error, 1)
	go func() {
		done <- s.cmd.Wait()
	}()

	select {
	case err := <-done:
		// Process exited
		if err != nil {
			s.logger.Warn("MCP server exited with error", zap.Error(err))
		} else {
			s.logger.Info("MCP server exited cleanly")
		}
	case <-time.After(5 * time.Second):
		// Timeout - force kill
		s.logger.Warn("MCP server did not exit cleanly, killing process")
		if err := s.cmd.Process.Kill(); err != nil {
			s.logger.Error("failed to kill process", zap.Error(err))
		}
		<-done
	}

	s.stdout.Close()
	s.stderr.Close()

	return nil
}
