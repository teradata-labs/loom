// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package failurerecovery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/types"
)

var errBoom = errors.New("boom")

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeStore is an in-memory GraphMemoryStore stand-in. Only the methods this
// feature uses (Remember, Recall, VectorRecall, TouchMemories, Supersede) are
// meaningful; the rest satisfy the interface and are unused here.
type fakeStore struct {
	mu   sync.Mutex
	mems []*memory.Memory
	next int

	// embeddingsDisabled simulates a store with no vector index: VectorRecall
	// returns nil,nil regardless of input (the real SQLite/Postgres behavior).
	embeddingsDisabled bool

	rememberErr error
	recallErr   error
	// vectorErr makes VectorRecall return an ERROR (distinct from the
	// embeddingsDisabled nil,nil case) so the FTS fallback on a hard vector
	// failure can be asserted.
	vectorErr error
	// touchErr makes TouchMemories fail; the engine ignores the error on the
	// reuse path (best-effort), which this lets the tests prove.
	touchErr error
	// supersedeErr makes Supersede fail so the store_error path on an evolved
	// solution can be asserted (the old lesson must survive).
	supersedeErr error

	rememberCalls  int
	recallCalls    int
	vectorCalls    int
	touchCalls     int
	supersedeCalls int
}

func (s *fakeStore) Remember(_ context.Context, mem *memory.Memory) (*memory.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rememberCalls++
	if s.rememberErr != nil {
		return nil, s.rememberErr
	}
	s.next++
	cp := *mem
	cp.ID = idOf(s.next)
	s.mems = append(s.mems, &cp)
	return &cp, nil
}

func (s *fakeStore) Recall(_ context.Context, opts memory.RecallOpts) ([]*memory.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recallCalls++
	if s.recallErr != nil {
		return nil, s.recallErr
	}
	var out []*memory.Memory
	for _, m := range s.mems {
		if opts.AgentID != "" && m.AgentID != opts.AgentID {
			continue
		}
		if opts.MemoryType != "" && m.MemoryType != opts.MemoryType {
			continue
		}
		if opts.Owner != "" && m.Owner != opts.Owner {
			continue
		}
		if opts.Owner == "" && opts.SharedOnly && m.Owner != "" {
			continue
		}
		if !hasAllTags(m.Tags, opts.Tags) {
			continue
		}
		if opts.Query != "" && !ftsMatch(m, opts.Query) {
			continue
		}
		out = append(out, m)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out, nil
}

func (s *fakeStore) VectorRecall(_ context.Context, opts memory.VectorRecallOpts) ([]*memory.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vectorCalls++
	if s.vectorErr != nil {
		return nil, s.vectorErr
	}
	if s.embeddingsDisabled {
		return nil, nil // matches real store: no vector index
	}
	type scored struct {
		m   *memory.Memory
		sim float32
	}
	var hits []scored
	for _, m := range s.mems {
		if len(m.Embedding) == 0 || m.EmbeddingModel != opts.Model {
			continue
		}
		if opts.Owner != "" && m.Owner != opts.Owner {
			continue
		}
		if opts.Owner == "" && opts.SharedOnly && m.Owner != "" {
			continue
		}
		sim := memory.CosineSimilarity(opts.Embedding, m.Embedding)
		if sim < opts.Threshold {
			continue
		}
		hits = append(hits, scored{m, sim})
	}
	// simple insertion sort by sim desc (small n)
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].sim > hits[j-1].sim; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	var out []*memory.Memory
	for _, h := range hits {
		out = append(out, h.m)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out, nil
}

func (s *fakeStore) TouchMemories(_ context.Context, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchCalls++
	if s.touchErr != nil {
		return s.touchErr
	}
	for _, id := range ids {
		for _, m := range s.mems {
			if m.ID == id {
				m.AccessCount++
			}
		}
	}
	return nil
}

// Supersede replaces the old lesson with a new one, preserving a single row per
// problem (the real store creates SUPERSEDES lineage; here we swap in place).
func (s *fakeStore) Supersede(_ context.Context, oldID string, newMem *memory.Memory) (*memory.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.supersedeCalls++
	if s.supersedeErr != nil {
		return nil, s.supersedeErr
	}
	s.next++
	cp := *newMem
	cp.ID = idOf(s.next)
	for i, m := range s.mems {
		if m.ID == oldID {
			s.mems[i] = &cp
			return &cp, nil
		}
	}
	s.mems = append(s.mems, &cp)
	return &cp, nil
}

func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.mems)
}

// ftsMatch approximates FTS5: every whitespace token of the query must appear in
// the record's content or summary (case-insensitive).
func ftsMatch(m *memory.Memory, query string) bool {
	hay := strings.ToLower(m.Content + " " + m.Summary)
	for _, tok := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(hay, tok) {
			return false
		}
	}
	return true
}

func hasAllTags(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func idOf(n int) string {
	return "mem-" + string(rune('0'+n%10)) + string(rune('a'+n/10))
}

// fakeEmbedder is a deterministic bag-of-words embedder: identical text yields an
// identical vector, so exact-abstraction recall scores 1.0. It also records every
// text it was asked to embed, for the PII assertion.
type fakeEmbedder struct {
	mu     sync.Mutex
	inputs []string
	// embedErr, when set, makes Embed fail so the engine's embed-error paths
	// (store-without-vector on learn, FTS fallback on recall) can be asserted.
	embedErr error
}

func (e *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	e.inputs = append(e.inputs, text)
	e.mu.Unlock()
	if e.embedErr != nil {
		return nil, e.embedErr
	}
	vec := make([]float32, 64)
	for _, tok := range strings.Fields(strings.ToLower(text)) {
		h := 0
		for _, r := range tok {
			h = (h*31 + int(r)) % 64
		}
		vec[h] += 1
	}
	return vec, nil
}

func (e *fakeEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i], _ = e.Embed(ctx, t)
	}
	return out, nil
}

func (e *fakeEmbedder) Dimensions() int { return 64 }
func (e *fakeEmbedder) Model() string   { return "fake-embed-v1" }

func (e *fakeEmbedder) embedded() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.inputs...)
}

// recordingSink captures emitted events.
type recordingSink struct {
	mu     sync.Mutex
	events []string
}

func (s *recordingSink) AddEvent(name string, _ map[string]interface{}) {
	s.mu.Lock()
	s.events = append(s.events, name)
	s.mu.Unlock()
}

func (s *recordingSink) has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e == name {
			return true
		}
	}
	return false
}

// enabledConfig returns a config with the feature on and everything else default.
func enabledConfig() Config {
	return Config{Enabled: true}
}

const (
	testAgent   = "agent-1"
	testSession = "sess-1"
)

// ---------------------------------------------------------------------------
// Learn (write path)
// ---------------------------------------------------------------------------

func TestLearnOnRecovery_WritesOneAbstractLesson(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())
	sink := &recordingSink{}

	e.OnToolFailure(testSession, ClassToolExec, "run_sql", `syntax error at or near "from"`)
	e.OnToolSuccess(context.Background(), testAgent, testSession, "run_sql", sink)

	if store.count() != 1 {
		t.Fatalf("want 1 lesson, got %d", store.count())
	}
	if !sink.has("failure_recovery.learned") {
		t.Fatalf("expected learned event")
	}
	m := store.mems[0]
	if m.MemoryType != memory.MemoryTypeFailureRecovery {
		t.Errorf("wrong type: %s", m.MemoryType)
	}
	if m.Content == "" || m.Summary == "" {
		t.Errorf("problem/solution must be non-empty: %q / %q", m.Content, m.Summary)
	}
	if !hasTag(m.Tags, "class:"+ClassToolExec) || !hasTag(m.Tags, "tool:run_sql") {
		t.Errorf("missing scoping tags: %v", m.Tags)
	}
	if m.Salience != defaultInitialSalience {
		t.Errorf("salience = %v, want %v", m.Salience, defaultInitialSalience)
	}
}

func TestWriteOnce_NoFixlessRows(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())

	// Failure with no subsequent success: nothing must be written.
	e.OnToolFailure(testSession, ClassToolExec, "run_sql", "permission denied")

	if store.count() != 0 {
		t.Fatalf("failure alone must not persist; got %d rows", store.count())
	}
	if store.rememberCalls != 0 {
		t.Fatalf("Remember must not be called without recovery")
	}
}

func TestDedup_ReusesInsteadOfDuplicating(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())

	learn := func() {
		e.OnToolFailure(testSession, ClassToolExec, "run_sql", `syntax error at or near "from"`)
		e.OnToolSuccess(context.Background(), testAgent, testSession, "run_sql", nil)
	}
	learn()
	learn()

	if store.count() != 1 {
		t.Fatalf("dedup failed: want 1 row, got %d", store.count())
	}
	if store.touchCalls == 0 {
		t.Fatalf("expected TouchMemories on dedup reuse")
	}
}

// ---------------------------------------------------------------------------
// PII boundary
// ---------------------------------------------------------------------------

func TestNoRawValuesPersistedOrEmbedded(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{}
	e := NewEngine(store, emb, enabledConfig())

	// A raw error stuffed with secrets and identifiers.
	raw := `ERROR: syntax error at or near "FROM" in ` +
		`SELECT ssn FROM patients WHERE ssn = '123-45-6789' AND email = 'alice@example.com' ` +
		`host=db.internal.corp password=hunter2`
	secrets := []string{"123-45-6789", "alice@example.com", "patients", "hunter2", "db.internal.corp", "ssn"}

	e.OnToolFailure(testSession, ClassToolExec, "run_sql", raw)
	e.OnToolSuccess(context.Background(), testAgent, testSession, "run_sql", nil)

	if store.count() != 1 {
		t.Fatalf("want 1 lesson, got %d", store.count())
	}
	m := store.mems[0]
	blob := strings.ToLower(m.Content + "\n" + m.Summary + "\n" + m.PropertiesJSON)
	for _, s := range secrets {
		if strings.Contains(blob, strings.ToLower(s)) {
			t.Errorf("stored lesson leaked literal %q in %q", s, blob)
		}
	}
	// The embedder must never have seen a raw literal either.
	for _, in := range emb.embedded() {
		low := strings.ToLower(in)
		for _, s := range secrets {
			if strings.Contains(low, strings.ToLower(s)) {
				t.Errorf("embedder received literal %q in %q", s, in)
			}
		}
	}
}

func TestScrubToStructure_DropsLiterals(t *testing.T) {
	in := `connection refused to host 'db.internal.corp:5432' for user 'admin_9000'`
	got := scrubToStructure(in)
	for _, leak := range []string{"db.internal.corp", "5432", "admin_9000", "admin"} {
		if strings.Contains(got, strings.ToLower(leak)) {
			t.Errorf("scrub leaked %q: %q", leak, got)
		}
	}
	for _, want := range []string{"connection", "refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("scrub dropped structural token %q: %q", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Reactive read
// ---------------------------------------------------------------------------

func seedLesson(t *testing.T, e *Engine, tool, rawErr string) {
	t.Helper()
	e.OnToolFailure(testSession, ClassToolExec, tool, rawErr)
	e.OnToolSuccess(context.Background(), testAgent, testSession, tool, nil)
}

func TestReactiveRecall_ReturnsStoredSolution(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())
	seedLesson(t, e, "run_sql", `syntax error at or near "from"`)

	sink := &recordingSink{}
	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, sink)

	if len(hints) == 0 {
		t.Fatalf("expected a reactive hint")
	}
	if !strings.Contains(strings.ToLower(hints[0]), "quote") {
		t.Errorf("unexpected hint: %q", hints[0])
	}
	if !sink.has("failure_recovery.reactive_hit") {
		t.Errorf("expected reactive_hit event")
	}
}

func TestReactiveRecall_MissEmitsEvent(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())
	sink := &recordingSink{}

	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		"permission denied", sink)

	if len(hints) != 0 {
		t.Fatalf("expected no hints on empty store")
	}
	if !sink.has("failure_recovery.recall_miss") {
		t.Errorf("expected recall_miss event")
	}
}

func TestSemanticMatchViaAbstraction(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())
	// Learn from one phrasing...
	seedLesson(t, e, "run_sql", `relation "users" does not exist`)
	// ...recall with a lexically different phrasing that abstracts identically.
	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		"no such table: users", nil)
	if len(hints) == 0 {
		t.Fatalf("abstraction should let different phrasings recall the same lesson")
	}
}

func TestClassAndToolScoping(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())
	seedLesson(t, e, "run_sql", `syntax error at or near "from"`)

	// Same class of error but a DIFFERENT tool must not surface the SQL lesson.
	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "http_get",
		`syntax error at or near "from"`, nil)
	if len(hints) != 0 {
		t.Fatalf("lesson leaked across tools: %v", hints)
	}
}

func TestHintCap(t *testing.T) {
	store := &fakeStore{}
	cfg := enabledConfig()
	cfg.MaxHints = 1
	e := NewEngine(store, nil, cfg)

	// Two distinct lessons for the same tool that both match a generic query.
	seedLesson(t, e, "run_sql", `syntax error at or near "from"`)
	seedLesson(t, e, "run_sql", `relation "users" does not exist`)

	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		"error", nil) // broad query
	if len(hints) > 1 {
		t.Fatalf("hint cap not enforced: got %d", len(hints))
	}
}

// ---------------------------------------------------------------------------
// FTS5 / vector selection
// ---------------------------------------------------------------------------

func TestFTS5Fallback_NoEmbedder(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig()) // no embedder
	seedLesson(t, e, "run_sql", `syntax error at or near "from"`)

	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "where"`, nil)
	if len(hints) == 0 {
		t.Fatalf("FTS5 path should recall the lesson without an embedder")
	}
	if store.vectorCalls != 0 {
		t.Errorf("VectorRecall must not be called when no embedder is configured")
	}
}

func TestFallback_WhenEmbeddingsDisabled(t *testing.T) {
	store := &fakeStore{embeddingsDisabled: true}
	emb := &fakeEmbedder{}
	e := NewEngine(store, emb, enabledConfig())
	seedLesson(t, e, "run_sql", `syntax error at or near "from"`)

	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "where"`, nil)
	if len(hints) == 0 {
		t.Fatalf("must fall back to FTS5 when VectorRecall returns nil,nil")
	}
	if store.vectorCalls == 0 {
		t.Errorf("expected a VectorRecall attempt before fallback")
	}
	if store.recallCalls == 0 {
		t.Errorf("expected an FTS5 Recall fallback")
	}
}

func TestVectorPath_UsedWhenEmbedderPresent(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{}
	e := NewEngine(store, emb, enabledConfig())
	seedLesson(t, e, "run_sql", `syntax error at or near "from"`)

	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil)
	if len(hints) == 0 {
		t.Fatalf("vector recall should return the lesson (identical abstraction => cosine 1.0)")
	}
	if store.vectorCalls == 0 {
		t.Errorf("expected VectorRecall to be used")
	}
}

func TestThresholdGate_BelowThresholdNotReturned(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{}
	cfg := enabledConfig()
	cfg.SimilarityThreshold = 0.99 // demand near-identical
	e := NewEngine(store, emb, cfg)
	seedLesson(t, e, "run_sql", `relation "users" does not exist`)

	// A query that abstracts to a DIFFERENT problem => low cosine => gated out.
	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		"permission denied", nil)
	if len(hints) != 0 {
		t.Fatalf("below-threshold match must be gated: %v", hints)
	}
}

// ---------------------------------------------------------------------------
// Proactive read
// ---------------------------------------------------------------------------

func TestProactiveRecall_WhenEnabled(t *testing.T) {
	store := &fakeStore{}
	cfg := enabledConfig()
	cfg.Proactive.Enabled = true
	e := NewEngine(store, nil, cfg)
	seedLesson(t, e, "run_sql", `relation "users" does not exist`)

	sink := &recordingSink{}
	// Intent shares words with the stored abstract problem ("table", "relation").
	hints := e.ProactiveHints(context.Background(), testAgent,
		"referenced a table that does not exist", sink)
	if len(hints) == 0 {
		t.Fatalf("expected a proactive pitfall")
	}
	if !sink.has("failure_recovery.proactive_hit") {
		t.Errorf("expected proactive_hit event")
	}
}

func TestProactive_DisabledByDefault(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig()) // proactive not enabled
	seedLesson(t, e, "run_sql", `relation "users" does not exist`)

	before := store.recallCalls
	hints := e.ProactiveHints(context.Background(), testAgent, "query a table", nil)
	if len(hints) != 0 {
		t.Fatalf("proactive must be off by default: %v", hints)
	}
	if store.recallCalls != before {
		t.Errorf("proactive must not query when disabled")
	}
}

// TestProactive_LLMIntent_CallsModel verifies that when Proactive.LLMIntent is
// on and a model is wired, the intent is translated by one LLM call before
// recall (the prediction becomes the query text).
func TestProactive_LLMIntent_CallsModel(t *testing.T) {
	store := &fakeStore{}
	model := &fakeLessonModel{content: "reserved keyword used unquoted; wrap the identifier in double quotes"}
	cfg := enabledConfig()
	cfg.Proactive.Enabled = true
	cfg.Proactive.LLMIntent = true
	e := NewEngine(store, nil, cfg, WithLLMAbstractor(model))
	seedLesson(t, e, "run_sql", `relation "users" does not exist`)

	_ = e.ProactiveHints(context.Background(), testAgent, "show me the order column", nil)
	if model.calls != 1 {
		t.Fatalf("LLMIntent should translate the intent with one model call, got %d", model.calls)
	}
}

// TestProactive_LLMIntent_OffDoesNotCallModel verifies the raw intent is used
// (no LLM call) when the flag is off, even if a model is wired.
func TestProactive_LLMIntent_OffDoesNotCallModel(t *testing.T) {
	store := &fakeStore{}
	model := &fakeLessonModel{content: "unused"}
	cfg := enabledConfig()
	cfg.Proactive.Enabled = true // LLMIntent stays false
	e := NewEngine(store, nil, cfg, WithLLMAbstractor(model))
	seedLesson(t, e, "run_sql", `relation "users" does not exist`)

	_ = e.ProactiveHints(context.Background(), testAgent, "show me the order column", nil)
	if model.calls != 0 {
		t.Fatalf("LLMIntent off must not call the model, got %d", model.calls)
	}
}

// TestProactive_LLMIntent_FallsBackOnError verifies a model error does not abort
// the turn: proactive still recalls with the raw intent.
func TestProactive_LLMIntent_FallsBackOnError(t *testing.T) {
	store := &fakeStore{}
	model := &fakeLessonModel{err: errBoom}
	cfg := enabledConfig()
	cfg.Proactive.Enabled = true
	cfg.Proactive.LLMIntent = true
	e := NewEngine(store, nil, cfg, WithLLMAbstractor(model))
	seedLesson(t, e, "run_sql", `relation "users" does not exist`)

	before := store.recallCalls + store.vectorCalls
	_ = e.ProactiveHints(context.Background(), testAgent, "referenced a table that does not exist", nil)
	if model.calls != 1 {
		t.Fatalf("expected one model attempt before fallback, got %d", model.calls)
	}
	if store.recallCalls+store.vectorCalls == before {
		t.Fatalf("expected a recall with the fallback raw intent")
	}
}

// ---------------------------------------------------------------------------
// Disabled + best-effort
// ---------------------------------------------------------------------------

func TestDisabled_IsNoOp(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, Config{Enabled: false})

	e.OnToolFailure(testSession, ClassToolExec, "run_sql", "syntax error")
	e.OnToolSuccess(context.Background(), testAgent, testSession, "run_sql", nil)
	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql", "syntax error", nil)

	if hints != nil {
		t.Errorf("disabled engine must return no hints")
	}
	if store.rememberCalls != 0 || store.recallCalls != 0 || store.vectorCalls != 0 {
		t.Errorf("disabled engine must not touch the store (r=%d c=%d v=%d)",
			store.rememberCalls, store.recallCalls, store.vectorCalls)
	}
}

func TestNilEngine_IsSafe(t *testing.T) {
	var e *Engine
	if e.Enabled() {
		t.Fatalf("nil engine must report disabled")
	}
	// Must not panic.
	e.OnToolFailure(testSession, ClassToolExec, "t", "err")
	e.OnToolSuccess(context.Background(), testAgent, testSession, "t", nil)
	_ = e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "t", "err", nil)
	_ = e.ProactiveHints(context.Background(), testAgent, "intent", nil)
	e.EndSession(testSession)
}

func TestBestEffort_StoreErrorsDoNotPanic(t *testing.T) {
	store := &fakeStore{rememberErr: errBoom, recallErr: errBoom}
	e := NewEngine(store, nil, enabledConfig())
	sink := &recordingSink{}

	// learn hits Remember error path
	e.OnToolFailure(testSession, ClassToolExec, "run_sql", "syntax error")
	e.OnToolSuccess(context.Background(), testAgent, testSession, "run_sql", sink)
	if store.count() != 0 {
		t.Fatalf("no row should persist on Remember error")
	}
	if !sink.has("failure_recovery.store_error") {
		t.Errorf("expected store_error event")
	}

	// recall hits Recall error path
	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql", "syntax error", nil)
	if hints != nil {
		t.Errorf("recall error must yield no hints, got %v", hints)
	}
}

func TestUncoveredClass_Ignored(t *testing.T) {
	store := &fakeStore{}
	cfg := enabledConfig()
	cfg.Classes = []string{ClassToolExec} // only tool_exec covered
	e := NewEngine(store, nil, cfg)

	e.OnToolFailure(testSession, ClassGuardrail, "run_sql", "syntax error")
	e.OnToolSuccess(context.Background(), testAgent, testSession, "run_sql", nil)
	if store.count() != 0 {
		t.Fatalf("uncovered class must not be staged/learned")
	}
}

func TestEndSession_DropsStagedFailure(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())

	e.OnToolFailure(testSession, ClassToolExec, "run_sql", "syntax error")
	e.EndSession(testSession)
	// A later success in a "reused" session id must not learn the dropped failure.
	e.OnToolSuccess(context.Background(), testAgent, testSession, "run_sql", nil)
	if store.count() != 0 {
		t.Fatalf("staged failure should have been dropped at session end")
	}
}

// ---------------------------------------------------------------------------
// Failure classification (tool_exec / tool_args / guardrail)
// ---------------------------------------------------------------------------

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want string
	}{
		{"missing required arg", "missing required parameter: schema", ClassToolArgs},
		{"invalid type", "invalid type for field limit: expected int got string", ClassToolArgs},
		{"bad json", "cannot unmarshal number into Go struct field", ClassToolArgs},
		{"sql syntax", `syntax error at or near "from"`, ClassGuardrail},
		{"reserved word", "reserved keyword used as identifier", ClassGuardrail},
		{"table missing", "relation users does not exist", ClassGuardrail},
		{"column missing", "no such column: foo", ClassGuardrail},
		{"unique", "duplicate key violates unique constraint", ClassGuardrail},
		{"timeout is exec", "operation timed out after 30s", ClassToolExec},
		{"permission is exec", "permission denied for resource", ClassToolExec},
		{"connection is exec", "connection refused", ClassToolExec},
		{"unknown is exec", "the backend exploded unexpectedly", ClassToolExec},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyError(tt.err); got != tt.want {
				t.Errorf("ClassifyError(%q) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestClassScoping_ArgsAndGuardrail verifies a lesson learned under one class is
// never recalled for a different class, across all three covered classes.
func TestClassScoping_ArgsAndGuardrail(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())
	ctx := context.Background()

	// Learn one lesson per class on the same tool.
	learn := func(class, errMsg string) {
		e.OnToolFailure(testSession, class, "run_sql", errMsg)
		e.OnToolSuccess(ctx, testAgent, testSession, "run_sql", nil)
	}
	learn(ClassToolArgs, "missing required parameter: schema")
	learn(ClassGuardrail, `syntax error at or near "from"`)

	if store.count() != 2 {
		t.Fatalf("want 2 distinct lessons, got %d", store.count())
	}

	// A guardrail-class recall must not surface the tool_args lesson.
	hints := e.ReactiveHints(ctx, testAgent, testSession, ClassGuardrail, "run_sql", `syntax error at or near "select"`, nil)
	for _, h := range hints {
		if strings.Contains(strings.ToLower(h), "argument") {
			t.Errorf("guardrail recall leaked a tool_args lesson: %q", h)
		}
	}
	// And the tool_args recall must return the arg lesson, not the guardrail one.
	argHints := e.ReactiveHints(ctx, testAgent, testSession, ClassToolArgs, "run_sql", "missing required parameter: table", nil)
	if len(argHints) == 0 {
		t.Fatalf("expected a tool_args hint")
	}
}

// ---------------------------------------------------------------------------
// Supersede (evolved solution)
// ---------------------------------------------------------------------------

// TestSupersede_OnEvolvedSolution verifies that re-learning the SAME problem with
// a DIFFERENT solution supersedes the row (no duplicate) and carries reuse_count.
func TestSupersede_OnEvolvedSolution(t *testing.T) {
	store := &fakeStore{}
	// Custom rules would be needed to vary the solution for an identical problem;
	// instead drive learn() directly through the engine with a crafted record so
	// the abstraction yields the same problem but we swap the solution.
	e := NewEngine(store, nil, enabledConfig())
	ctx := context.Background()
	sink := &recordingSink{}

	// First lesson.
	e.learn(ctx, testAgent, failureRecord{
		class: ClassToolExec, tool: "run_sql",
		problem: "some abstract problem", solution: "first fix",
	}, sink)
	if store.count() != 1 {
		t.Fatalf("want 1 row after first learn, got %d", store.count())
	}

	// Same problem, evolved solution → supersede, not duplicate.
	e.learn(ctx, testAgent, failureRecord{
		class: ClassToolExec, tool: "run_sql",
		problem: "some abstract problem", solution: "better fix",
	}, sink)

	if store.count() != 1 {
		t.Fatalf("supersede must keep a single row, got %d", store.count())
	}
	if store.supersedeCalls != 1 {
		t.Fatalf("expected exactly one Supersede call, got %d", store.supersedeCalls)
	}
	if !sink.has("failure_recovery.superseded") {
		t.Errorf("expected superseded event")
	}
	if got := store.mems[0].Summary; got != "better fix" {
		t.Errorf("superseded lesson should carry the new solution, got %q", got)
	}
	if rc := reuseCountOf(store.mems[0]); rc != 1 {
		t.Errorf("reuse_count should increment to 1 on supersede, got %d", rc)
	}
}

// TestSameSolution_TouchesNotSupersedes verifies an identical re-learn refreshes
// access instead of superseding.
func TestSameSolution_TouchesNotSupersedes(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())
	ctx := context.Background()

	rec := failureRecord{
		class: ClassToolExec, tool: "run_sql",
		problem: "some abstract problem", solution: "the fix",
	}
	e.learn(ctx, testAgent, rec, nil)
	e.learn(ctx, testAgent, rec, nil)

	if store.count() != 1 {
		t.Fatalf("want 1 row, got %d", store.count())
	}
	if store.supersedeCalls != 0 {
		t.Errorf("identical re-learn must not supersede")
	}
	if store.touchCalls == 0 {
		t.Errorf("identical re-learn must TouchMemories")
	}
}

// ---------------------------------------------------------------------------
// User-scoped memory (per-user partitioning)
// ---------------------------------------------------------------------------

// userScopedConfig turns the feature and per-user partitioning on.
func userScopedConfig() Config {
	return Config{Enabled: true, UserScoped: true}
}

// seedLessonCtx learns a lesson under an explicit request context (carrying a
// user identity when user-scoping is on).
func seedLessonCtx(t *testing.T, e *Engine, ctx context.Context, tool, rawErr string) {
	t.Helper()
	e.OnToolFailure(testSession, ClassToolExec, tool, rawErr)
	e.OnToolSuccess(ctx, testAgent, testSession, tool, nil)
}

// TestUserScoped_IsolatesLessonsAcrossUsers proves that when UserScoped is on, a
// lesson learned by one user is invisible to another and visible to its owner.
func TestUserScoped_IsolatesLessonsAcrossUsers(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, userScopedConfig())

	ctxA := types.ContextWithUserID(context.Background(), "user-a")
	ctxB := types.ContextWithUserID(context.Background(), "user-b")

	seedLessonCtx(t, e, ctxA, "run_sql", `syntax error at or near "from"`)

	// The lesson must be stamped with user-a as owner.
	if got := store.mems[0].Owner; got != "user-a" {
		t.Fatalf("lesson owner = %q, want user-a", got)
	}

	// User B must NOT recall user A's lesson.
	if hints := e.ReactiveHints(ctxB, testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil); len(hints) != 0 {
		t.Fatalf("user-b leaked user-a's lesson: %v", hints)
	}

	// User A must still recall their own lesson.
	if hints := e.ReactiveHints(ctxA, testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil); len(hints) == 0 {
		t.Fatalf("user-a could not recall their own lesson")
	}
}

// TestUserScoped_Off_SharesLessons is the backward-compat guarantee: with the
// flag off (default) lessons carry no owner and every user shares them.
func TestUserScoped_Off_SharesLessons(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig()) // UserScoped: false

	ctxA := types.ContextWithUserID(context.Background(), "user-a")
	ctxB := types.ContextWithUserID(context.Background(), "user-b")

	seedLessonCtx(t, e, ctxA, "run_sql", `syntax error at or near "from"`)

	// No owner is stamped when the feature is off.
	if got := store.mems[0].Owner; got != "" {
		t.Fatalf("owner should be empty when UserScoped is off, got %q", got)
	}

	// A different user recalls the shared lesson.
	if hints := e.ReactiveHints(ctxB, testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil); len(hints) == 0 {
		t.Fatalf("shared lesson must be visible to another user when UserScoped is off")
	}
}

// TestUserScoped_NoIdentityFallsBackToDefaultUser verifies that a request with
// no resolvable identity still lands in a concrete bucket (DefaultUserID) and is
// recallable by the same identity-less path.
func TestUserScoped_NoIdentityFallsBackToDefaultUser(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, userScopedConfig())

	ctx := context.Background() // no user id

	seedLessonCtx(t, e, ctx, "run_sql", `syntax error at or near "from"`)

	if got := store.mems[0].Owner; got != types.DefaultUserID {
		t.Fatalf("ownerless request should fall back to %q, got %q", types.DefaultUserID, got)
	}

	if hints := e.ReactiveHints(ctx, testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil); len(hints) == 0 {
		t.Fatalf("default-user lesson must be recallable by the identity-less path")
	}

	// A concrete different user must not see the default-user bucket.
	ctxOther := types.ContextWithUserID(context.Background(), "someone-else")
	if hints := e.ReactiveHints(ctxOther, testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil); len(hints) != 0 {
		t.Fatalf("a named user leaked the default-user bucket: %v", hints)
	}
}

// TestUserScoped_DisablingDoesNotLeakOwnedRows is the fail-closed guarantee: a
// lesson written while scoping was ON stays private after scoping is turned OFF.
// Turning the flag off must NOT expose previously-owned rows to other callers —
// privacy travels with the row (owner != ”), not with the current flag state.
func TestUserScoped_DisablingDoesNotLeakOwnedRows(t *testing.T) {
	store := &fakeStore{}

	// Phase 1: scoping ON — user-a learns a private lesson.
	on := NewEngine(store, nil, userScopedConfig())
	ctxA := types.ContextWithUserID(context.Background(), "user-a")
	seedLessonCtx(t, on, ctxA, "run_sql", `syntax error at or near "from"`)
	if got := store.mems[0].Owner; got != "user-a" {
		t.Fatalf("lesson owner = %q, want user-a", got)
	}

	// Phase 2: scoping turned OFF (same underlying store).
	off := NewEngine(store, nil, enabledConfig()) // UserScoped: false

	// A different user must still NOT see user-a's owned row.
	ctxB := types.ContextWithUserID(context.Background(), "user-b")
	if hints := off.ReactiveHints(ctxB, testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil); len(hints) != 0 {
		t.Fatalf("disabling scoping leaked an owned row to another user: %v", hints)
	}

	// Even an identity-less caller (the common off-mode path) must not see it.
	if hints := off.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil); len(hints) != 0 {
		t.Fatalf("disabling scoping leaked an owned row to the shared path: %v", hints)
	}
}

// TestUserScoped_OffStillReadsSharedRows confirms the fail-closed read does not
// over-restrict: unowned (legacy/shared) rows remain visible when scoping is off.
func TestUserScoped_OffStillReadsSharedRows(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig()) // off: writes owner=""

	seedLesson(t, e, "run_sql", `syntax error at or near "from"`)
	if got := store.mems[0].Owner; got != "" {
		t.Fatalf("off-mode lesson should be unowned, got %q", got)
	}

	if hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil); len(hints) == 0 {
		t.Fatalf("shared/unowned rows must remain visible when scoping is off")
	}
}

// ---------------------------------------------------------------------------
// Proactive similarity floor (false-positive guard)
// ---------------------------------------------------------------------------

// TestProactive_Floor_RejectsUnrelatedButPadsAtZero is the negative control for
// the proactive soft floor. With a positive floor an intent unrelated to any
// stored lesson yields NO proactive hints; with the floor at zero the SAME
// unrelated lesson is padded into the top-K (an irrelevant hint). This proves
// the floor suppresses false positives the all-positive-similarity path admits.
func TestProactive_Floor_RejectsUnrelatedButPadsAtZero(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{}

	// Seed one SQL lesson (its problem vector is SQL-flavored).
	seed := NewEngine(store, emb, enabledConfig())
	seedLesson(t, seed, "run_sql", `relation "users" does not exist`)

	// An intent with no token overlap with the stored problem => orthogonal
	// vector => cosine ~0.
	const unrelated = "upload the profile avatar photo to the storage bucket"

	// Floor 0.5: the orthogonal lesson is below the floor and is dropped.
	cfgHigh := enabledConfig()
	cfgHigh.Proactive.Enabled = true
	cfgHigh.Proactive.Threshold = 0.5
	eHigh := NewEngine(store, emb, cfgHigh)
	if hints := eHigh.ProactiveHints(context.Background(), testAgent, unrelated, nil); len(hints) != 0 {
		t.Fatalf("positive floor must reject an unrelated intent, got %v", hints)
	}
	if store.vectorCalls == 0 {
		t.Errorf("expected a VectorRecall attempt on the proactive path")
	}

	// Floor 0: with no floor, the same unrelated lesson pads the top-K — the
	// false positive the soft floor is there to prevent.
	cfgZero := enabledConfig()
	cfgZero.Proactive.Enabled = true
	cfgZero.Proactive.Threshold = 0 // withDefaults would raise this; set via the engine below
	eZero := NewEngine(store, emb, cfgZero)
	eZero.cfg.Proactive.Threshold = 0 // override the normalized default to demonstrate floor-off
	if hints := eZero.ProactiveHints(context.Background(), testAgent, unrelated, nil); len(hints) == 0 {
		t.Fatalf("with the floor off, the unrelated lesson should pad the top-K (demonstrating why the floor matters)")
	}
}

// ---------------------------------------------------------------------------
// Dedup: semantic near-duplicate (characterizes the exact-match-only boundary)
// ---------------------------------------------------------------------------

// TestDedup_SemanticNearDuplicate_NotMerged characterizes a KNOWN limitation:
// dedup keys on an EXACT (case-insensitive) problem-text match, so two lessons
// that mean the same thing but are phrased differently are stored as SEPARATE
// rows — near-duplicates are not merged. This locks in current behavior; if
// semantic dedup is added later, flip the expectation to 1 row + a reuse/
// supersede signal.
func TestDedup_SemanticNearDuplicate_NotMerged(t *testing.T) {
	store := &fakeStore{}
	// An LLM abstractor lets us script two distinct problem phrasings for the
	// same underlying defect. The raw errors share no tokens with the drafted
	// problems, so the leak gate keeps the model output verbatim.
	model := &fakeLessonModel{content: `{"problem":"unquoted identifier caused a syntax error","solution":"escape the identifier"}`}
	e := NewEngine(store, nil, llmConfig(), WithLLMAbstractor(model))
	ctx := context.Background()

	e.OnToolFailure(testSession, ClassGuardrail, "run_sql", "boom one")
	e.OnToolSuccess(ctx, testAgent, testSession, "run_sql", nil)

	// Same defect, different wording.
	model.content = `{"problem":"identifier was not escaped and broke parsing","solution":"escape the identifier"}`
	e.OnToolFailure(testSession, ClassGuardrail, "run_sql", "boom two")
	e.OnToolSuccess(ctx, testAgent, testSession, "run_sql", nil)

	if store.count() != 2 {
		t.Fatalf("exact-match dedup leaves near-duplicates separate: want 2 rows, got %d", store.count())
	}
	if store.supersedeCalls != 0 {
		t.Errorf("different problem text must not supersede, got %d supersede calls", store.supersedeCalls)
	}
}

// ---------------------------------------------------------------------------
// Agent isolation (lessons never cross agent IDs)
// ---------------------------------------------------------------------------

// TestAgentIsolation_LessonsDoNotCrossAgents proves a lesson learned by one
// agent is never recalled by a different agent, even for an identical failure.
func TestAgentIsolation_LessonsDoNotCrossAgents(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())
	ctx := context.Background()

	// agent-A learns.
	e.OnToolFailure(testSession, ClassToolExec, "run_sql", `syntax error at or near "from"`)
	e.OnToolSuccess(ctx, "agent-A", testSession, "run_sql", nil)

	// agent-B must NOT recall agent-A's lesson for the same failure.
	if hints := e.ReactiveHints(ctx, "agent-B", testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil); len(hints) != 0 {
		t.Fatalf("lesson leaked across agents: %v", hints)
	}

	// agent-A still recalls its own lesson.
	if hints := e.ReactiveHints(ctx, "agent-A", testSession, ClassToolExec, "run_sql",
		`syntax error at or near "select"`, nil); len(hints) == 0 {
		t.Fatalf("agent-A could not recall its own lesson")
	}
}

// ---------------------------------------------------------------------------
// Best-effort error paths (store/embedder failures must never abort a turn)
// ---------------------------------------------------------------------------

// TestBestEffort_SupersedeErrorKeepsOldLesson proves that when the SUPERSEDE
// write fails on an evolved solution, the engine emits a store_error and the
// ORIGINAL lesson is left intact — a failed upgrade never destroys the prior,
// still-useful fix, and the turn is not aborted.
func TestBestEffort_SupersedeErrorKeepsOldLesson(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())
	ctx := context.Background()

	// Seed the first lesson.
	e.learn(ctx, testAgent, failureRecord{
		class: ClassToolExec, tool: "run_sql",
		problem: "some abstract problem", solution: "first fix",
	}, nil)
	if store.count() != 1 {
		t.Fatalf("want 1 row after first learn, got %d", store.count())
	}

	// Now make Supersede fail, then re-learn the SAME problem with a new fix.
	store.supersedeErr = errBoom
	sink := &recordingSink{}
	e.learn(ctx, testAgent, failureRecord{
		class: ClassToolExec, tool: "run_sql",
		problem: "some abstract problem", solution: "better fix",
	}, sink)

	if store.supersedeCalls != 1 {
		t.Fatalf("expected one Supersede attempt, got %d", store.supersedeCalls)
	}
	if !sink.has("failure_recovery.store_error") {
		t.Errorf("expected a store_error event on Supersede failure")
	}
	if sink.has("failure_recovery.superseded") {
		t.Errorf("superseded event must not fire when the write failed")
	}
	// The original lesson must survive unchanged — a failed upgrade is not a loss.
	if store.count() != 1 {
		t.Fatalf("failed supersede must leave exactly the original row, got %d", store.count())
	}
	if got := store.mems[0].Summary; got != "first fix" {
		t.Errorf("original lesson must be intact after a failed supersede, got %q", got)
	}
}

// TestBestEffort_TouchErrorOnReuseIsSwallowed proves that a TouchMemories failure
// on the reuse path (identical re-learn) does not panic, still reports the reuse,
// and never falls through to an erroneous insert or supersede.
func TestBestEffort_TouchErrorOnReuseIsSwallowed(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig())
	ctx := context.Background()

	rec := failureRecord{
		class: ClassToolExec, tool: "run_sql",
		problem: "some abstract problem", solution: "the fix",
	}
	e.learn(ctx, testAgent, rec, nil) // first write
	if store.count() != 1 {
		t.Fatalf("want 1 row after first learn, got %d", store.count())
	}

	store.touchErr = errBoom
	sink := &recordingSink{}
	e.learn(ctx, testAgent, rec, sink) // identical re-learn => touch path

	if store.touchCalls == 0 {
		t.Fatalf("identical re-learn must attempt a Touch")
	}
	if !sink.has("failure_recovery.reused") {
		t.Errorf("reuse must still be reported even when Touch fails")
	}
	if store.count() != 1 {
		t.Errorf("a touch error must not create a duplicate row, got %d", store.count())
	}
	if store.supersedeCalls != 0 {
		t.Errorf("a touch error must not escalate to a supersede")
	}
}

// TestBestEffort_VectorRecallError_FallsBackToFTS proves a HARD VectorRecall
// error (not the embeddings-disabled nil,nil case) degrades gracefully to the
// FTS5 path instead of returning nothing.
func TestBestEffort_VectorRecallError_FallsBackToFTS(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{}
	e := NewEngine(store, emb, enabledConfig())
	seedLesson(t, e, "run_sql", `syntax error at or near "from"`)

	// Fail the vector path for the recall; FTS must still find the lesson.
	store.vectorErr = errBoom
	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "where"`, nil)

	if len(hints) == 0 {
		t.Fatalf("a VectorRecall error must fall back to FTS, got no hints")
	}
	if store.vectorCalls == 0 {
		t.Errorf("expected a VectorRecall attempt before the FTS fallback")
	}
	if store.recallCalls == 0 {
		t.Errorf("expected an FTS Recall fallback after the vector error")
	}
}

// TestBestEffort_EmbedError_OnLearn_StillStoresLesson proves that when the
// embedder fails at write time, the lesson is still persisted (without a vector),
// so it remains recallable via FTS — an embedding outage degrades retrieval, it
// does not drop the lesson.
func TestBestEffort_EmbedError_OnLearn_StillStoresLesson(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{embedErr: errBoom}
	e := NewEngine(store, emb, enabledConfig())

	seedLesson(t, e, "run_sql", `syntax error at or near "from"`)

	if store.count() != 1 {
		t.Fatalf("lesson must persist even when embedding fails, got %d rows", store.count())
	}
	if len(store.mems[0].Embedding) != 0 {
		t.Errorf("no vector should be stored when the embedder failed")
	}

	// The stored lesson is still recallable via the FTS path (embedder still
	// errors on recall, so the engine falls through to FTS).
	if hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "where"`, nil); len(hints) == 0 {
		t.Fatalf("a lesson stored without a vector must still be recallable via FTS")
	}
}

// TestBestEffort_EmbedError_OnRecall_FallsBackToFTS proves that an embedder
// failure at read time falls back to FTS rather than returning nothing, when the
// lesson itself was stored with a usable vector.
func TestBestEffort_EmbedError_OnRecall_FallsBackToFTS(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{}
	e := NewEngine(store, emb, enabledConfig())
	seedLesson(t, e, "run_sql", `syntax error at or near "from"`) // learns WITH a vector

	// Now the embedder starts failing: recall cannot build a query vector and
	// must fall back to FTS.
	emb.embedErr = errBoom
	hints := e.ReactiveHints(context.Background(), testAgent, testSession, ClassToolExec, "run_sql",
		`syntax error at or near "where"`, nil)

	if len(hints) == 0 {
		t.Fatalf("an embed error at recall must fall back to FTS, got no hints")
	}
	if store.recallCalls == 0 {
		t.Errorf("expected an FTS Recall fallback after the embed error")
	}
}
