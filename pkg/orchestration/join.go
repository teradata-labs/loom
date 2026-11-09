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
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

// The join step of a fan-out pattern (fork-join, parallel) hands every
// branch output to one consumer. Without a JoinPolicy that hand-off is one
// raw merge-LLM call with every branch inline, and it fails on the provider's
// context limit once the branches are chatty enough, after every branch has
// been paid for. This file is the bounded alternative: full outputs go to
// SharedMemory, the inline copy is capped per branch and budgeted as a whole,
// and the join may run through a real agent that can fetch any branch back.

const (
	// DefaultJoinBranchBytes is the per-branch inline cap applied when
	// JoinPolicy.max_branch_bytes is 0. It matches the pipeline stage
	// hand-off (MaxStageOutputBytes).
	DefaultJoinBranchBytes = MaxStageOutputBytes

	// DefaultJoinInputBudgetPercent is the share of the join agent's usable
	// context window (max_context_tokens less reserved output tokens) that
	// the rendered join input may take when JoinPolicy.max_input_tokens is
	// 0. The rest is left for the agent's system prompt, tool definitions
	// and the tool calls it makes during the join.
	DefaultJoinInputBudgetPercent = 50

	// MinJoinBranchShareTokens is the smallest per-branch token share the
	// overflow strategies will work with. A budget that leaves less than
	// this per branch is an error: a card that small carries nothing.
	MinJoinBranchShareTokens = 64

	// joinShareSafetyPercent discounts the computed per-branch share so the
	// re-rendered input lands under the budget even though token counts are
	// not exactly additive across concatenated sections.
	joinShareSafetyPercent = 95

	joinPlaceholderPrompt      = "{{prompt}}"
	joinPlaceholderBranches    = "{{branches}}"
	joinPlaceholderBranchCount = "{{branch_count}}"

	joinTruncatedNotice      = "[Branch output truncated: %d of %d bytes shown. Full output in SharedMemory: shared_memory_read(namespace=\"workflow\", key=\"%s\")]"
	joinTruncatedNoMemNotice = "[Branch output truncated: %d of %d bytes shown. SharedMemory is not configured, so the rest is not retrievable.]"
	joinReducedNotice        = "[Branch output reduced from %d bytes by the compressor LLM. Full output in SharedMemory: shared_memory_read(namespace=\"workflow\", key=\"%s\")]"
	joinReducedNoMemNotice   = "[Branch output reduced from %d bytes by the compressor LLM. SharedMemory is not configured, so the full text is not retrievable.]"
)

// ErrJoinInputOverBudget is returned when the rendered join input exceeds
// JoinPolicy.max_input_tokens and overflow_strategy is FAIL, or when the
// budget leaves fewer than MinJoinBranchShareTokens per branch so no
// overflow strategy can make the input fit.
var ErrJoinInputOverBudget = errors.New("join input exceeds token budget")

// joinBranchAction names what happened to a branch's inline copy.
type joinBranchAction string

const (
	joinBranchFull      joinBranchAction = "full"
	joinBranchTruncated joinBranchAction = "truncated"
	joinBranchReduced   joinBranchAction = "reduced"
)

// joinBranch is one fan-out result as the join sees it.
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

// joiner performs the join step of a fan-out pattern under a JoinPolicy.
type joiner struct {
	orch       *Orchestrator
	policy     *loomv1.JoinPolicy
	strategy   loomv1.MergeStrategy
	patternKey string // "fork-join" | "parallel": span naming
	workflowID string
	prompt     string // fan-out prompt; "" for parallel
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
	budget := j.budget(joinAgent)
	withHeader := joinAgent != nil
	rendered := j.render(tmpl, branches, withHeader)
	inputTokens := j.counter.CountTokens(rendered)

	outcome := &joinOutcome{Metadata: map[string]string{}}
	overflow := "none"

	if budget > 0 && inputTokens > budget {
		strategy := j.policy.OverflowStrategy
		if strategy == loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_UNSPECIFIED {
			strategy = loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_PRE_REDUCE
		}
		switch strategy {
		case loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL:
			return nil, fmt.Errorf("%w: %d tokens rendered for %d branches, budget %d; overflow_strategy is fail",
				ErrJoinInputOverBudget, inputTokens, len(branches), budget)
		case loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE:
			overflow = "truncate"
			if err := j.fitByTruncation(branches, rendered, budget); err != nil {
				return nil, err
			}
		case loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_PRE_REDUCE:
			overflow = "pre_reduce"
			usage, calls, err := j.fitByReduction(ctx, joinAgent, branches, rendered, budget)
			if err != nil {
				return nil, err
			}
			outcome.Usage = addUsage(outcome.Usage, usage)
			outcome.LLMCalls += calls
		default:
			return nil, fmt.Errorf("unsupported join overflow strategy: %s", strategy)
		}
		rendered = j.render(tmpl, branches, withHeader)
		inputTokens = j.counter.CountTokens(rendered)
		if inputTokens > budget {
			// The share discount should make this unreachable; if a
			// tokenizer quirk gets here, say so rather than hide it.
			j.orch.logger.Warn("Join input still over budget after overflow handling; sending anyway",
				zap.String("workflow_id", j.workflowID),
				zap.String("overflow", overflow),
				zap.Int("input_tokens", inputTokens),
				zap.Int("budget_tokens", budget))
		} else {
			j.orch.logger.Info("Join input bounded to budget",
				zap.String("workflow_id", j.workflowID),
				zap.String("overflow", overflow),
				zap.Int("input_tokens", inputTokens),
				zap.Int("budget_tokens", budget))
		}
	}

	var stored, truncated, reduced int
	for _, b := range branches {
		if b.memKey != "" {
			stored++
		}
		switch b.action {
		case joinBranchTruncated:
			truncated++
		case joinBranchReduced:
			reduced++
		case joinBranchFull:
		}
	}
	outcome.Metadata["join.input_tokens"] = strconv.Itoa(inputTokens)
	outcome.Metadata["join.budget_tokens"] = strconv.Itoa(budget)
	outcome.Metadata["join.overflow"] = overflow
	outcome.Metadata["join.branches_stored"] = strconv.Itoa(stored)
	outcome.Metadata["join.branches_truncated"] = strconv.Itoa(truncated)
	outcome.Metadata["join.branches_reduced"] = strconv.Itoa(reduced)

	output, model, usage, calls, err := j.execute(ctx, joinAgent, rendered)
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

// collectBranches turns the fan-out results into join branches: the full
// output is stored in SharedMemory when the orchestrator has one, and the
// inline copy is cut to the per-branch byte cap.
func (j *joiner) collectBranches(ctx context.Context, results []*loomv1.AgentResult) []joinBranch {
	maxBytes := int(j.policy.MaxBranchBytes)
	if maxBytes <= 0 {
		maxBytes = DefaultJoinBranchBytes
	}
	branches := make([]joinBranch, 0, len(results))
	for i, r := range results {
		if r == nil {
			continue
		}
		b := joinBranch{
			index:   i + 1,
			agentID: r.AgentId,
			full:    r.Output,
			inline:  r.Output,
			action:  joinBranchFull,
			failed:  r.Metadata["error"] != "",
		}
		if !b.failed && j.orch.sharedMemory != nil {
			key := fmt.Sprintf("%s-branch-%d-output", j.workflowID, b.index)
			if err := j.storeBranch(ctx, key, b); err != nil {
				j.orch.logger.Warn("failed to store branch output in SharedMemory; join gets the inline copy only",
					zap.String("workflow_id", j.workflowID),
					zap.Int("branch", b.index),
					zap.Error(err))
			} else {
				b.memKey = key
			}
		}
		if !b.failed && len(b.full) > maxBytes {
			b.inline = cutAtLineBoundary(b.full, maxBytes)
			b.action = joinBranchTruncated
		}
		branches = append(branches, b)
	}
	return branches
}

// storeBranch stashes one branch's full output in the WORKFLOW namespace.
func (j *joiner) storeBranch(ctx context.Context, key string, b joinBranch) error {
	_, err := j.orch.sharedMemory.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		Key:       key,
		Value:     []byte(b.full),
		AgentId:   b.agentID,
		Metadata: map[string]string{
			"type":        "branch_output",
			"pattern":     j.patternKey,
			"workflow_id": j.workflowID,
			"branch":      strconv.Itoa(b.index),
			"agent_id":    b.agentID,
			"full_size":   strconv.Itoa(len(b.full)),
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

// budget is the token budget for the rendered join input: the policy's
// explicit value, else a share of the join agent's usable context window,
// else 0 (no check) when there is no agent to derive it from.
func (j *joiner) budget(joinAgent *agent.Agent) int {
	if j.policy.MaxInputTokens > 0 {
		return int(j.policy.MaxInputTokens)
	}
	if joinAgent == nil {
		return 0
	}
	defaults := agent.DefaultTokenBudgetConfig()
	maxCtx, reserved := defaults.MaxContextTokens, defaults.ReservedOutputTokens
	if cfg := joinAgent.GetConfig(); cfg != nil {
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

// render fills the template. {{prompt}} and {{branch_count}} are replaced
// first; {{branches}} last and by splicing, so placeholder-like text inside a
// branch output is never re-scanned. A template without {{branches}} gets the
// sections appended, so the join never runs blind.
func (j *joiner) render(tmpl string, branches []joinBranch, withHeader bool) string {
	body := strings.ReplaceAll(tmpl, joinPlaceholderPrompt, j.prompt)
	body = strings.ReplaceAll(body, joinPlaceholderBranchCount, strconv.Itoa(len(branches)))
	sections := renderJoinBranches(branches)
	if !strings.Contains(body, joinPlaceholderBranches) {
		body = body + "\n\n" + sections
	} else {
		parts := strings.Split(body, joinPlaceholderBranches)
		body = strings.Join(parts, sections)
	}
	if !withHeader {
		return body
	}
	return sharedMemoryJoinHeader(branches) + body
}

// renderJoinBranches renders one section per branch: a heading, the inline
// copy and, when that copy is not the full output, the notice that says so
// and names the SharedMemory key.
func renderJoinBranches(branches []joinBranch) string {
	var b strings.Builder
	for _, br := range branches {
		if br.failed {
			b.WriteString(fmt.Sprintf("### Branch %d: agent %s (FAILED)\n%s\n\n", br.index, br.agentID, br.inline))
			continue
		}
		b.WriteString(fmt.Sprintf("### Branch %d: agent %s\n%s\n", br.index, br.agentID, br.inline))
		switch br.action {
		case joinBranchTruncated:
			if br.memKey != "" {
				b.WriteString(fmt.Sprintf(joinTruncatedNotice, len(br.inline), len(br.full), br.memKey))
			} else {
				b.WriteString(fmt.Sprintf(joinTruncatedNoMemNotice, len(br.inline), len(br.full)))
			}
			b.WriteString("\n")
		case joinBranchReduced:
			if br.memKey != "" {
				b.WriteString(fmt.Sprintf(joinReducedNotice, len(br.full), br.memKey))
			} else {
				b.WriteString(fmt.Sprintf(joinReducedNoMemNotice, len(br.full)))
			}
			b.WriteString("\n")
		case joinBranchFull:
		}
		b.WriteString("\n")
	}
	return b.String()
}

// sharedMemoryJoinHeader lists the SharedMemory keys holding full branch
// outputs, for a join agent that can call shared_memory_read. Empty when no
// branch was stored.
func sharedMemoryJoinHeader(branches []joinBranch) string {
	stored := 0
	for _, b := range branches {
		if b.memKey != "" {
			stored++
		}
	}
	if stored == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## AVAILABLE CONTEXT (SharedMemory)\n")
	b.WriteString(fmt.Sprintf("Full outputs of %d of %d branches are stored in SharedMemory namespace \"workflow\":\n", stored, len(branches)))
	for _, br := range branches {
		if br.memKey == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("- `%s`: branch %d, agent %s, %d bytes\n", br.memKey, br.index, br.agentID, len(br.full)))
	}
	b.WriteString("Use `shared_memory_read(namespace=\"workflow\", key=\"<key>\")` to fetch a branch whose section below is marked truncated or reduced.\n\n---\n\n")
	return b.String()
}

// branchShare is the per-branch token share under a budget: the budget less
// everything that is not branch text, divided among the branches, with the
// safety discount. It errors when the share is too small to carry anything.
func (j *joiner) branchShare(branches []joinBranch, rendered string, budget int) (int, error) {
	if len(branches) == 0 {
		return 0, fmt.Errorf("%w: no branches to fit", ErrJoinInputOverBudget)
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
		return 0, fmt.Errorf("%w: budget %d leaves %d tokens per branch for %d branches (template and headers take %d); at least %d is required",
			ErrJoinInputOverBudget, budget, share, len(branches), overhead, MinJoinBranchShareTokens)
	}
	return share, nil
}

// fitByTruncation cuts every branch over its share down to it. No LLM calls.
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

// truncateBranchToShare makes a branch's inline copy fit its share, notice
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

// noticeFor renders the notice a branch would carry under an action, for
// token accounting.
func (j *joiner) noticeFor(b *joinBranch, action joinBranchAction) string {
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

// fitByReduction asks an LLM to reduce every branch over its share to a card
// of at most that share, in parallel under the orchestrator LLM semaphore,
// then truncates anything that still does not fit. The reduce LLM is the
// join agent's compressor role (its main LLM when none is configured), else
// the orchestrator merge LLM; with neither it falls back to truncation.
func (j *joiner) fitByReduction(ctx context.Context, joinAgent *agent.Agent, branches []joinBranch, rendered string, budget int) (types.Usage, int32, error) {
	share, err := j.branchShare(branches, rendered, budget)
	if err != nil {
		return types.Usage{}, 0, err
	}

	var reduceLLM agent.LLMProvider
	if joinAgent != nil {
		reduceLLM = joinAgent.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_COMPRESSOR)
	} else {
		reduceLLM = j.orch.GetMergeLLM()
	}
	if reduceLLM == nil {
		j.orch.logger.Warn("no LLM available to pre-reduce join input; truncating instead",
			zap.String("workflow_id", j.workflowID))
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
				j.orch.logger.Warn("branch pre-reduce failed; truncating that branch instead",
					zap.String("workflow_id", j.workflowID),
					zap.Int("branch", b.index),
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

// reduceOne runs one reduce call for a branch. It holds the orchestrator LLM
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
	p.WriteString(fmt.Sprintf("Reduce the following output from agent %s to at most %d words so it fits a bounded hand-off to a join step.\n", b.agentID, words))
	p.WriteString("Keep every number, name, decision and conclusion; drop repetition, preamble and anything the join does not need.\n")
	if j.prompt != "" {
		p.WriteString("The join task is: ")
		p.WriteString(j.prompt)
		p.WriteString("\n")
	}
	p.WriteString("Output only the reduced text, nothing else.\n\n--- OUTPUT TO REDUCE ---\n")
	p.WriteString(b.full)

	sessionID := fmt.Sprintf("%s-join-reduce-%d", j.workflowID, b.index)
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
// call when no agent is configured.
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
			usage = resp.Usage
		}
		return resp.Content, joinAgent.GetLLMModel(), usage, 1, nil
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
