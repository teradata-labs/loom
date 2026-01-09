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
package docker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

// TestE2E_PythonExecution tests full Python execution workflow end-to-end.
//
// This test verifies:
// - Docker container creation
// - Python script execution
// - Stdout/stderr capture
// - Exit code handling
// - Distributed tracing (if tracer enabled)
func TestE2E_PythonExecution(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode (requires Docker daemon)")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)
	tracer := observability.NewMockTracer()

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	executor, err := NewDockerExecutor(ctx, DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
		Tracer:    tracer,
	})
	require.NoError(t, err)
	defer executor.Close()

	// Python script that uses loom_trace library (automatically injected)
	script := `
from loom_trace import tracer, trace_span
import time

print("Starting execution...")
print(f"Trace ID: {tracer.trace_id}")

with trace_span("data_processing"):
    time.sleep(0.05)
    print("Data processed")

print("Execution complete")
`

	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Command:     []string{"python3", "-c", script},
		Config: &loomv1.DockerBackendConfig{
			Name:        "e2e-python",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "python:3.11-slim",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	require.NoError(t, err, "Execution should succeed")
	assert.Equal(t, int32(0), resp.ExitCode, "Exit code should be 0")

	// Verify stdout captured correctly
	stdout := string(resp.Stdout)
	assert.Contains(t, stdout, "Starting execution...", "Stdout should contain start message")
	assert.Contains(t, stdout, "Trace ID:", "Stdout should contain trace ID")
	assert.Contains(t, stdout, "Data processed", "Stdout should contain processing message")
	assert.Contains(t, stdout, "Execution complete", "Stdout should contain completion message")

	// Wait for trace collection to complete
	time.Sleep(200 * time.Millisecond)

	// Verify trace spans were collected
	spans := tracer.GetSpans()
	require.GreaterOrEqual(t, len(spans), 1, "Should have at least 1 span (data_processing)")

	// Verify span names
	hasDataProcessing := false
	for _, span := range spans {
		if span.Name == "data_processing" {
			hasDataProcessing = true
			// Verify parent-child relationship
			assert.NotEmpty(t, span.ParentID, "Container span should have parent ID")
		}
	}
	assert.True(t, hasDataProcessing, "Should have data_processing span")
}

// TestE2E_NodeExecution tests full Node.js execution workflow end-to-end.
func TestE2E_NodeExecution(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode (requires Docker daemon)")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)
	tracer := observability.NewMockTracer()

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	executor, err := NewDockerExecutor(ctx, DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
		Tracer:    tracer,
	})
	require.NoError(t, err)
	defer executor.Close()

	// Node.js script that uses loom-trace library (automatically injected)
	script := `
const { tracer, traceSpanSync } = require('loom-trace.js');

console.log('Starting execution...');
console.log('Trace ID:', tracer.traceId);

traceSpanSync('compute', {}, () => {
    console.log('Computing...');
});

console.log('Execution complete');
`

	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
		Command:     []string{"node", "-e", script},
		Config: &loomv1.DockerBackendConfig{
			Name:        "e2e-node",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "node:20-alpine",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	require.NoError(t, err, "Execution should succeed")
	assert.Equal(t, int32(0), resp.ExitCode, "Exit code should be 0")

	// Verify stdout captured correctly
	stdout := string(resp.Stdout)
	assert.Contains(t, stdout, "Starting execution...", "Stdout should contain start message")
	assert.Contains(t, stdout, "Trace ID:", "Stdout should contain trace ID")
	assert.Contains(t, stdout, "Computing...", "Stdout should contain compute message")
	assert.Contains(t, stdout, "Execution complete", "Stdout should contain completion message")

	// Wait for trace collection to complete
	time.Sleep(200 * time.Millisecond)

	// Verify trace spans were collected
	spans := tracer.GetSpans()
	require.GreaterOrEqual(t, len(spans), 1, "Should have at least 1 span (compute)")

	// Verify span names
	hasCompute := false
	for _, span := range spans {
		if span.Name == "compute" {
			hasCompute = true
		}
	}
	assert.True(t, hasCompute, "Should have compute span")
}

// TestE2E_ErrorHandling tests non-zero exit codes are captured correctly.
func TestE2E_ErrorHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode (requires Docker daemon)")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	executor, err := NewDockerExecutor(ctx, DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
	})
	require.NoError(t, err)
	defer executor.Close()

	script := `
import sys
print("Starting...")
print("Error occurred", file=sys.stderr)
sys.exit(42)
`

	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Command:     []string{"python3", "-c", script},
		Config: &loomv1.DockerBackendConfig{
			Name:        "e2e-error",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "python:3.11-slim",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	require.NoError(t, err, "Execution should succeed (container ran)")
	assert.Equal(t, int32(42), resp.ExitCode, "Exit code should be 42")

	// Verify stdout/stderr captured
	stdout := string(resp.Stdout)
	stderr := string(resp.Stderr)
	assert.Contains(t, stdout, "Starting...")
	assert.Contains(t, stderr, "Error occurred")
}

// TestE2E_StderrCapture tests stderr is properly captured alongside traces.
func TestE2E_StderrCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode (requires Docker daemon)")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)
	tracer := observability.NewMockTracer()

	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	executor, err := NewDockerExecutor(ctx, DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
		Tracer:    tracer,
	})
	require.NoError(t, err)
	defer executor.Close()

	script := `
import sys
from loom_trace import trace_span

print("stdout message")
print("stderr message", file=sys.stderr)

with trace_span("operation"):
    print("inside operation")

print("stderr message 2", file=sys.stderr)
`

	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Command:     []string{"python3", "-c", script},
		Config: &loomv1.DockerBackendConfig{
			Name:        "e2e-stderr",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "python:3.11-slim",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.ExitCode)

	// Verify stdout/stderr captured correctly
	stdout := string(resp.Stdout)
	stderr := string(resp.Stderr)

	assert.Contains(t, stdout, "stdout message")
	assert.Contains(t, stdout, "inside operation")

	assert.Contains(t, stderr, "stderr message")
	assert.Contains(t, stderr, "stderr message 2")
	// Trace lines should be filtered out from stderr
	assert.NotContains(t, stderr, "__LOOM_TRACE__", "Stderr should not contain trace lines")

	// Wait for trace collection
	time.Sleep(200 * time.Millisecond)

	// Verify trace span was collected
	spans := tracer.GetSpans()
	require.GreaterOrEqual(t, len(spans), 1, "Should have at least 1 span")

	hasOperation := false
	for _, span := range spans {
		if span.Name == "operation" {
			hasOperation = true
		}
	}
	assert.True(t, hasOperation, "Should have operation span")
}
