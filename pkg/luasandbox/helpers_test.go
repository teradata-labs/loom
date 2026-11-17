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

package luasandbox

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

// toolFunc implements one fake tool.
type toolFunc func(ctx context.Context, args map[string]any) (*CallResult, error)

// fakeHost is a Host for tests. It is safe for concurrent use so the same
// host can back parallel runs.
type fakeHost struct {
	mu       sync.Mutex
	tools    map[string]toolFunc
	schemas  map[string]map[string]any
	turn     map[int64]*CallResult
	calls    []string
	events   []ProgressEvent
	listErr  error
	panicOn  string // method name: "ListTools", "ToolSchema", "Progress"
	listHits int
}

func newFakeHost() *fakeHost {
	return &fakeHost{tools: map[string]toolFunc{}, schemas: map[string]map[string]any{}, turn: map[int64]*CallResult{}}
}

func (h *fakeHost) with(name string, f toolFunc) *fakeHost {
	h.tools[name] = f
	return h
}

// echo returns its arguments as data.
func echo(_ context.Context, args map[string]any) (*CallResult, error) {
	return &CallResult{OK: true, Data: args, Decision: "allow"}, nil
}

// text returns a fixed string.
func text(s string) toolFunc {
	return func(context.Context, map[string]any) (*CallResult, error) {
		return &CallResult{OK: true, Data: s, Decision: "allow"}, nil
	}
}

// failing returns a tool failure.
func failing(code, msg string) toolFunc {
	return func(context.Context, map[string]any) (*CallResult, error) {
		return &CallResult{OK: false, Error: &CallError{Code: code, Message: msg, Suggestion: "try again", Retryable: true}, Decision: "allow"}, nil
	}
}

// blocking waits for its context, like a slow remote tool that honours
// cancellation.
func blocking(ctx context.Context, _ map[string]any) (*CallResult, error) {
	<-ctx.Done()
	return &CallResult{OK: false, Error: &CallError{Code: CodeExecutionFailed, Message: ctx.Err().Error()}}, nil
}

func (h *fakeHost) ListTools(context.Context) ([]ToolInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listHits++
	if h.panicOn == "ListTools" {
		panic("list exploded")
	}
	if h.listErr != nil {
		return nil, h.listErr
	}
	out := make([]ToolInfo, 0, len(h.tools))
	for name := range h.tools {
		out = append(out, ToolInfo{Name: name, Description: "fake " + name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (h *fakeHost) ToolSchema(_ context.Context, name string) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.panicOn == "ToolSchema" {
		panic("schema exploded")
	}
	s, ok := h.schemas[name]
	if !ok {
		return nil, ErrToolNotVisible
	}
	return s, nil
}

func (h *fakeHost) CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error) {
	h.mu.Lock()
	h.calls = append(h.calls, name)
	f, ok := h.tools[name]
	h.mu.Unlock()
	if !ok {
		return &CallResult{OK: false, Error: &CallError{Code: CodeToolNotVisible, Message: name + " is not available"}, Decision: "not_visible"}, nil
	}
	return f(ctx, args)
}

func (h *fakeHost) TurnResult(_ context.Context, id int64) (*CallResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r, ok := h.turn[id]; ok {
		return r, nil
	}
	return &CallResult{OK: false, Error: &CallError{Code: "NOT_FOUND", Message: "no such result"}}, nil
}

func (h *fakeHost) Progress(ev ProgressEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.panicOn == "Progress" {
		panic("progress exploded")
	}
	h.events = append(h.events, ev)
}

func (h *fakeHost) callNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

var errBoom = errors.New("boom")

// small returns tight limits so budget tests are fast and cheap.
func small() Limits {
	return Limits{
		Wall:               2 * time.Second,
		CPUTicks:           50_000_000,
		MemoryBytes:        32 << 20,
		MaxToolCalls:       10,
		ToolCallTimeout:    time.Second,
		MaxCallResultBytes: 64 << 10,
		MaxOutputBytes:     4 << 10,
		MaxResultBytes:     64 << 10,
		MaxSourceBytes:     64 << 10,
		MaxSleep:           time.Second,
	}
}

// runSrc runs source with the small limits and a fresh fake host.
func runSrc(t *testing.T, src string, h *fakeHost) *RunResult {
	t.Helper()
	if h == nil {
		h = newFakeHost()
	}
	return Run(context.Background(), Program{Source: src}, small(), h)
}

// slack scales time bounds for the race detector, which slows the
// interpreter several times over.
func slack(d time.Duration) time.Duration {
	if raceEnabled {
		return d * 10
	}
	return d * 3
}
