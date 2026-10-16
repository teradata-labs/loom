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
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/observability/storage"
	"go.uber.org/zap"
)

// gatedStorage wraps a Storage, records the order of the calls that matter for
// shutdown, and can hold the first CreateEvalRun, CreateEval or
// CalculateEvalMetrics call until released. Later calls are never held, so a
// call that does not wait for the held one runs through.
type gatedStorage struct {
	storage.Storage

	mu     sync.Mutex
	events []string

	holdRun, holdEval, holdCalc bool
	entered                     chan string
	release                     chan struct{}
	releaseAll                  func()
	heldRun, heldEval, heldCalc atomic.Bool
}

func newGatedStorage(inner storage.Storage) *gatedStorage {
	g := &gatedStorage{Storage: inner, entered: make(chan string, 4), release: make(chan struct{})}
	g.releaseAll = sync.OnceFunc(func() { close(g.release) })
	return g
}

func (g *gatedStorage) record(event string) {
	g.mu.Lock()
	g.events = append(g.events, event)
	g.mu.Unlock()
}

func (g *gatedStorage) eventLog() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.events...)
}

func (g *gatedStorage) hold(enabled bool, held *atomic.Bool, name string) {
	if !enabled || !held.CompareAndSwap(false, true) {
		return
	}
	g.entered <- name
	<-g.release
}

func (g *gatedStorage) CreateEval(ctx context.Context, eval *storage.Eval) error {
	g.hold(g.holdEval, &g.heldEval, "create_eval")
	g.record("create_eval")
	return g.Storage.CreateEval(ctx, eval)
}

func (g *gatedStorage) CreateEvalRun(ctx context.Context, run *storage.EvalRun) error {
	g.hold(g.holdRun, &g.heldRun, "create_eval_run")
	err := g.Storage.CreateEvalRun(ctx, run)
	g.record("create_eval_run")
	return err
}

func (g *gatedStorage) CalculateEvalMetrics(ctx context.Context, evalID string) (*storage.EvalMetrics, error) {
	g.hold(g.holdCalc, &g.heldCalc, "calculate_metrics")
	m, err := g.Storage.CalculateEvalMetrics(ctx, evalID)
	g.record("calculate_metrics")
	return m, err
}

func (g *gatedStorage) Close() error {
	g.record("close")
	return g.Storage.Close()
}

func newGatedRecorder(t *testing.T, flush time.Duration) (*EvalRunRecorder, *gatedStorage) {
	t.Helper()
	r, err := NewEvalRunRecorder(&EmbeddedConfig{StorageType: "memory", FlushInterval: flush}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewEvalRunRecorder: %v", err)
	}
	g := newGatedStorage(r.storage)
	r.storage = g
	// A failing test stops before it releases a held call; release it first
	// so Close does not wait on it forever.
	t.Cleanup(func() {
		g.releaseAll()
		_ = r.Close()
	})
	return r, g
}

func endedSpan(name string) *Span {
	return &Span{SpanID: name, Name: name, StartTime: time.Now(), Attributes: map[string]interface{}{}}
}

// indexOf returns the position of the last occurrence of event, or -1.
func indexOf(events []string, event string) int {
	last := -1
	for i, e := range events {
		if e == event {
			last = i
		}
	}
	return last
}

func waitClosed(t *testing.T, done <-chan struct{}, want bool, msg string) {
	t.Helper()
	select {
	case <-done:
		if !want {
			t.Fatal(msg)
		}
	case <-time.After(200 * time.Millisecond):
		if want {
			t.Fatal(msg)
		}
	}
}

func TestEvalRunRecorder_CloseWaitsForInFlightRecord(t *testing.T) {
	r, g := newGatedRecorder(t, 0)
	r.Record(endedSpan("first")) // creates the evals row
	g.holdRun = true

	recorded := make(chan struct{})
	go func() {
		r.Record(endedSpan("in-flight"))
		close(recorded)
	}()
	<-g.entered

	closed := make(chan struct{})
	go func() {
		_ = r.Close()
		close(closed)
	}()
	waitClosed(t, closed, false, "Close returned while a Record write was in flight")

	g.releaseAll()
	<-recorded
	<-closed

	ev := g.eventLog()
	if run, calc, cl := indexOf(ev, "create_eval_run"), indexOf(ev, "calculate_metrics"), indexOf(ev, "close"); !(run < calc && calc < cl) {
		t.Fatalf("want the in-flight row, then the final metrics, then storage close; got %v", ev)
	}
}

func TestEvalRunRecorder_CloseWaitsForPeriodicFlush(t *testing.T) {
	r, g := newGatedRecorder(t, 5*time.Millisecond)
	g.holdCalc = true
	r.Record(endedSpan("first"))
	<-g.entered // the flush loop is inside CalculateEvalMetrics

	closed := make(chan struct{})
	go func() {
		_ = r.Close()
		close(closed)
	}()
	waitClosed(t, closed, false, "Close returned while a periodic flush was in flight")

	g.releaseAll()
	<-closed

	ev := g.eventLog()
	if indexOf(ev, "close") != len(ev)-1 {
		t.Fatalf("storage must close after every metrics call; got %v", ev)
	}
}

func TestEvalRunRecorder_EvalCreationDoesNotBlockEvalIDAccess(t *testing.T) {
	r, g := newGatedRecorder(t, 0)
	g.holdEval = true

	go r.Record(endedSpan("first"))
	<-g.entered // the evals row write is in progress

	done := make(chan struct{})
	go func() {
		_ = r.CurrentEvalID()
		r.SetEvalID("eval-from-caller")
		close(done)
	}()
	waitClosed(t, done, true, "CurrentEvalID/SetEvalID waited on the evals row write")
	g.releaseAll()
}

func TestEvalRunRecorder_NoCallsAfterClose(t *testing.T) {
	r, g := newGatedRecorder(t, 0)
	r.Record(endedSpan("first"))
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before := len(g.eventLog())

	r.Record(endedSpan("late"))
	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("Flush after Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if after := g.eventLog(); len(after) != before {
		t.Fatalf("storage used after Close: %v", after[before:])
	}
}

func TestSpanToEvalRun_TokenCount(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]interface{}
		want  int32
	}{
		{name: "int", attrs: map[string]interface{}{"llm.tokens.total": 120}, want: 120},
		{name: "int32", attrs: map[string]interface{}{"llm.tokens.total": int32(121)}, want: 121},
		{name: "int64", attrs: map[string]interface{}{"llm.tokens.total": int64(122)}, want: 122},
		{name: "float64 from JSON", attrs: map[string]interface{}{"llm.tokens.total": float64(123)}, want: 123},
		{name: "token_count fallback int64", attrs: map[string]interface{}{"token_count": int64(124)}, want: 124},
		{name: "total wins over token_count", attrs: map[string]interface{}{"llm.tokens.total": 125, "token_count": 1}, want: 125},
		{name: "clamped", attrs: map[string]interface{}{"llm.tokens.total": int64(math.MaxInt64)}, want: math.MaxInt32},
		{name: "absent", attrs: map[string]interface{}{}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := spanToEvalRun(&Span{SpanID: "s", Attributes: tt.attrs}, "eval")
			if run.TokenCount != tt.want {
				t.Fatalf("TokenCount = %d, want %d", run.TokenCount, tt.want)
			}
		})
	}
}
