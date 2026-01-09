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
package fabric

import (
	"context"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
)

// InstrumentedBackend wraps any ExecutionBackend with observability instrumentation.
// It captures detailed traces and metrics for every backend operation, including:
// - Query execution with timing and result metrics
// - Schema operations
// - Resource listing
// - Metadata retrieval
// - Health checks
//
// This wrapper is transparent and can wrap any ExecutionBackend implementation.
type InstrumentedBackend struct {
	// backend is the underlying execution backend
	backend ExecutionBackend

	// tracer is used for creating spans
	tracer observability.Tracer
}

// NewInstrumentedBackend creates a new instrumented execution backend.
func NewInstrumentedBackend(backend ExecutionBackend, tracer observability.Tracer) *InstrumentedBackend {
	return &InstrumentedBackend{
		backend: backend,
		tracer:  tracer,
	}
}

// Name returns the underlying backend name.
func (ib *InstrumentedBackend) Name() string {
	return ib.backend.Name()
}

// ExecuteQuery executes a query with observability instrumentation.
func (ib *InstrumentedBackend) ExecuteQuery(ctx context.Context, query string) (*QueryResult, error) {
	// Start span
	ctx, span := ib.tracer.StartSpan(ctx, observability.SpanBackendQuery)
	defer ib.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrBackendType, ib.backend.Name())
	span.SetAttribute("query.length", len(query))

	// Truncate query for tracing (avoid huge spans)
	queryPreview := query
	if len(query) > 500 {
		queryPreview = query[:500] + "..."
	}
	span.SetAttribute("query.preview", queryPreview)

	// Record event: Query execution started
	span.AddEvent("backend.query.started", map[string]interface{}{
		"backend": ib.backend.Name(),
	})

	// Execute the query
	result, err := ib.backend.ExecuteQuery(ctx, query)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		// Record error event
		span.AddEvent("backend.query.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		// Emit error metric
		ib.tracer.RecordMetric("backend.errors.total", 1, map[string]string{
			observability.AttrBackendType: ib.backend.Name(),
			"operation":                   "query",
		})

		return nil, err
	}

	// Success - capture result metrics
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "",
	}

	// Capture result metadata
	span.SetAttribute("result.type", result.Type)
	span.SetAttribute("result.row_count", result.RowCount)
	span.SetAttribute("result.column_count", len(result.Columns))
	span.SetAttribute("execution.duration_ms", result.ExecutionStats.DurationMs)
	span.SetAttribute("execution.bytes_scanned", result.ExecutionStats.BytesScanned)
	span.SetAttribute("execution.rows_affected", result.ExecutionStats.RowsAffected)
	span.SetAttribute("execution.estimated_cost", result.ExecutionStats.EstimatedCost)

	// Record success event
	span.AddEvent("backend.query.completed", map[string]interface{}{
		"duration_ms": duration.Milliseconds(),
		"row_count":   result.RowCount,
		"bytes":       result.ExecutionStats.BytesScanned,
	})

	// Emit metrics
	ib.tracer.RecordMetric("backend.queries.total", 1, map[string]string{
		observability.AttrBackendType: ib.backend.Name(),
		"status":                      "success",
	})

	ib.tracer.RecordMetric("backend.query.duration", float64(duration.Milliseconds()), map[string]string{
		observability.AttrBackendType: ib.backend.Name(),
	})

	ib.tracer.RecordMetric("backend.query.rows", float64(result.RowCount), map[string]string{
		observability.AttrBackendType: ib.backend.Name(),
	})

	ib.tracer.RecordMetric("backend.query.bytes_scanned", float64(result.ExecutionStats.BytesScanned), map[string]string{
		observability.AttrBackendType: ib.backend.Name(),
	})

	return result, nil
}

// GetSchema retrieves schema with observability instrumentation.
func (ib *InstrumentedBackend) GetSchema(ctx context.Context, resource string) (*Schema, error) {
	ctx, span := ib.tracer.StartSpan(ctx, "backend.get_schema")
	defer ib.tracer.EndSpan(span)

	start := time.Now()

	span.SetAttribute(observability.AttrBackendType, ib.backend.Name())
	span.SetAttribute("resource.name", resource)

	schema, err := ib.backend.GetSchema(ctx, resource)

	duration := time.Since(start)

	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		ib.tracer.RecordMetric("backend.errors.total", 1, map[string]string{
			observability.AttrBackendType: ib.backend.Name(),
			"operation":                   "get_schema",
		})

		return nil, err
	}

	span.Status = observability.Status{Code: observability.StatusOK}
	span.SetAttribute("schema.field_count", len(schema.Fields))
	span.SetAttribute("schema.type", schema.Type)
	span.SetAttribute("duration_ms", duration.Milliseconds())

	return schema, nil
}

// ListResources lists resources with observability instrumentation.
func (ib *InstrumentedBackend) ListResources(ctx context.Context, filters map[string]string) ([]Resource, error) {
	ctx, span := ib.tracer.StartSpan(ctx, "backend.list_resources")
	defer ib.tracer.EndSpan(span)

	start := time.Now()

	span.SetAttribute(observability.AttrBackendType, ib.backend.Name())
	span.SetAttribute("filters.count", len(filters))

	resources, err := ib.backend.ListResources(ctx, filters)

	duration := time.Since(start)

	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		ib.tracer.RecordMetric("backend.errors.total", 1, map[string]string{
			observability.AttrBackendType: ib.backend.Name(),
			"operation":                   "list_resources",
		})

		return nil, err
	}

	span.Status = observability.Status{Code: observability.StatusOK}
	span.SetAttribute("resources.count", len(resources))
	span.SetAttribute("duration_ms", duration.Milliseconds())

	return resources, nil
}

// GetMetadata retrieves metadata with observability instrumentation.
func (ib *InstrumentedBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	ctx, span := ib.tracer.StartSpan(ctx, "backend.get_metadata")
	defer ib.tracer.EndSpan(span)

	start := time.Now()

	span.SetAttribute(observability.AttrBackendType, ib.backend.Name())
	span.SetAttribute("resource.name", resource)

	metadata, err := ib.backend.GetMetadata(ctx, resource)

	duration := time.Since(start)

	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		ib.tracer.RecordMetric("backend.errors.total", 1, map[string]string{
			observability.AttrBackendType: ib.backend.Name(),
			"operation":                   "get_metadata",
		})

		return nil, err
	}

	span.Status = observability.Status{Code: observability.StatusOK}
	span.SetAttribute("metadata.keys", len(metadata))
	span.SetAttribute("duration_ms", duration.Milliseconds())

	return metadata, nil
}

// Ping checks connectivity with observability instrumentation.
func (ib *InstrumentedBackend) Ping(ctx context.Context) error {
	ctx, span := ib.tracer.StartSpan(ctx, observability.SpanBackendConnect)
	defer ib.tracer.EndSpan(span)

	start := time.Now()

	span.SetAttribute(observability.AttrBackendType, ib.backend.Name())

	err := ib.backend.Ping(ctx)

	duration := time.Since(start)

	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		ib.tracer.RecordMetric("backend.ping.failures", 1, map[string]string{
			observability.AttrBackendType: ib.backend.Name(),
		})

		return err
	}

	span.Status = observability.Status{Code: observability.StatusOK}
	span.SetAttribute("duration_ms", duration.Milliseconds())

	ib.tracer.RecordMetric("backend.ping.success", 1, map[string]string{
		observability.AttrBackendType: ib.backend.Name(),
	})

	return nil
}

// Capabilities returns the underlying backend capabilities.
func (ib *InstrumentedBackend) Capabilities() *Capabilities {
	return ib.backend.Capabilities()
}

// ExecuteCustomOperation executes custom operations with observability.
func (ib *InstrumentedBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	ctx, span := ib.tracer.StartSpan(ctx, "backend.custom_operation")
	defer ib.tracer.EndSpan(span)

	start := time.Now()

	span.SetAttribute(observability.AttrBackendType, ib.backend.Name())
	span.SetAttribute("operation.name", op)
	span.SetAttribute("params.count", len(params))

	result, err := ib.backend.ExecuteCustomOperation(ctx, op, params)

	duration := time.Since(start)

	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		ib.tracer.RecordMetric("backend.errors.total", 1, map[string]string{
			observability.AttrBackendType: ib.backend.Name(),
			"operation":                   "custom",
			"custom_op":                   op,
		})

		return nil, err
	}

	span.Status = observability.Status{Code: observability.StatusOK}
	span.SetAttribute("duration_ms", duration.Milliseconds())

	ib.tracer.RecordMetric("backend.custom_operations.total", 1, map[string]string{
		observability.AttrBackendType: ib.backend.Name(),
		"operation":                   op,
	})

	return result, nil
}

// Close releases resources.
func (ib *InstrumentedBackend) Close() error {
	return ib.backend.Close()
}

// Ensure InstrumentedBackend implements ExecutionBackend interface
var _ ExecutionBackend = (*InstrumentedBackend)(nil)
