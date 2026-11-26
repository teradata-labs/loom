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

package failurerecovery

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/types"
)

// eventSink receives observability events. *observability.Span satisfies it
// structurally, so the engine stays decoupled from the tracer and is trivially
// testable with a fake. A nil sink disables event emission.
type eventSink interface {
	AddEvent(name string, attrs map[string]interface{})
}

// Store is the subset of memory.GraphMemoryStore that this engine needs.
// Depending on this narrow surface (rather than the full store) keeps the engine
// decoupled and unit-testable with a small fake. *memory.GraphMemoryStore
// implementations satisfy it.
type Store interface {
	Remember(ctx context.Context, mem *memory.Memory) (*memory.Memory, error)
	Recall(ctx context.Context, opts memory.RecallOpts) ([]*memory.Memory, error)
	VectorRecall(ctx context.Context, opts memory.VectorRecallOpts) ([]*memory.Memory, error)
	TouchMemories(ctx context.Context, ids []string) error
	Supersede(ctx context.Context, oldMemoryID string, newMem *memory.Memory) (*memory.Memory, error)
}

// tagFailureRecovery is present on every lesson so recall can filter to this
// feature's records regardless of the recall path used.
const tagFailureRecovery = "failure_recovery"

// Engine is the single reader/writer of failure-recovery memory. All methods are
// best-effort and nil-safe: a nil or disabled engine is a no-op, and no method
// ever returns an error that should abort a turn.
type Engine struct {
	store    Store
	embedder memory.Embedder // optional; nil => FTS5-only
	cfg      Config

	// abstractor turns a raw failure into a PII-safe (problem, solution) pair.
	// Defaults to the deterministic ruleAbstractor; WithLLMAbstractor swaps in
	// the LLM-backed one (which still falls back to rules). Never nil.
	abstractor Abstractor

	// llmModel is staged by an option and consumed once in NewEngine (after all
	// options run) so the LLM abstractor can be built regardless of option order.
	llmModel LessonModel
	// debugHook, when non-nil, receives structured, PII-safe records of engine
	// decisions: LLM-abstractor outcomes and recall outcomes (which threshold was
	// used and how many lessons matched). Fields carry counts and safe categories
	// only, never raw error text. Optional; nil disables it.
	debugHook func(event string, fields map[string]any)

	mu       sync.Mutex
	trackers map[string]*tracker // per-session in-turn failure staging (RAM)

	now func() time.Time
}

// EngineOption customizes an Engine at construction. Options are applied after
// the config is normalized, so they may read e.cfg.
type EngineOption func(*Engine)

// WithLLMAbstractor supplies the agent's LLM to the engine. The model powers two
// opt-in features: LLM-drafted lessons (when cfg.LLMAbstraction.Enabled) and
// LLM-predicted proactive queries (when cfg.Proactive.LLMIntent). The model is
// always stashed so either feature can use it; the lesson abstractor itself is
// only swapped in when cfg.LLMAbstraction.Enabled (see NewEngine). A nil model is
// a no-op.
func WithLLMAbstractor(model LessonModel) EngineOption {
	return func(e *Engine) {
		if model == nil {
			return
		}
		e.llmModel = model
	}
}

// WithLLMAbstractorDebug attaches an optional observer that records engine
// decisions: why the LLM abstractor used its output or fell back to rules, and
// recall outcomes (the threshold used and how many lessons matched). The
// observer only ever receives counts and safe categories, never raw error text.
func WithLLMAbstractorDebug(fn func(event string, fields map[string]any)) EngineOption {
	return func(e *Engine) {
		e.debugHook = fn
	}
}

// debugf forwards a PII-safe structured record to the debug hook if one is set.
// Nil-safe and cheap when no hook is attached.
func (e *Engine) debugf(event string, fields map[string]any) {
	if e != nil && e.debugHook != nil {
		e.debugHook(event, fields)
	}
}

// NewEngine builds an engine. store may be nil (feature inert). embedder may be
// nil (FTS5 fallback). The config is normalized with spec defaults. Options are
// applied after normalization.
func NewEngine(store Store, embedder memory.Embedder, cfg Config, opts ...EngineOption) *Engine {
	e := &Engine{
		store:      store,
		embedder:   embedder,
		cfg:        cfg.withDefaults(),
		abstractor: ruleAbstractor{},
		trackers:   make(map[string]*tracker),
		now:        time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	// Build the LLM abstractor once, after all options have run, so its optional
	// debug hook is wired regardless of option order. Falls back to rules. Only
	// swapped in when lesson drafting is enabled; the model may still be present
	// for the proactive LLM-intent path even when this is off.
	if e.llmModel != nil && e.cfg.LLMAbstraction.Enabled {
		la := newLLMAbstractor(e.llmModel, ruleAbstractor{}, e.cfg.llmTimeout())
		la.debug = e.debugHook
		// Detail preservation is only safe when memory is partitioned per user:
		// preserved identifiers are the author's own and are never shown to
		// another user. Requiring both flags prevents leaking one user's schema
		// into shared memory.
		la.preserveDetails = e.cfg.PreserveDetails && e.cfg.UserScoped
		e.abstractor = la
	}
	return e
}

// Enabled reports whether the feature is active. Safe on a nil receiver.
func (e *Engine) Enabled() bool {
	return e != nil && e.cfg.Enabled && e.store != nil
}

// ProactiveEnabled reports whether per-query pitfall pre-warning is active.
func (e *Engine) ProactiveEnabled() bool {
	return e.Enabled() && e.cfg.Proactive.Enabled
}

func (e *Engine) trackerFor(sessionID string) *tracker {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.trackers[sessionID]
	if !ok {
		t = newTracker()
		e.trackers[sessionID] = t
	}
	return t
}

// FailedCall carries the concrete tool call that failed, so the engine can
// build a deterministic signature (canonical tool+args + offending tokens from
// the error) for later LLM-free pre-execution matching. It is optional: callers
// that do not have the arguments (or older call sites) simply omit it and only
// the abstract problem/solution lesson is produced.
type FailedCall struct {
	Tool string
	Args map[string]any
}

// OnToolFailure stages an abstracted failure in the in-turn tracker (RAM only).
// Nothing is persisted here — a failure alone is never written (spec §7).
//
// The optional call argument supplies the concrete tool+args that failed. When
// present, a deterministic callSignature is built (canonical call for exact
// replay, offending tokens parsed from the error) and carried on the staged
// record; on recovery it is persisted (user-scoped only) so a future planned
// call can be matched without any LLM. Variadic for backward compatibility with
// callers that pass only tool+error.
func (e *Engine) OnToolFailure(sessionID, class, tool, errMsg string, call ...FailedCall) {
	if !e.Enabled() || !e.cfg.classAllowed(class) {
		return
	}
	// The staged (problem, solution) is what later becomes the durable lesson on
	// recovery, so this is the one place the LLM abstractor (when enabled) runs.
	// A detached context is used with the abstractor's own timeout; the call is
	// best-effort and must not be tied to the turn's cancellation. The LLM path
	// falls back to deterministic rules on any error.
	problem, solution := e.abstractor.Abstract(context.Background(), class, errMsg)
	rec := failureRecord{
		class:    class,
		tool:     tool,
		problem:  problem,
		solution: solution,
	}
	// Build the deterministic signature from the concrete call + error, if the
	// caller supplied the arguments. Offending tokens always come from the error
	// itself (never hand-picked), keeping tier-2 matching data-driven.
	if len(call) > 0 {
		c := call[0]
		rec.sig = callSignature{
			Tool:            c.Tool,
			CanonicalCall:   canonicalCall(c.Tool, c.Args),
			OffendingTokens: extractOffendingTokens(errMsg),
			ResourceTokens:  resourceTokensFromArgs(c.Args),
		}
	} else {
		rec.sig = callSignature{OffendingTokens: extractOffendingTokens(errMsg)}
	}
	e.trackerFor(sessionID).recordFailure(rec)
}

// OnToolSuccess checks whether this tool had a staged failure and, if so, learns
// the lesson (write-once). Called when a tool succeeds.
func (e *Engine) OnToolSuccess(ctx context.Context, agentID, sessionID, tool string, sink eventSink) {
	if !e.Enabled() {
		return
	}
	rec, recovered := e.trackerFor(sessionID).recordSuccess(tool)
	if !recovered {
		return
	}
	e.learn(ctx, agentID, rec, sink)
}

// ReactiveHints returns known solutions for a tool failure. Called on a tool
// failure, before the existing self-correction retry. Best-effort.
//
// The recall query text is taken from the failure OnToolFailure just staged for
// this session+tool (produced by the same abstractor that writes durable
// lessons), so the query and stored lessons share one representation and vector/
// FTS recall can actually match. If nothing is staged (e.g. ReactiveHints called
// without a prior OnToolFailure), it falls back to the deterministic abstraction.
func (e *Engine) ReactiveHints(ctx context.Context, agentID, sessionID, class, tool, errMsg string, sink eventSink) []string {
	if !e.Enabled() || !e.cfg.classAllowed(class) {
		return nil
	}
	problem := ""
	if rec, ok := e.trackerFor(sessionID).peekFailure(tool); ok && strings.TrimSpace(rec.problem) != "" {
		problem = rec.problem
	} else {
		problem, _ = abstract(class, errMsg)
	}
	matches := e.recall(ctx, agentID, problem, class, tool, e.cfg.SimilarityThreshold)
	e.debugf("reactive.recall", map[string]any{
		"hook":      "reactive",
		"class":     class,
		"tool":      tool,
		"threshold": e.cfg.SimilarityThreshold,
		"matches":   len(matches),
	})
	if len(matches) == 0 {
		emit(sink, "failure_recovery.recall_miss", map[string]interface{}{
			"failure_recovery.hook":  "reactive",
			"failure_recovery.class": class,
		})
		return nil
	}
	hints := e.collectHints(ctx, matches)
	emit(sink, "failure_recovery.reactive_hit", map[string]interface{}{
		"failure_recovery.hook":  "reactive",
		"failure_recovery.class": class,
		"failure_recovery.hints": len(hints),
	})
	return hints
}

// ProactiveHints returns advisory pitfalls for a task intent. Called once per
// user query when proactive is enabled. Best-effort.
//
// Lessons embed the problem (failure-space). A user query is task-space, so when
// cfg.Proactive.LLMIntent is set and a model is available the intent is first
// translated into its likely pitfall (failure-space) to align the comparison;
// best-effort, falling back to the raw intent on any error/timeout/"no pitfall".
//
// Unlike reactive (which has the exact error and matches its own lesson at a
// strict gate), proactive is advisory: it fetches the top-K nearest lessons
// above a soft similarity floor (Proactive.Threshold) and lets the injected
// directive do the precision — the agent reads each lesson and applies only
// those that match its planned action. The soft floor keeps the hook quiet when
// nothing is relevant while still surfacing near-misses worth showing.
func (e *Engine) ProactiveHints(ctx context.Context, agentID, intent string, sink eventSink) []string {
	if !e.ProactiveEnabled() || strings.TrimSpace(intent) == "" {
		return nil
	}
	query := intent
	usedLLM := false
	if e.cfg.Proactive.LLMIntent {
		if pitfall, ok := predictPitfall(ctx, e.llmModel, e.cfg.llmTimeout(), intent); ok {
			query = pitfall
			usedLLM = true
		}
	}
	matches := e.recallTopK(ctx, agentID, query, e.cfg.Proactive.Threshold, proactiveTopK)
	e.debugf("proactive.recall", map[string]any{
		"hook":       "proactive",
		"floor":      e.cfg.Proactive.Threshold,
		"top_k":      proactiveTopK,
		"matches":    len(matches),
		"llm_intent": usedLLM,
		"query":      query,
	})
	var hints []string
	if len(matches) > 0 {
		hints = e.collectPitfalls(ctx, matches)
	}
	// Deterministic resource-gated advisory. Runs regardless of the embedding
	// path (and regardless of whether an LLM translated the intent), so a lesson
	// is surfaced at planning time whenever the task names the same qualified
	// object a past call broke on — catching a SEMANTIC re-derivation of the
	// same defect (a different query that again mishandles this exact table)
	// that exact-replay/token tiers provably cannot. Scoped by resource overlap,
	// never "all memory".
	if det := e.resourceGatedHints(ctx, agentID, intent); len(det) > 0 {
		hints = mergeHints(hints, det)
	}
	if len(hints) == 0 {
		return nil
	}
	if len(hints) > e.cfg.MaxHints {
		hints = hints[:e.cfg.MaxHints]
	}
	emit(sink, "failure_recovery.proactive_hit", map[string]interface{}{
		"failure_recovery.hook":  "proactive",
		"failure_recovery.hints": len(hints),
	})
	return hints
}

// resourceGatedHints is the deterministic advisory tier: it surfaces a stored
// lesson's fix when the current TASK references the same schema-qualified object
// the failing call touched. It uses no LLM and no embeddings, so it works even
// when intent translation is disabled — closing the gap where a capable agent
// would otherwise re-derive the same defect (e.g. a different query that again
// leaves a reserved word unquoted on this table) with no warning.
//
// It is bounded by construction: only lessons whose failing call touched a
// schema-qualified resource that ALSO appears in the task qualify, so a task
// that names no known-bad object — or names only bare words — gets nothing. This
// is retrieval keyed on the object in hand, not a dump of all memory.
//
// Only active when user-scoped: signatures (which carry the resource tokens) are
// stored only in that mode, the same PII rule the guard follows. Disabled
// entirely when SemanticOnly is set.
func (e *Engine) resourceGatedHints(ctx context.Context, agentID, intent string) []string {
	if !e.cfg.UserScoped || e.cfg.SemanticOnly {
		return nil
	}
	taskResources := resourceTokensFromText(intent)
	if len(taskResources) == 0 {
		return nil
	}
	candidates := e.signatureCandidates(ctx, agentID, "")
	if len(candidates) == 0 {
		return nil
	}
	var out []string
	var ids []string
	seen := map[string]bool{}
	for _, m := range candidates {
		if m == nil {
			continue
		}
		sig := parseSignature(signatureOf(m))
		if !matchResource(sig, taskResources) {
			continue
		}
		hint := formatGuardHint(0, strings.TrimSpace(m.Content), strings.TrimSpace(m.Summary))
		if hint == "" || seen[hint] {
			continue
		}
		seen[hint] = true
		out = append(out, hint)
		ids = append(ids, m.ID)
	}
	if len(out) == 0 {
		return nil
	}
	if len(ids) > 0 {
		_ = e.store.TouchMemories(ctx, ids)
	}
	e.debugf("proactive.resource", map[string]any{
		"agent_id": agentID,
		"matches":  len(out),
	})
	return out
}

// mergeHints appends the deterministic hints to the embedding-derived ones,
// skipping exact duplicates so the same lesson is not shown twice when both
// paths surface it.
func mergeHints(base, extra []string) []string {
	seen := map[string]bool{}
	for _, h := range base {
		seen[h] = true
	}
	for _, h := range extra {
		if !seen[h] {
			seen[h] = true
			base = append(base, h)
		}
	}
	return base
}

// GuardHint is the result of a deterministic pre-execution match: a planned
// tool call matched a stored failure signature. Tier records how the match was
// made (1 = exact replay, 2 = offending-token overlap) for honest observability
// — so a prevented repeat can be attributed to a real guard hit rather than mere
// model competence. Hint is the advisory text injected back for the model to act
// on (it names the known problem and the verified fix, then lets the model
// re-plan — detection is deterministic, correction stays general).
type GuardHint struct {
	Tier    int
	Problem string
	Fix     string
	Hint    string
}

// PreExecGuard checks a PLANNED tool call against stored failure signatures
// before it executes, using only deterministic string logic — no LLM, no
// embeddings. It is the robust core of proactive prevention: it matches the
// agent's concrete planned action (ground truth, same space as the stored
// failure) rather than predicting a failure from the user's intent.
//
// It returns advisory hints for every stored lesson whose signature matches the
// planned call (tier 1 exact replay, or tier 2 offending-token overlap), capped
// at MaxHints and strongest-tier first. Empty result => let the call through.
// Best-effort and nil-safe: any store error yields no hints (the call proceeds
// and the existing reactive path still catches a real failure).
//
// Only active when the feature is user-scoped — signatures are stored only in
// that mode (buildLesson), so there is nothing to match against otherwise.
// Disabled entirely when SemanticOnly is set.
func (e *Engine) PreExecGuard(ctx context.Context, agentID, tool string, args map[string]any, sink eventSink) []GuardHint {
	if !e.Enabled() || !e.cfg.UserScoped || e.cfg.SemanticOnly {
		return nil
	}
	candidates := e.signatureCandidates(ctx, agentID, tool)
	if len(candidates) == 0 {
		return nil
	}
	plannedCanonical := canonicalCall(tool, args)
	plannedTokens := argTokens(tool, args)

	var hints []GuardHint
	ids := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, m := range candidates {
		if m == nil {
			continue
		}
		sig := parseSignature(signatureOf(m))
		tier := matchTier(sig, plannedCanonical, plannedTokens)
		if tier == 0 {
			continue
		}
		problem := strings.TrimSpace(m.Content)
		fix := strings.TrimSpace(m.Summary)
		key := problem + "|" + fix
		if seen[key] {
			continue
		}
		seen[key] = true
		hints = append(hints, GuardHint{
			Tier:    tier,
			Problem: problem,
			Fix:     fix,
			Hint:    formatGuardHint(tier, problem, fix),
		})
		ids = append(ids, m.ID)
	}
	if len(hints) == 0 {
		return nil
	}
	// Strongest tier first (exact replay before token overlap), stable otherwise.
	sort.SliceStable(hints, func(i, j int) bool { return hints[i].Tier < hints[j].Tier })
	if len(hints) > e.cfg.MaxHints {
		hints = hints[:e.cfg.MaxHints]
		ids = ids[:e.cfg.MaxHints]
	}
	if len(ids) > 0 {
		_ = e.store.TouchMemories(ctx, ids)
	}
	e.debugf("guard.match", map[string]any{
		"agent_id": agentID,
		"tool":     tool,
		"matches":  len(hints),
		"top_tier": hints[0].Tier,
	})
	emit(sink, "failure_recovery.guard_hit", map[string]interface{}{
		"failure_recovery.hook":     "guard",
		"failure_recovery.tool":     tool,
		"failure_recovery.matches":  len(hints),
		"failure_recovery.top_tier": hints[0].Tier,
	})
	return hints
}

// PreExecAdvice is the CROSS-OBJECT prevention layer, run at the dispatch seam
// ("a tool call is about to happen"). The deterministic guard (PreExecGuard)
// catches a planned call that is the SAME call or carries an engine-declared
// invalid token; the resource-gated advisory catches a task that names the SAME
// object a lesson failed on. Neither fires when the model plans a call of the
// SAME SHAPE on a DIFFERENT object — e.g. the reserved-word mistake repeated on
// another table. PreExecAdvice closes that gap.
//
// It compares the planned call's STRUCTURAL skeleton (tool + arg keys + value
// identifiers, with the concrete object names removed) against the skeletons of
// stored failing calls. Dropping the object identifiers is what makes it
// cross-table: two calls that differ only in which table they touch collapse to
// the same skeleton and match at 1.0, while an unrelated call shares little and
// scores below the floor. This is deterministic (no LLM, no embeddings), so —
// unlike a vector query keyed on the raw call, which would have to match the
// abstracted problem text in a DIFFERENT vocabulary — it actually fires, and it
// stays explainable and consistent with the rest of the guard layer.
//
// It is advisory, not a block decision: the structural match is a strong signal
// but not a certainty, so the engine only SURFACES the lesson's problem+fix; the
// caller injects it and the planning model decides. To keep it from trapping a
// loop, it fires at most once per distinct planned call per session
// (markAdvisedIfNew). Returns nil when disabled, when nothing matches, or when
// this call was already advised.
//
// Only active when user-scoped, matching the rest of the deterministic layer:
// signatures (the stored skeletons) exist only in that mode. Disabled entirely
// when SemanticOnly is set.
func (e *Engine) PreExecAdvice(ctx context.Context, agentID, sessionID, tool string, args map[string]any, sink eventSink) []string {
	if !e.Enabled() || !e.cfg.UserScoped || e.cfg.SemanticOnly {
		return nil
	}
	candidates := e.signatureCandidates(ctx, agentID, tool)
	if len(candidates) == 0 {
		return nil
	}
	plannedSkel := callSkeleton(tool, args, resourceTokensFromArgs(args))
	if len(plannedSkel) == 0 {
		return nil
	}
	plannedCanonical := canonicalCall(tool, args)

	var advice []string
	ids := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, m := range candidates {
		if m == nil {
			continue
		}
		sig := parseSignature(signatureOf(m))
		if sig.empty() || strings.TrimSpace(sig.CanonicalCall) == "" {
			continue
		}
		// Exact replay is PreExecGuard's job (a certain block); skip it here so
		// advice is reserved for the SIMILAR-but-not-identical, cross-object case.
		if strings.EqualFold(sig.CanonicalCall, plannedCanonical) {
			continue
		}
		storedSkel := skeletonFromCanonical(sig.CanonicalCall, sig.ResourceTokens)
		jac, overlap := skeletonSimilarity(plannedSkel, storedSkel)
		if overlap < adviceMinSkeletonOverlap || jac < adviceSkeletonThreshold {
			continue
		}
		problem := strings.TrimSpace(m.Content)
		fix := strings.TrimSpace(m.Summary)
		if problem == "" && fix == "" {
			continue
		}
		var line string
		switch {
		case problem != "" && fix != "":
			line = "Known problem: " + problem + " Known fix: " + fix
		case problem != "":
			line = "Known problem: " + problem
		default:
			line = "Known fix: " + fix
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		advice = append(advice, line)
		ids = append(ids, m.ID)
	}
	if len(advice) == 0 {
		return nil
	}
	// Bound to once per distinct planned call: warn, then let the model decide.
	if !e.trackerFor(sessionID).markAdvisedIfNew(plannedCanonical) {
		return nil
	}
	if len(advice) > e.cfg.MaxHints {
		advice = advice[:e.cfg.MaxHints]
		ids = ids[:e.cfg.MaxHints]
	}
	if len(ids) > 0 {
		_ = e.store.TouchMemories(ctx, ids)
	}
	e.debugf("preexec.advice", map[string]any{
		"agent_id": agentID,
		"tool":     tool,
		"matches":  len(advice),
	})
	emit(sink, "failure_recovery.preexec_advice", map[string]interface{}{
		"failure_recovery.hook":    "preexec",
		"failure_recovery.tool":    tool,
		"failure_recovery.matches": len(advice),
	})
	return advice
}

// signatureCandidates fetches lessons that could carry a signature to match
// against the planned call. It uses the deterministic FTS/tag path (no
// embedding): all failure-recovery lessons in scope, which is a small per-agent
// set. Tool is NOT used as a hard filter because the gateway pattern stores the
// offending token under a differently-named outer tool.
func (e *Engine) signatureCandidates(ctx context.Context, agentID, tool string) []*memory.Memory {
	owner, sharedOnly := e.scopeRecall(ctx)
	mems, err := e.store.Recall(ctx, memory.RecallOpts{
		AgentID:    agentID,
		MemoryType: memory.MemoryTypeFailureRecovery,
		Tags:       []string{tagFailureRecovery},
		Owner:      owner,
		SharedOnly: sharedOnly,
		Limit:      maxGuardCandidates,
	})
	if err != nil {
		return nil
	}
	return mems
}

// signatureOf best-effort reads the stored signature JSON from a lesson's
// properties.
func signatureOf(m *memory.Memory) string {
	if m == nil || m.PropertiesJSON == "" {
		return ""
	}
	var props struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal([]byte(m.PropertiesJSON), &props); err != nil {
		return ""
	}
	return props.Signature
}

// formatGuardHint renders the advisory for a deterministic match. It states the
// known problem and verified fix and asks the model to revise its planned call —
// correction stays the model's job (general), detection is deterministic.
func formatGuardHint(tier int, problem, fix string) string {
	switch {
	case problem != "" && fix != "":
		return "Known problem: " + problem + " Known fix: " + fix
	case fix != "":
		return "Known fix: " + fix
	default:
		return "Known problem: " + problem
	}
}

// EndSession drops the in-turn tracker for a finished session, so staged
// (never-recovered) failures do not accumulate across sessions.
func (e *Engine) EndSession(sessionID string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	delete(e.trackers, sessionID)
	e.mu.Unlock()
}

// learn writes exactly one lesson for a confirmed recovery. If a near-identical
// lesson already exists for this class/tool, it either refreshes it (same
// solution) or supersedes it (the solution evolved), so the store never holds a
// duplicate and always carries the latest fix (spec §7).
func (e *Engine) learn(ctx context.Context, agentID string, rec failureRecord, sink eventSink) {
	e.debugf("learn.write", map[string]any{
		"agent_id": agentID,
		"owner":    e.scopeOwner(ctx),
		"class":    rec.class,
		"tool":     rec.tool,
	})
	if existing := e.findExisting(ctx, agentID, rec); existing != nil {
		// Same problem, same fix → just refresh access/salience (reuse signal).
		if strings.EqualFold(strings.TrimSpace(existing.Summary), strings.TrimSpace(rec.solution)) {
			_ = e.store.TouchMemories(ctx, []string{existing.ID})
			emit(sink, "failure_recovery.reused", map[string]interface{}{
				"failure_recovery.hook":  "learn",
				"failure_recovery.class": rec.class,
			})
			return
		}
		// Same problem, better/evolved fix → supersede so the latest wins while
		// preserving lineage. reuse_count carries forward, incremented.
		mem := e.buildLesson(ctx, agentID, rec, reuseCountOf(existing)+1)
		if _, err := e.store.Supersede(ctx, existing.ID, mem); err != nil {
			emit(sink, "failure_recovery.store_error", map[string]interface{}{
				"failure_recovery.hook": "learn",
				"error":                 err.Error(),
			})
			return
		}
		emit(sink, "failure_recovery.superseded", map[string]interface{}{
			"failure_recovery.hook":        "learn",
			"failure_recovery.class":       rec.class,
			"failure_recovery.reuse_count": reuseCountOf(existing) + 1,
		})
		return
	}

	mem := e.buildLesson(ctx, agentID, rec, 0)
	if _, err := e.store.Remember(ctx, mem); err != nil {
		emit(sink, "failure_recovery.store_error", map[string]interface{}{
			"failure_recovery.hook": "learn",
			"error":                 err.Error(),
		})
		return
	}
	emit(sink, "failure_recovery.learned", map[string]interface{}{
		"failure_recovery.hook":  "learn",
		"failure_recovery.class": rec.class,
		"failure_recovery.tool":  rec.tool,
	})
}

// buildLesson assembles the abstract Memory record for a lesson. Only PII-free
// problem/solution text is stored or embedded (spec §10).
// scopeOwner returns the owner id to stamp on new lessons. When UserScoped is
// off (default) it returns "" so a lesson is written unowned (shared). When on,
// it returns the caller's user id from the context, falling back to
// types.DefaultUserID when the request carries no identity so a lesson always
// lands in a concrete, isolatable bucket.
func (e *Engine) scopeOwner(ctx context.Context) string {
	if !e.cfg.UserScoped {
		return ""
	}
	if uid := types.UserIDFromContext(ctx); uid != "" {
		return uid
	}
	return types.DefaultUserID
}

// scopeRecall returns the owner filter and shared-only flag for recall. Privacy
// travels with the row, not the flag: when UserScoped is off we read ONLY
// unowned (shared) rows (SharedOnly), so any lesson written while scoping was on
// stays private even after the flag is later turned off (fail-closed). When on,
// we strictly match the caller's own owner bucket and never see other users' or
// legacy shared rows.
func (e *Engine) scopeRecall(ctx context.Context) (owner string, sharedOnly bool) {
	if !e.cfg.UserScoped {
		return "", true
	}
	if uid := types.UserIDFromContext(ctx); uid != "" {
		return uid, false
	}
	return types.DefaultUserID, false
}

func (e *Engine) buildLesson(ctx context.Context, agentID string, rec failureRecord, reuseCount int) *memory.Memory {
	propsMap := map[string]interface{}{
		"problem":       rec.problem,
		"solution":      rec.solution,
		"failure_class": rec.class,
		"tool":          rec.tool,
		"first_seen":    e.now().UTC().Format(time.RFC3339),
		"reuse_count":   reuseCount,
	}
	// Persist the deterministic signature ONLY when the feature is user-scoped.
	// The signature carries concrete identifiers from the author's own call, so
	// — exactly like LLM detail preservation — it is safe to store and replay
	// only within that user's partition, never in shared memory. In shared mode
	// the lesson keeps only its abstract problem/solution text.
	if e.cfg.UserScoped && !rec.sig.empty() {
		if s := rec.sig.marshal(); s != "" {
			propsMap["signature"] = s
		}
	}
	props, _ := json.Marshal(propsMap)

	mem := &memory.Memory{
		AgentID:        agentID,
		Content:        rec.problem,  // abstract "what went wrong"
		Summary:        rec.solution, // one-line reusable fix (the injected hint)
		MemoryType:     memory.MemoryTypeFailureRecovery,
		Source:         "agent",
		Tags:           []string{tagFailureRecovery, "class:" + rec.class, "tool:" + rec.tool},
		Salience:       e.cfg.InitialSalience,
		PropertiesJSON: string(props),
		Owner:          e.scopeOwner(ctx),
	}

	// Embed the PROBLEM only — never raw calls or errors (spec §10). Both recall
	// hooks query in problem-space (reactive: the error that occurred; proactive:
	// a predicted pitfall), so the solution text adds no retrieval signal and,
	// when concatenated, only dilutes the vector away from a problem-shaped query.
	// The solution is still stored as Summary (and indexed in memory_search for
	// FTS), so it remains available for the injected hint and keyword search.
	if e.embedder != nil {
		if vec, err := e.embedder.Embed(ctx, rec.problem); err == nil {
			mem.Embedding = vec
			mem.EmbeddingModel = e.embedder.Model()
		}
	}
	return mem
}

// reuseCountOf best-effort reads reuse_count from a lesson's PropertiesJSON.
func reuseCountOf(m *memory.Memory) int {
	if m == nil || m.PropertiesJSON == "" {
		return 0
	}
	var props struct {
		ReuseCount int `json:"reuse_count"`
	}
	if err := json.Unmarshal([]byte(m.PropertiesJSON), &props); err != nil {
		return 0
	}
	return props.ReuseCount
}

// findExisting looks for an already-stored lesson with the same abstract problem
// in the same class/tool, using the FTS5 path (dedup does not need vectors).
func (e *Engine) findExisting(ctx context.Context, agentID string, rec failureRecord) *memory.Memory {
	owner, sharedOnly := e.scopeRecall(ctx)
	mems, err := e.store.Recall(ctx, memory.RecallOpts{
		AgentID:    agentID,
		Query:      rec.problem,
		MemoryType: memory.MemoryTypeFailureRecovery,
		Tags:       []string{tagFailureRecovery, "class:" + rec.class, "tool:" + rec.tool},
		Owner:      owner,
		SharedOnly: sharedOnly,
		Limit:      e.cfg.MaxHints * 4,
	})
	if err != nil {
		return nil
	}
	for _, m := range mems {
		if m != nil && strings.EqualFold(strings.TrimSpace(m.Content), strings.TrimSpace(rec.problem)) {
			return m
		}
	}
	return nil
}

// recall returns lessons matching queryText, using the embedder when present and
// falling back to FTS5 otherwise. One search per lookup, never both (spec §13).
// class/tool may be empty to widen the scope (proactive).
func (e *Engine) recall(ctx context.Context, agentID, queryText, class, tool string, threshold float32) []*memory.Memory {
	owner, sharedOnly := e.scopeRecall(ctx)
	if e.embedder != nil {
		if vec, err := e.embedder.Embed(ctx, queryText); err == nil {
			mems, verr := e.store.VectorRecall(ctx, memory.VectorRecallOpts{
				AgentID:    agentID,
				Embedding:  vec,
				Model:      e.embedder.Model(),
				MemoryType: memory.MemoryTypeFailureRecovery,
				Threshold:  threshold,
				Owner:      owner,
				SharedOnly: sharedOnly,
				Limit:      e.cfg.MaxHints * 5,
			})
			filtered := filterLessons(mems, class, tool)
			e.debugf("recall.vector", map[string]any{
				"agent_id":  agentID,
				"owner":     owner,
				"threshold": threshold,
				"raw":       len(mems),
				"filtered":  len(filtered),
				"err":       verr != nil,
			})
			// VectorRecall returns nil,nil when embeddings are disabled for the
			// store; only trust a non-empty vector result, else fall through.
			if verr == nil && len(mems) > 0 {
				return e.capHints(filtered)
			}
		} else {
			e.debugf("recall.embed_error", map[string]any{"agent_id": agentID})
		}
	}

	mems, err := e.store.Recall(ctx, memory.RecallOpts{
		AgentID:    agentID,
		Query:      queryText,
		MemoryType: memory.MemoryTypeFailureRecovery,
		Tags:       tagFilter(class, tool),
		Owner:      owner,
		SharedOnly: sharedOnly,
		Limit:      e.cfg.MaxHints * 5,
	})
	if err != nil {
		return nil
	}
	fts := filterLessons(mems, class, tool)
	e.debugf("recall.fts", map[string]any{
		"agent_id": agentID,
		"owner":    owner,
		"tags":     tagFilter(class, tool),
		"raw":      len(mems),
		"filtered": len(fts),
	})
	return e.capHints(fts)
}

// recallTopK returns up to k lessons nearest to queryText above a low floor,
// ordered by similarity (VectorRecall orders by distance ascending). It is the
// proactive counterpart to recall: where recall applies a precise gate, this
// trades the gate for recall — the floor only drops clearly-unrelated lessons,
// and the injected directive lets the agent filter the rest by its planned
// action. When no embedder is present (or the vector path yields nothing) it
// falls back to FTS, capped at k, so proactive still pre-warns keyword-first.
// Returns nil on store error (best-effort, never fatal).
func (e *Engine) recallTopK(ctx context.Context, agentID, queryText string, floor float32, k int) []*memory.Memory {
	if k <= 0 {
		return nil
	}
	owner, sharedOnly := e.scopeRecall(ctx)
	if e.embedder != nil {
		if vec, err := e.embedder.Embed(ctx, queryText); err == nil {
			mems, verr := e.store.VectorRecall(ctx, memory.VectorRecallOpts{
				AgentID:    agentID,
				Embedding:  vec,
				Model:      e.embedder.Model(),
				MemoryType: memory.MemoryTypeFailureRecovery,
				Threshold:  floor,
				Owner:      owner,
				SharedOnly: sharedOnly,
				Limit:      k,
			})
			e.debugf("recall.vector", map[string]any{
				"agent_id": agentID,
				"owner":    owner,
				"floor":    floor,
				"top_k":    k,
				"raw":      len(mems),
				"err":      verr != nil,
			})
			// VectorRecall returns nil,nil when embeddings are disabled for the
			// store; only trust a non-empty vector result, else fall through.
			if verr == nil && len(mems) > 0 {
				return mems
			}
		} else {
			e.debugf("recall.embed_error", map[string]any{"agent_id": agentID})
		}
	}

	mems, err := e.store.Recall(ctx, memory.RecallOpts{
		AgentID:    agentID,
		Query:      queryText,
		MemoryType: memory.MemoryTypeFailureRecovery,
		Tags:       []string{tagFailureRecovery},
		Owner:      owner,
		SharedOnly: sharedOnly,
		Limit:      k,
	})
	if err != nil {
		return nil
	}
	e.debugf("recall.fts", map[string]any{
		"agent_id": agentID,
		"owner":    owner,
		"top_k":    k,
		"raw":      len(mems),
	})
	if len(mems) > k {
		mems = mems[:k]
	}
	return mems
}

// collectHints extracts the solution text from matches and refreshes their
// access counters (reuse signal, spec §15).
func (e *Engine) collectHints(ctx context.Context, matches []*memory.Memory) []string {
	hints := make([]string, 0, len(matches))
	ids := make([]string, 0, len(matches))
	seen := make(map[string]bool)
	for _, m := range matches {
		if m == nil {
			continue
		}
		hint := strings.TrimSpace(m.Summary)
		if hint == "" || seen[hint] {
			continue
		}
		seen[hint] = true
		hints = append(hints, hint)
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		_ = e.store.TouchMemories(ctx, ids)
	}
	return hints
}

// collectPitfalls pairs each match's problem (Content) with its fix (Summary)
// for the proactive hook, so the injected guidance shows BOTH what tends to go
// wrong and the verified correction. Seeing the problem lets the model detect
// when the current request (or its own plan) matches a known mistake — e.g. a
// user naming a tool that a lesson says is wrong — instead of treating the fix
// as a floating tip. Reactive keeps the solution-only collectHints, since there
// the failure has already happened and the fix is all that's needed.
func (e *Engine) collectPitfalls(ctx context.Context, matches []*memory.Memory) []string {
	pitfalls := make([]string, 0, len(matches))
	ids := make([]string, 0, len(matches))
	seen := make(map[string]bool)
	for _, m := range matches {
		if m == nil {
			continue
		}
		problem := strings.TrimSpace(m.Content)
		fix := strings.TrimSpace(m.Summary)
		if problem == "" && fix == "" {
			continue
		}
		var line string
		switch {
		case problem != "" && fix != "":
			line = "Known problem: " + problem + " Known fix: " + fix
		case problem != "":
			line = "Known problem: " + problem
		default:
			line = "Known fix: " + fix
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		pitfalls = append(pitfalls, line)
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		_ = e.store.TouchMemories(ctx, ids)
	}
	return pitfalls
}

// capHints trims a result set to MaxHints, preserving order (recall returns most
// relevant first). Also sorts stably by salience as a tiebreak so hotter lessons
// win (implicit hot ranking; no explicit tiers, spec discussion).
func (e *Engine) capHints(mems []*memory.Memory) []*memory.Memory {
	sort.SliceStable(mems, func(i, j int) bool {
		return mems[i].Salience > mems[j].Salience
	})
	if len(mems) > e.cfg.MaxHints {
		mems = mems[:e.cfg.MaxHints]
	}
	return mems
}

// tagFilter builds the tag set for an FTS5 recall.
func tagFilter(class, tool string) []string {
	tags := []string{tagFailureRecovery}
	if class != "" {
		tags = append(tags, "class:"+class)
	}
	if tool != "" {
		tags = append(tags, "tool:"+tool)
	}
	return tags
}

// filterLessons keeps only failure_recovery records matching the requested
// class/tool scope. Needed after VectorRecall, which ranks across all embeddings
// and does not filter by type or tag.
func filterLessons(mems []*memory.Memory, class, tool string) []*memory.Memory {
	out := make([]*memory.Memory, 0, len(mems))
	for _, m := range mems {
		if m == nil || m.MemoryType != memory.MemoryTypeFailureRecovery {
			continue
		}
		if class != "" && !hasTag(m.Tags, "class:"+class) {
			continue
		}
		if tool != "" && !hasTag(m.Tags, "tool:"+tool) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func emit(sink eventSink, name string, attrs map[string]interface{}) {
	if sink != nil {
		sink.AddEvent(name, attrs)
	}
}
