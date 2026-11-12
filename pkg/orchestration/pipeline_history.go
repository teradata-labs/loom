// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
)

// A pipeline stage that reads {{history}} (or runs under pass_full_history)
// receives every prior stage's output in one prompt. {{previous}} has been
// bounded since the hybrid hand-off: the full output is in SharedMemory under
// <workflow-id>-stage-N-output and the next stage gets an 8 KiB copy with a
// notice. History
// entries now get the same treatment, and a HistoryPolicy adds a whole-prompt
// token budget enforced with the join overflow strategies. The joiner does
// the capping and fitting; this file renders the result as the stage prompt.

// historyJoinPolicy maps a HistoryPolicy (nil = defaults) onto the JoinPolicy
// shape the joiner works with. The per-entry cap defaults to
// MaxStageOutputBytes so a history entry reads exactly like {{previous}}.
func historyJoinPolicy(p *loomv1.HistoryPolicy) *loomv1.JoinPolicy {
	jp := &loomv1.JoinPolicy{MaxBranchBytes: MaxStageOutputBytes}
	if p == nil {
		return jp
	}
	if p.MaxEntryBytes > 0 {
		jp.MaxBranchBytes = p.MaxEntryBytes
	}
	jp.MaxInputTokens = p.MaxInputTokens
	jp.OverflowStrategy = p.OverflowStrategy
	return jp
}

// stageRendersHistory reports whether a stage prompt carries the prior
// outputs: the template names {{history}}, or pass_full_history appends them
// because the template names neither placeholder.
func stageRendersHistory(stage *loomv1.PipelineStage, passFullHistory bool, priorStages int) bool {
	if priorStages == 0 {
		return false
	}
	tmpl := stage.GetPromptTemplate()
	if strings.Contains(tmpl, "{{history}}") {
		return true
	}
	return passFullHistory && !strings.Contains(tmpl, "{{previous}}")
}

// stageStashed reports whether prior stage i's full output is in
// SharedMemory: the recorded flag when the run tracked one, else whether a
// store exists at all (a resumed run re-seeds every completed stage).
func stageStashed(stashed []bool, i int, haveStore bool) bool {
	if i < len(stashed) {
		return stashed[i]
	}
	return haveStore && len(stashed) == 0
}

// historyStats aggregates, over a run, what bounding the history hand-offs
// did, for the result metadata.
type historyStats struct {
	rendered      int // stage prompts that carried history
	overBudget    int // of those, prompts that needed an overflow strategy
	truncated     int // history entries cut (per-entry cap or truncate)
	reduced       int // history entries replaced by an LLM-reduced card
	reduceCalls   int32
	budgetApplied bool
}

func (s *historyStats) add(fit *handoffFit) {
	if fit == nil {
		return
	}
	s.rendered++
	if fit.Overflow != "none" {
		s.overBudget++
	}
	s.truncated += fit.Truncated
	s.reduced += fit.Reduced
	s.reduceCalls += fit.LLMCalls
	if fit.Budget > 0 {
		s.budgetApplied = true
	}
}

// addTo writes the aggregate under history.* when any stage carried history.
func (s *historyStats) addTo(metadata map[string]string) {
	if s.rendered == 0 {
		return
	}
	metadata["history.stages_rendered"] = strconv.Itoa(s.rendered)
	metadata["history.budget_applied"] = strconv.FormatBool(s.budgetApplied)
	metadata["history.stages_over_budget"] = strconv.Itoa(s.overBudget)
	metadata["history.entries_truncated"] = strconv.Itoa(s.truncated)
	metadata["history.entries_reduced"] = strconv.Itoa(s.reduced)
	metadata["history.reduce_llm_calls"] = strconv.Itoa(int(s.reduceCalls))
}

// boundStagePrompt builds a stage's prompt. Prior outputs rendered through
// {{history}} / pass_full_history are each the inline copy {{previous}}
// would carry (full when SharedMemory has no copy, else cut to the per-entry
// cap with the <workflow-id>-stage-N-output notice). With a HistoryPolicy the whole prompt
// is token-counted against the stage agent's budget and the history entries
// are reduced or truncated to fit, or the stage fails, per the policy.
//
// stashed says, per prior stage, whether its full output is in SharedMemory;
// an entry whose stash failed is passed in full, as {{previous}} was for
// that stage. nil means every entry is stashed when the orchestrator has a
// store (the resume path, which re-seeds every stage). header is prepended
// to the prompt (the SharedMemory key list); it counts toward the budget.
// The returned fit is nil when the prompt carries no history. Pre-reduce
// usage is in fit.Usage / fit.LLMCalls for the caller to add to the run's
// cost.
func (e *PipelineExecutor) boundStagePrompt(ctx context.Context, stage *loomv1.PipelineStage, stageNum int, previousOutput string, allOutputs []string, stashed []bool, structuredCtx *StructuredContext, header string) (string, *handoffFit, error) {
	if !stageRendersHistory(stage, e.pattern.PassFullHistory, len(allOutputs)) {
		return header + e.buildStagePromptWithContext(stage, previousOutput, allOutputs, structuredCtx), nil, nil
	}

	j := newJoiner(e.orchestrator, historyJoinPolicy(e.pattern.HistoryPolicy), loomv1.MergeStrategy_MERGE_STRATEGY_UNSPECIFIED, "pipeline", e.workflowID, "")
	j.scope = fmt.Sprintf("stage-%d-history", stageNum)
	j.notice = noticeStyleStage

	entries := make([]handoffEntry, 0, len(allOutputs))
	for i, out := range allOutputs {
		entry := handoffEntry{index: i + 1, full: out}
		if i < len(e.pattern.Stages) {
			entry.agentID = e.pattern.Stages[i].AgentId
		}
		if stageStashed(stashed, i, e.orchestrator.sharedMemory != nil) {
			entry.memKey = stageOutputKey(e.workflowID, i+1)
		}
		entries = append(entries, entry)
	}
	branches := j.branchesFor(entries, false)

	render := func(bs []joinBranch) string {
		history := make([]string, len(bs))
		for i := range bs {
			history[i] = bs[i].inline + j.noticeFor(&bs[i], bs[i].action)
		}
		return header + e.buildStagePromptWithContext(stage, previousOutput, history, structuredCtx)
	}

	var (
		receiver *agent.Agent
		budget   int
	)
	if e.pattern.HistoryPolicy != nil {
		ag, err := e.orchestrator.GetAgent(ctx, stage.AgentId)
		if err != nil {
			return "", nil, fmt.Errorf("stage %d agent %s: %w", stageNum, stage.AgentId, err)
		}
		receiver = ag
		budget = j.budget(ag)
	}
	fit, err := j.fit(ctx, receiver, branches, budget, render)
	if err != nil {
		return "", nil, fmt.Errorf("stage %d history: %w", stageNum, err)
	}
	return fit.Rendered, fit, nil
}
