// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package shuttle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// ContactHumanTool provides human-in-the-loop capabilities for agents.
// Implements 12-Factor Agent Compliance (Factor 7: Human Oversight & Approval).
//
// Use cases:
// - Approval workflows (e.g., "Should I delete this data?")
// - High-stakes decisions (e.g., "Confirm this financial transaction")
// - Ambiguity resolution (e.g., "Which interpretation is correct?")
// - Quality gates (e.g., "Review this generated code before deployment")
type ContactHumanTool struct {
	store        HumanRequestStore
	notifier     Notifier
	timeout      time.Duration
	pollInterval time.Duration
	tracer       observability.Tracer
	logger       *zap.Logger

	// For testing - allows mocking time
	now func() time.Time
}

// HumanRequest represents a request for human input.
type HumanRequest struct {
	ID          string                 `json:"id"`
	AgentID     string                 `json:"agent_id"`
	SessionID   string                 `json:"session_id"`
	Question    string                 `json:"question"`
	Context     map[string]interface{} `json:"context"`
	RequestType string                 `json:"request_type"` // "approval", "decision", "input", "review"
	Priority    string                 `json:"priority"`     // "low", "normal", "high", "critical"
	Timeout     time.Duration          `json:"timeout"`
	CreatedAt   time.Time              `json:"created_at"`
	ExpiresAt   time.Time              `json:"expires_at"`

	// Response fields (populated when human responds)
	Status       string                 `json:"status"` // "pending", "approved", "rejected", "timeout", "responded"
	Response     string                 `json:"response"`
	ResponseData map[string]interface{} `json:"response_data"`
	RespondedAt  *time.Time             `json:"responded_at"`
	RespondedBy  string                 `json:"responded_by"`
}

// HumanRequestStore manages storage and retrieval of human requests.
type HumanRequestStore interface {
	// Store saves a new human request
	Store(ctx context.Context, req *HumanRequest) error

	// Get retrieves a human request by ID
	Get(ctx context.Context, id string) (*HumanRequest, error)

	// Update updates an existing human request
	Update(ctx context.Context, req *HumanRequest) error

	// List returns all pending requests (for human review interface)
	ListPending(ctx context.Context) ([]*HumanRequest, error)

	// ListBySession returns all requests for a session
	ListBySession(ctx context.Context, sessionID string) ([]*HumanRequest, error)
}

// Notifier sends notifications to humans when their input is requested.
type Notifier interface {
	// Notify sends a notification about a human request
	Notify(ctx context.Context, req *HumanRequest) error
}

// ContactHumanConfig configures the ContactHumanTool.
type ContactHumanConfig struct {
	Store        HumanRequestStore
	Notifier     Notifier
	Timeout      time.Duration        // Default timeout for requests (default: 5 minutes)
	PollInterval time.Duration        // How often to check for responses (default: 1 second)
	Tracer       observability.Tracer // Tracer for observability (default: NoOpTracer)
	Logger       *zap.Logger          // Logger for structured logging (default: NoOp logger)
}

// NewContactHumanTool creates a new human-in-the-loop tool.
func NewContactHumanTool(config ContactHumanConfig) *ContactHumanTool {
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Minute
	}
	if config.PollInterval == 0 {
		config.PollInterval = 1 * time.Second
	}
	if config.Store == nil {
		config.Store = NewInMemoryHumanRequestStore()
	}
	if config.Notifier == nil {
		config.Notifier = &NoOpNotifier{}
	}
	if config.Tracer == nil {
		config.Tracer = observability.NewNoOpTracer()
	}
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	return &ContactHumanTool{
		store:        config.Store,
		notifier:     config.Notifier,
		timeout:      config.Timeout,
		pollInterval: config.PollInterval,
		tracer:       config.Tracer,
		logger:       config.Logger,
		now:          time.Now,
	}
}

func (t *ContactHumanTool) Name() string {
	return "contact_human"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/human.yaml).
// This fallback is used only when prompts are not configured.
func (t *ContactHumanTool) Description() string {
	return `Contacts a human for approval, input, or decision-making. Use this tool when:
- You need human approval for a high-stakes action (e.g., deleting data, making purchases)
- You encounter ambiguity that requires human judgment
- You need human input to proceed (e.g., "Which option should I choose?")
- You want a human to review your work before proceeding

This tool blocks execution until the human responds or the request times out.`
}

func (t *ContactHumanTool) InputSchema() *JSONSchema {
	return NewObjectSchema(
		"Parameters for contacting a human",
		map[string]*JSONSchema{
			"question": NewStringSchema("The question or request for the human (required). Be clear and specific."),
			"request_type": NewStringSchema("Type of request: 'approval', 'decision', 'input', or 'review' (default: 'input')").
				WithEnum("approval", "decision", "input", "review").
				WithDefault("input"),
			"priority": NewStringSchema("Priority level: 'low', 'normal', 'high', or 'critical' (default: 'normal')").
				WithEnum("low", "normal", "high", "critical").
				WithDefault("normal"),
			"context": NewObjectSchema(
				"Additional context for the human (optional)",
				map[string]*JSONSchema{},
				[]string{},
			),
			"timeout_seconds": NewNumberSchema("Maximum time to wait for human response in seconds (default: 300 = 5 minutes)").
				WithDefault(300),
		},
		[]string{"question"},
	)
}

func (t *ContactHumanTool) Execute(ctx context.Context, params map[string]interface{}) (*Result, error) {
	start := t.now()

	// Start span for observability
	ctx, span := t.tracer.StartSpan(ctx, "hitl.contact_human")
	defer t.tracer.EndSpan(span)

	// Extract parameters
	question, ok := params["question"].(string)
	if !ok || question == "" {
		span.SetAttribute("error", "missing_question")
		span.SetAttribute("success", false)
		return &Result{
			Success: false,
			Error: &Error{
				Code:       "INVALID_PARAMS",
				Message:    "question is required",
				Suggestion: "Provide a clear question for the human (e.g., 'Should I proceed with deleting table X?')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	requestType := "input"
	if rt, ok := params["request_type"].(string); ok {
		requestType = rt
	}

	priority := "normal"
	if p, ok := params["priority"].(string); ok {
		priority = p
	}

	// Add request attributes to span
	span.SetAttribute("hitl.request_type", requestType)
	span.SetAttribute("hitl.priority", priority)
	span.SetAttribute("hitl.question", question)

	// Extract context (if provided)
	contextData := make(map[string]interface{})
	if c, ok := params["context"].(map[string]interface{}); ok {
		contextData = c
	}

	// Extract timeout
	timeoutSeconds := float64(t.timeout.Seconds())
	if ts, ok := params["timeout_seconds"].(float64); ok {
		timeoutSeconds = ts
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	span.SetAttribute("hitl.timeout_seconds", int32(timeout.Seconds()))

	// Extract session ID and agent ID from context (if available)
	sessionID := extractFromContext(ctx, "session_id")
	agentID := extractFromContext(ctx, "agent_id")

	if sessionID != "" {
		span.SetAttribute("session_id", sessionID)
	}
	if agentID != "" {
		span.SetAttribute("agent_id", agentID)
	}

	// Create human request
	now := t.now()
	req := &HumanRequest{
		ID:          uuid.New().String(),
		AgentID:     agentID,
		SessionID:   sessionID,
		Question:    question,
		Context:     contextData,
		RequestType: requestType,
		Priority:    priority,
		Timeout:     timeout,
		CreatedAt:   now,
		ExpiresAt:   now.Add(timeout),
		Status:      "pending",
	}

	span.SetAttribute("hitl.request_id", req.ID)
	span.AddEvent("request_created", nil)

	// Store the request
	if err := t.store.Store(ctx, req); err != nil {
		span.SetAttribute("error", "store_failed")
		span.SetAttribute("error_message", err.Error())
		span.SetAttribute("success", false)
		return &Result{
			Success: false,
			Error: &Error{
				Code:    "STORE_FAILED",
				Message: fmt.Sprintf("Failed to store human request: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	span.AddEvent("request_stored", nil)

	// Send notification
	if err := t.notifier.Notify(ctx, req); err != nil {
		// Log warning but don't fail - request is still stored
		span.AddEvent("notification_failed", map[string]interface{}{
			"error": err.Error(),
		})
		t.logger.Warn("Failed to send notification for human request",
			zap.String("request_id", req.ID),
			zap.String("agent_id", req.AgentID),
			zap.String("session_id", req.SessionID),
			zap.String("request_type", req.RequestType),
			zap.Error(err),
		)
	} else {
		span.AddEvent("notification_sent", nil)
	}

	// Wait for response (with polling)
	span.AddEvent("waiting_for_human", nil)
	response, timedOut := t.waitForResponse(ctx, req.ID, timeout)

	executionTime := time.Since(start).Milliseconds()

	if timedOut {
		span.SetAttribute("hitl.status", "timeout")
		span.SetAttribute("success", false)
		span.SetAttribute("wait_time_ms", int32(executionTime))
		span.AddEvent("human_timeout", nil)

		// Record timeout metric
		t.tracer.RecordMetric("hitl.timeout_count", 1, map[string]string{
			"request_type": requestType,
			"priority":     priority,
		})

		return &Result{
			Success: false,
			Error: &Error{
				Code:       "TIMEOUT",
				Message:    fmt.Sprintf("Human did not respond within %v", timeout),
				Suggestion: "Consider increasing the timeout or marking this request as 'low' priority",
				Retryable:  true,
			},
			Metadata: map[string]interface{}{
				"request_id": req.ID,
				"timeout":    timeout.String(),
			},
			ExecutionTimeMs: executionTime,
		}, nil
	}

	// Success path
	span.SetAttribute("hitl.status", response.Status)
	span.SetAttribute("success", true)
	span.SetAttribute("wait_time_ms", int32(executionTime))
	if response.RespondedBy != "" {
		span.SetAttribute("hitl.responded_by", response.RespondedBy)
	}
	span.AddEvent("human_responded", nil)

	// Record wait time metric
	t.tracer.RecordMetric("hitl.wait_time_ms", float64(executionTime), map[string]string{
		"request_type": requestType,
		"priority":     priority,
		"status":       response.Status,
	})

	// Record success count
	t.tracer.RecordMetric("hitl.response_count", 1, map[string]string{
		"request_type": requestType,
		"priority":     priority,
		"status":       response.Status,
	})

	return &Result{
		Success: true,
		Data: map[string]interface{}{
			"request_id":    response.ID,
			"status":        response.Status,
			"response":      response.Response,
			"response_data": response.ResponseData,
			"responded_by":  response.RespondedBy,
			"responded_at":  response.RespondedAt,
		},
		Metadata: map[string]interface{}{
			"request_type": response.RequestType,
			"priority":     response.Priority,
			"wait_time_ms": executionTime,
		},
		ExecutionTimeMs: executionTime,
	}, nil
}

func (t *ContactHumanTool) Backend() string {
	return "" // Backend-agnostic
}

// waitForResponse polls the store until a response is received or timeout occurs.
func (t *ContactHumanTool) waitForResponse(ctx context.Context, requestID string, timeout time.Duration) (*HumanRequest, bool) {
	deadline := t.now().Add(timeout)
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, true // Context canceled
		case <-ticker.C:
			// Check if we've exceeded the deadline
			if t.now().After(deadline) {
				return nil, true // Timed out
			}

			// Poll for response
			req, err := t.store.Get(ctx, requestID)
			if err != nil {
				continue // Retry on error
			}

			// Check if human has responded
			if req.Status != "pending" {
				return req, false
			}
		}
	}
}

// extractFromContext extracts a value from the context (if it exists).
// This is a helper for extracting session_id, agent_id, etc.
func extractFromContext(ctx context.Context, key string) string {
	if val := ctx.Value(key); val != nil {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return ""
}

// InMemoryHumanRequestStore provides an in-memory implementation of HumanRequestStore.
// Suitable for testing and single-instance deployments.
type InMemoryHumanRequestStore struct {
	mu       sync.RWMutex
	requests map[string]*HumanRequest
}

// NewInMemoryHumanRequestStore creates a new in-memory store.
func NewInMemoryHumanRequestStore() *InMemoryHumanRequestStore {
	return &InMemoryHumanRequestStore{
		requests: make(map[string]*HumanRequest),
	}
}

func (s *InMemoryHumanRequestStore) Store(ctx context.Context, req *HumanRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Deep copy to prevent external modification
	reqCopy := *req
	if req.Context != nil {
		reqCopy.Context = make(map[string]interface{})
		for k, v := range req.Context {
			reqCopy.Context[k] = v
		}
	}
	if req.ResponseData != nil {
		reqCopy.ResponseData = make(map[string]interface{})
		for k, v := range req.ResponseData {
			reqCopy.ResponseData[k] = v
		}
	}

	s.requests[req.ID] = &reqCopy
	return nil
}

func (s *InMemoryHumanRequestStore) Get(ctx context.Context, id string) (*HumanRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	req, exists := s.requests[id]
	if !exists {
		return nil, fmt.Errorf("request not found: %s", id)
	}

	// Deep copy to prevent external modification
	reqCopy := *req
	if req.Context != nil {
		reqCopy.Context = make(map[string]interface{})
		for k, v := range req.Context {
			reqCopy.Context[k] = v
		}
	}
	if req.ResponseData != nil {
		reqCopy.ResponseData = make(map[string]interface{})
		for k, v := range req.ResponseData {
			reqCopy.ResponseData[k] = v
		}
	}

	return &reqCopy, nil
}

func (s *InMemoryHumanRequestStore) Update(ctx context.Context, req *HumanRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.requests[req.ID]; !exists {
		return fmt.Errorf("request not found: %s", req.ID)
	}

	// Deep copy
	reqCopy := *req
	if req.Context != nil {
		reqCopy.Context = make(map[string]interface{})
		for k, v := range req.Context {
			reqCopy.Context[k] = v
		}
	}
	if req.ResponseData != nil {
		reqCopy.ResponseData = make(map[string]interface{})
		for k, v := range req.ResponseData {
			reqCopy.ResponseData[k] = v
		}
	}

	s.requests[req.ID] = &reqCopy
	return nil
}

func (s *InMemoryHumanRequestStore) ListPending(ctx context.Context) ([]*HumanRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var pending []*HumanRequest
	for _, req := range s.requests {
		if req.Status == "pending" {
			// Deep copy
			reqCopy := *req
			if req.Context != nil {
				reqCopy.Context = make(map[string]interface{})
				for k, v := range req.Context {
					reqCopy.Context[k] = v
				}
			}
			pending = append(pending, &reqCopy)
		}
	}
	return pending, nil
}

func (s *InMemoryHumanRequestStore) ListBySession(ctx context.Context, sessionID string) ([]*HumanRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sessionRequests []*HumanRequest
	for _, req := range s.requests {
		if req.SessionID == sessionID {
			// Deep copy
			reqCopy := *req
			if req.Context != nil {
				reqCopy.Context = make(map[string]interface{})
				for k, v := range req.Context {
					reqCopy.Context[k] = v
				}
			}
			sessionRequests = append(sessionRequests, &reqCopy)
		}
	}
	return sessionRequests, nil
}

// RespondToRequest updates a human request with a response.
// This is called by the human review interface (CLI, Web UI, API).
func (s *InMemoryHumanRequestStore) RespondToRequest(ctx context.Context, requestID, status, response, respondedBy string, responseData map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, exists := s.requests[requestID]
	if !exists {
		return fmt.Errorf("request not found: %s", requestID)
	}

	if req.Status != "pending" {
		return fmt.Errorf("request already responded to (status: %s)", req.Status)
	}

	now := time.Now()
	req.Status = status
	req.Response = response
	req.ResponseData = responseData
	req.RespondedAt = &now
	req.RespondedBy = respondedBy

	return nil
}

// NoOpNotifier is a no-op implementation of Notifier for testing.
type NoOpNotifier struct{}

func (n *NoOpNotifier) Notify(ctx context.Context, req *HumanRequest) error {
	return nil
}

// JSONNotifier sends notifications as JSON to a configured endpoint (webhook).
type JSONNotifier struct {
	webhookURL string
	httpClient *http.Client
}

// NewJSONNotifier creates a new JSON webhook notifier.
func NewJSONNotifier(webhookURL string) *JSONNotifier {
	return &JSONNotifier{
		webhookURL: webhookURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second, // 10 second timeout for webhook requests
		},
	}
}

func (n *JSONNotifier) Notify(ctx context.Context, req *HumanRequest) error {
	// Marshal request to JSON
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP POST request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "Loom-HITL/1.0")

	// Send request
	resp, err := n.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send webhook: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned non-success status: %d %s", resp.StatusCode, resp.Status)
	}

	return nil
}
