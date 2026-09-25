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

// Package jev is the decision.Decider for TypeSafe AI's Jev, a hosted
// System One decision model.
//
// The wire protocol is one JSON POST: state plus typed questions in, one
// typed answer per question out. The client speaks to the TypeSafe API
// directly or through any gateway that forwards the same request shape (the
// Vercel AI Gateway serves Jev); base URL, path and the auth header are
// configuration. It owns its own request budget (a per-minute limiter
// separate from the LLM slot scheduler, which meters a different resource),
// retries only what the API says is retryable (429, 529) and honours
// Retry-After, and computes cost from the configured input-token price.
//
// Nothing here is vendor-generic: the JSON field names are TypeSafe's. The
// generic contract lives in pkg/decision.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// Defaults.
const (
	// DefaultBaseURL is TypeSafe's own endpoint.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultPath is the System One evaluation path under the base URL.
	DefaultPath = "/v1/systemone"
	// DefaultModel is the pinned release this client was written against.
	// Aliases ("jev-latest") are accepted by the API but rejected by Loom's
	// config validation unless allow_alias is set.
	DefaultModel = "jev-1.13.0"
	// DefaultTimeout bounds one attempt. Independent measurements put the
	// direct endpoint at ~300 ms and gateways under 1 s at p95.
	DefaultTimeout = 5 * time.Second
	// DefaultMaxAttempts is the total attempts per Decide (1 + retries).
	DefaultMaxAttempts = 3
	// DefaultRequestsPerMinute stays under the published 1,200 rpm.
	DefaultRequestsPerMinute = 1000
	// DefaultMaxQuestionsPerRequest is the chunk size decision.Chunked uses
	// for this client when the config does not set one. Measured on the
	// LongMemEval A/B through the Vercel gateway (2,215 rerank requests):
	// requests of 1–10 questions failed 0%, 11–20 1%, 21–40 9%, 41–50 27%,
	// 51–64 39%, all with the upstream's 503 "temporarily unavailable".
	DefaultMaxQuestionsPerRequest = 16
	// DefaultPricePerMillionInputTokens is TypeSafe's list price; output is
	// free. Configurable so a price change is one line of config.
	DefaultPricePerMillionInputTokens = 0.042
	// Name is the Decider name written on traces and shadow rows.
	Name = "jev"
)

// Config configures a Client. Zero values take the defaults above.
type Config struct {
	BaseURL string
	Path    string
	// APIKey is sent as "<AuthScheme> <APIKey>" in AuthHeader.
	APIKey string
	// AuthHeader defaults to "Authorization"; AuthScheme to "Bearer". A
	// gateway that wants "x-api-key: <key>" sets AuthHeader and an empty
	// AuthScheme.
	AuthHeader string
	AuthScheme string
	// ExtraHeaders are sent verbatim on every request (gateway routing
	// headers, for example).
	ExtraHeaders map[string]string
	Model        string
	Timeout      time.Duration
	MaxAttempts  int
	// RequestsPerMinute caps this process's request rate; <= 0 disables the
	// limiter.
	RequestsPerMinute          float64
	PricePerMillionInputTokens float64
	// MaxQuestionsPerRequest is the size hint decision.Chunked reads
	// (decision.SizeHinter); <= 0 means DefaultMaxQuestionsPerRequest.
	MaxQuestionsPerRequest int
	// HTTPClient overrides the transport; its Timeout is ignored in favour of
	// per-attempt contexts.
	HTTPClient *http.Client
}

// Client is a decision.Decider backed by Jev.
type Client struct {
	cfg     Config
	url     string
	http    *http.Client
	limiter *limiter
}

// New validates cfg, applies defaults and returns a Client. An empty API key
// is an error: there is no anonymous access, and failing here is clearer than
// a 401 on the first decision.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("jev: API key is required (TYPESAFE_API_KEY or a gateway key)")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Path == "" {
		cfg.Path = DefaultPath
	}
	if cfg.AuthHeader == "" {
		cfg.AuthHeader = "Authorization"
		if cfg.AuthScheme == "" {
			cfg.AuthScheme = "Bearer"
		}
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxQuestionsPerRequest <= 0 {
		cfg.MaxQuestionsPerRequest = DefaultMaxQuestionsPerRequest
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.RequestsPerMinute == 0 {
		cfg.RequestsPerMinute = DefaultRequestsPerMinute
	}
	if cfg.PricePerMillionInputTokens == 0 {
		cfg.PricePerMillionInputTokens = DefaultPricePerMillionInputTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	c := &Client{
		cfg:  cfg,
		url:  strings.TrimRight(cfg.BaseURL, "/") + "/" + strings.TrimLeft(cfg.Path, "/"),
		http: cfg.HTTPClient,
	}
	if cfg.RequestsPerMinute > 0 {
		c.limiter = newLimiter(cfg.RequestsPerMinute)
	}
	return c, nil
}

// Name implements decision.Decider.
func (c *Client) Name() string { return Name }

// Model implements decision.Decider.
func (c *Client) Model() string { return c.cfg.Model }

// MaxQuestionsPerRequest implements decision.SizeHinter: the chunk size
// decision.Chunked uses for requests sent through this client.
func (c *Client) MaxQuestionsPerRequest() int { return c.cfg.MaxQuestionsPerRequest }

// URL is the resolved endpoint, for logs.
func (c *Client) URL() string { return c.url }

// Decide implements decision.Decider.
func (c *Client) Decide(ctx context.Context, req *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error) {
	if err := decision.Validate(req); err != nil {
		return nil, err
	}
	body, err := encodeRequest(req, c.cfg.Model)
	if err != nil {
		return nil, err
	}
	if c.limiter != nil {
		if err := c.limiter.wait(ctx); err != nil {
			return nil, err
		}
	}

	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		resp, retryAfter, err := c.attempt(ctx, body)
		if err == nil {
			out, err := decodeResponse(req, resp, c.cfg.PricePerMillionInputTokens)
			if err != nil {
				return nil, err
			}
			return out, nil
		}
		lastErr = err
		if !isRetryable(err) || attempt == c.cfg.MaxAttempts {
			break
		}
		wait := backoff(attempt, retryAfter)
		if !fitsBeforeDeadline(ctx, wait) {
			// The caller's deadline expires inside this backoff. Returning
			// now lets it fall back with time to spare, instead of sleeping
			// the budget away and handing back a deadline error. A patient
			// caller (the shadow path) has the room and still retries.
			break
		}
		if err := sleepCtx(ctx, wait); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// attempt performs one HTTP round trip. It returns the raw response body on
// 2xx, a typed error otherwise, and the parsed Retry-After when present.
func (c *Client) attempt(ctx context.Context, body []byte) ([]byte, time.Duration, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("jev: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.cfg.AuthScheme != "" {
		httpReq.Header.Set(c.cfg.AuthHeader, c.cfg.AuthScheme+" "+c.cfg.APIKey)
	} else {
		httpReq.Header.Set(c.cfg.AuthHeader, c.cfg.APIKey)
	}
	for k, v := range c.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, &TransportError{Err: err}
	}
	defer func() { _ = httpResp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, 4<<20))
	if err != nil {
		return nil, 0, &TransportError{Err: fmt.Errorf("read body: %w", err)}
	}
	if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
		return respBody, 0, nil
	}
	return nil, parseRetryAfter(httpResp.Header.Get("Retry-After")), statusError(httpResp.StatusCode, respBody)
}

// APIError is a non-2xx response. It unwraps to the matching decision
// sentinel so callers can errors.Is against the generic contract.
type APIError struct {
	Status int
	Body   string
}

// Error implements error.
func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Body)
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return fmt.Sprintf("jev: HTTP %d: %s", e.Status, msg)
}

// Unwrap maps the status onto the decision sentinels.
func (e *APIError) Unwrap() error {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return decision.ErrUnauthorized
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		return decision.ErrValidation
	case http.StatusTooManyRequests:
		return decision.ErrRateLimited
	case 529, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return decision.ErrOverloaded
	}
	return nil
}

// TransportError is a failure below HTTP: DNS, connect, TLS, a cut stream.
type TransportError struct{ Err error }

// Error implements error.
func (e *TransportError) Error() string { return "jev: transport: " + e.Err.Error() }

// Unwrap returns the underlying error.
func (e *TransportError) Unwrap() error { return e.Err }

func statusError(status int, body []byte) error {
	return &APIError{Status: status, Body: string(body)}
}

// isRetryable says whether an attempt error is worth another try: rate
// limits, overload, and transport failures. Auth and validation are not.
func isRetryable(err error) bool {
	var te *TransportError
	if errors.As(err, &te) {
		return true
	}
	return errors.Is(err, decision.ErrRateLimited) || errors.Is(err, decision.ErrOverloaded)
}

// backoff is the wait before attempt+1: Retry-After when the server said,
// else 500 ms doubling with jitter, capped at 4 s. The gateway's transient
// 503 ("try again shortly") clears in about a second; the first campaign's
// 200 ms base burned all three attempts inside that window.
func backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	base := 500 * time.Millisecond << (attempt - 1)
	if base > 4*time.Second {
		base = 4 * time.Second
	}
	jitter := time.Duration(rand.Int64N(int64(base) / 2)) // #nosec G404 -- backoff jitter, not a security boundary
	return base/2 + jitter
}

// retryRoundTripFloor is the least time another attempt needs to be worth
// starting: about the measured median round trip (215-440 ms across 3,700
// chunked requests through the gateway).
const retryRoundTripFloor = 250 * time.Millisecond

// fitsBeforeDeadline reports whether ctx leaves room to wait out a backoff
// and still make an attempt that could finish. Without a deadline, yes.
func fitsBeforeDeadline(ctx context.Context, wait time.Duration) bool {
	dl, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(dl) > wait+retryRoundTripFloor
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// parseRetryAfter accepts seconds or an HTTP date; anything else is 0.
func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// --- wire format -----------------------------------------------------------

type wireRequest struct {
	Model     string                  `json:"model"`
	State     json.RawMessage         `json:"state"`
	Questions map[string]wireQuestion `json:"questions"`
}

type wireQuestion struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

type wireResponse struct {
	Model   string                `json:"model"`
	Answers map[string]wireAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	// ProviderMetadata is present on gateway responses and carries the
	// gateway's own cost figure, which is authoritative when it is there.
	ProviderMetadata struct {
		Gateway struct {
			Cost string `json:"cost"`
		} `json:"gateway"`
	} `json:"provider_metadata"`
}

type wireAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// encodeRequest renders a DecisionRequest as TypeSafe JSON. Values pass
// through protojson so structured instructions and criteria stay structured.
func encodeRequest(req *loomv1.DecisionRequest, model string) ([]byte, error) {
	if req.Model != "" {
		model = req.Model
	}
	state, err := valueJSON(req.State)
	if err != nil {
		return nil, fmt.Errorf("jev: encode state: %w", err)
	}
	w := wireRequest{Model: model, State: state, Questions: make(map[string]wireQuestion, len(req.Questions))}
	for id, q := range req.Questions {
		instr, err := valueJSON(q.Instructions)
		if err != nil {
			return nil, fmt.Errorf("jev: encode %q instructions: %w", id, err)
		}
		wq := wireQuestion{Instructions: instr}
		switch k := q.Kind.(type) {
		case *loomv1.DecisionQuestion_Noul:
			wq.Type = "noul"
			crit := map[string]json.RawMessage{}
			if k.Noul.CriteriaTrue != nil {
				v, err := valueJSON(k.Noul.CriteriaTrue)
				if err != nil {
					return nil, fmt.Errorf("jev: encode %q criteria.true: %w", id, err)
				}
				crit["true"] = v
			}
			if k.Noul.CriteriaFalse != nil {
				v, err := valueJSON(k.Noul.CriteriaFalse)
				if err != nil {
					return nil, fmt.Errorf("jev: encode %q criteria.false: %w", id, err)
				}
				crit["false"] = v
			}
			if len(crit) > 0 {
				raw, _ := json.Marshal(crit)
				wq.Criteria = raw
			}
		case *loomv1.DecisionQuestion_Choice:
			wq.Type = "choice"
			crit := make(map[string]json.RawMessage, len(k.Choice.Options))
			for key, v := range k.Choice.Options {
				raw, err := valueJSON(v)
				if err != nil {
					return nil, fmt.Errorf("jev: encode %q option %q: %w", id, key, err)
				}
				crit[key] = raw
			}
			raw, _ := json.Marshal(crit)
			wq.Criteria = raw
		case *loomv1.DecisionQuestion_Score:
			wq.Type = "score"
			levels := make([]json.RawMessage, 0, len(k.Score.Levels))
			for i, v := range k.Score.Levels {
				raw, err := valueJSON(v)
				if err != nil {
					return nil, fmt.Errorf("jev: encode %q level %d: %w", id, i, err)
				}
				levels = append(levels, raw)
			}
			raw, _ := json.Marshal(levels)
			wq.Criteria = raw
		default:
			return nil, &decision.ValidationError{Field: "questions." + id + ".kind", Msg: "missing"}
		}
		w.Questions[id] = wq
	}
	return json.Marshal(w)
}

// decodeResponse parses TypeSafe JSON into a DecisionResponse and checks it
// against the request. Missing confidence is computed locally; missing score
// legends come from the question.
func decodeResponse(req *loomv1.DecisionRequest, body []byte, pricePerMillion float64) (*loomv1.DecisionResponse, error) {
	var w wireResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("%w: jev response is not JSON: %v", decision.ErrMalformedAnswer, err)
	}
	cost := float64(w.Usage.InputTokens) * pricePerMillion / 1_000_000
	if gc, err := strconv.ParseFloat(strings.TrimSpace(w.ProviderMetadata.Gateway.Cost), 64); err == nil && gc >= 0 {
		cost = gc
	}
	out := &loomv1.DecisionResponse{
		Model:   w.Model,
		Answers: make(map[string]*loomv1.DecisionAnswer, len(w.Answers)),
		Usage: &loomv1.DecisionUsage{
			InputTokens:  w.Usage.InputTokens,
			OutputTokens: w.Usage.OutputTokens,
			CostUsd:      cost,
		},
	}
	for id, a := range w.Answers {
		q, ok := req.Questions[id]
		if !ok {
			return nil, fmt.Errorf("%w: answer for unknown question %q", decision.ErrMalformedAnswer, id)
		}
		switch q.Kind.(type) {
		case *loomv1.DecisionQuestion_Noul:
			if a.Noul == nil {
				return nil, fmt.Errorf("%w: %q missing noul", decision.ErrMalformedAnswer, id)
			}
			out.Answers[id] = &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Noul{Noul: &loomv1.NoulAnswer{Probability: *a.Noul}}}
		case *loomv1.DecisionQuestion_Choice:
			if a.Choice == "" || len(a.Probabilities) == 0 {
				return nil, fmt.Errorf("%w: %q missing choice or probabilities", decision.ErrMalformedAnswer, id)
			}
			conf := decision.Confidence(a.Probabilities)
			if a.Confidence != nil {
				conf = *a.Confidence
			}
			out.Answers[id] = &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Choice{Choice: &loomv1.ChoiceAnswer{
				Choice: a.Choice, Probabilities: a.Probabilities, Confidence: conf,
			}}}
		case *loomv1.DecisionQuestion_Score:
			if a.Score == nil || len(a.Probabilities) == 0 {
				return nil, fmt.Errorf("%w: %q missing score or probabilities", decision.ErrMalformedAnswer, id)
			}
			conf := decision.Confidence(a.Probabilities)
			if a.Confidence != nil {
				conf = *a.Confidence
			}
			legend := a.Legend
			if len(legend) == 0 {
				legend = decision.LegendOf(q.GetScore())
			}
			out.Answers[id] = &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Score{Score: &loomv1.ScoreAnswer{
				Score: *a.Score, Probabilities: a.Probabilities, Legend: legend, Confidence: conf,
			}}}
		}
	}
	if err := decision.CheckAnswers(req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// valueJSON renders a protobuf Value as JSON; nil is JSON null.
func valueJSON(v *structpb.Value) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	raw, err := protojson.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}
