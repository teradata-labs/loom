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
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teradata-labs/loom/internal/sqlitedriver"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

// evalRunRow is one eval_runs row with the columns that must match between
// mode: embedded and OTLP export with otlp_record_eval_runs.
type evalRunRow struct {
	Query, Model, Response, ErrorMessage, SessionID string
	TokenCount                                      int64
	Success                                         bool
	Config                                          map[string]interface{}
}

func newSQLiteRecorder(t *testing.T) (*EvalRunRecorder, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "observability.db")
	r, err := NewEvalRunRecorder(&EmbeddedConfig{StorageType: "sqlite", SQLitePath: path}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewEvalRunRecorder: %v", err)
	}
	return r, path
}

// newRecordingOTelTracer returns an OTelTracer with an in-memory exporter and
// a SQLite recorder, as looms serve builds it with otlp_record_eval_runs.
func newRecordingOTelTracer(t *testing.T, privacy PrivacyConfig, filter SpanFilterConfig) (*OTelTracer, *tracetest.InMemoryExporter, string) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(newFilteringSpanProcessor(sdktrace.NewSimpleSpanProcessor(exp), filter)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	rec, path := newSQLiteRecorder(t)
	return &OTelTracer{provider: provider, tracer: provider.Tracer("loom"), privacy: privacy, recorder: rec}, exp, path
}

func readEvalRuns(t *testing.T, path string) map[string]evalRunRow {
	t.Helper()
	db, err := sql.Open("sqlite3", sqlitedriver.DSN(path, sqlitedriver.Options{}))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`SELECT query, model, response, error_message, session_id,
		token_count, success, configuration_json FROM eval_runs ORDER BY rowid`)
	if err != nil {
		t.Fatalf("query eval_runs: %v", err)
	}
	defer func() { _ = rows.Close() }()

	byName := map[string]evalRunRow{}
	for rows.Next() {
		var (
			r                                         evalRunRow
			query, model, response, errMsg, sessionID sql.NullString
			configJSON                                string
		)
		if err := rows.Scan(&query, &model, &response, &errMsg, &sessionID, &r.TokenCount, &r.Success, &configJSON); err != nil {
			t.Fatalf("scan: %v", err)
		}
		r.Query, r.Model, r.Response, r.ErrorMessage, r.SessionID = query.String, model.String, response.String, errMsg.String, sessionID.String
		if err := json.Unmarshal([]byte(configJSON), &r.Config); err != nil {
			t.Fatalf("configuration_json: %v", err)
		}
		name, _ := r.Config["test.span"].(string)
		byName[name] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return byName
}

// paritySpans emits the span sequence the analysis scripts read: an agent
// turn, an LLM call and a memory recall, with the attributes they query.
func paritySpans(tr Tracer) {
	ctx, agent := tr.StartSpan(context.Background(), "agent.chat")
	agent.SetAttribute("test.span", "agent.chat")
	agent.SetAttribute(AttrSessionID, "smoke-1")
	agent.SetAttribute("agent.id", "b508685f-f62a-424e-8a32-0d23e408cf70")
	agent.SetAttribute("message.preview", "Read the note; call 555-123-4567")
	agent.SetAttribute("query.length", 42)

	llmCtx, llm := tr.StartSpan(ctx, SpanLLMCompletion)
	llm.SetAttribute("test.span", SpanLLMCompletion)
	llm.SetAttribute(AttrSessionID, "smoke-1")
	llm.SetAttribute(AttrLLMModel, "claude-sonnet-4-5")
	llm.SetAttribute("llm.operation", "chat")
	llm.SetAttribute("llm.tokens.input", 5)
	llm.SetAttribute("llm.tokens.output", 13)
	llm.SetAttribute("llm.tokens.total", 18)
	llm.SetAttribute("llm.tokens.cache_read", 5565)
	llm.SetAttribute("llm.cost.usd", 0.0025395)
	llm.SetAttribute("llm.messages.count", 3)
	llm.SetAttribute("llm.content.length", 33)
	tr.EndSpan(llm)
	_ = llmCtx

	_, recall := tr.StartSpan(ctx, "graph_memory.recall")
	recall.SetAttribute("test.span", "graph_memory.recall")
	recall.SetAttribute(AttrSessionID, "smoke-1")
	recall.SetAttribute("recall.outcome", "injected")
	recall.SetAttribute("recall.injected", 2)
	recall.SetAttribute("recall.candidates", 7)
	recall.Status = Status{Code: StatusError, Message: "partial"}
	tr.EndSpan(recall)

	tr.EndSpan(agent)
}

// TestEvalRunRecorder_RowParityWithEmbedded is the D3 golden test: OTLP
// export with a recorder writes the same eval_runs rows as mode: embedded,
// differing only in id, eval_id and timing columns.
func TestEvalRunRecorder_RowParityWithEmbedded(t *testing.T) {
	embeddedPath := filepath.Join(t.TempDir(), "observability.db")
	embedded, err := NewEmbeddedTracer(&EmbeddedConfig{StorageType: "sqlite", SQLitePath: embeddedPath, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("NewEmbeddedTracer: %v", err)
	}
	paritySpans(embedded)
	if err := embedded.Close(); err != nil {
		t.Fatalf("embedded.Close: %v", err)
	}

	otelTr, _, otelPath := newRecordingOTelTracer(t,
		PrivacyConfig{RedactCredentials: true, RedactPII: true}, SpanFilterConfig{})
	paritySpans(otelTr)
	if err := otelTr.Shutdown(context.Background()); err != nil {
		t.Fatalf("otel.Shutdown: %v", err)
	}

	want := readEvalRuns(t, embeddedPath)
	got := readEvalRuns(t, otelPath)
	if len(want) != 3 || len(got) != 3 {
		t.Fatalf("rows: embedded %d, otel %d, want 3 each", len(want), len(got))
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s: no row from the OTLP recorder", name)
			continue
		}
		if !reflect.DeepEqual(g, w) {
			t.Errorf("%s row differs:\n got  %+v\n want %+v", name, g, w)
		}
	}
	// The analysis reads these Loom-native keys; they must be present as is.
	for _, k := range []string{"recall.outcome", "recall.injected", "recall.candidates"} {
		if _, ok := got["graph_memory.recall"].Config[k]; !ok {
			t.Errorf("recall row lacks %s", k)
		}
	}
	for _, k := range []string{"llm.tokens.input", "llm.tokens.cache_read", "llm.cost.usd", "llm.operation", "llm.messages.count", "llm.content.length"} {
		if _, ok := got[SpanLLMCompletion].Config[k]; !ok {
			t.Errorf("llm row lacks %s", k)
		}
	}
}

func TestEvalRunRecorder_RecordsSpansTheExportFilterDrops(t *testing.T) {
	tr, exp, path := newRecordingOTelTracer(t, PrivacyConfig{}, SpanFilterConfig{IncludePrefixes: []string{"llm."}})
	_, dropped := tr.StartSpan(context.Background(), "session_store.load_session")
	dropped.SetAttribute("test.span", "session_store.load_session")
	tr.EndSpan(dropped)
	_, kept := tr.StartSpan(context.Background(), SpanLLMCompletion)
	kept.SetAttribute("test.span", SpanLLMCompletion)
	tr.EndSpan(kept)

	// InMemoryExporter clears its spans on Shutdown, so read them first.
	exported := exp.GetSpans()
	if err := tr.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if len(exported) != 1 || exported[0].Name != SpanLLMCompletion {
		t.Fatalf("exported %d spans, want only %s", len(exported), SpanLLMCompletion)
	}
	rows := readEvalRuns(t, path)
	for _, name := range []string{"session_store.load_session", SpanLLMCompletion} {
		if _, ok := rows[name]; !ok {
			t.Errorf("%s not recorded; otlp_include_spans must only affect the export", name)
		}
	}
}

func TestEvalRunRecorder_RowIsUnredactedWhileExportIsRedacted(t *testing.T) {
	tr, exp, path := newRecordingOTelTracer(t, PrivacyConfig{RedactCredentials: true, RedactPII: true}, SpanFilterConfig{})
	_, s := tr.StartSpan(context.Background(), "agent.chat")
	s.SetAttribute("test.span", "agent.chat")
	s.SetAttribute("message.preview", "call 555-123-4567")
	tr.EndSpan(s)
	// InMemoryExporter clears its spans on Shutdown, so read them first.
	spans := exp.GetSpans()
	if err := tr.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}

	var exported string
	for _, kv := range spans[0].Attributes {
		if kv.Key == attribute.Key("input.value") {
			exported = kv.Value.AsString()
		}
	}
	if strings.Contains(exported, "555-123-4567") {
		t.Errorf("OTLP export not redacted: %q", exported)
	}
	if got := readEvalRuns(t, path)["agent.chat"].Config["message.preview"]; got != "call 555-123-4567" {
		t.Errorf("recorded preview = %v, want the unredacted text (parity with mode: embedded)", got)
	}
}

func TestEvalRunRecorder_FlushAndShutdownFinalizeEval(t *testing.T) {
	tr, _, path := newRecordingOTelTracer(t, PrivacyConfig{}, SpanFilterConfig{})
	for i := 0; i < 3; i++ {
		_, s := tr.StartSpan(context.Background(), SpanLLMCompletion)
		s.SetAttribute("llm.tokens.total", 10)
		tr.EndSpan(s)
	}
	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	evalID := tr.recorder.CurrentEvalID()
	if err := tr.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	db, err := sql.Open("sqlite3", sqlitedriver.DSN(path, sqlitedriver.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var totalRuns, totalTokens int64
	if err := db.QueryRow(`SELECT total_runs, total_tokens FROM eval_metrics WHERE eval_id = ?`, evalID).Scan(&totalRuns, &totalTokens); err != nil {
		t.Fatalf("eval_metrics: %v", err)
	}
	if totalRuns != 3 || totalTokens != 30 {
		t.Errorf("eval_metrics total_runs=%d total_tokens=%d, want 3 and 30", totalRuns, totalTokens)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM evals WHERE id = ?`, evalID).Scan(&status); err != nil {
		t.Fatalf("evals: %v", err)
	}
	if status != "completed" {
		t.Errorf("eval status = %q after Shutdown, want completed", status)
	}
}

func TestEvalRunRecorder_ConcurrentSpans(t *testing.T) {
	tr, _, path := newRecordingOTelTracer(t, PrivacyConfig{RedactPII: true}, SpanFilterConfig{})
	const workers, perWorker = 8, 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				ctx, parent := tr.StartSpan(context.Background(), "agent.chat")
				_, child := tr.StartSpan(ctx, SpanLLMCompletion)
				child.SetAttribute("llm.tokens.input", i)
				tr.EndSpan(child)
				tr.EndSpan(parent)
			}
		}()
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := tr.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	db, err := sql.Open("sqlite3", sqlitedriver.DSN(path, sqlitedriver.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM eval_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if want := workers * perWorker * 2; n != want {
		t.Errorf("eval_runs rows = %d, want %d", n, want)
	}
}

func TestEvalRunRecorder_NilIsNoOp(t *testing.T) {
	var r *EvalRunRecorder
	r.Record(&Span{SpanID: "x"}) // must not panic
	tr, _ := newInMemoryOTelTracer(t)
	if err := tr.Flush(context.Background()); err != nil {
		t.Errorf("Flush without recorder: %v", err)
	}
}
