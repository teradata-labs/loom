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
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
)

// testBackendForInstrumentation is a test implementation of ExecutionBackend
type testBackendForInstrumentation struct {
	mu           sync.Mutex
	name         string
	queryResult  *QueryResult
	queryError   error
	schema       *Schema
	schemaError  error
	resources    []Resource
	resourcesErr error
	metadata     map[string]interface{}
	metadataErr  error
	pingError    error
	customResult interface{}
	customError  error
	capabilities *Capabilities
	queryCalled  bool
	schemaCalled bool
	listCalled   bool
	metaCalled   bool
	pingCalled   bool
	customCalled bool
}

func (m *testBackendForInstrumentation) Name() string {
	return m.name
}

func (m *testBackendForInstrumentation) ExecuteQuery(ctx context.Context, query string) (*QueryResult, error) {
	m.mu.Lock()
	m.queryCalled = true
	queryError := m.queryError
	queryResult := m.queryResult
	m.mu.Unlock()

	if queryError != nil {
		return nil, queryError
	}
	return queryResult, nil
}

func (m *testBackendForInstrumentation) GetSchema(ctx context.Context, resource string) (*Schema, error) {
	m.mu.Lock()
	m.schemaCalled = true
	schemaError := m.schemaError
	schema := m.schema
	m.mu.Unlock()

	if schemaError != nil {
		return nil, schemaError
	}
	return schema, nil
}

func (m *testBackendForInstrumentation) ListResources(ctx context.Context, filters map[string]string) ([]Resource, error) {
	m.mu.Lock()
	m.listCalled = true
	resourcesErr := m.resourcesErr
	resources := m.resources
	m.mu.Unlock()

	if resourcesErr != nil {
		return nil, resourcesErr
	}
	return resources, nil
}

func (m *testBackendForInstrumentation) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	m.mu.Lock()
	m.metaCalled = true
	metadataErr := m.metadataErr
	metadata := m.metadata
	m.mu.Unlock()

	if metadataErr != nil {
		return nil, metadataErr
	}
	return metadata, nil
}

func (m *testBackendForInstrumentation) Ping(ctx context.Context) error {
	m.mu.Lock()
	m.pingCalled = true
	pingError := m.pingError
	m.mu.Unlock()

	return pingError
}

func (m *testBackendForInstrumentation) Capabilities() *Capabilities {
	return m.capabilities
}

func (m *testBackendForInstrumentation) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	m.mu.Lock()
	m.customCalled = true
	customError := m.customError
	customResult := m.customResult
	m.mu.Unlock()

	if customError != nil {
		return nil, customError
	}
	return customResult, nil
}

func (m *testBackendForInstrumentation) Close() error {
	return nil
}

func TestInstrumentedBackend_Name(t *testing.T) {
	mock := &testBackendForInstrumentation{name: "test-backend"}
	tracer := observability.NewNoOpTracer()
	instrumented := NewInstrumentedBackend(mock, tracer)

	assert.Equal(t, "test-backend", instrumented.Name())
}

func TestInstrumentedBackend_ExecuteQuery_Success(t *testing.T) {
	// Create mock tracer to capture spans
	mockTracer := observability.NewMockTracer()

	// Create mock backend
	mock := &testBackendForInstrumentation{
		name: "test-backend",
		queryResult: &QueryResult{
			Type:     "rows",
			RowCount: 10,
			Columns:  []Column{{Name: "id", Type: "int"}},
			ExecutionStats: ExecutionStats{
				DurationMs:   100,
				BytesScanned: 1024,
			},
		},
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	// Execute query
	ctx := context.Background()
	result, err := instrumented.ExecuteQuery(ctx, "SELECT * FROM test")

	// Verify result
	require.NoError(t, err)
	assert.Equal(t, 10, result.RowCount)
	assert.True(t, mock.queryCalled)

	// Verify span was created
	spans := mockTracer.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, observability.SpanBackendQuery, span.Name)
	assert.Equal(t, observability.StatusOK, span.Status.Code)
	assert.Equal(t, "test-backend", span.Attributes[observability.AttrBackendType])
	assert.Equal(t, "rows", span.Attributes["result.type"])
	assert.Equal(t, 10, span.Attributes["result.row_count"])
	assert.Equal(t, 1, span.Attributes["result.column_count"])
	assert.Equal(t, int64(100), span.Attributes["execution.duration_ms"])
	assert.Equal(t, int64(1024), span.Attributes["execution.bytes_scanned"])
}

func TestInstrumentedBackend_ExecuteQuery_Error(t *testing.T) {
	mockTracer := observability.NewMockTracer()

	mock := &testBackendForInstrumentation{
		name:       "test-backend",
		queryError: errors.New("query failed"),
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	ctx := context.Background()
	_, err := instrumented.ExecuteQuery(ctx, "SELECT * FROM test")

	// Verify error
	require.Error(t, err)
	assert.Equal(t, "query failed", err.Error())

	// Verify span captured error
	spans := mockTracer.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, observability.StatusError, span.Status.Code)
	assert.Equal(t, "query failed", span.Status.Message)
	assert.NotNil(t, span.Attributes[observability.AttrErrorMessage])
}

func TestInstrumentedBackend_GetSchema_Success(t *testing.T) {
	mockTracer := observability.NewMockTracer()

	mock := &testBackendForInstrumentation{
		name: "test-backend",
		schema: &Schema{
			Name: "test_table",
			Type: "table",
			Fields: []Field{
				{Name: "id", Type: "int"},
				{Name: "name", Type: "string"},
			},
		},
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	ctx := context.Background()
	schema, err := instrumented.GetSchema(ctx, "test_table")

	require.NoError(t, err)
	assert.Equal(t, "test_table", schema.Name)
	assert.Len(t, schema.Fields, 2)
	assert.True(t, mock.schemaCalled)

	spans := mockTracer.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, "backend.get_schema", span.Name)
	assert.Equal(t, observability.StatusOK, span.Status.Code)
	assert.Equal(t, 2, span.Attributes["schema.field_count"])
	assert.Equal(t, "table", span.Attributes["schema.type"])
}

func TestInstrumentedBackend_ListResources_Success(t *testing.T) {
	mockTracer := observability.NewMockTracer()

	mock := &testBackendForInstrumentation{
		name: "test-backend",
		resources: []Resource{
			{Name: "table1", Type: "table"},
			{Name: "table2", Type: "table"},
			{Name: "view1", Type: "view"},
		},
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	ctx := context.Background()
	resources, err := instrumented.ListResources(ctx, map[string]string{"type": "table"})

	require.NoError(t, err)
	assert.Len(t, resources, 3)
	assert.True(t, mock.listCalled)

	spans := mockTracer.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, "backend.list_resources", span.Name)
	assert.Equal(t, observability.StatusOK, span.Status.Code)
	assert.Equal(t, 3, span.Attributes["resources.count"])
	assert.Equal(t, 1, span.Attributes["filters.count"])
}

func TestInstrumentedBackend_GetMetadata_Success(t *testing.T) {
	mockTracer := observability.NewMockTracer()

	mock := &testBackendForInstrumentation{
		name: "test-backend",
		metadata: map[string]interface{}{
			"row_count":    1000,
			"size_bytes":   1024000,
			"last_updated": "2025-01-01",
		},
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	ctx := context.Background()
	metadata, err := instrumented.GetMetadata(ctx, "test_table")

	require.NoError(t, err)
	assert.Len(t, metadata, 3)
	assert.True(t, mock.metaCalled)

	spans := mockTracer.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, "backend.get_metadata", span.Name)
	assert.Equal(t, observability.StatusOK, span.Status.Code)
	assert.Equal(t, 3, span.Attributes["metadata.keys"])
}

func TestInstrumentedBackend_Ping_Success(t *testing.T) {
	mockTracer := observability.NewMockTracer()

	mock := &testBackendForInstrumentation{
		name: "test-backend",
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	ctx := context.Background()
	err := instrumented.Ping(ctx)

	require.NoError(t, err)
	assert.True(t, mock.pingCalled)

	spans := mockTracer.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, observability.SpanBackendConnect, span.Name)
	assert.Equal(t, observability.StatusOK, span.Status.Code)
}

func TestInstrumentedBackend_Ping_Error(t *testing.T) {
	mockTracer := observability.NewMockTracer()

	mock := &testBackendForInstrumentation{
		name:      "test-backend",
		pingError: errors.New("connection failed"),
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	ctx := context.Background()
	err := instrumented.Ping(ctx)

	require.Error(t, err)
	assert.Equal(t, "connection failed", err.Error())

	spans := mockTracer.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, observability.StatusError, span.Status.Code)
}

func TestInstrumentedBackend_ExecuteCustomOperation_Success(t *testing.T) {
	mockTracer := observability.NewMockTracer()

	mock := &testBackendForInstrumentation{
		name:         "test-backend",
		customResult: map[string]interface{}{"status": "success"},
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	ctx := context.Background()
	result, err := instrumented.ExecuteCustomOperation(ctx, "vacuum", map[string]interface{}{"table": "test"})

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, mock.customCalled)

	spans := mockTracer.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, "backend.custom_operation", span.Name)
	assert.Equal(t, observability.StatusOK, span.Status.Code)
	assert.Equal(t, "vacuum", span.Attributes["operation.name"])
	assert.Equal(t, 1, span.Attributes["params.count"])
}

func TestInstrumentedBackend_Capabilities(t *testing.T) {
	mock := &testBackendForInstrumentation{
		name: "test-backend",
		capabilities: &Capabilities{
			SupportsTransactions: true,
			SupportsConcurrency:  true,
			MaxConcurrentOps:     10,
		},
	}

	tracer := observability.NewNoOpTracer()
	instrumented := NewInstrumentedBackend(mock, tracer)

	caps := instrumented.Capabilities()
	assert.True(t, caps.SupportsTransactions)
	assert.True(t, caps.SupportsConcurrency)
	assert.Equal(t, 10, caps.MaxConcurrentOps)
}

func TestInstrumentedBackend_Close(t *testing.T) {
	mock := &testBackendForInstrumentation{name: "test-backend"}
	tracer := observability.NewNoOpTracer()
	instrumented := NewInstrumentedBackend(mock, tracer)

	err := instrumented.Close()
	assert.NoError(t, err)
}

func TestInstrumentedBackend_ConcurrentQueries(t *testing.T) {
	mockTracer := observability.NewMockTracer()

	mock := &testBackendForInstrumentation{
		name: "test-backend",
		queryResult: &QueryResult{
			Type:     "rows",
			RowCount: 1,
			ExecutionStats: ExecutionStats{
				DurationMs: 10,
			},
		},
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	// Execute queries concurrently
	concurrency := 20
	done := make(chan bool, concurrency)
	errs := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			ctx := context.Background()
			_, err := instrumented.ExecuteQuery(ctx, "SELECT 1")
			errs <- err
			done <- true
		}(i)
	}

	// Wait for all
	for i := 0; i < concurrency; i++ {
		<-done
		err := <-errs
		assert.NoError(t, err)
	}

	// Should have created spans for all queries
	spans := mockTracer.GetSpans()
	assert.Equal(t, concurrency, len(spans))
}

func TestInstrumentedBackend_QueryPreviewTruncation(t *testing.T) {
	mockTracer := observability.NewMockTracer()

	mock := &testBackendForInstrumentation{
		name: "test-backend",
		queryResult: &QueryResult{
			Type:     "rows",
			RowCount: 1,
		},
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	// Create a very long query
	longQuery := "SELECT * FROM table WHERE " + string(make([]byte, 1000))

	ctx := context.Background()
	_, err := instrumented.ExecuteQuery(ctx, longQuery)
	require.NoError(t, err)

	spans := mockTracer.GetSpans()
	require.Len(t, spans, 1)

	// Query preview should be truncated
	preview := spans[0].Attributes["query.preview"].(string)
	assert.Less(t, len(preview), len(longQuery))
	assert.Contains(t, preview, "...")
}

func TestInstrumentedBackend_SpanHierarchy(t *testing.T) {
	mockTracer := observability.NewMockTracer()

	mock := &testBackendForInstrumentation{
		name: "test-backend",
		queryResult: &QueryResult{
			Type:     "rows",
			RowCount: 1,
		},
	}

	instrumented := NewInstrumentedBackend(mock, mockTracer)

	// Create parent span
	ctx := context.Background()
	parentCtx, parentSpan := mockTracer.StartSpan(ctx, "parent.operation")

	// Execute query within parent span
	_, err := instrumented.ExecuteQuery(parentCtx, "SELECT 1")
	require.NoError(t, err)

	mockTracer.EndSpan(parentSpan)

	spans := mockTracer.GetSpans()
	require.Len(t, spans, 2)

	// Find child span
	var childSpan *observability.Span
	for _, span := range spans {
		if span.Name == observability.SpanBackendQuery {
			childSpan = span
			break
		}
	}

	require.NotNil(t, childSpan)
	assert.Equal(t, parentSpan.TraceID, childSpan.TraceID, "Child should share parent's trace ID")
	assert.Equal(t, parentSpan.SpanID, childSpan.ParentID, "Child should reference parent span ID")
}

func TestInstrumentedBackend_ImplementsInterface(t *testing.T) {
	mock := &testBackendForInstrumentation{name: "test"}
	tracer := observability.NewNoOpTracer()

	var _ ExecutionBackend = NewInstrumentedBackend(mock, tracer)
}

// Benchmark tests
func BenchmarkInstrumentedBackend_ExecuteQuery(b *testing.B) {
	mockTracer := observability.NewNoOpTracer()
	mock := &testBackendForInstrumentation{
		name: "test-backend",
		queryResult: &QueryResult{
			Type:     "rows",
			RowCount: 10,
		},
	}
	instrumented := NewInstrumentedBackend(mock, mockTracer)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = instrumented.ExecuteQuery(ctx, "SELECT 1")
	}
}

func BenchmarkInstrumentedBackend_ConcurrentQueries(b *testing.B) {
	mockTracer := observability.NewNoOpTracer()
	mock := &testBackendForInstrumentation{
		name: "test-backend",
		queryResult: &QueryResult{
			Type:     "rows",
			RowCount: 10,
		},
	}
	instrumented := NewInstrumentedBackend(mock, mockTracer)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			_, _ = instrumented.ExecuteQuery(ctx, "SELECT 1")
		}
	})
}
