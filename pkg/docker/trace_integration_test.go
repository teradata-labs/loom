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
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

// TestTraceCollector_PythonIntegration tests end-to-end Python trace propagation.
//
// Flow:
//  1. Create DockerExecutor with memory tracer
//  2. Execute Python script that uses loom_trace.py
//  3. Verify trace context injected (LOOM_TRACE_ID, LOOM_SPAN_ID, LOOM_TRACE_BAGGAGE)
//  4. Verify container spans collected and forwarded to tracer
//  5. Verify parent-child span relationships
func TestTraceCollector_PythonIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create mock tracer
	tracer := observability.NewMockTracer()

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor with tracer
	executor, err := NewDockerExecutor(ctx, DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
		Tracer:    tracer,
	})
	require.NoError(t, err)
	defer executor.Close()

	// Python script that creates child spans
	pythonScript := `
import sys
import json
import os

# Verify environment variables injected
trace_id = os.environ.get("LOOM_TRACE_ID", "")
span_id = os.environ.get("LOOM_SPAN_ID", "")
baggage = os.environ.get("LOOM_TRACE_BAGGAGE", "")

print(f"TRACE_ID={trace_id}")
print(f"SPAN_ID={span_id}")
print(f"BAGGAGE={baggage}")

# Simulate loom_trace.py span export
import uuid
from datetime import datetime

child_span = {
    "trace_id": trace_id,
    "span_id": str(uuid.uuid4()),
    "parent_id": span_id,
    "name": "python_operation",
    "start_time": datetime.utcnow().isoformat() + "Z",
    "end_time": datetime.utcnow().isoformat() + "Z",
    "attributes": {"language": "python", "operation": "test"},
    "status": "ok"
}

# Export to stderr with magic prefix
print(f"__LOOM_TRACE__:{json.dumps(child_span)}", file=sys.stderr, flush=True)
`

	// Execute Python script
	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Command:     []string{"python3", "-c", pythonScript},
		Config: &loomv1.DockerBackendConfig{
			Name:        "trace-test-python",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "python:3.11-slim",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.ExitCode)

	// Verify trace context injected (check stdout)
	stdout := string(resp.Stdout)
	assert.Contains(t, stdout, "TRACE_ID=")
	assert.Contains(t, stdout, "SPAN_ID=")
	assert.NotContains(t, stdout, "TRACE_ID=\n") // Should have value

	// Verify spans collected
	spans := tracer.GetSpans()
	require.GreaterOrEqual(t, len(spans), 2, "Expected at least docker.execute + python_operation spans")

	// Find docker.execute span (host)
	var dockerSpan *observability.Span
	for _, span := range spans {
		if span.Name == "docker.execute" {
			dockerSpan = span
			break
		}
	}
	require.NotNil(t, dockerSpan, "docker.execute span not found")

	// Find python_operation span (container)
	var pythonSpan *observability.Span
	for _, span := range spans {
		if span.Name == "python_operation" {
			pythonSpan = span
			break
		}
	}
	require.NotNil(t, pythonSpan, "python_operation span not found")

	// Verify parent-child relationship
	assert.Equal(t, dockerSpan.TraceID, pythonSpan.TraceID, "Spans should share trace ID")
	assert.Equal(t, dockerSpan.SpanID, pythonSpan.ParentID, "Python span should be child of docker.execute")

	// Verify container metadata added
	assert.Equal(t, resp.ContainerId, pythonSpan.Attributes["container.id"])
	assert.Equal(t, true, pythonSpan.Attributes["container.source"])

	// Verify span attributes
	assert.Equal(t, "python", pythonSpan.Attributes["language"])
	assert.Equal(t, "test", pythonSpan.Attributes["operation"])
}

// TestTraceCollector_NodeIntegration tests end-to-end Node.js trace propagation.
func TestTraceCollector_NodeIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create mock tracer
	tracer := observability.NewMockTracer()

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor with tracer
	executor, err := NewDockerExecutor(ctx, DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
		Tracer:    tracer,
	})
	require.NoError(t, err)
	defer executor.Close()

	// Node.js script that creates child spans
	nodeScript := `
const crypto = require('crypto');

// Verify environment variables injected
const traceId = process.env.LOOM_TRACE_ID || '';
const spanId = process.env.LOOM_SPAN_ID || '';
const baggage = process.env.LOOM_TRACE_BAGGAGE || '';

console.log('TRACE_ID=' + traceId);
console.log('SPAN_ID=' + spanId);
console.log('BAGGAGE=' + baggage);

// Simulate loom-trace.js span export
const childSpan = {
  trace_id: traceId,
  span_id: crypto.randomUUID(),
  parent_id: spanId,
  name: 'node_operation',
  start_time: new Date().toISOString(),
  end_time: new Date().toISOString(),
  attributes: { language: 'node', operation: 'test' },
  status: 'ok'
};

// Export to stderr with magic prefix
process.stderr.write('__LOOM_TRACE__:' + JSON.stringify(childSpan) + '\n');
`

	// Execute Node.js script
	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
		Command:     []string{"node", "-e", nodeScript},
		Config: &loomv1.DockerBackendConfig{
			Name:        "trace-test-node",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_NODE,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "node:20-slim",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.ExitCode)

	// Verify trace context injected
	stdout := string(resp.Stdout)
	assert.Contains(t, stdout, "TRACE_ID=")
	assert.Contains(t, stdout, "SPAN_ID=")

	// Verify spans collected
	spans := tracer.GetSpans()
	require.GreaterOrEqual(t, len(spans), 2)

	// Find node_operation span
	var nodeSpan *observability.Span
	for _, span := range spans {
		if span.Name == "node_operation" {
			nodeSpan = span
			break
		}
	}
	require.NotNil(t, nodeSpan, "node_operation span not found")

	// Verify container metadata
	assert.Equal(t, resp.ContainerId, nodeSpan.Attributes["container.id"])
	assert.Equal(t, true, nodeSpan.Attributes["container.source"])
}

// TestTraceCollector_MultipleSpans tests collecting multiple spans from single execution.
func TestTraceCollector_MultipleSpans(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create mock tracer
	tracer := observability.NewMockTracer()

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor with tracer
	executor, err := NewDockerExecutor(ctx, DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
		Tracer:    tracer,
	})
	require.NoError(t, err)
	defer executor.Close()

	// Python script that creates multiple spans
	pythonScript := `
import sys
import json
import os
import uuid
from datetime import datetime

trace_id = os.environ.get("LOOM_TRACE_ID", "")
parent_span_id = os.environ.get("LOOM_SPAN_ID", "")

# Create 3 sequential spans
for i in range(3):
    span = {
        "trace_id": trace_id,
        "span_id": str(uuid.uuid4()),
        "parent_id": parent_span_id,
        "name": f"operation_{i+1}",
        "start_time": datetime.utcnow().isoformat() + "Z",
        "end_time": datetime.utcnow().isoformat() + "Z",
        "attributes": {"index": i+1},
        "status": "ok"
    }
    print(f"__LOOM_TRACE__:{json.dumps(span)}", file=sys.stderr, flush=True)

print("Created 3 spans")
`

	// Execute Python script
	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Command:     []string{"python3", "-c", pythonScript},
		Config: &loomv1.DockerBackendConfig{
			Name:        "trace-test-multi",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "python:3.11-slim",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.ExitCode)

	// Verify stdout
	assert.Contains(t, string(resp.Stdout), "Created 3 spans")

	// Verify 3 container spans collected (plus docker.execute span)
	spans := tracer.GetSpans()
	require.GreaterOrEqual(t, len(spans), 4, "Expected docker.execute + 3 operation spans")

	// Count operation_N spans
	operationCount := 0
	for _, span := range spans {
		if strings.HasPrefix(span.Name, "operation_") {
			operationCount++
		}
	}
	assert.Equal(t, 3, operationCount, "Expected 3 operation spans")
}

// TestTraceCollector_ErrorHandling tests trace collection with span errors.
func TestTraceCollector_ErrorHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create mock tracer
	tracer := observability.NewMockTracer()

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor with tracer
	executor, err := NewDockerExecutor(ctx, DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
		Tracer:    tracer,
	})
	require.NoError(t, err)
	defer executor.Close()

	// Python script with error span
	pythonScript := `
import sys
import json
import os
import uuid
from datetime import datetime

trace_id = os.environ.get("LOOM_TRACE_ID", "")
parent_span_id = os.environ.get("LOOM_SPAN_ID", "")

# Create error span
span = {
    "trace_id": trace_id,
    "span_id": str(uuid.uuid4()),
    "parent_id": parent_span_id,
    "name": "failed_operation",
    "start_time": datetime.utcnow().isoformat() + "Z",
    "end_time": datetime.utcnow().isoformat() + "Z",
    "attributes": {"error_message": "Something went wrong"},
    "status": "error"
}
print(f"__LOOM_TRACE__:{json.dumps(span)}", file=sys.stderr, flush=True)
`

	// Execute Python script
	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Command:     []string{"python3", "-c", pythonScript},
		Config: &loomv1.DockerBackendConfig{
			Name:        "trace-test-error",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "python:3.11-slim",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	require.NoError(t, err) // Script succeeds even with error span
	assert.Equal(t, int32(0), resp.ExitCode)

	// Find error span
	spans := tracer.GetSpans()
	var errorSpan *observability.Span
	for _, span := range spans {
		if span.Name == "failed_operation" {
			errorSpan = span
			break
		}
	}
	require.NotNil(t, errorSpan)

	// Verify error status
	assert.Equal(t, observability.StatusError, errorSpan.Status.Code)
	assert.Equal(t, "Something went wrong", errorSpan.Attributes["error_message"])
}

// TestTraceCollector_BaggagePropagation tests W3C baggage propagation.
func TestTraceCollector_BaggagePropagation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create mock tracer
	tracer := observability.NewMockTracer()

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor with tracer
	executor, err := NewDockerExecutor(ctx, DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
		Tracer:    tracer,
	})
	require.NoError(t, err)
	defer executor.Close()

	// Start span with tenant context
	ctx, parentSpan := tracer.StartSpan(ctx, "test_baggage",
		observability.WithAttribute("tenant_id", "acme-corp"),
		observability.WithAttribute("org_id", "engineering"),
	)
	defer tracer.EndSpan(parentSpan)

	// Python script that reads baggage
	pythonScript := `
import sys
import os

baggage = os.environ.get("LOOM_TRACE_BAGGAGE", "")
print(f"BAGGAGE={baggage}")

# Parse baggage
parts = baggage.split(",")
for part in parts:
    if "=" in part:
        key, val = part.split("=", 1)
        print(f"{key}={val}")
`

	// Execute Python script with baggage context
	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Command:     []string{"python3", "-c", pythonScript},
		Config: &loomv1.DockerBackendConfig{
			Name:        "trace-test-baggage",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "python:3.11-slim",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.ExitCode)

	// Verify baggage propagated
	stdout := string(resp.Stdout)
	assert.Contains(t, stdout, "tenant_id=acme-corp")
	assert.Contains(t, stdout, "org_id=engineering")
}

// TestTraceCollector_NoTracer tests graceful behavior when tracing disabled.
func TestTraceCollector_NoTracer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor WITHOUT tracer
	executor, err := NewDockerExecutor(ctx, DockerExecutorConfig{
		Scheduler: scheduler,
		Logger:    logger,
		Tracer:    nil, // No tracer!
	})
	require.NoError(t, err)
	defer executor.Close()

	// Python script that tries to use tracing
	pythonScript := `
import os
trace_id = os.environ.get("LOOM_TRACE_ID", "not-set")
print(f"TRACE_ID={trace_id}")
`

	// Execute Python script
	req := &loomv1.ExecuteRequest{
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		Command:     []string{"python3", "-c", pythonScript},
		Config: &loomv1.DockerBackendConfig{
			Name:        "trace-test-disabled",
			RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
			ImageSource: &loomv1.DockerBackendConfig_BaseImage{
				BaseImage: "python:3.11-slim",
			},
		},
	}

	resp, err := executor.Execute(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.ExitCode)

	// Verify no trace context injected
	stdout := string(resp.Stdout)
	assert.Contains(t, stdout, "TRACE_ID=not-set")
}

// TestTraceCollector_Stats tests trace collector statistics.
func TestTraceCollector_Stats(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracer := observability.NewMockTracer()

	collector, err := NewTraceCollector(TraceCollectorConfig{
		Tracer: tracer,
		Logger: logger,
	})
	require.NoError(t, err)

	// Initial stats should be zero
	collected, errors := collector.GetStats()
	assert.Equal(t, int64(0), collected)
	assert.Equal(t, int64(0), errors)

	// Create pipe for testing
	reader, writer := io.Pipe()

	// Start collection in goroutine
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- collector.CollectFromReader(ctx, reader, "test-container")
	}()

	// Write valid trace line
	validTrace := `__LOOM_TRACE__:{"trace_id":"abc123","span_id":"def456","parent_id":"ghi789","name":"test","start_time":"2025-01-15T10:30:00Z","end_time":"2025-01-15T10:30:01Z","attributes":{},"status":"ok"}` + "\n"
	if _, err := writer.Write([]byte(validTrace)); err != nil {
		t.Fatalf("Failed to write valid trace: %v", err)
	}

	// Write invalid trace line (missing trace_id)
	invalidTrace := `__LOOM_TRACE__:{"span_id":"xyz"}` + "\n"
	if _, err := writer.Write([]byte(invalidTrace)); err != nil {
		t.Fatalf("Failed to write invalid trace: %v", err)
	}

	// Close writer to signal EOF
	writer.Close()

	// Wait for collection to complete
	<-done

	// Verify stats
	collected, errors = collector.GetStats()
	assert.Equal(t, int64(1), collected, "Should have collected 1 valid span")
	assert.Equal(t, int64(1), errors, "Should have 1 parse error")

	// Reset and verify
	collector.Reset()
	collected, errors = collector.GetStats()
	assert.Equal(t, int64(0), collected)
	assert.Equal(t, int64(0), errors)
}

// TestTraceCollector_ContextCancellation tests graceful shutdown on context cancellation.
func TestTraceCollector_ContextCancellation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracer := observability.NewMockTracer()

	collector, err := NewTraceCollector(TraceCollectorConfig{
		Tracer: tracer,
		Logger: logger,
	})
	require.NoError(t, err)

	// Create pipe
	reader, writer := io.Pipe()

	// Create cancelable context
	ctx, cancel := context.WithCancel(context.Background())

	// Start collection in goroutine
	done := make(chan error, 1)
	go func() {
		done <- collector.CollectFromReader(ctx, reader, "test-container")
	}()

	// Write a valid trace
	validTrace := `__LOOM_TRACE__:{"trace_id":"trace-123","span_id":"span-456","parent_id":"parent-789","name":"test_span","start_time":"2025-01-01T00:00:00Z","end_time":"2025-01-01T00:00:01Z","attributes":{},"status":"ok"}` + "\n"
	_, writeErr := writer.Write([]byte(validTrace))
	require.NoError(t, writeErr)

	// Give collector time to process
	time.Sleep(50 * time.Millisecond)

	// Cancel context (should trigger graceful shutdown)
	cancel()

	// Close writer
	writer.Close()

	// Wait for collection to complete
	err = <-done
	// Context cancellation should cause context.Canceled error or EOF
	assert.True(t, err == context.Canceled || err == io.EOF || err == nil,
		"Expected context.Canceled, EOF, or nil, got: %v", err)

	// Verify at least one span was collected before cancellation
	collected, _ := collector.GetStats()
	assert.GreaterOrEqual(t, collected, int64(1), "Should have collected at least 1 span before cancellation")
}

// TestTraceCollector_PipeWriteFailure tests handling of pipe write failures.
func TestTraceCollector_PipeWriteFailure(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracer := observability.NewMockTracer()

	collector, err := NewTraceCollector(TraceCollectorConfig{
		Tracer: tracer,
		Logger: logger,
	})
	require.NoError(t, err)

	// Create pipe
	reader, writer := io.Pipe()

	ctx := context.Background()

	// Start collection in goroutine
	done := make(chan error, 1)
	go func() {
		done <- collector.CollectFromReader(ctx, reader, "test-container")
	}()

	// Close writer immediately to cause write failure
	writer.Close()

	// Wait for collection to complete
	collectionErr := <-done
	// Should get EOF when writer is closed
	assert.True(t, collectionErr == io.EOF || collectionErr == nil,
		"Expected EOF or nil when pipe closed, got: %v", collectionErr)

	// Verify no spans collected due to immediate closure
	collected, _ := collector.GetStats()
	assert.Equal(t, int64(0), collected, "Should have collected 0 spans due to immediate pipe closure")
}

// TestTraceCollector_MalformedJSON tests handling of various malformed JSON scenarios.
func TestTraceCollector_MalformedJSON(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
	}{
		{
			name:        "empty JSON object",
			input:       `__LOOM_TRACE__:{}`,
			expectError: true,
		},
		{
			name:        "missing trace_id",
			input:       `__LOOM_TRACE__:{"span_id":"span-123","name":"test"}`,
			expectError: true,
		},
		{
			name:        "invalid JSON syntax",
			input:       `__LOOM_TRACE__:{invalid json}`,
			expectError: true,
		},
		{
			name:        "truncated JSON",
			input:       `__LOOM_TRACE__:{"trace_id":"trace-123","span_id":"span-456"`,
			expectError: true,
		},
		{
			name:        "extra fields (should succeed)",
			input:       `__LOOM_TRACE__:{"trace_id":"trace-123","span_id":"span-456","parent_id":"parent-789","name":"test","start_time":"2025-01-01T00:00:00Z","end_time":"2025-01-01T00:00:01Z","attributes":{},"status":"ok","extra_field":"ignored"}`,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := zaptest.NewLogger(t)
			tracer := observability.NewMockTracer()

			collector, err := NewTraceCollector(TraceCollectorConfig{
				Tracer: tracer,
				Logger: logger,
			})
			require.NoError(t, err)
			collector.Reset() // Reset stats for each test

			// Create pipe
			reader, writer := io.Pipe()

			ctx := context.Background()

			// Start collection in goroutine
			done := make(chan error, 1)
			go func() {
				done <- collector.CollectFromReader(ctx, reader, "test-container")
			}()

			// Write trace line
			_, writeErr := writer.Write([]byte(tt.input + "\n"))
			require.NoError(t, writeErr)

			// Close writer
			writer.Close()

			// Wait for collection to complete
			<-done

			// Verify stats
			collected, errors := collector.GetStats()
			if tt.expectError {
				assert.Equal(t, int64(0), collected, "Should have collected 0 valid spans")
				assert.Equal(t, int64(1), errors, "Should have 1 parse error")
			} else {
				assert.Equal(t, int64(1), collected, "Should have collected 1 valid span")
				assert.Equal(t, int64(0), errors, "Should have 0 parse errors")
			}
		})
	}
}
