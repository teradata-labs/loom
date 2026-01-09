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
// Package docker implements trace collection from Docker containers.
package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// TraceCollector collects trace spans from container stderr output.
//
// Container-side trace libraries (loom_trace.py, loom-trace.js) write spans
// to stderr with the prefix "__LOOM_TRACE__:". This collector parses those
// lines, deserializes the spans, and forwards them to the host tracer for
// export to Hawk.
//
// Thread Safety: Safe for concurrent use (can read from multiple containers).
type TraceCollector struct {
	tracer observability.Tracer
	logger *zap.Logger
	mu     sync.Mutex

	// Stats for monitoring
	spansCollected int64
	parseErrors    int64
}

// TraceCollectorConfig configures the trace collector.
type TraceCollectorConfig struct {
	Tracer observability.Tracer
	Logger *zap.Logger
}

// NewTraceCollector creates a new trace collector.
func NewTraceCollector(config TraceCollectorConfig) (*TraceCollector, error) {
	if config.Tracer == nil {
		return nil, fmt.Errorf("tracer is required")
	}

	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	return &TraceCollector{
		tracer: config.Tracer,
		logger: config.Logger,
	}, nil
}

// CollectFromReader reads from an io.Reader (typically container stderr)
// and collects trace spans written by container-side trace libraries.
//
// This method blocks until EOF or context cancellation.
// It's designed to be run in a goroutine per container execution.
//
// Format: __LOOM_TRACE__:{"trace_id":"...","span_id":"...","name":"..."}
//
// Example:
//
//	go collector.CollectFromReader(ctx, stderrPipe, containerID)
func (tc *TraceCollector) CollectFromReader(ctx context.Context, reader io.Reader, containerID string) error {
	scanner := bufio.NewScanner(reader)
	lineNum := 0

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		lineNum++
		line := scanner.Text()

		// Look for trace lines with special prefix
		if strings.HasPrefix(line, "__LOOM_TRACE__:") {
			jsonStr := strings.TrimPrefix(line, "__LOOM_TRACE__:")
			if err := tc.processTraceJSON(jsonStr, containerID); err != nil {
				tc.logger.Warn("Failed to parse trace line",
					zap.String("container_id", containerID),
					zap.Int("line", lineNum),
					zap.Error(err),
				)
				tc.mu.Lock()
				tc.parseErrors++
				tc.mu.Unlock()
			}
		} else if strings.HasPrefix(line, "__LOOM_TRACE_ERROR__:") {
			// Container-side trace library reported an error
			errMsg := strings.TrimPrefix(line, "__LOOM_TRACE_ERROR__:")
			tc.logger.Warn("Container trace library error",
				zap.String("container_id", containerID),
				zap.String("error", errMsg),
			)
		}
		// Ignore other lines (normal application output)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	return nil
}

// containerSpan represents the span format exported by container-side trace libraries.
// This matches the JSON format from loom_trace.py and loom-trace.js.
type containerSpan struct {
	TraceID    string                 `json:"trace_id"`
	SpanID     string                 `json:"span_id"`
	ParentID   string                 `json:"parent_id"`
	Name       string                 `json:"name"`
	StartTime  string                 `json:"start_time"` // ISO 8601 format
	EndTime    string                 `json:"end_time"`   // ISO 8601 format
	Attributes map[string]interface{} `json:"attributes"`
	Status     string                 `json:"status"` // "ok", "error", "unset"
}

// processTraceJSON deserializes a trace JSON line and forwards to host tracer.
func (tc *TraceCollector) processTraceJSON(jsonStr, containerID string) error {
	// Parse container span format (ISO 8601 timestamps)
	var cspan containerSpan
	if err := json.Unmarshal([]byte(jsonStr), &cspan); err != nil {
		return fmt.Errorf("failed to unmarshal span: %w", err)
	}

	// Validate span (basic sanity checks)
	if cspan.TraceID == "" {
		return fmt.Errorf("span missing trace_id")
	}
	if cspan.SpanID == "" {
		return fmt.Errorf("span missing span_id")
	}
	if cspan.Name == "" {
		return fmt.Errorf("span missing name")
	}

	// Parse ISO 8601 timestamps
	startTime, err := parseISO8601(cspan.StartTime)
	if err != nil {
		return fmt.Errorf("invalid start_time: %w", err)
	}

	endTime, err := parseISO8601(cspan.EndTime)
	if err != nil {
		return fmt.Errorf("invalid end_time: %w", err)
	}

	// Convert to observability.Span
	span := &observability.Span{
		TraceID:    cspan.TraceID,
		SpanID:     cspan.SpanID,
		ParentID:   cspan.ParentID,
		Name:       cspan.Name,
		StartTime:  startTime,
		EndTime:    endTime,
		Duration:   endTime.Sub(startTime),
		Attributes: cspan.Attributes,
		Status:     statusFromString(cspan.Status),
	}

	// Add container metadata to span
	if span.Attributes == nil {
		span.Attributes = make(map[string]interface{})
	}
	span.Attributes["container.id"] = containerID
	span.Attributes["container.source"] = true // Mark as from container

	tc.logger.Debug("Collected trace span from container",
		zap.String("container_id", containerID),
		zap.String("trace_id", span.TraceID),
		zap.String("span_id", span.SpanID),
		zap.String("name", span.Name),
		zap.String("status", span.Status.Code.String()),
		zap.Duration("duration", span.Duration),
	)

	// Forward span to Hawk via host tracer
	// Note: EndSpan is a bit of a misnomer here - it actually "exports" a completed span
	tc.tracer.EndSpan(span)

	tc.mu.Lock()
	tc.spansCollected++
	tc.mu.Unlock()

	return nil
}

// GetStats returns collector statistics.
func (tc *TraceCollector) GetStats() (spansCollected int64, parseErrors int64) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.spansCollected, tc.parseErrors
}

// Reset resets collector statistics (for testing).
func (tc *TraceCollector) Reset() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.spansCollected = 0
	tc.parseErrors = 0
}

// parseISO8601 parses ISO 8601 timestamp string to time.Time.
// Supports formats: 2006-01-02T15:04:05Z, 2006-01-02T15:04:05.999Z
func parseISO8601(timestamp string) (time.Time, error) {
	// Try RFC3339 with nanoseconds first
	t, err := time.Parse(time.RFC3339Nano, timestamp)
	if err == nil {
		return t, nil
	}

	// Try RFC3339 without nanoseconds
	t, err = time.Parse(time.RFC3339, timestamp)
	if err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("invalid ISO 8601 timestamp: %s", timestamp)
}

// statusFromString converts status string to observability.Status.
func statusFromString(status string) observability.Status {
	switch strings.ToLower(status) {
	case "ok":
		return observability.Status{Code: observability.StatusOK}
	case "error":
		return observability.Status{Code: observability.StatusError}
	default:
		return observability.Status{Code: observability.StatusUnset}
	}
}

// FilteringWriter wraps an io.Writer and filters out trace lines before writing.
// This is used to remove __LOOM_TRACE__ lines from stderr output.
type FilteringWriter struct {
	underlying io.Writer
	buffer     []byte // buffer for incomplete lines
}

// NewFilteringWriter creates a new filtering writer.
func NewFilteringWriter(w io.Writer) *FilteringWriter {
	return &FilteringWriter{
		underlying: w,
		buffer:     make([]byte, 0, 4096),
	}
}

// Write implements io.Writer. It filters out lines starting with __LOOM_TRACE__.
func (fw *FilteringWriter) Write(p []byte) (n int, err error) {
	// Always report that we wrote all bytes (even if we filter some)
	n = len(p)

	// Append to buffer
	fw.buffer = append(fw.buffer, p...)

	// Process complete lines
	for {
		idx := strings.IndexByte(string(fw.buffer), '\n')
		if idx == -1 {
			break // No complete line yet
		}

		// Extract line (including newline)
		line := fw.buffer[:idx+1]
		fw.buffer = fw.buffer[idx+1:]

		// Filter out trace lines
		lineStr := string(line)
		if !strings.HasPrefix(lineStr, "__LOOM_TRACE__:") &&
			!strings.HasPrefix(lineStr, "__LOOM_TRACE_ERROR__:") {
			// Write non-trace line to underlying writer
			if _, err := fw.underlying.Write(line); err != nil {
				return n, err
			}
		}
		// Trace lines are silently dropped
	}

	return n, nil
}

// Flush writes any remaining buffered data (called at end of stream).
func (fw *FilteringWriter) Flush() error {
	if len(fw.buffer) > 0 {
		// Write remaining data (no newline at end)
		lineStr := string(fw.buffer)
		if !strings.HasPrefix(lineStr, "__LOOM_TRACE__:") &&
			!strings.HasPrefix(lineStr, "__LOOM_TRACE_ERROR__:") {
			if _, err := fw.underlying.Write(fw.buffer); err != nil {
				return err
			}
		}
		fw.buffer = fw.buffer[:0]
	}
	return nil
}
