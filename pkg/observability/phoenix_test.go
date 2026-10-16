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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type phoenixCall struct {
	path, auth, contentType string
	body                    map[string]interface{}
}

// fakePhoenix records requests and answers with statuses[i] for request i
// (the last status repeats).
type fakePhoenix struct {
	mu       sync.Mutex
	calls    []phoenixCall
	statuses []int
	server   *httptest.Server
}

func newFakePhoenix(t *testing.T, statuses ...int) *fakePhoenix {
	t.Helper()
	f := &fakePhoenix{statuses: statuses}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		n := len(f.calls)
		f.calls = append(f.calls, phoenixCall{path: r.URL.RequestURI(), auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"), body: body})
		status := http.StatusOK
		if len(f.statuses) > 0 {
			status = f.statuses[min(n, len(f.statuses)-1)]
		}
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePhoenix) recorded() []phoenixCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]phoenixCall(nil), f.calls...)
}

func newTestPhoenixClient(t *testing.T, baseURL string, logger *zap.Logger) *PhoenixClient {
	t.Helper()
	c, err := NewPhoenixClient(PhoenixClientConfig{BaseURL: baseURL, APIKey: "k-123", RetryDelay: time.Millisecond, Logger: logger})
	if err != nil {
		t.Fatalf("NewPhoenixClient: %v", err)
	}
	return c
}

func stopClient(t *testing.T, c *PhoenixClient) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func score(v float64) *float64 { return &v }

func TestPhoenixClient_PostsEachTargetKind(t *testing.T) {
	f := newFakePhoenix(t)
	c := newTestPhoenixClient(t, f.server.URL+"/", nil)

	ann := PhoenixAnnotation{
		Name: "loom.correctness", AnnotatorKind: PhoenixAnnotatorLLM,
		Label: "PASS", Score: score(87.5), Explanation: "matches the reference",
		Metadata: map[string]interface{}{"judge_id": "j1"}, Identifier: "j1:correctness",
	}
	trace, span, session := ann, ann, ann
	trace.TargetID, span.TargetID, session.TargetID = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7", "sess-1"
	for _, err := range []error{c.AnnotateTraces([]PhoenixAnnotation{trace}), c.AnnotateSpans([]PhoenixAnnotation{span}), c.AnnotateSessions([]PhoenixAnnotation{session})} {
		if err != nil {
			t.Fatalf("Annotate: %v", err)
		}
	}
	stopClient(t, c)

	calls := f.recorded()
	if len(calls) != 3 {
		t.Fatalf("got %d requests, want 3", len(calls))
	}
	want := []struct{ path, key, id string }{
		{"/v1/trace_annotations", "trace_id", trace.TargetID},
		{"/v1/span_annotations", "span_id", span.TargetID},
		{"/v1/session_annotations", "session_id", session.TargetID},
	}
	for i, w := range want {
		got := calls[i]
		if got.path != w.path {
			t.Errorf("request %d path = %q, want %q (asynchronous mode, no sync=true)", i, got.path, w.path)
		}
		if got.auth != "Bearer k-123" || got.contentType != "application/json" {
			t.Errorf("request %d headers: auth %q, content type %q", i, got.auth, got.contentType)
		}
		data, _ := got.body["data"].([]interface{})
		if len(data) != 1 {
			t.Fatalf("request %d data = %v", i, got.body)
		}
		item := data[0].(map[string]interface{})
		if item[w.key] != w.id || item["name"] != "loom.correctness" || item["annotator_kind"] != "LLM" || item["identifier"] != "j1:correctness" {
			t.Errorf("request %d item = %v", i, item)
		}
		result := item["result"].(map[string]interface{})
		if result["label"] != "PASS" || result["score"] != 87.5 || result["explanation"] != "matches the reference" {
			t.Errorf("request %d result = %v", i, result)
		}
		if md := item["metadata"].(map[string]interface{}); md["judge_id"] != "j1" {
			t.Errorf("request %d metadata = %v", i, md)
		}
	}
}

func TestPhoenixClient_RetriesTransientFailures(t *testing.T) {
	tests := []struct {
		name     string
		statuses []int
		want     int
	}{
		{name: "503 then success", statuses: []int{503, 200}, want: 2},
		{name: "429 every time stops after two retries", statuses: []int{429}, want: 3},
		{name: "400 is not retried", statuses: []int{400}, want: 1},
		{name: "404 is not retried", statuses: []int{404}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakePhoenix(t, tt.statuses...)
			c := newTestPhoenixClient(t, f.server.URL, nil)
			if err := c.AnnotateTraces([]PhoenixAnnotation{{TargetID: "t1", Name: "loom.quality", AnnotatorKind: PhoenixAnnotatorLLM}}); err != nil {
				t.Fatal(err)
			}
			stopClient(t, c)
			if got := len(f.recorded()); got != tt.want {
				t.Fatalf("requests = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPhoenixClient_UnreachableIsLoggedNotReturned(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	f := newFakePhoenix(t)
	base := f.server.URL
	f.server.Close() // nothing listens there now
	c := newTestPhoenixClient(t, base, zap.New(core))

	if err := c.AnnotateTraces([]PhoenixAnnotation{{TargetID: "t1", Name: "loom.quality", AnnotatorKind: PhoenixAnnotatorLLM}}); err != nil {
		t.Fatalf("an unreachable Phoenix must not fail the caller: %v", err)
	}
	stopClient(t, c)
	if logs.FilterMessage("phoenix annotations not sent").Len() != 1 {
		t.Fatalf("want one warning, got %v", logs.All())
	}
}

func TestPhoenixClient_ValidatesAndRedacts(t *testing.T) {
	f := newFakePhoenix(t)
	c := newTestPhoenixClient(t, f.server.URL, nil)

	for _, bad := range []PhoenixAnnotation{
		{Name: "loom.quality", AnnotatorKind: PhoenixAnnotatorLLM},
		{TargetID: "t1", AnnotatorKind: PhoenixAnnotatorLLM},
		{TargetID: "t1", Name: "note", AnnotatorKind: PhoenixAnnotatorHuman},
		{TargetID: "t1", Name: "loom.quality", AnnotatorKind: "ROBOT"},
	} {
		if err := c.AnnotateTraces([]PhoenixAnnotation{bad}); err == nil {
			t.Errorf("annotation %+v must be rejected", bad)
		}
	}
	if err := c.AnnotateTraces([]PhoenixAnnotation{{TargetID: "t1", Name: "loom.quality", AnnotatorKind: PhoenixAnnotatorLLM,
		Explanation: "the user wrote from jane@example.com"}}); err != nil {
		t.Fatal(err)
	}
	stopClient(t, c)

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("requests = %d, want 1 (invalid annotations are not sent)", len(calls))
	}
	item := calls[0].body["data"].([]interface{})[0].(map[string]interface{})
	if expl := item["result"].(map[string]interface{})["explanation"].(string); strings.Contains(expl, "jane@example.com") {
		t.Fatalf("explanation not redacted: %q", expl)
	}
}

func TestPhoenixClient_StopEndsAcceptance(t *testing.T) {
	f := newFakePhoenix(t)
	c := newTestPhoenixClient(t, f.server.URL, nil)
	stopClient(t, c)
	stopClient(t, c) // twice is fine

	err := c.AnnotateTraces([]PhoenixAnnotation{{TargetID: "t1", Name: "loom.quality", AnnotatorKind: PhoenixAnnotatorLLM}})
	if !errors.Is(err, ErrPhoenixClientStopped) {
		t.Fatalf("Annotate after Stop = %v, want ErrPhoenixClientStopped", err)
	}
	var nilClient *PhoenixClient
	if err := nilClient.Stop(context.Background()); err != nil {
		t.Fatalf("nil Stop: %v", err)
	}
}

func TestNewPhoenixClient_RejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "127.0.0.1:6006", "ftp://phoenix", "http://"} {
		if _, err := NewPhoenixClient(PhoenixClientConfig{BaseURL: u}); err == nil {
			t.Errorf("base URL %q must be rejected", u)
		}
	}
}
