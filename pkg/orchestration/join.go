// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/collaboration"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

// A hand-off carries several agents' outputs into one prompt that a single
// consumer receives: the join step of a fan-out pattern (fork-join,
// parallel), a pipeline stage reading {{history}}, a debate agent answering
// the other positions, the debate moderator writing the synthesis. Left
// unbounded, that prompt grows with the producers' verbosity until the
// provider refuses it, after every producer has been paid for.
//
// This file is the bounded hand-off: full outputs go to SharedMemory, the
// inline copy is capped per entry and budgeted as a whole, and the consumer
// may be a real agent that can fetch any entry back. The joiner type runs
// the fan-out join end to end (run); pipeline history and the debate engine
// use its store/cap/fit core (branchesFor, fit) with their own rendering.

const (
	// DefaultJoinBranchBytes is the per-branch inline cap applied when
	// JoinPolicy.max_branch_bytes is 0. It matches the pipeline stage
	// hand-off (MaxStageOutputBytes).
	DefaultJoinBranchBytes = MaxStageOutputBytes

	// DefaultJoinInputBudgetPercent is the share of the receiving agent's
	// usable context window (max_context_tokens less reserved output
	// tokens) that the rendered hand-off may take when max_input_tokens is
	// 0. The rest is left for the agent's system prompt, tool definitions
	// and the tool calls it makes during its turn.
	DefaultJoinInputBudgetPercent = 50

	// MinJoinBranchShareTokens is the smallest per-entry token share the
	// overflow strategies will work with. A budget that leaves less than
	// this per entry is an error: a card that small carries nothing.
	MinJoinBranchShareTokens = 64

	// joinShareSafetyPercent discounts the computed per-entry share so the
	// re-rendered input lands under the budget even though token counts are
	// not exactly additive across concatenated sections.
	joinShareSafetyPercent = 95

	joinTruncatedNotice      = "[Branch output truncated: %d of %d bytes shown. Full output in SharedMemory: shared_memory_read(namespace=\"workflow\", key=\"%s\")]"
	joinTruncatedNoMemNotice = "[Branch output truncated: %d of %d bytes shown. SharedMemory is not configured, so the rest is not retrievable.]"
	joinReducedNotice        = "[Branch output reduced from %d bytes by the compressor LLM. Full output in SharedMemory: shared_memory_read(namespace=\"workflow\", key=\"%s\")]"
	joinReducedNoMemNotice   = "[Branch output reduced from %d bytes by the compressor LLM. SharedMemory is not configured, so the full text is not retrievable.]"

	// Stage-style notices continue the pipeline's own wording
	// (StageOutputTruncationNoticeTemplate) for history entries.
	stageTruncatedNoMemNotice = "\n\n[OUTPUT TRUNCATED: %d of %d bytes shown - SharedMemory is not configured, so the rest is not retrievable]"
	stageReducedNotice        = "\n\n[OUTPUT REDUCED from %d bytes by the compressor LLM - Full data stored in SharedMemory. Use shared_memory_read(namespace=\"workflow\", key=\"%s\") to fetch complete output]"
	stageReducedNoMemNotice   = "\n\n[OUTPUT REDUCED from %d bytes by the compressor LLM - SharedMemory is not configured, so the full text is not retrievable]"
)

// ErrJoinInputOverBudget is returned when the rendered hand-off exceeds
// max_input_tokens and overflow_strategy is FAIL, or when the budget leaves
// fewer than MinJoinBranchShareTokens per entry so no overflow strategy can
// make the input fit.
var ErrJoinInputOverBudget = errors.New("join input exceeds token budget")

// joinBranchAction names what happened to an entry's inline copy.
type joinBranchAction string

const (
	joinBranchFull      joinBranchAction = "full"
	joinBranchTruncated joinBranchAction = "truncated"
	joinBranchReduced   joinBranchAction = "reduced"
)

// joinNoticeStyle selects the wording of the notice an entry carries when
// its inline copy is not the full text.
type joinNoticeStyle int

const (
	// noticeStyleJoin: "[Branch output truncated: ...]", for fan-out joins
	// and debate hand-offs. Rendered on its own line after the inline copy.
	noticeStyleJoin joinNoticeStyle = iota
	// noticeStyleStage: the pipeline stage wording ("\n\n[OUTPUT TRUNCATED
	// - Full data stored in SharedMemory ...]"), appended to the inline
	// copy, so a history entry reads exactly like {{previous}} does.
	noticeStyleStage
)

// handoffEntry is one producer output as handed to the joiner.
type handoffEntry struct {
	index   int    // 1-based position in the hand-off
	agentID string // producer
	full    string
	memKey  string // SharedMemory key holding full; "" when not stored
	failed  bool   // an error result: rendered as is, never stored or cut
}

// joinBranch is one hand-off entry as the consumer sees it.
type joinBranch struct {
	index   int // 1-based, configured order
	agentID string
	full    string
	inline  string
	memKey  string // SharedMemory key holding full; "" when not stored
	action  joinBranchAction
	failed  bool
}

// joinOutcome is what the join step produced, for the executor's
// WorkflowResult: the output, the usage of every LLM call the join made
// itself (the join turn and any pre-reduce calls) and operator metadata.
type joinOutcome struct {
	Output   string
	AgentID  string // join agent; "" on the raw merge-LLM path
	Model    string // model behind the join call; "" when unknown
	Usage    types.Usage
	LLMCalls int32
	Metadata map[string]string
}

// handoffFit is a rendered hand-off brought within its budget: the prompt
// to send, how it compares to the budget, what the overflow strategy did to
// the entries and what that cost.
type handoffFit struct {
	Rendered    string
	InputTokens int
	Budget      int    // 0 when no budget applied
	Overflow    string // none | pre_reduce | truncate
	Usage       types.Usage
	LLMCalls    int32
	Stored      int // entries whose full text is in SharedMemory
	Truncated   int // entries whose inline copy was cut
	Reduced     int // entries replaced by an LLM-reduced card
}

// metadata renders the fit for result metadata and span attributes under
// prefix (e.g. "join") with unit naming the entries (e.g. "branches").
func (f *handoffFit) metadata(prefix, unit string) map[string]string {
	return map[string]string{
		prefix + ".input_tokens":           strconv.Itoa(f.InputTokens),
		prefix + ".budget_tokens":          strconv.Itoa(f.Budget),
		prefix + ".overflow":               f.Overflow,
		prefix + "." + unit + "_stored":    strconv.Itoa(f.Stored),
		prefix + "." + unit + "_truncated": strconv.Itoa(f.Truncated),
		prefix + "." + unit + "_reduced":   strconv.Itoa(f.Reduced),
	}
}

// joiner bounds a hand-off under a JoinPolicy and, for fan-out patterns,
// performs the join step itself.
type joiner struct {
	orch       *Orchestrator
	policy     *loomv1.JoinPolicy
	strategy   loomv1.MergeStrategy
	patternKey string // "fork-join" | "parallel" | "pipeline" | "debate": store metadata, logs
	workflowID string
	prompt     string // the task the hand-off serves (fan-out prompt, debate topic); "" when none
	scope      string // names this hand-off in reduce sessions and logs: "join", "stage-3-history", ...
	storeType  string // SharedMemory metadata "type" for stored entries
	notice     joinNoticeStyle
	counter    *agent.TokenCounter
}

func newJoiner(orch *Orchestrator, policy *loomv1.JoinPolicy, strategy loomv1.MergeStrategy, patternKey, workflowID, prompt string) *joiner {
	return &joiner{
		orch:       orch,
		policy:     policy,
		strategy:   strategy,
		patternKey: patternKey,
		workflowID: workflowID,
		prompt:     prompt,
		scope:      "join",
		storeType:  "branch_output",
		notice:     noticeStyleJoin,
		counter:    agent.GetTokenCounter(),
	}
}

// joinPolicyActive reports whether a JoinPolicy changes the join of this
// strategy. A policy with a join agent always does: the agent produces the
// output whatever the strategy says. Without an agent the policy only bounds
// the LLM-merge strategies; CONCATENATE and FIRST build the workflow's final
// output from the branch text itself, which must not be cut.
func joinPolicyActive(policy *loomv1.JoinPolicy, strategy loomv1.MergeStrategy) bool {
	if policy == nil {
		return false
	}
	if policy.AgentId != "" {
		return true
	}
	switch strategy {
	case loomv1.MergeStrategy_CONSENSUS, loomv1.MergeStrategy_SUMMARY,
		loomv1.MergeStrategy_VOTING, loomv1.MergeStrategy_BEST:
		return true
	default:
		return false
	}
}

// run performs the bounded join and returns its outcome.
func (j *joiner) run(ctx context.Context, results []*loomv1.AgentResult) (*joinOutcome, error) {
	var joinAgent *agent.Agent
	if j.policy.AgentId != "" {
		ag, err := j.orch.GetAgent(ctx, j.policy.AgentId)
		if err != nil {
			return nil, fmt.Errorf("join agent not found: %s: %w", j.policy.AgentId, err)
		}
		joinAgent = ag
	}

	branches := j.collectBranches(ctx, results)
	tmpl := j.template()
	withHeader := joinAgent != nil
	fit, err := j.fit(ctx, joinAgent, branches, j.budget(joinAgent), func(bs []joinBranch) string {
		return j.render(tmpl, bs, withHeader)
	})
	if err != nil {
		return nil, err
	}

	outcome := &joinOutcome{
		Usage:    fit.Usage,
		LLMCalls: fit.LLMCalls,
		Metadata: fit.metadata("join", "branches"),
	}

	output, model, usage, calls, err := j.execute(ctx, joinAgent, fit.Rendered)
	if err != nil {
		return nil, err
	}
	outcome.Output = output
	outcome.Model = model
	outcome.Usage = addUsage(outcome.Usage, usage)
	outcome.LLMCalls += calls
	if joinAgent != nil {
		outcome.AgentID = j.policy.AgentId
		outcome.Metadata["join.mode"] = "agent"
		outcome.Metadata["join.agent_id"] = j.policy.AgentId
	} else {
		outcome.Metadata["join.mode"] = "merge_llm"
	}
	return outcome, nil
}

// fit renders the hand-off and, when a budget applies and the rendering
// exceeds it, brings the entries under budget with the policy's overflow
// strategy and renders again. receiver is the agent that will get the
// prompt; its compressor-role LLM does the pre-reduce (the orchestrator
// merge LLM when receiver is nil). budget 0 means no check.
func (j *joiner) fit(ctx context.Context, receiver *agent.Agent, branches []joinBranch, budget int, render func([]joinBranch) string) (*handoffFit, error) {
	fit := &handoffFit{Budget: budget, Overflow: "none"}
	fit.Rendered = render(branches)
	fit.InputTokens = j.counter.CountTokens(fit.Rendered)

	if budget > 0 && fit.InputTokens > budget {
		strategy := j.policy.OverflowStrategy
		if strategy == loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_UNSPECIFIED {
			strategy = loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_PRE_REDUCE
		}
		switch strategy {
		case loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL:
			return nil, fmt.Errorf("%w: %s: %d tokens rendered for %d entries, budget %d; overflow_strategy is fail",
				ErrJoinInputOverBudget, j.scope, fit.InputTokens, len(branches), budget)
		case loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE:
			fit.Overflow = "truncate"
			if err := j.fitByTruncation(branches, fit.Rendered, budget); err != nil {
				return nil, err
			}
		case loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_PRE_REDUCE:
			fit.Overflow = "pre_reduce"
			usage, calls, err := j.fitByReduction(ctx, receiver, branches, fit.Rendered, budget)
			if err != nil {
				return nil, err
			}
			fit.Usage = addUsage(fit.Usage, usage)
			fit.LLMCalls += calls
		default:
			return nil, fmt.Errorf("unsupported join overflow strategy: %s", strategy)
		}
		fit.Rendered = render(branches)
		fit.InputTokens = j.counter.CountTokens(fit.Rendered)
		if fit.InputTokens > budget {
			// The share discount should make this unreachable; if a
			// tokenizer quirk gets here, say so rather than hide it.
			j.orch.logger.Warn("Hand-off still over budget after overflow handling; sending anyway",
				zap.String("workflow_id", j.workflowID),
				zap.String("scope", j.scope),
				zap.String("overflow", fit.Overflow),
				zap.Int("input_tokens", fit.InputTokens),
				zap.Int("budget_tokens", budget))
		} else {
			j.orch.logger.Info("Hand-off bounded to budget",
				zap.String("workflow_id", j.workflowID),
				zap.String("scope", j.scope),
				zap.String("overflow", fit.Overflow),
				zap.Int("input_tokens", fit.InputTokens),
				zap.Int("budget_tokens", budget))
		}
	}

	for _, b := range branches {
		if b.memKey != "" {
			fit.Stored++
		}
		switch b.action {
		case joinBranchTruncated:
			fit.Truncated++
		case joinBranchReduced:
			fit.Reduced++
		case joinBranchFull:
		}
	}
	return fit, nil
}

// collectBranches turns the fan-out results into join branches: the full
// output is stored in SharedMemory when the orchestrator has one, and the
// inline copy is cut to the per-branch byte cap.
func (j *joiner) collectBranches(ctx context.Context, results []*loomv1.AgentResult) []joinBranch {
	entries := make([]handoffEntry, 0, len(results))
	for i, r := range results {
		if r == nil {
			continue
		}
		entries = append(entries, handoffEntry{
			index:   i + 1,
			agentID: r.AgentId,
			full:    r.Output,
			failed:  r.Metadata["error"] != "",
		})
	}
	for i := range entries {
		e := &entries[i]
		if e.failed || j.orch.sharedMemory == nil {
			continue
		}
		key := fmt.Sprintf("%s-branch-%d-output", j.workflowID, e.index)
		if err := j.store(ctx, key, e.agentID, e.index, e.full); err != nil {
			j.orch.logger.Warn("failed to store branch output in SharedMemory; join gets the inline copy only",
				zap.String("workflow_id", j.workflowID),
				zap.Int("branch", e.index),
				zap.Error(err))
			continue
		}
		e.memKey = key
	}
	return j.branchesFor(entries, true)
}

// branchesFor builds the branches of a hand-off whose entries are already
// stored (or not) by the caller, applying the per-entry byte cap. With
// cutUnstored false an entry whose full text is not in SharedMemory is
// passed in full: the pipeline rule, where nothing is cut that the next
// stage could not fetch back. Failed entries are never cut.
func (j *joiner) branchesFor(entries []handoffEntry, cutUnstored bool) []joinBranch {
	maxBytes := int(j.policy.GetMaxBranchBytes())
	if maxBytes <= 0 {
		maxBytes = DefaultJoinBranchBytes
	}
	branches := make([]joinBranch, 0, len(entries))
	for _, e := range entries {
		b := joinBranch{
			index:   e.index,
			agentID: e.agentID,
			full:    e.full,
			inline:  e.full,
			memKey:  e.memKey,
			action:  joinBranchFull,
			failed:  e.failed,
		}
		if !b.failed && (b.memKey != "" || cutUnstored) && len(b.full) > maxBytes {
			b.inline = cutAtLineBoundary(b.full, maxBytes)
			b.action = joinBranchTruncated
		}
		branches = append(branches, b)
	}
	return branches
}

// store stashes one entry's full text in the WORKFLOW namespace.
func (j *joiner) store(ctx context.Context, key, agentID string, index int, full string) error {
	if j.orch.sharedMemory == nil {
		return fmt.Errorf("SharedMemory not available")
	}
	_, err := j.orch.sharedMemory.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		Key:       key,
		Value:     []byte(full),
		AgentId:   agentID,
		Metadata: map[string]string{
			"type":        j.storeType,
			"pattern":     j.patternKey,
			"workflow_id": j.workflowID,
			"branch":      strconv.Itoa(index),
			"agent_id":    agentID,
			"full_size":   strconv.Itoa(len(full)),
		},
	})
	return err
}

// template is the join prompt template: the policy's, or the strategy's
// default when the policy leaves it empty.
func (j *joiner) template() string {
	if strings.TrimSpace(j.policy.PromptTemplate) != "" {
		return j.policy.PromptTemplate
	}
	return defaultJoinTemplate(j.strategy, j.prompt != "")
}

// defaultJoinTemplate mirrors the raw merge prompts of the LLM strategies,
// with {{branches}} in place of the inline outputs. CONCATENATE, FIRST and
// UNSPECIFIED only reach here with a join agent, which gets a plain
// combine instruction.
func defaultJoinTemplate(strategy loomv1.MergeStrategy, withPrompt bool) string {
	var b strings.Builder
	if withPrompt {
		b.WriteString("Original prompt: {{prompt}}\n\n")
	}
	switch strategy {
	case loomv1.MergeStrategy_CONSENSUS:
		b.WriteString("The following {{branch_count}} agents provided responses:\n\n{{branches}}\n")
		b.WriteString("Synthesize these perspectives into a consensus view that incorporates the strongest points from each agent.")
	case loomv1.MergeStrategy_VOTING:
		b.WriteString("Review these {{branch_count}} responses and select the most compelling answer:\n\n{{branches}}\n")
		b.WriteString("Identify which option is most convincing and explain why.")
	case loomv1.MergeStrategy_SUMMARY:
		b.WriteString("Summarize these {{branch_count}} agent responses concisely:\n\n{{branches}}\n")
		b.WriteString("Provide a concise summary highlighting key points and conclusions.")
	case loomv1.MergeStrategy_BEST:
		b.WriteString("Evaluate these {{branch_count}} responses and select the highest quality one:\n\n{{branches}}\n")
		b.WriteString("Select and return the best response based on clarity, accuracy, and depth.")
	case loomv1.MergeStrategy_CONCATENATE, loomv1.MergeStrategy_FIRST,
		loomv1.MergeStrategy_MERGE_STRATEGY_UNSPECIFIED:
		if withPrompt {
			b.WriteString("Combine the following {{branch_count}} branch outputs into one coherent result that answers the original prompt:\n\n{{branches}}")
		} else {
			b.WriteString("Combine the following {{branch_count}} task outputs into one coherent result:\n\n{{branches}}")
		}
	}
	return b.String()
}

// budget is the token budget for the rendered hand-off: the policy's
// explicit value, else a share of the receiving agent's usable context
// window, else 0 (no check) when there is no agent to derive it from.
func (j *joiner) budget(receiver *agent.Agent) int {
	if j.policy.GetMaxInputTokens() > 0 {
		return int(j.policy.MaxInputTokens)
	}
	if receiver == nil {
		return 0
	}
	defaults := agent.DefaultTokenBudgetConfig()
	maxCtx, reserved := defaults.MaxContextTokens, defaults.ReservedOutputTokens
	if cfg := receiver.GetConfig(); cfg != nil {
		if cfg.MaxContextTokens > 0 {
			maxCtx = cfg.MaxContextTokens
		}
		if cfg.ReservedOutputTokens > 0 {
			reserved = cfg.ReservedOutputTokens
		}
	}
	usable := maxCtx - reserved
	if usable <= 0 {
		return 0
	}
	return usable * DefaultJoinInputBudgetPercent / 100
}

// render fills the template (collaboration.SpliceTemplate: {{prompt}} and
// {{branch_count}} first, {{branches}} last by splicing, sections appended
// when the template has no {{branches}}) and, for a join agent, prepends the
// SharedMemory key header.
func (j *joiner) render(tmpl string, branches []joinBranch, withHeader bool) string {
	body := collaboration.SpliceTemplate(tmpl, j.prompt, j.renderJoinBranches(branches), len(branches))
	if !withHeader {
		return body
	}
	return collaboration.SharedMemoryHeader(j.pieces(branches), "branches", j.workflowID) + body
}

// pieces renders the branches in the shape collaboration callers receive:
// the inline copy, its notice and the key that holds the full text.
func (j *joiner) pieces(branches []joinBranch) []collaboration.HandoffPiece {
	out := make([]collaboration.HandoffPiece, len(branches))
	for i := range branches {
		b := &branches[i]
		out[i] = collaboration.HandoffPiece{
			Index:    b.index,
			AgentID:  b.agentID,
			Inline:   b.inline,
			Notice:   j.noticeFor(b, b.action),
			Key:      b.memKey,
			FullSize: len(b.full),
		}
	}
	return out
}

// renderJoinBranches renders one section per branch: a heading, the inline
// copy and, when that copy is not the full output, the notice that says so
// and names the SharedMemory key.
func (j *joiner) renderJoinBranches(branches []joinBranch) string {
	var b strings.Builder
	for i := range branches {
		br := &branches[i]
		if br.failed {
			b.WriteString(fmt.Sprintf("### Branch %d: agent %s (FAILED)\n%s\n\n", br.index, br.agentID, br.inline))
			continue
		}
		b.WriteString(fmt.Sprintf("### Branch %d: agent %s\n%s\n", br.index, br.agentID, br.inline))
		if notice := j.noticeFor(br, br.action); notice != "" {
			b.WriteString(notice)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// branchShare is the per-entry token share under a budget: the budget less
// everything that is not entry text, divided among the entries, with the
// safety discount. It errors when the share is too small to carry anything.
func (j *joiner) branchShare(branches []joinBranch, rendered string, budget int) (int, error) {
	if len(branches) == 0 {
		return 0, fmt.Errorf("%w: %s: nothing to fit", ErrJoinInputOverBudget, j.scope)
	}
	inlineTokens := 0
	for _, b := range branches {
		inlineTokens += j.counter.CountTokens(b.inline)
	}
	overhead := j.counter.CountTokens(rendered) - inlineTokens
	if overhead < 0 {
		overhead = 0
	}
	share := (budget - overhead) / len(branches) * joinShareSafetyPercent / 100
	if share < MinJoinBranchShareTokens {
		return 0, fmt.Errorf("%w: %s: budget %d leaves %d tokens per entry for %d entries (template and headers take %d); at least %d is required",
			ErrJoinInputOverBudget, j.scope, budget, share, len(branches), overhead, MinJoinBranchShareTokens)
	}
	return share, nil
}

// fitByTruncation cuts every entry over its share down to it. No LLM calls.
func (j *joiner) fitByTruncation(branches []joinBranch, rendered string, budget int) error {
	share, err := j.branchShare(branches, rendered, budget)
	if err != nil {
		return err
	}
	for i := range branches {
		j.truncateBranchToShare(&branches[i], share)
	}
	return nil
}

// truncateBranchToShare makes an entry's inline copy fit its share, notice
// included, when it does not already.
func (j *joiner) truncateBranchToShare(b *joinBranch, share int) {
	if b.failed {
		return
	}
	noticeTokens := j.counter.CountTokens(j.noticeFor(b, joinBranchTruncated))
	if j.counter.CountTokens(b.inline)+noticeTokens <= share {
		return
	}
	b.inline = j.truncateToTokens(b.full, share-noticeTokens)
	b.action = joinBranchTruncated
}

// noticeFor renders the notice an entry would carry under an action, in the
// joiner's notice style. Empty for a full copy.
func (j *joiner) noticeFor(b *joinBranch, action joinBranchAction) string {
	switch j.notice {
	case noticeStyleStage:
		switch action {
		case joinBranchReduced:
			if b.memKey != "" {
				return fmt.Sprintf(stageReducedNotice, len(b.full), b.memKey)
			}
			return fmt.Sprintf(stageReducedNoMemNotice, len(b.full))
		case joinBranchTruncated:
			if b.memKey != "" {
				return fmt.Sprintf(StageOutputTruncationNoticeTemplate, b.memKey)
			}
			return fmt.Sprintf(stageTruncatedNoMemNotice, len(b.inline), len(b.full))
		case joinBranchFull:
		}
		return ""
	default:
		switch action {
		case joinBranchReduced:
			if b.memKey != "" {
				return fmt.Sprintf(joinReducedNotice, len(b.full), b.memKey)
			}
			return fmt.Sprintf(joinReducedNoMemNotice, len(b.full))
		case joinBranchTruncated:
			if b.memKey != "" {
				return fmt.Sprintf(joinTruncatedNotice, len(b.inline), len(b.full), b.memKey)
			}
			return fmt.Sprintf(joinTruncatedNoMemNotice, len(b.inline), len(b.full))
		case joinBranchFull:
		}
		return ""
	}
}

// fitByReduction asks an LLM to reduce every entry over its share to a card
// of at most that share, in parallel under the orchestrator LLM semaphore,
// then truncates anything that still does not fit. The reduce LLM is the
// receiving agent's compressor role (its main LLM when none is configured),
// else the orchestrator merge LLM; with neither it falls back to truncation.
func (j *joiner) fitByReduction(ctx context.Context, receiver *agent.Agent, branches []joinBranch, rendered string, budget int) (types.Usage, int32, error) {
	share, err := j.branchShare(branches, rendered, budget)
	if err != nil {
		return types.Usage{}, 0, err
	}

	var reduceLLM agent.LLMProvider
	if receiver != nil {
		reduceLLM = receiver.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_COMPRESSOR)
	} else {
		reduceLLM = j.orch.GetMergeLLM()
	}
	if reduceLLM == nil {
		j.orch.logger.Warn("no LLM available to pre-reduce hand-off; truncating instead",
			zap.String("workflow_id", j.workflowID),
			zap.String("scope", j.scope))
		for i := range branches {
			j.truncateBranchToShare(&branches[i], share)
		}
		return types.Usage{}, 0, nil
	}

	var oversize []int
	for i := range branches {
		b := &branches[i]
		if b.failed {
			continue
		}
		if j.counter.CountTokens(b.inline)+j.counter.CountTokens(j.noticeFor(b, joinBranchReduced)) > share {
			oversize = append(oversize, i)
		}
	}
	if len(oversize) == 0 {
		return types.Usage{}, 0, nil
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		usage types.Usage
		calls int32
	)
	for _, idx := range oversize {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := &branches[i]
			reduced, u, err := j.reduceOne(ctx, reduceLLM, b, share)
			if err != nil {
				j.orch.logger.Warn("entry pre-reduce failed; truncating that entry instead",
					zap.String("workflow_id", j.workflowID),
					zap.String("scope", j.scope),
					zap.Int("entry", b.index),
					zap.Error(err))
				j.truncateBranchToShare(b, share)
				return
			}
			mu.Lock()
			usage = addUsage(usage, u)
			calls++
			mu.Unlock()
			b.action = joinBranchReduced
			noticeTokens := j.counter.CountTokens(j.noticeFor(b, joinBranchReduced))
			if j.counter.CountTokens(reduced)+noticeTokens > share {
				reduced = j.truncateToTokens(reduced, share-noticeTokens)
			}
			b.inline = reduced
		}(idx)
	}
	wg.Wait()
	return usage, calls, nil
}

// reduceOne runs one reduce call for an entry. It holds the orchestrator LLM
// semaphore for the call when one is configured.
func (j *joiner) reduceOne(ctx context.Context, llm agent.LLMProvider, b *joinBranch, share int) (string, types.Usage, error) {
	if sem := j.orch.llmSemaphore; sem != nil {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return "", types.Usage{}, ctx.Err()
		}
	}
	// Roughly three words per four tokens; the share is a token bound.
	words := share * 3 / 4
	var p strings.Builder
	p.WriteString(fmt.Sprintf("Reduce the following output from agent %s to at most %d words so it fits a bounded hand-off to the agent that consumes it.\n", b.agentID, words))
	p.WriteString("Keep every number, name, decision and conclusion; drop repetition, preamble and anything the consumer does not need.\n")
	if j.prompt != "" {
		p.WriteString("The hand-off serves this task: ")
		p.WriteString(j.prompt)
		p.WriteString("\n")
	}
	p.WriteString("Output only the reduced text, nothing else.\n\n--- OUTPUT TO REDUCE ---\n")
	p.WriteString(b.full)

	sessionID := fmt.Sprintf("%s-%s-reduce-%d", j.workflowID, j.scope, b.index)
	resp, err := llm.Chat(j.mergeContext(ctx, sessionID), []agent.Message{{
		Role:      "user",
		Content:   p.String(),
		Timestamp: time.Now(),
	}}, nil)
	if err != nil {
		return "", types.Usage{}, err
	}
	reduced := strings.TrimSpace(resp.Content)
	if reduced == "" {
		return "", resp.Usage, fmt.Errorf("reduce LLM returned empty content")
	}
	return reduced, resp.Usage, nil
}

// execute runs the join call: the join agent's turn, or one raw merge-LLM
// call when no agent is configured. The call count of an agent turn is the
// turn's own successful LLM call count (one for a plain completion, more
// when the agent ran a tool loop, zero when the turn made none).
func (j *joiner) execute(ctx context.Context, joinAgent *agent.Agent, prompt string) (string, string, types.Usage, int32, error) {
	if joinAgent != nil {
		if sem := j.orch.llmSemaphore; sem != nil {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return "", "", types.Usage{}, 0, ctx.Err()
			}
		}
		sessionID := fmt.Sprintf("%s-join-%s", j.workflowID, j.policy.AgentId)
		resp, err := joinAgent.Chat(ctx, sessionID, prompt)
		if err != nil {
			return "", "", types.Usage{}, 0, fmt.Errorf("join agent %s failed: %w", j.policy.AgentId, err)
		}
		usage := resp.TurnUsage
		if usage.TotalTokens == 0 && usage.CostUSD == 0 {
			// Providers that do not fill TurnUsage report the turn's usage
			// in Usage.
			usage = resp.Usage
		}
		return resp.Content, joinAgent.GetLLMModel(), usage, types.SafeInt32(resp.TurnLLMCalls), nil
	}

	mergeLLM := j.orch.GetMergeLLM()
	if mergeLLM == nil {
		return "", "", types.Usage{}, 0, fmt.Errorf("LLM provider required for %s merge strategy (configure orchestrator LLM or agent orchestrator role LLM)", j.strategy)
	}
	sessionID := fmt.Sprintf("%s-merge-%s", j.workflowID, j.strategy.String())
	resp, err := mergeLLM.Chat(j.mergeContext(ctx, sessionID), []agent.Message{{
		Role:      "user",
		Content:   prompt,
		Timestamp: time.Now(),
	}}, nil)
	if err != nil {
		return "", "", types.Usage{}, 0, fmt.Errorf("LLM merge failed: %w", err)
	}
	return resp.Content, mergeLLM.Model(), resp.Usage, 1, nil
}

// mergeContext wraps ctx in the agent.Context shape the raw LLM path uses.
func (j *joiner) mergeContext(ctx context.Context, sessionID string) *mergeContext {
	now := time.Now()
	return &mergeContext{
		Context: ctx,
		session: &agent.Session{
			ID:        sessionID,
			Messages:  []agent.Message{},
			CreatedAt: now,
			UpdatedAt: now,
		},
		tracer: j.orch.tracer,
	}
}

// truncateToTokens returns the longest prefix of text, cut at a line
// boundary where one exists in the back half, that counts at most maxTokens.
func (j *joiner) truncateToTokens(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	tokens := j.counter.CountTokens(text)
	if tokens <= maxTokens {
		return text
	}
	for i := 0; i < 8 && tokens > maxTokens && len(text) > 0; i++ {
		cut := int(float64(len(text)) * float64(maxTokens) / float64(tokens) * float64(joinShareSafetyPercent) / 100)
		if cut >= len(text) {
			cut = len(text) - 1
		}
		if cut <= 0 {
			return ""
		}
		text = cutAtLineBoundary(text, cut)
		tokens = j.counter.CountTokens(text)
	}
	if tokens > maxTokens {
		// Eight proportional passes did not converge; hard cut without the
		// line-boundary preference.
		cut := len(text) * maxTokens / tokens
		text = trimToValidUTF8(text[:cut])
	}
	return text
}

// cutAtLineBoundary returns at most maxBytes of s, preferring the last line
// break in the back half, and never splits a UTF-8 sequence.
func cutAtLineBoundary(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	cut := s[:maxBytes]
	if nl := strings.LastIndex(cut, "\n"); nl > maxBytes/2 {
		cut = cut[:nl]
	}
	return trimToValidUTF8(cut)
}

// trimToValidUTF8 drops a trailing partial UTF-8 sequence.
func trimToValidUTF8(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// addUsage sums two usages field by field.
func addUsage(a, b types.Usage) types.Usage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.TotalTokens += b.TotalTokens
	a.CostUSD += b.CostUSD
	return a
}

// addJoinCost folds the join step's own LLM usage into a run's cost. The
// branch results already carry their own.
func addJoinCost(cost *loomv1.WorkflowCost, outcome *joinOutcome) {
	if cost == nil || outcome == nil || outcome.LLMCalls == 0 {
		return
	}
	cost.TotalCostUsd += outcome.Usage.CostUSD
	cost.TotalTokens += types.SafeInt32(outcome.Usage.TotalTokens)
	cost.LlmCalls += outcome.LLMCalls
	if outcome.AgentID != "" {
		if cost.AgentCostsUsd == nil {
			cost.AgentCostsUsd = make(map[string]float64)
		}
		cost.AgentCostsUsd[outcome.AgentID] += outcome.Usage.CostUSD
	}
}
