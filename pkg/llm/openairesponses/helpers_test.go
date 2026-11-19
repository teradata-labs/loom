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

package openairesponses

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// testAPIKey is a fake credential; it never reaches a real endpoint.
const testAPIKey = "test-key-not-real-responses"

// recorder is an httptest server that records every request body and answers
// with handler.
type recorder struct {
	mu     sync.Mutex
	bodies [][]byte
	hdrs   []http.Header
	srv    *httptest.Server
}

func newRecorder(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int)) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, body)
		rec.hdrs = append(rec.hdrs, r.Header.Clone())
		n := len(rec.bodies)
		rec.mu.Unlock()
		handler(w, r, n)
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

// body returns request i decoded as a generic JSON object.
func (r *recorder) body(t *testing.T, i int) map[string]interface{} {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Greater(t, len(r.bodies), i, "server must have received request %d", i)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(r.bodies[i], &m), "request body must be JSON")
	return m
}

// jsonHandler answers every request with status and body.
func jsonHandler(status int, body string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// sseHandler answers every request with an event stream.
func sseHandler(stream string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, stream)
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) // #nosec G304 -- fixed test fixture names
	require.NoError(t, err, "fixture %s must exist", name)
	return string(b)
}

func weatherTool() shuttle.Tool {
	return &shuttle.MockTool{
		MockName:        "weather:get_weather",
		MockDescription: "Get weather",
		MockSchema: &shuttle.JSONSchema{
			Type:       "object",
			Properties: map[string]*shuttle.JSONSchema{"location": {Type: "string"}},
			Required:   []string{"location"},
		},
	}
}

// minimalCompleted is a small blocking response with one text output.
const minimalCompleted = `{"id":"gen_1","status":"completed","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`
