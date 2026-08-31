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
	// heartbeat is how often a still-pending question pokes the notifier when
	// it implements Heartbeater. Zero disables heartbeating.
	heartbeat time.Duration

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
	Kind        string                 `json:"kind"`         // "approval" (hook-held) | "question" (contact_human); absent → question
	Summary     string                 `json:"summary"`      // display digest: tool+args (approval) | question text (question)
	Timeout     time.Duration          `json:"timeout"`
	CreatedAt   time.Time              `json:"created_at"`
	ExpiresAt   time.Time              `json:"expires_at"`

	// Params carries the held call's full parameter map for an approval; a
	// question has no held call and leaves it empty. Stamped at origin, never
	// re-derived downstream.
	Params map[string]interface{} `json:"params"`
	// ParamsTruncated reports that the paramsMaxBytes bound cut whole pairs out
	// of Params.
	ParamsTruncated bool `json:"params_truncated"`

	// Response fields (populated when human responds)
	Status       string                 `json:"status"` // "pending", "approved", "rejected", "timeout", "responded"
	Response     string                 `json:"response"`
	ResponseData map[string]interface{} `json:"response_data"`
	RespondedAt  *time.Time             `json:"responded_at"`
	RespondedBy  string                 `json:"responded_by"`

	// TaskID attributes this request to the task it blocks. Empty when the
	// request was raised outside a claimed task, which is normal; it is stored
	// as NULL. Stamped from the ambient task attribution on the context.
	TaskID string `json:"task_id,omitempty"`
}

// HumanRequestStore manages storage and retrieval of human requests.
type HumanRequestStore interface {
	// Store saves a new human request
	Store(ctx context.Context, req *HumanRequest) error

	// Get retrieves a human request by ID. Absence contract: the postgres
	// store returns (nil, nil) for a missing row so callers can distinguish
	// absence from a store failure; the sqlite and in-memory stores return an
	// error. Callers must treat BOTH a nil request and an error as
	// possibly-absent (the ask waiter does), never dereference unchecked.
	Get(ctx context.Context, id string) (*HumanRequest, error)

	// Update updates an existing human request
	Update(ctx context.Context, req *HumanRequest) error

	// List returns all pending requests (for human review interface)
	ListPending(ctx context.Context) ([]*HumanRequest, error)

	// ListBySession returns all requests for a session
	ListBySession(ctx context.Context, sessionID string) ([]*HumanRequest, error)

	// RespondToRequest resolves a pending, non-expired request exactly once.
	// The expiry guard is the store's own — no caller payload can lift it. On
	// an already-decided or expired request it is a no-op returning nil (the
	// caller reads current state via Get). Errors only on a missing row / store failure.
	RespondToRequest(ctx context.Context, requestID, status, response, respondedBy string, responseData map[string]interface{}) error

	// ExpireRequest terminally closes a pending request as "timeout" on behalf
	// of the harness — the waiter's give-up path, a canceled turn's abandon
	// write, an expiry sweep. It is the ONLY path that may close a row past its
	// expiry; a row already resolved is left untouched (closing is not
	// resolving). A missing row is a no-op. respondedBy records the closing
	// actor (e.g. "system:expiry", "system:cancel").
	ExpireRequest(ctx context.Context, requestID, respondedBy string) error

	// Close releases any resources held by the store.
	Close() error
}

// Notifier sends notifications to humans when their input is requested.
type Notifier interface {
	// Notify sends a notification about a human request
	Notify(ctx context.Context, req *HumanRequest) error
}

// Heartbeater — the optional keep-the-stream-alive capability a Notifier may
// implement — lives in hold_heartbeat.go, shared by both hold origins.

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
		heartbeat:    holdHeartbeatInterval,
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

	// Extract context (if provided). The model's map is copied, and the origin
	// discriminator is stamped by the harness OVER whatever the model supplied:
	// "kind" is the one field chosen to be free of model control, and store
	// backstops read it out of this map when the kind column is absent — so it
	// must be harness-written in both origins.
	contextData := make(map[string]interface{})
	if c, ok := params["context"].(map[string]interface{}); ok {
		for k, v := range c {
			contextData[k] = v
		}
	}
	contextData["kind"] = "question"

	// Extract timeout. The model-supplied value is clamped both ways: a
	// positive floor (a zero or negative timeout would store an already-expired
	// pending request no response can resolve) and a ceiling at the turn's own
	// deadline — the model chooses how long it is willing to wait, never a
	// window outliving the turn, which would leave a row answerable for a call
	// that already returned.
	timeoutSeconds := float64(t.timeout.Seconds())
	if ts, ok := params["timeout_seconds"].(float64); ok {
		timeoutSeconds = ts
	}
	const minTimeoutSeconds = 5
	if timeoutSeconds < minTimeoutSeconds {
		timeoutSeconds = minTimeoutSeconds
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	// Two ceilings, both the harness's, neither the model's: the CONFIGURED
	// timeout is the maximum window the host allows (so a worker-driven turn
	// with no deadline still cannot hold a row for a model-chosen day), and
	// the turn deadline caps it further when one exists. No re-floor after
	// the deadline ceiling — a floor pushing past the deadline would store a
	// row outliving its waiter.
	if timeout > t.timeout {
		timeout = t.timeout
	}
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining < timeout {
			timeout = remaining
		}
	}
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

	// Create human request. The stored expiry is clamped AT the deadline
	// itself: the duration was ceilinged an instant earlier, and stamping
	// now+duration from a later clock would overshoot the deadline by the gap
	// — the exact row-outlives-its-turn sliver the ceiling exists to remove.
	now := t.now()
	expiresAt := now.Add(timeout)
	if dl, ok := ctx.Deadline(); ok && expiresAt.After(dl) {
		expiresAt = dl
	}
	req := &HumanRequest{
		ID:          uuid.New().String(),
		AgentID:     agentID,
		SessionID:   sessionID,
		Question:    question,
		Context:     contextData,
		RequestType: requestType,
		Priority:    priority,
		Kind:        "question",
		Summary:     question,
		Timeout:     timeout,
		CreatedAt:   now,
		ExpiresAt:   expiresAt,
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
		span.SetAttribute("wait_time_ms", executionTime)
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
	span.SetAttribute("wait_time_ms", executionTime)
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

// waitForResponse polls the store until a response is received or timeout
// occurs. BOTH give-up exits terminally close the row they abandon — the same
// law the ask waiter follows — so an unanswered or canceled question can
// never sit answerable for a call that already returned; a response that won
// the race against the close is returned as a response, never misreported as
// a timeout the row contradicts.
func (t *ContactHumanTool) waitForResponse(ctx context.Context, requestID string, timeout time.Duration) (*HumanRequest, bool) {
	deadline := t.now().Add(timeout)
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()

	// A question hold is byte-silent on the caller's stream for its whole
	// window exactly like an ask hold, so it beats on the same terms (see
	// holdBeater): opt-in by capability, best-effort, and emitted on this
	// goroutine after the resolution poll.
	beater := newHoldBeater(t.notifier, t.heartbeat)

	for {
		select {
		case <-ctx.Done():
			// A deadline-clamped wait dies at the same instant as its context,
			// and this arm usually wins that race — label the close by which
			// clock actually ran out so the recorded actor is truthful.
			by := "system:cancel"
			if t.now().After(deadline) {
				by = "system:expiry"
			}
			if won := t.closeAbandoned(ctx, requestID, by); won != nil {
				return won, false
			}
			return nil, true // Context canceled
		case <-ticker.C:
			// Check if we've exceeded the deadline
			if t.now().After(deadline) {
				if won := t.closeAbandoned(ctx, requestID, "system:expiry"); won != nil {
					return won, false
				}
				return nil, true // Timed out
			}

			// Poll for response. The postgres store reports an absent row as
			// (nil, nil) — its documented contract — so a nil row must be
			// tolerated exactly like a transient error, never dereferenced: a
			// sweep may legitimately retire the row under a live waiter.
			req, err := t.store.Get(ctx, requestID)
			if err != nil || req == nil {
				// Retry on error or absent row until the deadline. The beat
				// still runs: an unreadable row is not a reason to let the
				// caller's stream go silent.
				beater.beat(ctx)
				continue
			}

			// Check if human has responded
			if req.Status != "pending" {
				return req, false
			}

			// Still pending: keep the caller's stream alive. Last, so it can
			// never delay a response the poll above already saw.
			beater.beat(ctx)
		}
	}
}

// closeAbandoned terminally closes a question row the waiter is giving up on,
// then re-reads it: a resolution that won the race is returned so the caller
// reports the response instead of a timeout the durable row contradicts. The
// write runs detached from the (possibly canceled) caller context while
// keeping its values, so the tenant identity the postgres store requires
// travels with it.
func (t *ContactHumanTool) closeAbandoned(ctx context.Context, requestID, by string) *HumanRequest {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = t.store.ExpireRequest(wctx, requestID, by)
	if req, err := t.store.Get(wctx, requestID); err == nil && req != nil &&
		req.Status != "pending" && req.Status != "timeout" {
		return req
	}
	return nil
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

// cloneHumanRequest copies req with its maps duplicated, so stored state and
// caller-visible state never share a map. Nil maps stay nil.
func cloneHumanRequest(req *HumanRequest) *HumanRequest {
	reqCopy := *req
	reqCopy.Context = cloneStringMap(req.Context)
	reqCopy.ResponseData = cloneStringMap(req.ResponseData)
	reqCopy.Params = cloneStringMap(req.Params)
	return &reqCopy
}

// cloneStringMap returns a copy of m one level deep; nil in, nil out.
func cloneStringMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	c := make(map[string]interface{}, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (s *InMemoryHumanRequestStore) Store(ctx context.Context, req *HumanRequest) error {
	// A pending request must carry an expiry: a zero ExpiresAt would make the
	// row permanently approvable, and the resolve CAS's expiry guard is keyed
	// on the stored value. Both in-repo producers always stamp one; this guards
	// exported-API callers.
	if req.Status == "pending" && req.ExpiresAt.IsZero() {
		return fmt.Errorf("pending human request %s has no expiry", req.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requests[req.ID] = cloneHumanRequest(req)
	return nil
}

func (s *InMemoryHumanRequestStore) Get(ctx context.Context, id string) (*HumanRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	req, exists := s.requests[id]
	if !exists {
		return nil, fmt.Errorf("request not found: %s", id)
	}

	return cloneHumanRequest(req), nil
}

func (s *InMemoryHumanRequestStore) Update(ctx context.Context, req *HumanRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.requests[req.ID]; !exists {
		return fmt.Errorf("request not found: %s", req.ID)
	}

	s.requests[req.ID] = cloneHumanRequest(req)
	return nil
}

func (s *InMemoryHumanRequestStore) ListPending(ctx context.Context) ([]*HumanRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var pending []*HumanRequest
	for _, req := range s.requests {
		if req.Status == "pending" {
			pending = append(pending, cloneHumanRequest(req))
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
			sessionRequests = append(sessionRequests, cloneHumanRequest(req))
		}
	}
	return sessionRequests, nil
}

// RespondToRequest resolves a pending, non-expired request exactly once.
// On an already-decided or expired request it is a no-op returning nil, so the
// caller reads current state via Get. Errors only on a missing request. The
// expiry guard is the store's own: no status value in the caller's payload can
// lift it — terminal closes past expiry go through ExpireRequest.
func (s *InMemoryHumanRequestStore) RespondToRequest(ctx context.Context, requestID, status, response, respondedBy string, responseData map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, exists := s.requests[requestID]
	if !exists {
		return fmt.Errorf("request not found: %s", requestID)
	}

	now := time.Now()
	if req.Status != "pending" || now.After(req.ExpiresAt) {
		return nil
	}

	req.Status = status
	req.Response = response
	req.ResponseData = cloneResponseData(responseData)
	req.RespondedAt = &now
	req.RespondedBy = respondedBy

	return nil
}

// ExpireRequest terminally closes a pending request as "timeout" regardless of
// its expiry — the harness's close for abandoned or swept rows. A resolved row
// is left untouched (closing is not resolving); a missing row is a no-op.
func (s *InMemoryHumanRequestStore) ExpireRequest(ctx context.Context, requestID, respondedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, exists := s.requests[requestID]
	if !exists || req.Status != "pending" {
		return nil
	}

	now := time.Now()
	req.Status = "timeout"
	req.Response = ""
	req.ResponseData = nil
	req.RespondedAt = &now
	req.RespondedBy = respondedBy
	return nil
}

// cloneResponseData copies the caller's map so later caller mutation cannot
// corrupt stored state (every other method in this store clones likewise).
func cloneResponseData(data map[string]interface{}) map[string]interface{} {
	if data == nil {
		return nil
	}
	out := make(map[string]interface{}, len(data))
	for k, v := range data {
		out[k] = v
	}
	return out
}

// Close is a no-op; in-memory store has no resources to release.
func (s *InMemoryHumanRequestStore) Close() error {
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
	defer func() { _ = resp.Body.Close() }()

	// Check response status
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned non-success status: %d %s", resp.StatusCode, resp.Status)
	}

	return nil
}
