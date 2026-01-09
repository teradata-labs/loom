// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// HawkConfig configures the Hawk tracer.
type HawkConfig struct {
	// Endpoint is the Hawk API endpoint for trace export.
	// Example: "http://localhost:8080/v1/traces"
	Endpoint string

	// APIKey for authentication with Hawk.
	APIKey string

	// BatchSize is the number of spans to buffer before flushing.
	// Default: 100
	BatchSize int

	// FlushInterval is how often to flush buffered spans.
	// Default: 10s
	FlushInterval time.Duration

	// MaxRetries is the maximum number of retry attempts for failed exports.
	// Default: 3
	MaxRetries int

	// RetryBackoff is the initial backoff duration for retries.
	// Default: 1s (doubles with each retry: 1s, 2s, 4s, 8s...)
	RetryBackoff time.Duration

	// Privacy controls PII redaction.
	Privacy PrivacyConfig

	// HTTPClient for custom transport (e.g., timeouts, proxies).
	// If nil, uses http.DefaultClient.
	HTTPClient *http.Client
}

// PrivacyConfig controls what data is redacted before export.
type PrivacyConfig struct {
	// RedactCredentials removes password, api_key, token fields from spans.
	RedactCredentials bool

	// RedactPII removes email, phone, SSN patterns from attribute values.
	RedactPII bool

	// AllowedAttributes is a whitelist of attribute keys that bypass redaction.
	// Example: []string{"session.id", "llm.model", "tool.name"}
	AllowedAttributes []string
}

// HawkTracer exports traces to Hawk via HTTP.
type HawkTracer struct {
	config HawkConfig
	buffer *traceBuffer
	client *http.Client

	// Background flusher
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewHawkTracer creates a tracer that exports to Hawk.
func NewHawkTracer(config HawkConfig) (*HawkTracer, error) {
	if config.Endpoint == "" {
		return nil, fmt.Errorf("hawk endpoint required")
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 100
	}
	if config.FlushInterval == 0 {
		config.FlushInterval = 10 * time.Second
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3
	}
	if config.RetryBackoff == 0 {
		config.RetryBackoff = 1 * time.Second
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	tracer := &HawkTracer{
		config: config,
		buffer: newTraceBuffer(config.BatchSize),
		client: config.HTTPClient,
		stopCh: make(chan struct{}),
	}

	// Start background flusher
	tracer.wg.Add(1)
	go tracer.backgroundFlush()

	return tracer, nil
}

// StartSpan creates and starts a new span.
func (t *HawkTracer) StartSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span) {
	span := &Span{
		TraceID:    uuid.New().String(),
		SpanID:     uuid.New().String(),
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
	}

	// Apply options
	for _, opt := range opts {
		opt(span)
	}

	// Link to parent if exists
	if parent := SpanFromContext(ctx); parent != nil {
		span.TraceID = parent.TraceID
		span.ParentID = parent.SpanID
	}

	return ContextWithSpan(ctx, span), span
}

// EndSpan completes a span and buffers it for export.
func (t *HawkTracer) EndSpan(span *Span) {
	span.EndTime = time.Now()
	span.Duration = span.EndTime.Sub(span.StartTime)

	// Apply privacy redaction
	span = t.redact(span)

	// Buffer for export
	t.buffer.add(span)

	// Flush if buffer is full
	if t.buffer.shouldFlush() {
		go func() { _ = t.flushNow() }()
	}
}

// RecordMetric records a metric by creating a short-lived span with metric attributes.
// Metrics are exported as spans with span.kind="metric" for Hawk to aggregate.
func (t *HawkTracer) RecordMetric(name string, value float64, labels map[string]string) {
	// Create a metric span
	ctx := context.Background()
	_, span := t.StartSpan(ctx, "metric."+name, WithSpanKind("metric"))

	// Set metric value
	span.SetAttribute("metric.value", value)
	span.SetAttribute("metric.name", name)

	// Add labels as attributes
	for k, v := range labels {
		span.SetAttribute("metric.label."+k, v)
	}

	// End immediately (metrics are point-in-time)
	t.EndSpan(span)
}

// RecordEvent records a standalone event.
// If a span exists in the context, adds the event to that span.
// Otherwise, creates a short-lived event span.
func (t *HawkTracer) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
	// Try to add to existing span in context
	if span := SpanFromContext(ctx); span != nil {
		span.AddEvent(name, attributes)
		return
	}

	// No span in context, create a standalone event span
	_, span := t.StartSpan(ctx, "event."+name, WithSpanKind("event"))

	// Add attributes
	for k, v := range attributes {
		span.SetAttribute(k, v)
	}

	// End immediately
	t.EndSpan(span)
}

// Flush forces immediate export of all buffered spans.
func (t *HawkTracer) Flush(ctx context.Context) error {
	return t.flushNow()
}

// Close stops the background flusher and flushes remaining spans.
func (t *HawkTracer) Close() error {
	close(t.stopCh)
	t.wg.Wait()
	return t.flushNow()
}

// backgroundFlush periodically flushes buffered spans.
func (t *HawkTracer) backgroundFlush() {
	defer t.wg.Done()

	ticker := time.NewTicker(t.config.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_ = t.flushNow()
		case <-t.stopCh:
			return
		}
	}
}

// flushNow exports all buffered spans to Hawk with retry and exponential backoff.
func (t *HawkTracer) flushNow() error {
	spans := t.buffer.drain()
	if len(spans) == 0 {
		return nil
	}

	// Prepare payload once (outside retry loop)
	payload := hawkExportPayload{
		Spans: make([]hawkSpan, len(spans)),
	}

	for i, span := range spans {
		payload.Spans[i] = hawkSpan{
			TraceID:    span.TraceID,
			SpanID:     span.SpanID,
			ParentID:   span.ParentID,
			Name:       span.Name,
			StartTime:  span.StartTime.Format(time.RFC3339Nano),
			EndTime:    span.EndTime.Format(time.RFC3339Nano),
			DurationMS: span.Duration.Milliseconds(),
			Status:     span.Status.Code.String(),
			Attributes: span.Attributes,
			Events:     convertEvents(span.Events),
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal spans: %w", err)
	}

	// Retry with exponential backoff
	var lastErr error
	backoff := t.config.RetryBackoff

	for attempt := 0; attempt <= t.config.MaxRetries; attempt++ {
		// Create fresh request for each attempt
		req, err := http.NewRequest("POST", t.config.Endpoint, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		if t.config.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+t.config.APIKey)
		}

		// Attempt export
		resp, err := t.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("send to hawk (attempt %d/%d): %w", attempt+1, t.config.MaxRetries+1, err)

			// Exponential backoff before retry (except on last attempt)
			if attempt < t.config.MaxRetries {
				time.Sleep(backoff)
				backoff *= 2 // Double the backoff
			}
			continue
		}
		defer resp.Body.Close()

		// Check status code
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
			// Success!
			return nil
		}

		// Non-retryable status codes (4xx client errors)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return fmt.Errorf("hawk returned non-retryable status %d", resp.StatusCode)
		}

		// Retryable status codes (5xx server errors)
		lastErr = fmt.Errorf("hawk returned status %d (attempt %d/%d)", resp.StatusCode, attempt+1, t.config.MaxRetries+1)

		// Exponential backoff before retry (except on last attempt)
		if attempt < t.config.MaxRetries {
			time.Sleep(backoff)
			backoff *= 2
		}
	}

	// All retries exhausted
	return fmt.Errorf("hawk export failed after %d attempts: %w", t.config.MaxRetries+1, lastErr)
}

// PII redaction patterns (compiled once at package init)
var (
	emailPattern      = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	phonePattern      = regexp.MustCompile(`\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`)
	ssnPattern        = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	creditCardPattern = regexp.MustCompile(`\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`)
)

// redact applies privacy rules to span before export.
// This removes sensitive data (credentials, PII) while preserving debugging utility.
func (t *HawkTracer) redact(span *Span) *Span {
	if !t.config.Privacy.RedactCredentials && !t.config.Privacy.RedactPII {
		return span
	}

	// Create allowlist map for fast lookup
	allowed := make(map[string]bool)
	for _, key := range t.config.Privacy.AllowedAttributes {
		allowed[key] = true
	}

	// Redact credential keys (remove entire attribute)
	if t.config.Privacy.RedactCredentials {
		credentialKeys := []string{
			"password", "api_key", "token", "secret", "authorization",
			"access_token", "refresh_token", "bearer", "apikey",
			"client_secret", "private_key", "ssh_key", "aws_secret",
		}
		for _, key := range credentialKeys {
			if !allowed[key] {
				delete(span.Attributes, key)
			}
		}

		// Also check for credential patterns in attribute keys
		for key := range span.Attributes {
			keyLower := strings.ToLower(key)
			if !allowed[key] {
				if strings.Contains(keyLower, "password") ||
					strings.Contains(keyLower, "secret") ||
					strings.Contains(keyLower, "token") ||
					strings.Contains(keyLower, "key") && strings.Contains(keyLower, "api") {
					delete(span.Attributes, key)
				}
			}
		}
	}

	// Redact PII patterns from attribute values
	if t.config.Privacy.RedactPII {
		for key, value := range span.Attributes {
			if allowed[key] {
				continue // Skip allowlisted attributes
			}

			// Only redact string values
			if strVal, ok := value.(string); ok {
				redacted := strVal

				// Redact emails
				redacted = emailPattern.ReplaceAllString(redacted, "[EMAIL_REDACTED]")

				// Redact phone numbers
				redacted = phonePattern.ReplaceAllString(redacted, "[PHONE_REDACTED]")

				// Redact SSN
				redacted = ssnPattern.ReplaceAllString(redacted, "[SSN_REDACTED]")

				// Redact credit cards
				redacted = creditCardPattern.ReplaceAllString(redacted, "[CARD_REDACTED]")

				// Optionally redact IP addresses (can be debated if PII)
				// redacted = ipv4Pattern.ReplaceAllString(redacted, "[IP_REDACTED]")

				// Update attribute if changed
				if redacted != strVal {
					span.Attributes[key] = redacted
				}
			}
		}

		// Redact PII from events
		for i := range span.Events {
			for key, value := range span.Events[i].Attributes {
				if allowed[key] {
					continue
				}

				if strVal, ok := value.(string); ok {
					redacted := strVal
					redacted = emailPattern.ReplaceAllString(redacted, "[EMAIL_REDACTED]")
					redacted = phonePattern.ReplaceAllString(redacted, "[PHONE_REDACTED]")
					redacted = ssnPattern.ReplaceAllString(redacted, "[SSN_REDACTED]")
					redacted = creditCardPattern.ReplaceAllString(redacted, "[CARD_REDACTED]")

					if redacted != strVal {
						span.Events[i].Attributes[key] = redacted
					}
				}
			}
		}
	}

	return span
}

// traceBuffer is a thread-safe buffer for spans.
type traceBuffer struct {
	mu       sync.Mutex
	spans    []*Span
	capacity int
}

func newTraceBuffer(capacity int) *traceBuffer {
	return &traceBuffer{
		spans:    make([]*Span, 0, capacity),
		capacity: capacity,
	}
}

func (b *traceBuffer) add(span *Span) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spans = append(b.spans, span)
}

func (b *traceBuffer) shouldFlush() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.spans) >= b.capacity
}

func (b *traceBuffer) drain() []*Span {
	b.mu.Lock()
	defer b.mu.Unlock()

	spans := b.spans
	b.spans = make([]*Span, 0, b.capacity)
	return spans
}

// Hawk export payload format.
type hawkExportPayload struct {
	Spans []hawkSpan `json:"spans"`
}

type hawkSpan struct {
	TraceID    string                 `json:"trace_id"`
	SpanID     string                 `json:"span_id"`
	ParentID   string                 `json:"parent_span_id,omitempty"`
	Name       string                 `json:"name"`
	StartTime  string                 `json:"start_time"`
	EndTime    string                 `json:"end_time"`
	DurationMS int64                  `json:"duration_ms"`
	Status     string                 `json:"status"`
	Attributes map[string]interface{} `json:"attributes"`
	Events     []hawkEvent            `json:"events,omitempty"`
}

type hawkEvent struct {
	Timestamp  string                 `json:"timestamp"`
	Name       string                 `json:"name"`
	Attributes map[string]interface{} `json:"attributes,omitempty"`
}

func convertEvents(events []Event) []hawkEvent {
	if len(events) == 0 {
		return nil
	}

	hawkEvents := make([]hawkEvent, len(events))
	for i, e := range events {
		hawkEvents[i] = hawkEvent{
			Timestamp:  e.Timestamp.Format(time.RFC3339Nano),
			Name:       e.Name,
			Attributes: e.Attributes,
		}
	}
	return hawkEvents
}

// Ensure HawkTracer implements Tracer interface.
var _ Tracer = (*HawkTracer)(nil)
