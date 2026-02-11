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

package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pendingPermission represents a tool permission request waiting for user decision.
// Mirrors the clarification question pattern used by AnswerClarificationQuestion.
type pendingPermission struct {
	SessionID      string
	ToolName       string
	ArgsJSON       string
	Description    string
	RiskLevel      string
	TimeoutSeconds int32
	AnswerChan     chan permissionAnswer // Buffered channel (size 1) for user response
	CreatedAt      time.Time
}

// permissionAnswer carries the user's permission decision through the answer channel.
type permissionAnswer struct {
	Granted          bool
	Message          string
	RememberDecision bool
}

// traceStore provides in-process trace storage for the GetTrace RPC.
// The observability.Tracer interface does not expose trace retrieval, so the server
// maintains its own trace store that captures completed spans grouped by trace ID.
// Thread-safe: all access is protected by mu.
type traceStore struct {
	mu     sync.RWMutex
	traces map[string]*storedTrace // trace ID -> stored trace
	maxAge time.Duration           // Maximum age before traces are evicted
}

// storedTrace holds all spans associated with a single trace ID.
type storedTrace struct {
	ID        string
	SessionID string
	Spans     []*observability.Span
	CreatedAt time.Time
}

// newTraceStore creates a new trace store with the given maximum trace age.
func newTraceStore(maxAge time.Duration) *traceStore {
	return &traceStore{
		traces: make(map[string]*storedTrace),
		maxAge: maxAge,
	}
}

// AddSpan stores a completed span, grouping it by trace ID.
func (ts *traceStore) AddSpan(span *observability.Span) {
	if span == nil {
		return
	}

	ts.mu.Lock()
	defer ts.mu.Unlock()

	trace, exists := ts.traces[span.TraceID]
	if !exists {
		sessionID, _ := span.Attributes[observability.AttrSessionID].(string)
		trace = &storedTrace{
			ID:        span.TraceID,
			SessionID: sessionID,
			CreatedAt: time.Now(),
		}
		ts.traces[span.TraceID] = trace
	}
	trace.Spans = append(trace.Spans, span)
}

// GetTrace retrieves a stored trace by ID. Returns nil if not found.
func (ts *traceStore) GetTrace(traceID string) *storedTrace {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	return ts.traces[traceID]
}

// EvictExpired removes traces older than maxAge. Should be called periodically.
func (ts *traceStore) EvictExpired() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	cutoff := time.Now().Add(-ts.maxAge)
	evicted := 0
	for id, trace := range ts.traces {
		if trace.CreatedAt.Before(cutoff) {
			delete(ts.traces, id)
			evicted++
		}
	}
	return evicted
}

// SetPendingPermissions initializes the pending permissions map if needed.
// This should be called from server initialization if not already set.
func (s *MultiAgentServer) ensurePermissionsInit() {
	if s.pendingPermissions == nil {
		s.pendingPermissions = make(map[string]*pendingPermission)
	}
}

// RegisterTool registers a tool definition with the server's tool registry.
// The tool becomes available for discovery via SearchTools and can be used by agents.
func (s *MultiAgentServer) RegisterTool(ctx context.Context, req *loomv1.RegisterToolRequest) (*loomv1.RegisterToolResponse, error) {
	if s.logger != nil {
		s.logger.Info("RegisterTool RPC invoked",
			zap.String("tool_name", req.GetTool().GetName()))
	}

	// Validate request
	if req.GetTool() == nil {
		return &loomv1.RegisterToolResponse{
			Success: false,
			Message: "tool definition is required",
		}, status.Error(codes.InvalidArgument, "tool definition is required")
	}

	toolDef := req.GetTool()
	if toolDef.GetName() == "" {
		return &loomv1.RegisterToolResponse{
			Success: false,
			Message: "tool name is required",
		}, status.Error(codes.InvalidArgument, "tool name is required")
	}

	// Check that tool registry is configured
	s.mu.RLock()
	registry := s.toolRegistry
	s.mu.RUnlock()

	if registry == nil {
		return &loomv1.RegisterToolResponse{
			Success: false,
			Message: "tool registry not configured",
		}, status.Error(codes.FailedPrecondition, "tool registry not configured")
	}

	// Convert ToolDefinition to IndexedTool for registry storage
	indexedTool := &loomv1.IndexedTool{
		Id:               fmt.Sprintf("custom:%s", toolDef.GetName()),
		Name:             toolDef.GetName(),
		Description:      toolDef.GetDescription(),
		Source:           loomv1.ToolSource_TOOL_SOURCE_CUSTOM,
		InputSchema:      toolDef.GetInputSchemaJson(),
		OutputSchema:     toolDef.GetOutputSchemaJson(),
		Capabilities:     toolDef.GetCapabilities(),
		Keywords:         toolDef.GetKeywords(),
		IndexedAt:        time.Now().Format(time.RFC3339),
		RequiresApproval: toolDef.GetRateLimit() != nil,
		RateLimit:        toolDef.GetRateLimit(),
	}

	// Register in the tool registry
	if err := registry.RegisterTool(ctx, indexedTool); err != nil {
		if s.logger != nil {
			s.logger.Error("RegisterTool failed to register tool in registry",
				zap.String("tool_name", toolDef.GetName()),
				zap.Error(err))
		}
		return &loomv1.RegisterToolResponse{
			Success: false,
			Message: fmt.Sprintf("failed to register tool: %v", err),
		}, status.Errorf(codes.Internal, "failed to register tool: %v", err)
	}

	if s.logger != nil {
		s.logger.Info("RegisterTool succeeded",
			zap.String("tool_name", toolDef.GetName()),
			zap.String("tool_id", indexedTool.Id),
			zap.String("source", loomv1.ToolSource_TOOL_SOURCE_CUSTOM.String()))
	}

	return &loomv1.RegisterToolResponse{
		Success: true,
		Message: fmt.Sprintf("tool %q registered successfully", toolDef.GetName()),
	}, nil
}

// GetTrace retrieves a trace by ID from the server's local trace store.
// The observability.Tracer interface does not support trace retrieval, so the server
// maintains its own trace store. If no trace store is initialized, it returns
// FailedPrecondition. If the trace is not found, it returns NotFound.
func (s *MultiAgentServer) GetTrace(ctx context.Context, req *loomv1.GetTraceRequest) (*loomv1.Trace, error) {
	if s.logger != nil {
		s.logger.Info("GetTrace RPC invoked",
			zap.String("trace_id", req.GetTraceId()))
	}

	// Validate request
	if req.GetTraceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "trace_id is required")
	}

	// Check tracer is configured
	s.mu.RLock()
	tracer := s.tracer
	traceStoreLcl := s.traceStoreLocal
	s.mu.RUnlock()

	if tracer == nil {
		return nil, status.Error(codes.FailedPrecondition, "tracer not configured")
	}

	if traceStoreLcl == nil {
		return nil, status.Error(codes.FailedPrecondition, "trace store not initialized")
	}

	// Look up the trace
	stored := traceStoreLcl.GetTrace(req.GetTraceId())
	if stored == nil {
		return nil, status.Errorf(codes.NotFound, "trace %q not found", req.GetTraceId())
	}

	// Convert internal spans to proto Span messages
	protoTrace := convertStoredTraceToProto(stored)

	if s.logger != nil {
		s.logger.Info("GetTrace returning trace",
			zap.String("trace_id", req.GetTraceId()),
			zap.Int("span_count", len(protoTrace.Spans)))
	}

	return protoTrace, nil
}

// convertStoredTraceToProto converts an internal storedTrace to a proto Trace message.
func convertStoredTraceToProto(stored *storedTrace) *loomv1.Trace {
	trace := &loomv1.Trace{
		Id:        stored.ID,
		SessionId: stored.SessionID,
	}

	var totalDurationMs int64

	for _, span := range stored.Spans {
		protoSpan := convertSpanToProto(span)
		trace.Spans = append(trace.Spans, protoSpan)

		// Track the root span (no parent)
		if span.ParentID == "" && trace.RootSpan == nil {
			trace.RootSpan = protoSpan
		}

		totalDurationMs += span.Duration.Milliseconds()
	}

	trace.TotalDurationMs = totalDurationMs

	return trace
}

// convertSpanToProto converts an observability.Span to a proto loomv1.Span.
func convertSpanToProto(span *observability.Span) *loomv1.Span {
	protoSpan := &loomv1.Span{
		Id:          span.SpanID,
		ParentId:    span.ParentID,
		Name:        span.Name,
		StartTimeUs: span.StartTime.UnixMicro(),
		EndTimeUs:   span.EndTime.UnixMicro(),
		DurationUs:  span.Duration.Microseconds(),
		Status:      span.Status.Code.String(),
	}

	// Convert attributes (interface{} -> string)
	if len(span.Attributes) > 0 {
		protoSpan.Attributes = make(map[string]string, len(span.Attributes))
		for k, v := range span.Attributes {
			protoSpan.Attributes[k] = fmt.Sprintf("%v", v)
		}
	}

	// Convert events
	for _, event := range span.Events {
		protoEvent := &loomv1.SpanEvent{
			Name:        event.Name,
			TimestampUs: event.Timestamp.UnixMicro(),
		}
		if len(event.Attributes) > 0 {
			protoEvent.Attributes = make(map[string]string, len(event.Attributes))
			for k, v := range event.Attributes {
				protoEvent.Attributes[k] = fmt.Sprintf("%v", v)
			}
		}
		protoSpan.Events = append(protoSpan.Events, protoEvent)
	}

	return protoSpan
}

// RecordTraceSpan records a completed span in the server's local trace store.
// Call this after ending a span to make it available via the GetTrace RPC.
func (s *MultiAgentServer) RecordTraceSpan(span *observability.Span) {
	s.mu.RLock()
	traceStoreLcl := s.traceStoreLocal
	s.mu.RUnlock()

	if traceStoreLcl == nil {
		return
	}

	traceStoreLcl.AddSpan(span)
}

// SetTraceStore initializes the server's local trace store for GetTrace RPC support.
// This should be called after NewMultiAgentServer() to enable trace retrieval.
func (s *MultiAgentServer) SetTraceStore(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traceStoreLocal = newTraceStore(maxAge)
}

// RequestToolPermission requests user permission before executing a tool.
// It creates a pending permission request and waits for a response on a channel,
// following the same pattern as AnswerClarificationQuestion for HITL workflows.
//
// If no answering mechanism responds within the timeout, the request times out
// with granted=false, timed_out=true.
func (s *MultiAgentServer) RequestToolPermission(ctx context.Context, req *loomv1.ToolPermissionRequest) (*loomv1.ToolPermissionResponse, error) {
	if s.logger != nil {
		s.logger.Info("RequestToolPermission RPC invoked",
			zap.String("session_id", req.GetSessionId()),
			zap.String("tool_name", req.GetToolName()),
			zap.String("risk_level", req.GetRiskLevel()),
			zap.Int32("timeout_seconds", req.GetTimeoutSeconds()))
	}

	// Validate required fields
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.GetToolName() == "" {
		return nil, status.Error(codes.InvalidArgument, "tool_name is required")
	}

	// Default timeout to 300 seconds
	timeoutSec := req.GetTimeoutSeconds()
	if timeoutSec <= 0 {
		timeoutSec = 300
	}

	// Generate a unique permission request ID
	permID := fmt.Sprintf("perm-%s-%s-%d", req.GetSessionId(), req.GetToolName(), time.Now().UnixNano())

	// Create the pending permission request with buffered channel
	perm := &pendingPermission{
		SessionID:      req.GetSessionId(),
		ToolName:       req.GetToolName(),
		ArgsJSON:       req.GetArgsJson(),
		Description:    req.GetDescription(),
		RiskLevel:      req.GetRiskLevel(),
		TimeoutSeconds: timeoutSec,
		AnswerChan:     make(chan permissionAnswer, 1),
		CreatedAt:      time.Now(),
	}

	// Store the pending permission
	s.pendingPermissionsMu.Lock()
	s.ensurePermissionsInit()
	s.pendingPermissions[permID] = perm
	s.pendingPermissionsMu.Unlock()

	if s.logger != nil {
		s.logger.Info("RequestToolPermission waiting for user decision",
			zap.String("permission_id", permID),
			zap.String("tool_name", req.GetToolName()),
			zap.Int32("timeout_seconds", timeoutSec))
	}

	// Wait for answer or timeout
	timeout := time.Duration(timeoutSec) * time.Second
	select {
	case answer := <-perm.AnswerChan:
		// Clean up from pending map
		s.pendingPermissionsMu.Lock()
		delete(s.pendingPermissions, permID)
		s.pendingPermissionsMu.Unlock()

		if s.logger != nil {
			s.logger.Info("RequestToolPermission received answer",
				zap.String("permission_id", permID),
				zap.Bool("granted", answer.Granted),
				zap.String("message", answer.Message))
		}

		return &loomv1.ToolPermissionResponse{
			Granted:          answer.Granted,
			Message:          answer.Message,
			RememberDecision: answer.RememberDecision,
			TimedOut:         false,
		}, nil

	case <-time.After(timeout):
		// Clean up from pending map
		s.pendingPermissionsMu.Lock()
		delete(s.pendingPermissions, permID)
		s.pendingPermissionsMu.Unlock()

		if s.logger != nil {
			s.logger.Warn("RequestToolPermission timed out",
				zap.String("permission_id", permID),
				zap.String("tool_name", req.GetToolName()),
				zap.Int32("timeout_seconds", timeoutSec))
		}

		return &loomv1.ToolPermissionResponse{
			Granted:  false,
			Message:  fmt.Sprintf("permission request timed out after %d seconds", timeoutSec),
			TimedOut: true,
		}, nil

	case <-ctx.Done():
		// Context cancelled (client disconnect, etc.)
		s.pendingPermissionsMu.Lock()
		delete(s.pendingPermissions, permID)
		s.pendingPermissionsMu.Unlock()

		if s.logger != nil {
			s.logger.Warn("RequestToolPermission context cancelled",
				zap.String("permission_id", permID),
				zap.Error(ctx.Err()))
		}

		return nil, status.Error(codes.Canceled, "request cancelled")
	}
}

// GrantToolPermission provides an answer to a pending tool permission request.
// This is the counterpart to RequestToolPermission, called by the UI layer
// when the user makes a decision. It follows the same pattern as AnswerClarificationQuestion.
func (s *MultiAgentServer) GrantToolPermission(permID string, granted bool, message string, remember bool) error {
	s.pendingPermissionsMu.Lock()
	perm, exists := s.pendingPermissions[permID]
	if !exists {
		s.pendingPermissionsMu.Unlock()
		return fmt.Errorf("permission request %q not found or already answered", permID)
	}
	// Remove from pending to prevent double-answer
	delete(s.pendingPermissions, permID)
	s.pendingPermissionsMu.Unlock()

	// Send answer (non-blocking since channel has buffer of 1)
	select {
	case perm.AnswerChan <- permissionAnswer{
		Granted:          granted,
		Message:          message,
		RememberDecision: remember,
	}:
		if s.logger != nil {
			s.logger.Info("GrantToolPermission: answer delivered",
				zap.String("permission_id", permID),
				zap.Bool("granted", granted))
		}
		return nil
	default:
		// Channel full -- should not happen with buffer of 1 and single writer,
		// but handle gracefully
		if s.logger != nil {
			s.logger.Warn("GrantToolPermission: channel full, answer not delivered",
				zap.String("permission_id", permID))
		}
		return fmt.Errorf("answer channel full for permission %q", permID)
	}
}

// GetPendingPermissions returns all pending permission requests.
// Useful for UI layers that need to display pending permission requests.
func (s *MultiAgentServer) GetPendingPermissions() map[string]*pendingPermission {
	s.pendingPermissionsMu.RLock()
	defer s.pendingPermissionsMu.RUnlock()

	result := make(map[string]*pendingPermission, len(s.pendingPermissions))
	for k, v := range s.pendingPermissions {
		result[k] = v
	}
	return result
}
