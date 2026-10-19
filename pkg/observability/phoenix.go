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
package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Phoenix annotator kinds.
const (
	PhoenixAnnotatorLLM   = "LLM"
	PhoenixAnnotatorCode  = "CODE"
	PhoenixAnnotatorHuman = "HUMAN"
)

// Defaults for PhoenixClientConfig.
const (
	defaultPhoenixTimeout    = 5 * time.Second
	defaultPhoenixQueueSize  = 256
	defaultPhoenixMaxRetries = 2
	defaultPhoenixRetryDelay = 500 * time.Millisecond
)

// ErrPhoenixClientStopped is returned by the Annotate methods after Stop.
var ErrPhoenixClientStopped = errors.New("phoenix client stopped")

// PhoenixAnnotation is one annotation for Arize Phoenix's REST API. TargetID
// is the OTel trace ID, span ID or session ID, depending on the method it is
// passed to. Identifier makes the annotation an upsert: Phoenix keeps one
// annotation per (name, target, identifier).
type PhoenixAnnotation struct {
	TargetID      string
	Name          string
	AnnotatorKind string
	Label         string
	Score         *float64
	Explanation   string
	Metadata      map[string]interface{}
	Identifier    string
}

// PhoenixClientConfig configures a PhoenixClient.
type PhoenixClientConfig struct {
	// BaseURL is Phoenix's HTTP address, for example http://127.0.0.1:6006.
	BaseURL string
	// APIKey, when set, is sent as a bearer token.
	APIKey string
	// Timeout bounds one HTTP request (default 5s).
	Timeout time.Duration
	// QueueSize bounds the requests waiting to be sent (default 256); a full
	// queue drops new annotations with a warning.
	QueueSize int
	// MaxRetries is the number of retries after a network error, HTTP 429 or
	// HTTP 5xx (default 2; negative disables retries).
	MaxRetries int
	// RetryDelay is the first retry's delay; each later retry waits one more
	// RetryDelay (default 500ms).
	RetryDelay time.Duration
	// HTTPClient replaces the default client (tests).
	HTTPClient *http.Client
	Logger     *zap.Logger
}

// PhoenixClient posts annotations to Phoenix from a background worker, so the
// Annotate methods never block on the network and never fail the caller
// because Phoenix is unreachable; failures are logged. Label and explanation
// text is cleaned of credentials and PII like exported span attributes.
//
// Requests use Phoenix's default asynchronous mode: an annotation whose trace
// or span has not been exported yet (the OTLP exporter batches spans) is
// queued by Phoenix and retried there, instead of failing with 404 as the
// synchronous mode does.
type PhoenixClient struct {
	baseURL    string
	apiKey     string
	http       *http.Client
	logger     *zap.Logger
	maxRetries int
	retryDelay time.Duration

	mu      sync.Mutex
	stopped bool
	queue   chan phoenixRequest
	done    chan struct{}
}

type phoenixTarget struct {
	path string // REST path below the base URL
	key  string // JSON key of the target ID
}

var (
	phoenixTraceTarget   = phoenixTarget{path: "/v1/trace_annotations", key: "trace_id"}     //nolint:gochecknoglobals
	phoenixSpanTarget    = phoenixTarget{path: "/v1/span_annotations", key: "span_id"}       //nolint:gochecknoglobals
	phoenixSessionTarget = phoenixTarget{path: "/v1/session_annotations", key: "session_id"} //nolint:gochecknoglobals
)

type phoenixRequest struct {
	target phoenixTarget
	items  []PhoenixAnnotation
}

// NewPhoenixClient validates cfg and starts the client's worker. Call Stop
// to send what is queued and end the worker.
func NewPhoenixClient(cfg PhoenixClientConfig) (*PhoenixClient, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("phoenix base URL must be an http or https URL, got %q", cfg.BaseURL)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultPhoenixTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = defaultPhoenixQueueSize
	}
	maxRetries := cfg.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultPhoenixMaxRetries
	} else if maxRetries < 0 {
		maxRetries = 0
	}
	retryDelay := cfg.RetryDelay
	if retryDelay <= 0 {
		retryDelay = defaultPhoenixRetryDelay
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	c := &PhoenixClient{
		baseURL:    base,
		apiKey:     cfg.APIKey,
		http:       httpClient,
		logger:     logger,
		maxRetries: maxRetries,
		retryDelay: retryDelay,
		queue:      make(chan phoenixRequest, queueSize),
		done:       make(chan struct{}),
	}
	go c.run()
	return c, nil
}

// AnnotateTraces queues annotations on traces (TargetID = OTel trace ID).
func (c *PhoenixClient) AnnotateTraces(items []PhoenixAnnotation) error {
	return c.enqueue(phoenixTraceTarget, items)
}

// AnnotateSpans queues annotations on spans (TargetID = OTel span ID).
func (c *PhoenixClient) AnnotateSpans(items []PhoenixAnnotation) error {
	return c.enqueue(phoenixSpanTarget, items)
}

// AnnotateSessions queues annotations on sessions (TargetID = session.id).
func (c *PhoenixClient) AnnotateSessions(items []PhoenixAnnotation) error {
	return c.enqueue(phoenixSessionTarget, items)
}

// Stop stops accepting annotations and waits until the queued ones are sent
// or ctx ends. It is safe to call more than once, and on a nil client.
func (c *PhoenixClient) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if !c.stopped {
		c.stopped = true
		close(c.queue)
	}
	c.mu.Unlock()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// enqueue validates items and queues them without blocking. A full queue
// drops them with a warning; an invalid annotation is the caller's error.
func (c *PhoenixClient) enqueue(target phoenixTarget, items []PhoenixAnnotation) error {
	if len(items) == 0 {
		return nil
	}
	for _, a := range items {
		if err := validatePhoenixAnnotation(a); err != nil {
			return err
		}
	}
	req := phoenixRequest{target: target, items: append([]PhoenixAnnotation(nil), items...)}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return ErrPhoenixClientStopped
	}
	select {
	case c.queue <- req:
	default:
		c.logger.Warn("phoenix annotation queue full; annotations dropped",
			zap.String("path", target.path), zap.Int("count", len(items)))
	}
	return nil
}

func validatePhoenixAnnotation(a PhoenixAnnotation) error {
	switch {
	case strings.TrimSpace(a.TargetID) == "":
		return fmt.Errorf("phoenix annotation %q: target ID required", a.Name)
	case strings.TrimSpace(a.Name) == "":
		return fmt.Errorf("phoenix annotation name required")
	case a.Name == "note":
		// Phoenix reserves "note" for trace and span notes (POST /v1/trace_notes).
		return fmt.Errorf("phoenix annotation name %q is reserved", a.Name)
	}
	switch a.AnnotatorKind {
	case PhoenixAnnotatorLLM, PhoenixAnnotatorCode, PhoenixAnnotatorHuman:
		return nil
	default:
		return fmt.Errorf("phoenix annotation %q: annotator kind must be LLM, CODE or HUMAN, got %q", a.Name, a.AnnotatorKind)
	}
}

func (c *PhoenixClient) run() {
	defer close(c.done)
	for req := range c.queue {
		c.send(req)
	}
}

// send posts one request, retrying network errors, 429 and 5xx.
func (c *PhoenixClient) send(req phoenixRequest) {
	body, err := json.Marshal(map[string]interface{}{"data": phoenixPayload(req)})
	if err != nil {
		c.logger.Warn("phoenix annotations not sent: encoding failed", zap.Error(err))
		return
	}
	endpoint := c.baseURL + req.target.path
	for attempt := 0; ; attempt++ {
		status, err := c.post(endpoint, body)
		retryable := err != nil || status == http.StatusTooManyRequests || status >= 500
		if err == nil && status >= 200 && status < 300 {
			return
		}
		if !retryable || attempt >= c.maxRetries {
			fields := []zap.Field{zap.String("url", endpoint), zap.Int("count", len(req.items)), zap.Int("attempts", attempt+1)}
			if err != nil {
				fields = append(fields, zap.Error(err))
			} else {
				fields = append(fields, zap.Int("status", status))
			}
			c.logger.Warn("phoenix annotations not sent", fields...)
			return
		}
		time.Sleep(time.Duration(attempt+1) * c.retryDelay)
	}
}

func (c *PhoenixClient) post(endpoint string, body []byte) (int, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// phoenixPayload renders the request body's data array, with label and
// explanation cleaned of credentials and PII.
func phoenixPayload(req phoenixRequest) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(req.items))
	for _, a := range req.items {
		item := map[string]interface{}{
			req.target.key:   a.TargetID,
			"name":           a.Name,
			"annotator_kind": a.AnnotatorKind,
		}
		result := map[string]interface{}{}
		if a.Label != "" {
			result["label"] = redactPIIString(redactCredentialString(a.Label))
		}
		if a.Score != nil {
			result["score"] = *a.Score
		}
		if a.Explanation != "" {
			result["explanation"] = redactPIIString(redactCredentialString(a.Explanation))
		}
		if len(result) > 0 {
			item["result"] = result
		}
		if len(a.Metadata) > 0 {
			item["metadata"] = a.Metadata
		}
		if a.Identifier != "" {
			item["identifier"] = a.Identifier
		}
		out = append(out, item)
	}
	return out
}
