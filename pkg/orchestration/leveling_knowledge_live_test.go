// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package orchestration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/embedding"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/types"
)

// This file is the KNOWLEDGE-BOUND live experiment for capability leveling — the
// third and last live harness, after leveling_live_ollama_test.go (format-bound)
// and leveling_reasoning_live_test.go (reasoning-bound). It shares their gating
// conventions exactly: opt-in via LOOM_LIVE_OLLAMA=1, a reachability probe over
// /api/tags, assertLocalOnlyModel on every model name before a socket opens, and
// SKIPPED in -short so `go test -tags fts5 -race -short ./...` never touches a
// model.
//
// The claim under test is the plan's last untested original claim:
//
//	retrieval closes the knowledge gap FULLY — a weak model with the right fact
//	injected matches or beats a strong model without it, because a fictional fact
//	is unknowable to any weights at any scale.
//
// The two earlier experiments both found the same shape of result: nothing that
// tried to extract more from the weak model's weights worked (same-model retry
// repaired 0 reasoning errors, self-critique 0/18, scaffolding actively harmful).
// This one tests the opposite intervention — change the PROMPT's information
// content rather than the model or the procedure — on a task where the weights
// cannot possibly help.
//
// Five arms over the same 30 questions:
//
//	1 llama2, no context   — the floor. Measures hallucination vs abstention.
//	2 llama2 + oracle      — the RAG ceiling: the gold sentence, zero retrieval risk.
//	3 llama2 + BM25 top-5  — realistic: Loom's FTS5 recall over 200 ingested records.
//	4 llama2 + vector top-5 — the orphaned VectorRecall path, exercised for the first time.
//	5 deepseek-r1, no context — proves knowledge ≠ reasoning. Expect ≈ arm 1.
//
// Run the full experiment with:
//
//	LOOM_LIVE_OLLAMA=1 LOOM_LEVELING_RESULTS_DIR=/tmp/kg-results \
//	  go test -tags fts5 -run TestLiveOllamaKnowledgeLeveling ./pkg/orchestration/ -v -timeout 120m
//
// Smoke-test the harness itself with LOOM_LEVELING_N=3.
//
// The build tag is not decoration: the corpus is ingested into a real
// GraphMemoryStore, whose migration creates an FTS5 virtual table. Without
// -tags fts5 the driver has no fts5 module and MigrateUp fails, so the file is
// excluded from the build rather than failing confusingly at runtime.
//
// ── What this harness deliberately does NOT use ──
//
// LevelingExecutor. Every arm here is a single model with a single call, and the
// variable is the prompt's information content. Routing the calls through the
// executor would add schema validation, coercion and retry to the measurement
// path — three confounds for a question that is purely about accuracy.
//
// The agent's injectGraphMemoryContext. That path gates retrieval behind two
// extra LLM calls (a relevance decision and a query rewrite), so a miss there
// could be either retrieval failing or a gate declining. Retrieval is called
// directly and the retrieved sentences are prepended here, which is what makes
// retrieval_hit attributable.

const (
	// Ladder models. All local; every name passes through assertLocalOnlyModel
	// before any socket opens, INCLUDING the embedder's — a cloud-routed model
	// reached through Ollama can bill the operator.
	kgWeakModel   = "llama2:latest"      // arms 1–4
	kgStrongModel = "deepseek-r1:latest" // arm 5
	// kgEmbedModel embeds the corpus and the questions for arm 4. It is reached
	// through Ollama's OpenAI-compatible /v1/embeddings endpoint, so Loom's
	// existing OpenAIEmbedder is reused unchanged rather than a new provider
	// being written.
	kgEmbedModel = "llama3.2:latest"

	// Sampling matches the reasoning experiment so results are comparable across
	// harnesses: seed pinned, temperature low. Embeddings need no seed — they are
	// a deterministic forward pass, not sampled.
	kgTemperature = 0.1
	kgSeed        = int64(0)
	kgCallTimeout = 300 * time.Second

	// Output budgets are per model, for the reason documented at length in the
	// reasoning harness: a reasoning model spends its budget thinking before it
	// writes any content, and too small a cap turns it into a no-output rung.
	kgWeakMaxTokens   = 1024
	kgStrongMaxTokens = 3072

	// kgTopK is how many records the retrieval arms fetch per question.
	kgTopK = 5

	// kgEmbedBatchSize chunks the corpus embedding pass. 200 records at 32 per
	// request is ~7 requests and ~15s total on local llama3.2.
	kgEmbedBatchSize = 32
	kgEmbedTimeout   = 180 * time.Second

	// kgAgentID scopes every ingested memory. Both Recall and VectorRecall filter
	// on agent_id, so this is what keeps the corpus isolated.
	kgAgentID = "knowledge-leveling-harness"

	// kgNEnv overrides the question count for smoke runs. Same variable name as
	// the reasoning harness on purpose — one knob for every live harness.
	kgNEnv = "LOOM_LEVELING_N"
	// kgResultsDirEnv names the directory per-trial JSONL is written to. Unset
	// means no files are written; t.Log still carries every row.
	kgResultsDirEnv = "LOOM_LEVELING_RESULTS_DIR"
)

// kgContextPrompt is used by the context arms (2, 3, 4). The "using only the
// provided information" clause plus the -1 escape is what lets a retrieval MISS
// show up as an abstention rather than as a guess, which is the whole reason the
// retrieval arms can be split into retrieval failure and utilization failure.
//
// No role framing, per project rules.
const kgContextPrompt = `Answer using only the provided information. If the information needed is not present, reply with {"answer": -1}.

%s

Question: %s

Reply with a single JSON object and nothing else, in this form:
{"answer": <integer>}`

// kgNoContextPrompt is used by the no-context arms (1, 5): the "provided
// information" clause and the context block are dropped entirely, and the -1
// escape becomes an explicit "if you do not know". Keeping the escape in both
// shapes is what separates abstention from hallucination on the floor arms.
const kgNoContextPrompt = `If you do not know, reply with {"answer": -1}.

Question: %s

Reply with a single JSON object and nothing else, in this form:
{"answer": <integer>}`

// kgQuestionCountForRun is the number of questions every arm runs: kgQuestionCount
// unless kgNEnv overrides it with a positive integer no larger than the corpus's
// question set.
func kgQuestionCountForRun() int {
	if v := os.Getenv(kgNEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= kgQuestionCount {
			return n
		}
	}
	return kgQuestionCount
}

// ─────────────────────────── models ───────────────────────────

// kgModel is one chat model plus its call counter.
type kgModel struct {
	model  string
	client *ollama.Client
	calls  int
}

func newKGModel(t *testing.T, endpoint, model string, maxTokens int) *kgModel {
	t.Helper()
	assertLocalOnlyModel(t, model)
	seed := kgSeed
	return &kgModel{
		model: model,
		client: ollama.NewClient(ollama.Config{
			Endpoint:    endpoint,
			Model:       model,
			Temperature: kgTemperature,
			MaxTokens:   maxTokens,
			Timeout:     kgCallTimeout,
			Seed:        &seed,
		}),
	}
}

// ask makes one chat call and returns the raw reply.
func (m *kgModel) ask(ctx context.Context, prompt string) (string, time.Duration, error) {
	m.calls++
	start := time.Now()
	resp, err := m.client.Chat(ctx, []types.Message{{
		Role:      "user",
		Content:   prompt,
		Timestamp: start,
	}}, nil)
	elapsed := time.Since(start)
	if err != nil {
		return "", elapsed, fmt.Errorf("%s: %w", m.model, err)
	}
	return resp.Content, elapsed, nil
}

// ─────────────────────────── corpus store ───────────────────────────

// kgStore is the ingested corpus: the memory store plus the memory-ID → record
// mapping that makes a retrieved memory attributable to a corpus record.
type kgStore struct {
	store   *sqlite.GraphMemoryStore
	byMemID map[string]kgRecord
	dims    int // observed embedding width, read from a probe — never assumed
}

// kgNewStore opens a throwaway SQLite database, migrates it, and returns the
// graph memory store. The blank driver import comes in with pkg/storage/sqlite
// itself (migrator.go registers "sqlite3"), so no extra import is needed here.
func kgNewStore(t *testing.T) *sqlite.GraphMemoryStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	db, err := sql.Open("sqlite3", path+"?_fk=1&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("opening the corpus database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	migrator, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("creating the migrator: %v", err)
	}
	if err := migrator.MigrateUp(context.Background()); err != nil {
		t.Fatalf("migrating the corpus database (is the build tagged fts5?): %v", err)
	}
	// A nil TokenCounter is accepted by the store (prepareMemory checks it); token
	// counts play no part in this measurement.
	return sqlite.NewGraphMemoryStore(db, nil, observability.NewNoOpTracer())
}

// kgProbeEmbeddingDims asks the embeddings endpoint for one vector and returns its
// width, WITHOUT sending a "dimensions" field.
//
// The probe exists because the width must be observed, not assumed, and because
// of a specific trap: Ollama's OpenAI-compatible endpoint HONORS the optional
// "dimensions" request field by truncating, and OpenAIEmbedder defaults that
// field to 1536 whenever it is left at zero. Constructing the embedder without
// first learning the native width therefore silently truncates every vector
// (measured: llama3.2 returns 3072 natively, 1536 when asked for 1536). Uniform
// truncation would still "work" — CosineSimilarity only breaks on MIXED widths —
// but it would be an unreported, unintended degradation of the arm under test.
func kgProbeEmbeddingDims(t *testing.T, endpoint string) int {
	t.Helper()
	assertLocalOnlyModel(t, kgEmbedModel)

	body, err := json.Marshal(map[string]any{
		"model": kgEmbedModel,
		"input": []string{genKGRecord(0).Sentence},
	})
	if err != nil {
		t.Fatalf("marshaling the embedding probe: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building the embedding probe request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer local-ollama-no-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("embedding probe to %s/v1/embeddings failed: %v", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the embedding probe response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("embedding probe returned HTTP %d: %s", resp.StatusCode, oneLine(string(raw), 300))
	}

	var decoded struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding the embedding probe response: %v", err)
	}
	if len(decoded.Data) != 1 || len(decoded.Data[0].Embedding) == 0 {
		t.Fatalf("embedding probe returned no vector: %s", oneLine(string(raw), 300))
	}
	return len(decoded.Data[0].Embedding)
}

// kgIngest embeds the corpus and writes all kgCorpusSize records into the store.
//
// Both retrieval arms are served by this single ingestion: the FTS index is
// populated by the store's own AFTER INSERT trigger, and the embedding is written
// in the same row. That matters — the gRPC Remember path never embeds, so
// VectorRecall (which only sees rows with a non-null embedding whose
// embedding_model matches the query's Model exactly) is unreachable through it.
// Writing Memory.Embedding directly is the only way to exercise that path.
func kgIngest(t *testing.T, store *sqlite.GraphMemoryStore, embedder *embedding.OpenAIEmbedder, wantDims int) *kgStore {
	t.Helper()
	corpus := kgCorpus()
	ctx := context.Background()

	// ── embed, in batches ──
	vectors := make([][]float32, 0, len(corpus))
	embedStart := time.Now()
	for lo := 0; lo < len(corpus); lo += kgEmbedBatchSize {
		hi := lo + kgEmbedBatchSize
		if hi > len(corpus) {
			hi = len(corpus)
		}
		texts := make([]string, 0, hi-lo)
		for _, rec := range corpus[lo:hi] {
			texts = append(texts, rec.Sentence)
		}
		bctx, cancel := context.WithTimeout(ctx, kgEmbedTimeout)
		batch, err := embedder.EmbedBatch(bctx, texts)
		cancel()
		if err != nil {
			t.Fatalf("embedding corpus records %d..%d: %v", lo, hi-1, err)
		}
		if len(batch) != hi-lo {
			t.Fatalf("embedding corpus records %d..%d: got %d vectors, want %d", lo, hi-1, len(batch), hi-lo)
		}
		vectors = append(vectors, batch...)
	}
	embedElapsed := time.Since(embedStart)

	// Uniform width is a hard requirement, not a nicety: CosineSimilarity returns
	// 0 on a length mismatch, so one odd vector would silently drop a record out
	// of every ranking rather than erroring.
	for i, v := range vectors {
		if len(v) != wantDims {
			t.Fatalf("record %d embedded to %d dimensions, want %d — mixed widths make CosineSimilarity return 0",
				i, len(v), wantDims)
		}
	}

	// ── write ──
	byMemID := make(map[string]kgRecord, len(corpus))
	writeStart := time.Now()
	for i, rec := range corpus {
		mem := &memory.Memory{
			AgentID:        kgAgentID,
			Content:        rec.Sentence,
			MemoryType:     "fact",
			Source:         "manual",
			SourceID:       rec.DeviceID,
			Tags:           []string{"knowledge-experiment"},
			Embedding:      vectors[i],
			EmbeddingModel: kgEmbedModel,
		}
		stored, err := store.Remember(ctx, mem)
		if err != nil {
			t.Fatalf("ingesting record %d (%s): %v", i, rec.DeviceID, err)
		}
		byMemID[stored.ID] = rec
	}
	writeElapsed := time.Since(writeStart)

	t.Logf("ingested %d records: embedded in %s (%d dims, model %s), written in %s",
		len(corpus), embedElapsed.Round(time.Millisecond), wantDims, kgEmbedModel,
		writeElapsed.Round(time.Millisecond))

	return &kgStore{store: store, byMemID: byMemID, dims: wantDims}
}

// recordsFor maps recalled memories back to corpus records, in rank order.
func (k *kgStore) recordsFor(mems []*memory.Memory) []kgRecord {
	out := make([]kgRecord, 0, len(mems))
	for _, m := range mems {
		if rec, ok := k.byMemID[m.ID]; ok {
			out = append(out, rec)
		}
	}
	return out
}

// bm25 retrieves top-k by Loom's FTS5 recall path. See kgFTS5Query for why the
// question text cannot be passed through verbatim.
func (k *kgStore) bm25(ctx context.Context, q kgQuestion) ([]kgRecord, error) {
	mems, err := k.store.Recall(ctx, memory.RecallOpts{
		AgentID: kgAgentID,
		Query:   kgFTS5Query(q.Text),
		Limit:   kgTopK,
	})
	if err != nil {
		return nil, fmt.Errorf("bm25 recall: %w", err)
	}
	return k.recordsFor(mems), nil
}

// vector retrieves top-k by cosine similarity over the stored embeddings. The
// Model field must match the ingested embedding_model exactly; a mismatch (or an
// empty value) makes VectorRecall return nil results with no error at all.
func (k *kgStore) vector(ctx context.Context, embedder *embedding.OpenAIEmbedder, q kgQuestion) ([]kgRecord, error) {
	vec, err := embedder.Embed(ctx, q.Text)
	if err != nil {
		return nil, fmt.Errorf("embedding the question: %w", err)
	}
	if len(vec) != k.dims {
		return nil, fmt.Errorf("question embedded to %d dimensions, corpus is %d", len(vec), k.dims)
	}
	mems, err := k.store.VectorRecall(ctx, memory.VectorRecallOpts{
		AgentID:   kgAgentID,
		Embedding: vec,
		Model:     kgEmbedModel,
		Limit:     kgTopK,
	})
	if err != nil {
		return nil, fmt.Errorf("vector recall: %w", err)
	}
	return k.recordsFor(mems), nil
}

// ─────────────────────────── trials ───────────────────────────

// kgTrial is one (arm, question) observation.
type kgTrial struct {
	Arm       string `json:"arm"`
	Index     int    `json:"index"`
	DeviceID  string `json:"device_id"`
	Attribute string `json:"attribute"`
	Truth     int    `json:"truth"`
	Got       int    `json:"got"`
	Parsed    bool   `json:"parsed"`
	Correct   bool   `json:"correct"`
	// Abstained is a committed -1: the model said it did not have the fact.
	Abstained bool `json:"abstained"`
	// Hallucinated is a committed number that is neither right nor an abstention
	// — the failure mode that makes an un-augmented model dangerous rather than
	// merely useless.
	Hallucinated bool `json:"hallucinated"`
	// RetrievalUsed marks the arms that had a context block at all.
	RetrievalUsed bool `json:"retrieval_used"`
	// RetrievalHit is whether the GOLD record was in the top-k. This is the
	// metric that separates retrieval failure from utilization failure: a wrong
	// answer with hit=true is the model failing to use a fact it was handed.
	RetrievalHit bool     `json:"retrieval_hit"`
	RetrievedIDs []string `json:"retrieved_ids,omitempty"`
	ContextChars int      `json:"context_chars"`
	Seconds      float64  `json:"seconds"`
	Output       string   `json:"output"`
	Err          string   `json:"error,omitempty"`
}

// kgRetriever fetches the context records for one question. nil means a
// no-context arm.
type kgRetriever func(ctx context.Context, q kgQuestion) ([]kgRecord, error)

// kgArm is one arm: one model, one retrieval strategy (or none).
type kgArm struct {
	name      string
	model     *kgModel
	retrieve  kgRetriever
	trials    []kgTrial
	retrieveS float64 // seconds spent in retrieval, excluded from model time
}

// kgRenderContext renders retrieved records into the prompt's context block. Every
// context arm uses the identical rendering, so the only difference between the
// oracle arm and the retrieval arms is WHICH records are in the block — not how
// they are presented.
func kgRenderContext(recs []kgRecord) string {
	if len(recs) == 0 {
		return "Information:\n(none)"
	}
	var b strings.Builder
	b.WriteString("Information:")
	for _, rec := range recs {
		b.WriteString("\n- ")
		b.WriteString(rec.Sentence)
	}
	return b.String()
}

// run executes the arm over questions 0..n-1.
func (a *kgArm) run(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		q := genKGQuestion(i)
		tr := kgTrial{
			Arm:       a.name,
			Index:     i,
			DeviceID:  q.Gold.DeviceID,
			Attribute: q.Attribute,
			Truth:     q.Truth,
		}

		prompt := fmt.Sprintf(kgNoContextPrompt, q.Text)
		if a.retrieve != nil {
			tr.RetrievalUsed = true
			rctx, cancel := context.WithTimeout(context.Background(), kgEmbedTimeout)
			rStart := time.Now()
			recs, err := a.retrieve(rctx, q)
			a.retrieveS += time.Since(rStart).Seconds()
			cancel()
			if err != nil {
				tr.Err = err.Error()
				a.trials = append(a.trials, tr)
				t.Logf("[%s] i=%-2d %-8s RETRIEVAL ERROR: %v", a.name, i, q.Gold.DeviceID, err)
				continue
			}
			for _, rec := range recs {
				tr.RetrievedIDs = append(tr.RetrievedIDs, rec.DeviceID)
				if rec.Index == q.Gold.Index {
					tr.RetrievalHit = true
				}
			}
			block := kgRenderContext(recs)
			tr.ContextChars = len(block)
			prompt = fmt.Sprintf(kgContextPrompt, block, q.Text)
		}

		ctx, cancel := context.WithTimeout(context.Background(), kgCallTimeout)
		out, elapsed, err := a.model.ask(ctx, prompt)
		cancel()
		tr.Seconds = elapsed.Seconds()
		tr.Output = out
		if err != nil {
			tr.Err = err.Error()
			a.trials = append(a.trials, tr)
			t.Logf("[%s] i=%-2d %-8s MODEL ERROR: %v", a.name, i, q.Gold.DeviceID, err)
			continue
		}

		// parseArithAnswer is reused verbatim from the reasoning harness: same
		// {"answer": <integer>} contract, and the same defensive ladder (strip
		// <think>, whole reply as JSON, last balanced object, regex over the
		// tail). It already handles negatives, which is what makes -1 detectable.
		tr.Got, tr.Parsed = parseArithAnswer(out)
		tr.Correct = tr.Parsed && tr.Got == q.Truth
		tr.Abstained = tr.Parsed && tr.Got == kgAbstain
		tr.Hallucinated = tr.Parsed && !tr.Correct && !tr.Abstained
		a.trials = append(a.trials, tr)

		t.Logf("[%s] i=%-2d %-8s %-17s truth=%-5d got=%-9s correct=%-5t abstain=%-5t hit=%-5s ret=%-24s %6.1fs",
			a.name, i, q.Gold.DeviceID, q.Attribute, q.Truth, kgAnswerStr(tr), tr.Correct,
			tr.Abstained, kgHitStr(tr), kgIDsStr(tr), tr.Seconds)
	}
}

func kgAnswerStr(tr kgTrial) string {
	if !tr.Parsed {
		return "UNPARSED"
	}
	if tr.Got == kgAbstain {
		return "-1(abst)"
	}
	return strconv.Itoa(tr.Got)
}

func kgHitStr(tr kgTrial) string {
	if !tr.RetrievalUsed {
		return "-"
	}
	return strconv.FormatBool(tr.RetrievalHit)
}

func kgIDsStr(tr kgTrial) string {
	if !tr.RetrievalUsed {
		return "-"
	}
	if len(tr.RetrievedIDs) == 0 {
		return "(empty)"
	}
	return strings.Join(tr.RetrievedIDs, ",")
}

// ─────────────────────────── aggregation ───────────────────────────

type kgSummary struct {
	name             string
	n                int
	correct          int
	abstained        int
	hallucinated     int
	unparsed         int
	errors           int
	retrievalUsed    bool
	retrievalHits    int
	emptyRetrievals  int
	minS, medS, maxS float64
	modelS           float64
	retrieveS        float64
}

func kgSummarize(a *kgArm) kgSummary {
	s := kgSummary{name: a.name, n: len(a.trials), retrieveS: a.retrieveS}
	lat := make([]float64, 0, len(a.trials))
	for _, tr := range a.trials {
		switch {
		case tr.Correct:
			s.correct++
		case tr.Abstained:
			s.abstained++
		case tr.Hallucinated:
			s.hallucinated++
		}
		if !tr.Parsed {
			s.unparsed++
		}
		if tr.Err != "" {
			s.errors++
		}
		if tr.RetrievalUsed {
			s.retrievalUsed = true
			if tr.RetrievalHit {
				s.retrievalHits++
			}
			if len(tr.RetrievedIDs) == 0 {
				s.emptyRetrievals++
			}
		}
		lat = append(lat, tr.Seconds)
		s.modelS += tr.Seconds
	}
	if len(lat) > 0 {
		sort.Float64s(lat)
		s.minS, s.medS, s.maxS = lat[0], lat[len(lat)/2], lat[len(lat)-1]
	}
	return s
}

// ─────────────────────────── the experiment ───────────────────────────

// TestLiveOllamaKnowledgeLeveling is the five-arm knowledge-bound measurement.
// Skipped by default; see the file header.
func TestLiveOllamaKnowledgeLeveling(t *testing.T) {
	endpoint := requireLiveOllama(t, kgWeakModel, kgStrongModel, kgEmbedModel)
	// requireLiveOllama already refuses cloud-routed names; this restates the
	// guard next to the models it protects, embedder included.
	for _, m := range []string{kgWeakModel, kgStrongModel, kgEmbedModel} {
		assertLocalOnlyModel(t, m)
	}

	n := kgQuestionCountForRun()
	runStart := time.Now()

	// ── corpus setup ──
	dims := kgProbeEmbeddingDims(t, endpoint)
	t.Logf("embeddings endpoint %s/v1/embeddings answered with %d dimensions for model %s",
		endpoint, dims, kgEmbedModel)

	embedder := embedding.NewOpenAIEmbedder(embedding.OpenAIConfig{
		BaseURL: endpoint + "/v1",
		// Ollama's compat endpoint ignores the key; a placeholder is required
		// because OpenAIEmbedder always sends an Authorization header.
		APIKey: "local-ollama-no-key",
		Model:  kgEmbedModel,
		// Set from the probe, NOT left at zero: zero defaults to 1536 and Ollama
		// honors it by truncating. See kgProbeEmbeddingDims.
		Dimensions: dims,
		Timeout:    kgEmbedTimeout,
	})

	corpus := kgIngest(t, kgNewStore(t), embedder, dims)

	// Preconditions on the two retrieval paths, checked once before any arm runs
	// so a silent retrieval failure cannot be mistaken for a model failure. The
	// vector check matters most: VectorRecall returns (nil, nil) on an
	// embedding_model mismatch, which is indistinguishable from "nothing was
	// similar" in the arm's own output.
	probeQ := genKGQuestion(0)
	pctx, pcancel := context.WithTimeout(context.Background(), kgEmbedTimeout)
	bm25Probe, err := corpus.bm25(pctx, probeQ)
	if err != nil {
		pcancel()
		t.Fatalf("BM25 precondition probe failed: %v", err)
	}
	vecProbe, err := corpus.vector(pctx, embedder, probeQ)
	pcancel()
	if err != nil {
		t.Fatalf("vector precondition probe failed: %v", err)
	}
	if len(bm25Probe) == 0 {
		t.Fatalf("BM25 precondition failed: recall returned nothing for %q", probeQ.Text)
	}
	if len(vecProbe) == 0 {
		t.Fatalf("vector precondition failed: VectorRecall returned nothing — check that " +
			"Memory.EmbeddingModel matches VectorRecallOpts.Model exactly")
	}
	t.Logf("retrieval preconditions ok: bm25 top-%d=%v (gold %s), vector top-%d=%v",
		len(bm25Probe), kgRecordIDs(bm25Probe), probeQ.Gold.DeviceID, len(vecProbe), kgRecordIDs(vecProbe))

	// ── arms ──
	weak := func() *kgModel { return newKGModel(t, endpoint, kgWeakModel, kgWeakMaxTokens) }

	arms := []*kgArm{
		// 1 — the floor. No context at all: a fictional fact the weights cannot
		// contain. Every non-abstention here is a hallucination by construction.
		{name: "1-llama2-nocontext", model: weak()},

		// 2 — the RAG ceiling. Exactly the gold sentence, no retrieval risk.
		{name: "2-llama2-oracle", model: weak(),
			retrieve: func(_ context.Context, q kgQuestion) ([]kgRecord, error) {
				return []kgRecord{q.Gold}, nil
			}},

		// 3 — realistic BM25: Loom's FTS5 recall over all 200 records.
		{name: "3-llama2-bm25", model: weak(),
			retrieve: func(ctx context.Context, q kgQuestion) ([]kgRecord, error) {
				return corpus.bm25(ctx, q)
			}},

		// 4 — vector retrieval through VectorRecall, exercised for the first time.
		{name: "4-llama2-vector", model: weak(),
			retrieve: func(ctx context.Context, q kgQuestion) ([]kgRecord, error) {
				return corpus.vector(ctx, embedder, q)
			}},

		// 5 — knowledge ≠ reasoning. The strong reasoning model, no context.
		// Expected to land near arm 1: no amount of reasoning recovers a fact that
		// was never in the weights.
		{name: "5-r1-nocontext", model: newKGModel(t, endpoint, kgStrongModel, kgStrongMaxTokens)},
	}

	var all []kgTrial
	summaries := make([]kgSummary, 0, len(arms))
	for _, arm := range arms {
		arm.run(t, n)
		all = append(all, arm.trials...)
		summaries = append(summaries, kgSummarize(arm))
	}

	// ── reporting ──
	kgReport(t, summaries, n)
	kgPerQuestion(t, arms, n)
	for _, arm := range arms {
		kgRetrievalBreakdown(t, arm)
	}
	kgHypothesisVerdict(t, summaries)

	t.Logf("")
	t.Logf("total experiment wall-clock: %s (N=%d, corpus=%d records, top-k=%d, seed=%d, temp=%.1f, "+
		"num_predict weak/strong=%d/%d, embeddings %s @ %d dims)",
		time.Since(runStart).Round(time.Second), n, kgCorpusSize, kgTopK, kgSeed, kgTemperature,
		kgWeakMaxTokens, kgStrongMaxTokens, kgEmbedModel, dims)

	kgWriteResults(t, all)
}

func kgRecordIDs(recs []kgRecord) []string {
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.DeviceID)
	}
	return ids
}

// kgReport prints the per-arm table.
func kgReport(t *testing.T, summaries []kgSummary, n int) {
	t.Helper()
	t.Logf("")
	t.Logf("── knowledge-bound leveling: %d fictional device facts, N=%d identical questions/arm ──", kgCorpusSize, n)
	t.Logf("%-20s %9s %9s %9s %8s %10s %7s %7s %7s",
		"arm", "accuracy", "abstain", "halluc", "unparsed", "retr-hit", "min", "med", "max")
	for _, s := range summaries {
		hit := "-"
		if s.retrievalUsed {
			hit = fmt.Sprintf("%d/%d", s.retrievalHits, s.n)
		}
		t.Logf("%-20s %4d/%-4d %4d/%-4d %4d/%-4d %8d %10s %6.1fs %6.1fs %6.1fs",
			s.name, s.correct, s.n, s.abstained, s.n, s.hallucinated, s.n, s.unparsed, hit,
			s.minS, s.medS, s.maxS)
	}
	for _, s := range summaries {
		t.Logf("%-20s errors=%d empty_retrievals=%d model_time=%.1fs retrieval_time=%.1fs",
			s.name, s.errors, s.emptyRetrievals, s.modelS, s.retrieveS)
	}
}

// kgPerQuestion prints the cross-arm comparison. Running identical questions in
// every arm is what makes a row-by-row read possible.
func kgPerQuestion(t *testing.T, arms []*kgArm, n int) {
	t.Helper()
	t.Logf("")
	t.Logf("── per-question outcomes (✓ = correct, value = answer, ! = retrieval miss) ──")
	header := fmt.Sprintf("%-3s %-8s %-17s %-6s", "i", "device", "attribute", "truth")
	for _, a := range arms {
		header += fmt.Sprintf(" %-14s", a.name)
	}
	t.Logf("%s", header)
	for i := 0; i < n; i++ {
		q := genKGQuestion(i)
		row := fmt.Sprintf("%-3d %-8s %-17s %-6d", i, q.Gold.DeviceID, q.Attribute, q.Truth)
		for _, a := range arms {
			if i >= len(a.trials) {
				row += fmt.Sprintf(" %-14s", "-")
				continue
			}
			tr := a.trials[i]
			mark := " "
			if tr.Correct {
				mark = "✓"
			}
			miss := ""
			if tr.RetrievalUsed && !tr.RetrievalHit {
				miss = "!"
			}
			row += fmt.Sprintf(" %-14s", mark+kgAnswerStr(tr)+miss)
		}
		t.Logf("%s", row)
	}
}

// kgRetrievalBreakdown is the central diagnostic for the retrieval arms: it splits
// their failures into RETRIEVAL failure (the fact never reached the prompt) and
// UTILIZATION failure (the fact was in the prompt and the model still got it
// wrong). Those two have completely different fixes, and an accuracy number alone
// cannot tell them apart.
func kgRetrievalBreakdown(t *testing.T, a *kgArm) {
	t.Helper()
	if len(a.trials) == 0 || !a.trials[0].RetrievalUsed {
		return
	}
	var hitCorrect, hitWrong, hitAbstain, missCorrect, missWrong, missAbstain int
	for _, tr := range a.trials {
		switch {
		case tr.RetrievalHit && tr.Correct:
			hitCorrect++
		case tr.RetrievalHit && tr.Abstained:
			hitAbstain++
		case tr.RetrievalHit:
			hitWrong++
		case tr.Correct:
			missCorrect++
		case tr.Abstained:
			missAbstain++
		default:
			missWrong++
		}
	}
	hits := hitCorrect + hitWrong + hitAbstain
	misses := missCorrect + missWrong + missAbstain
	t.Logf("")
	t.Logf("── [%s] retrieval vs utilization ──", a.name)
	t.Logf("gold record retrieved: %d/%d (%s)", hits, len(a.trials), rzPct(hits, len(a.trials)))
	t.Logf("  of those: correct=%d  wrong=%d  abstained=%d  → utilization rate %s",
		hitCorrect, hitWrong, hitAbstain, rzPct(hitCorrect, hits))
	t.Logf("gold record MISSED: %d/%d", misses, len(a.trials))
	t.Logf("  of those: correct=%d (answered from a distractor or by luck)  wrong=%d  abstained=%d",
		missCorrect, missWrong, missAbstain)
	t.Logf("interpretation: %d failures are retrieval's (fact never reached the prompt), "+
		"%d are the model's (fact was in the prompt, answer still wrong)",
		missWrong+missAbstain, hitWrong+hitAbstain)
}

// kgHypothesisVerdict states the hypothesis arithmetic explicitly rather than
// leaving it to a reader of the table: retrieval closes the knowledge gap fully
// if a weak model WITH the fact matches or beats a strong model WITHOUT it.
func kgHypothesisVerdict(t *testing.T, summaries []kgSummary) {
	t.Helper()
	byName := make(map[string]kgSummary, len(summaries))
	for _, s := range summaries {
		byName[s.name] = s
	}
	floor, okFloor := byName["1-llama2-nocontext"]
	oracle, okOracle := byName["2-llama2-oracle"]
	bm25, okBM25 := byName["3-llama2-bm25"]
	vec, okVec := byName["4-llama2-vector"]
	strong, okStrong := byName["5-r1-nocontext"]
	if !okFloor || !okOracle || !okBM25 || !okVec || !okStrong {
		t.Logf("hypothesis verdict skipped: not every arm reported")
		return
	}

	t.Logf("")
	t.Logf("── hypothesis: retrieval closes the knowledge gap fully ──")
	t.Logf("weak, no context (arm 1):      %d/%d %s", floor.correct, floor.n, rzPct(floor.correct, floor.n))
	t.Logf("strong, no context (arm 5):    %d/%d %s", strong.correct, strong.n, rzPct(strong.correct, strong.n))
	t.Logf("weak + oracle fact (arm 2):    %d/%d %s", oracle.correct, oracle.n, rzPct(oracle.correct, oracle.n))
	t.Logf("weak + BM25 top-%d (arm 3):     %d/%d %s", kgTopK, bm25.correct, bm25.n, rzPct(bm25.correct, bm25.n))
	t.Logf("weak + vector top-%d (arm 4):   %d/%d %s", kgTopK, vec.correct, vec.n, rzPct(vec.correct, vec.n))
	t.Logf("weak+oracle beats strong-alone: %t   weak+BM25 beats strong-alone: %t   weak+vector beats strong-alone: %t",
		oracle.correct >= strong.correct, bm25.correct >= strong.correct, vec.correct >= strong.correct)
	t.Logf("hallucination on the floor arms: weak %d/%d, strong %d/%d (the rest abstained or did not parse)",
		floor.hallucinated, floor.n, strong.hallucinated, strong.n)
}

// ─────────────────────────── per-trial artifacts ───────────────────────────

// kgWriteResults writes per-trial JSONL when kgResultsDirEnv is set. A write
// failure is logged, never fatal: the measurement already happened and t.Log
// carries every row.
func kgWriteResults(t *testing.T, trials []kgTrial) {
	t.Helper()
	dir := os.Getenv(kgResultsDirEnv)
	if dir == "" {
		t.Logf("per-trial JSONL not written: set %s to a directory to capture it", kgResultsDirEnv)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("cannot create results dir %q: %v", dir, err)
		return
	}
	var buf strings.Builder
	for _, tr := range trials {
		b, err := json.Marshal(tr)
		if err != nil {
			t.Logf("marshaling a trial row: %v", err)
			continue
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	path := filepath.Join(dir, "knowledge_arms.jsonl")
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil { //nolint:gosec // local measurement artifact
		t.Logf("writing %s: %v", path, err)
		return
	}
	t.Logf("wrote %d rows to %s", len(trials), path)

	// The corpus is written alongside, so a re-analysis of the JSONL never has to
	// re-derive ground truth from the generator.
	var cbuf strings.Builder
	for _, rec := range kgCorpus() {
		b, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		cbuf.Write(b)
		cbuf.WriteByte('\n')
	}
	cpath := filepath.Join(dir, "knowledge_corpus.jsonl")
	if err := os.WriteFile(cpath, []byte(cbuf.String()), 0o644); err != nil { //nolint:gosec // local measurement artifact
		t.Logf("writing %s: %v", cpath, err)
		return
	}
	t.Logf("wrote %d corpus rows to %s", kgCorpusSize, cpath)
}
