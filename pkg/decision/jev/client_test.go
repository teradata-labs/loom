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

package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// fakeKey is deliberately not shaped like a real credential.
const fakeKey = "jv_test_not_a_real_key"

func fullRequest(t *testing.T) *loomv1.DecisionRequest {
	t.Helper()
	choice, err := decision.Choice("What kind of failure?", map[string]any{
		"transient": "a retry may succeed",
		"auth":      "credentials rejected",
	}, decision.WithNoneOption("not a failure"))
	require.NoError(t, err)
	score, err := decision.Score("How severe?", "Cosmetic", "Degraded", "Blocking")
	require.NoError(t, err)
	req, err := decision.NewRequest("test.site", map[string]any{"tool": "t", "error_text": "boom"}, map[string]*loomv1.DecisionQuestion{
		"is_failure": decision.Noul("Did it fail?", decision.WithCriteria("it failed", "it succeeded")),
		"kind":       choice,
		"severity":   score,
	})
	require.NoError(t, err)
	return req
}

// goodBody is a recorded-shape TypeSafe response for fullRequest.
const goodBody = `{
  "model": "jev-1.13.0",
  "answers": {
    "is_failure": {"type": "noul", "noul": 0.97},
    "kind": {"type": "choice", "choice": "none_of_these",
             "probabilities": {"transient": 0.05, "auth": 0.01, "none_of_these": 0.94}, "confidence": 0.91},
    "severity": {"type": "score", "score": 1.84,
                 "legend": {"0": "Cosmetic", "1": "Degraded", "2": "Blocking"},
                 "probabilities": {"0": 0.02, "1": 0.12, "2": 0.86}, "confidence": 0.79}
  },
  "usage": {"input_tokens": 412, "output_tokens": 0}
}`

type capture struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  []wireRequest
}

func (c *capture) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var w wireRequest
	_ = json.Unmarshal(body, &w)
	c.mu.Lock()
	c.headers = append(c.headers, r.Header.Clone())
	c.bodies = append(c.bodies, w)
	c.mu.Unlock()
}

func newClient(t *testing.T, srv *httptest.Server, mut func(*Config)) *Client {
	t.Helper()
	cfg := Config{BaseURL: srv.URL, APIKey: fakeKey, RequestsPerMinute: -1, Timeout: 2 * time.Second}
	if mut != nil {
		mut(&cfg)
	}
	c, err := New(cfg)
	require.NoError(t, err)
	return c
}

func TestNewDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	_, err := New(Config{})
	assert.Error(t, err, "API key required")

	c, err := New(Config{APIKey: fakeKey})
	require.NoError(t, err)
	assert.Equal(t, DefaultBaseURL+DefaultPath, c.URL())
	assert.Equal(t, DefaultModel, c.Model())
	assert.Equal(t, Name, c.Name())
	assert.NotNil(t, c.limiter)

	g, err := New(Config{APIKey: fakeKey, BaseURL: VercelGatewayBaseURL + "/", Path: "v1/systemone", RequestsPerMinute: -1})
	require.NoError(t, err)
	assert.Equal(t, "https://ai-gateway.vercel.sh/typesafe/v1/systemone", g.URL(), "slashes normalized")
	assert.Nil(t, g.limiter, "negative rpm disables the limiter")
}

func TestDecideHappyPathAndWireShape(t *testing.T) {
	t.Parallel()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/systemone", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(goodBody))
	}))
	t.Cleanup(srv.Close)
	c := newClient(t, srv, func(cfg *Config) { cfg.ExtraHeaders = map[string]string{"X-Gateway-Route": "test"} })

	resp, err := c.Decide(context.Background(), fullRequest(t))
	require.NoError(t, err)
	assert.Equal(t, "jev-1.13.0", resp.Model)
	assert.Equal(t, int64(412), resp.Usage.InputTokens)
	assert.InDelta(t, 412*0.042/1_000_000, resp.Usage.CostUsd, 1e-12)

	n, err := decision.NoulOf(resp, "is_failure")
	require.NoError(t, err)
	assert.InDelta(t, 0.97, n.Probability, 1e-9)
	ch, err := decision.ChoiceOf(resp, "kind")
	require.NoError(t, err)
	assert.Equal(t, decision.NoneOption, ch.Choice)
	assert.InDelta(t, 0.91, ch.Confidence, 1e-9, "wire confidence kept")
	sc, err := decision.ScoreOf(resp, "severity")
	require.NoError(t, err)
	assert.InDelta(t, 1.84, sc.Score, 1e-9)
	assert.Equal(t, "Blocking", sc.Legend["2"])

	require.Len(t, cap.bodies, 1)
	h := cap.headers[0]
	assert.Equal(t, "Bearer "+fakeKey, h.Get("Authorization"))
	assert.Equal(t, "application/json", h.Get("Content-Type"))
	assert.Equal(t, "test", h.Get("X-Gateway-Route"))
	body := cap.bodies[0]
	assert.Equal(t, DefaultModel, body.Model)
	assert.JSONEq(t, `{"tool":"t","error_text":"boom"}`, string(body.State))
	require.Len(t, body.Questions, 3)
	assert.Equal(t, "noul", body.Questions["is_failure"].Type)
	assert.JSONEq(t, `{"true":"it failed","false":"it succeeded"}`, string(body.Questions["is_failure"].Criteria))
	assert.Equal(t, "choice", body.Questions["kind"].Type)
	assert.JSONEq(t, `{"transient":"a retry may succeed","auth":"credentials rejected","none_of_these":"not a failure"}`, string(body.Questions["kind"].Criteria))
	assert.Equal(t, "score", body.Questions["severity"].Type)
	assert.JSONEq(t, `["Cosmetic","Degraded","Blocking"]`, string(body.Questions["severity"].Criteria))
	assert.JSONEq(t, `"How severe?"`, string(body.Questions["severity"].Instructions))
}

func TestDecideGatewayCostWins(t *testing.T) {
	t.Parallel()
	body := `{"model":"typesafe-ai/jev","answers":{
	  "is_failure":{"type":"noul","noul":0.2},
	  "kind":{"type":"choice","choice":"auth","probabilities":{"transient":0.1,"auth":0.8,"none_of_these":0.1},"confidence":0.7},
	  "severity":{"type":"score","score":0.5,"legend":{"0":"a","1":"b","2":"c"},"probabilities":{"0":0.5,"1":0.5,"2":0},"confidence":0.25}},
	  "usage":{"input_tokens":275,"output_tokens":20},
	  "provider_metadata":{"gateway":{"cost":"0.00001155","generationId":"gen_x"}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	resp, err := newClient(t, srv, func(cfg *Config) { cfg.Model = VercelGatewayModel }).Decide(context.Background(), fullRequest(t))
	require.NoError(t, err)
	assert.Equal(t, "typesafe-ai/jev", resp.Model)
	assert.InDelta(t, 0.00001155, resp.Usage.CostUsd, 1e-12, "gateway-reported cost replaces the list-price estimate")
	assert.Equal(t, int64(20), resp.Usage.OutputTokens)
}

func TestDecideRequestModelOverridesConfig(t *testing.T) {
	t.Parallel()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		_, _ = w.Write([]byte(goodBody))
	}))
	t.Cleanup(srv.Close)
	req := fullRequest(t)
	req.Model = "jev-1.12.0"
	_, err := newClient(t, srv, nil).Decide(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "jev-1.12.0", cap.bodies[0].Model)
}

func TestDecideFillsMissingConfidenceAndLegend(t *testing.T) {
	t.Parallel()
	body := `{"model":"jev-1.13.0","answers":{
	  "is_failure":{"type":"noul","noul":0.2},
	  "kind":{"type":"choice","choice":"auth","probabilities":{"transient":0.1,"auth":0.8,"none_of_these":0.1}},
	  "severity":{"type":"score","score":0.5,"probabilities":{"0":0.5,"1":0.5,"2":0}}},
	  "usage":{"input_tokens":10,"output_tokens":0}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	resp, err := newClient(t, srv, nil).Decide(context.Background(), fullRequest(t))
	require.NoError(t, err)
	ch, _ := decision.ChoiceOf(resp, "kind")
	assert.InDelta(t, decision.Confidence(map[string]float64{"transient": 0.1, "auth": 0.8, "none_of_these": 0.1}), ch.Confidence, 1e-9)
	sc, _ := decision.ScoreOf(resp, "severity")
	assert.Equal(t, map[string]string{"0": "Cosmetic", "1": "Degraded", "2": "Blocking"}, sc.Legend, "legend from the question")
}

func TestDecideRejectsMalformedAnswers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "not json", body: `<html>`},
		{name: "unknown question", body: `{"answers":{"zzz":{"type":"noul","noul":0.1}}}`},
		{name: "missing question", body: `{"answers":{"is_failure":{"type":"noul","noul":0.1}}}`},
		{name: "choice not an option", body: `{"answers":{"is_failure":{"type":"noul","noul":0.1},"kind":{"type":"choice","choice":"bogus","probabilities":{"bogus":1}},"severity":{"type":"score","score":0,"probabilities":{"0":1}}}}`},
		{name: "noul out of range", body: `{"answers":{"is_failure":{"type":"noul","noul":7},"kind":{"type":"choice","choice":"auth","probabilities":{"auth":1}},"severity":{"type":"score","score":0,"probabilities":{"0":1}}}}`},
		{name: "score missing probabilities", body: `{"answers":{"is_failure":{"type":"noul","noul":0.1},"kind":{"type":"choice","choice":"auth","probabilities":{"auth":1}},"severity":{"type":"score","score":0}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tt.body)) }))
			t.Cleanup(srv.Close)
			_, err := newClient(t, srv, nil).Decide(context.Background(), fullRequest(t))
			assert.True(t, errors.Is(err, decision.ErrMalformedAnswer), "got %v", err)
		})
	}
}

func TestDecideStatusMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status    int
		wantIs    error
		wantCalls int32 // with MaxAttempts 3
	}{
		{status: 401, wantIs: decision.ErrUnauthorized, wantCalls: 1},
		{status: 403, wantIs: decision.ErrUnauthorized, wantCalls: 1},
		{status: 422, wantIs: decision.ErrValidation, wantCalls: 1},
		{status: 400, wantIs: decision.ErrValidation, wantCalls: 1},
		{status: 429, wantIs: decision.ErrRateLimited, wantCalls: 3},
		{status: 529, wantIs: decision.ErrOverloaded, wantCalls: 3},
		{status: 503, wantIs: decision.ErrOverloaded, wantCalls: 3},
		{status: 500, wantIs: nil, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status)+"_"+itoa(tt.status), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"error":"nope"}`))
			}))
			t.Cleanup(srv.Close)
			_, err := newClient(t, srv, nil).Decide(context.Background(), fullRequest(t))
			require.Error(t, err)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tt.status, apiErr.Status)
			if tt.wantIs != nil {
				assert.True(t, errors.Is(err, tt.wantIs), "want %v, got %v", tt.wantIs, err)
			}
			assert.Equal(t, tt.wantCalls, calls.Load())
		})
	}
}

func TestDecideRetriesThenSucceeds(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(goodBody))
	}))
	t.Cleanup(srv.Close)
	resp, err := newClient(t, srv, nil).Decide(context.Background(), fullRequest(t))
	require.NoError(t, err)
	assert.Equal(t, "jev-1.13.0", resp.Model)
	assert.Equal(t, int32(3), calls.Load())
}

func TestDecideHonoursContextDuringBackoff(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newClient(t, srv, nil).Decide(ctx, fullRequest(t))
	// A 30 s Retry-After cannot fit in a 100 ms budget, so the client hands
	// back the provider's own error instead of starting the sleep; if the
	// first attempt itself outran the deadline, a deadline error is the
	// honest answer. Either way it must not wait out the Retry-After.
	assert.True(t, errors.Is(err, decision.ErrRateLimited) || errors.Is(err, context.DeadlineExceeded), "got %v", err)
	assert.Less(t, time.Since(start), 5*time.Second, "did not sleep the full Retry-After")
}

func TestDecideTransportErrorIsRetriedAndTyped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(goodBody)) }))
	url := srv.URL
	srv.Close() // nothing listening
	c, err := New(Config{BaseURL: url, APIKey: fakeKey, RequestsPerMinute: -1, Timeout: 500 * time.Millisecond, MaxAttempts: 2})
	require.NoError(t, err)
	_, err = c.Decide(context.Background(), fullRequest(t))
	var te *TransportError
	assert.ErrorAs(t, err, &te)
	assert.True(t, isRetryable(err))
}

func TestDecideValidatesBeforeSending(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }))
	t.Cleanup(srv.Close)
	_, err := newClient(t, srv, nil).Decide(context.Background(), &loomv1.DecisionRequest{})
	assert.True(t, errors.Is(err, decision.ErrValidation))
	assert.Equal(t, int32(0), calls.Load(), "invalid requests never leave the process")
}

func TestAuthHeaderVariants(t *testing.T) {
	t.Parallel()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		_, _ = w.Write([]byte(goodBody))
	}))
	t.Cleanup(srv.Close)
	c := newClient(t, srv, func(cfg *Config) { cfg.AuthHeader = "x-api-key"; cfg.AuthScheme = "" })
	_, err := c.Decide(context.Background(), fullRequest(t))
	require.NoError(t, err)
	assert.Equal(t, fakeKey, cap.headers[0].Get("x-api-key"))
	assert.Empty(t, cap.headers[0].Get("Authorization"))
}

func TestParseRetryAfterAndBackoff(t *testing.T) {
	t.Parallel()
	assert.Equal(t, time.Duration(0), parseRetryAfter(""))
	assert.Equal(t, 7*time.Second, parseRetryAfter("7"))
	assert.Equal(t, time.Duration(0), parseRetryAfter("soon"))
	future := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	got := parseRetryAfter(future)
	assert.Greater(t, got, time.Second)
	assert.LessOrEqual(t, got, 3*time.Second)
	assert.Equal(t, time.Duration(0), parseRetryAfter(time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)), "past dates are zero")

	for attempt := 1; attempt <= 6; attempt++ {
		d := backoff(attempt, 0)
		assert.GreaterOrEqual(t, d, 250*time.Millisecond)
		assert.LessOrEqual(t, d, 4*time.Second)
	}
	assert.Equal(t, 5*time.Second, backoff(1, 5*time.Second), "Retry-After wins")
}

func TestLimiterPacesAndHonoursContext(t *testing.T) {
	t.Parallel()
	// 120 rpm = 2 tokens/s. The burst floors at one whole fan-out
	// (decision.DefaultChunkConcurrency = 4), so the first four are free and
	// the fifth waits half a second for the next token.
	l := newLimiter(120)
	now := time.Unix(1_000, 0)
	var slept []time.Duration
	l.now = func() time.Time { return now }
	l.last = now
	l.sleepFor = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		now = now.Add(d)
		return nil
	}
	ctx := context.Background()
	for i := 0; i < decision.DefaultChunkConcurrency; i++ {
		require.NoError(t, l.wait(ctx), "call %d is inside the burst", i+1)
	}
	require.Empty(t, slept, "one fan-out never queues against itself")
	require.NoError(t, l.wait(ctx))
	require.Len(t, slept, 1)
	assert.InDelta(t, 0.5, slept[0].Seconds(), 1e-6)

	l.sleepFor = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	assert.ErrorIs(t, l.wait(cctx), context.Canceled)
}

func TestLimiterConcurrentUnderRace(t *testing.T) {
	t.Parallel()
	l := newLimiter(600_000) // effectively unlimited; exercises the mutex only
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, l.wait(context.Background()))
		}()
	}
	wg.Wait()
}

func TestFromDecisionConfig(t *testing.T) {
	t.Parallel()
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	_, err := fromDecisionConfig(nil, env(nil))
	assert.ErrorIs(t, err, ErrNoCredentials)

	c, err := fromDecisionConfig(nil, env(map[string]string{EnvTypeSafeAPIKey: "ts"}))
	require.NoError(t, err)
	assert.Equal(t, "ts", c.APIKey)
	assert.Equal(t, "", c.BaseURL, "TypeSafe key: client defaults to the direct endpoint")

	c, err = fromDecisionConfig(nil, env(map[string]string{EnvAIGatewayAPIKey: "gw"}))
	require.NoError(t, err)
	assert.Equal(t, "gw", c.APIKey)
	assert.Equal(t, VercelGatewayBaseURL, c.BaseURL, "gateway key alone implies the gateway URL")
	assert.Equal(t, VercelGatewayModel, c.Model, "gateway implies its own model id")

	_, err = fromDecisionConfig(&loomv1.DecisionConfig{Model: "jev-1.13.0"}, env(map[string]string{EnvAIGatewayAPIKey: "gw"}))
	assert.ErrorIs(t, err, ErrGatewayModel, "a pinned TypeSafe id is rejected against the gateway")

	c, err = fromDecisionConfig(&loomv1.DecisionConfig{Model: "typesafe-ai/jev"}, env(map[string]string{EnvAIGatewayAPIKey: "gw"}))
	require.NoError(t, err)
	assert.Equal(t, "typesafe-ai/jev", c.Model)
	assert.True(t, IsGatewayURL(c.BaseURL))
	assert.False(t, IsGatewayURL(DefaultBaseURL))

	c, err = fromDecisionConfig(&loomv1.DecisionConfig{BaseUrl: "https://proxy.example/typesafe", Model: "jev-1.12.0", TimeoutMs: 1500},
		env(map[string]string{EnvTypeSafeAPIKey: "ts", EnvAIGatewayAPIKey: "gw"}))
	require.NoError(t, err)
	assert.Equal(t, "ts", c.APIKey, "TypeSafe key wins when both are set")
	assert.Equal(t, "https://proxy.example/typesafe", c.BaseURL, "config base URL wins")
	assert.Equal(t, "jev-1.12.0", c.Model)
	assert.Equal(t, 1500*time.Millisecond, c.Timeout)

	c, err = fromDecisionConfig(nil, env(map[string]string{EnvJevAPIKey: "j", EnvBaseURL: "https://env.example"}))
	require.NoError(t, err)
	assert.Equal(t, "j", c.APIKey)
	assert.Equal(t, "https://env.example", c.BaseURL)
}

func itoa(i int) string { return strconv.Itoa(i) }

// The client tells decision.Chunked how many questions one request should
// carry: the config's value, else the measured default.
func TestClientSizeHint(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	c, err := fromDecisionConfig(&loomv1.DecisionConfig{Model: "typesafe-ai/jev"}, env(map[string]string{EnvAIGatewayAPIKey: "gw"}))
	require.NoError(t, err)
	assert.Equal(t, 0, c.MaxQuestionsPerRequest, "config leaves the default to the client")
	cl, err := New(c)
	require.NoError(t, err)
	var hinter decision.SizeHinter = cl
	assert.Equal(t, DefaultMaxQuestionsPerRequest, hinter.MaxQuestionsPerRequest())

	c, err = fromDecisionConfig(&loomv1.DecisionConfig{Model: "typesafe-ai/jev", MaxQuestionsPerRequest: 24}, env(map[string]string{EnvAIGatewayAPIKey: "gw"}))
	require.NoError(t, err)
	cl, err = New(c)
	require.NoError(t, err)
	assert.Equal(t, 24, cl.MaxQuestionsPerRequest())
}

// A live caller gives the client a deadline it intends to act on: when the
// backoff would outlast it, the client returns the provider's error now so
// the caller can fall back, instead of sleeping the budget away and handing
// back a deadline error it cannot tell apart from a hung provider.
func TestDecideStopsRetryingBeforeTheDeadline(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"busy"}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newClient(t, srv, nil).Decide(ctx, fullRequest(t))
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.True(t, errors.Is(err, decision.ErrOverloaded), "the provider's error survives, not a deadline error: %v", err)
	assert.Equal(t, int32(1), calls.Load(), "a 5 s Retry-After does not fit in a 900 ms budget")
	assert.Less(t, elapsed, 800*time.Millisecond, "returned early instead of sleeping out the budget")
}

// A patient caller (the shadow path) still gets the full retry ladder.
func TestDecideStillRetriesWithRoomToSpare(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"busy"}`))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := newClient(t, srv, nil).Decide(ctx, fullRequest(t))
	require.Error(t, err)
	assert.Equal(t, int32(3), calls.Load(), "every attempt is used when the deadline allows")
}

func TestFitsBeforeDeadline(t *testing.T) {
	t.Parallel()
	assert.True(t, fitsBeforeDeadline(context.Background(), time.Hour), "no deadline, no limit")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	assert.True(t, fitsBeforeDeadline(ctx, 500*time.Millisecond))
	assert.False(t, fitsBeforeDeadline(ctx, 2*time.Second), "the wait alone would consume the budget")
	assert.False(t, fitsBeforeDeadline(ctx, 1900*time.Millisecond), "no room left for the attempt after the wait")
}

// The burst must admit one whole chunked request however low the rate is.
// Before this, a 64-candidate rerank at 80 rpm queued its own four chunks
// behind a 1.33-token bucket and blew the caller's 4 s budget: 30% of live
// recall visits were lost to "context deadline exceeded" that way.
func TestLimiterBurstAdmitsAWholeFanOut(t *testing.T) {
	t.Parallel()
	for _, rpm := range []float64{1, 30, 80, 120} {
		l := newLimiter(rpm)
		assert.GreaterOrEqual(t, l.burst, float64(decision.DefaultChunkConcurrency),
			"%v rpm must still admit one fan-out at once", rpm)
	}
	// Above the floor the burst is still one second's worth.
	assert.InDelta(t, 10, newLimiter(600).burst, 1e-9)
}

// A live caller's deadline is not something to queue past: the wait would
// consume the whole budget and then report a deadline error indistinguishable
// from a hung provider.
func TestLimiterFailsFastPastTheDeadline(t *testing.T) {
	t.Parallel()
	l := newLimiter(60) // 1 token/s, burst 4
	now := time.Unix(2_000, 0)
	l.now = func() time.Time { return now }
	l.last = now
	l.tokens = 0 // bucket empty: the next token is a second away
	slept := 0
	l.sleepFor = func(_ context.Context, d time.Duration) error { slept++; now = now.Add(d); return nil }

	tight, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := l.wait(tight)
	require.Error(t, err)
	assert.ErrorIs(t, err, decision.ErrRateLimited, "the caller learns it was throttled, not that time ran out")
	assert.Equal(t, 0, slept, "returned without sleeping the budget away")

	// A patient caller still waits it out.
	patient, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	require.NoError(t, l.wait(patient))
	assert.Equal(t, 1, slept)
}
