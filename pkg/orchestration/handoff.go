// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/collaboration"
	"go.uber.org/zap"
)

// The orchestrator is the collaboration engine's AgentProvider; it also
// offers the bounded hand-off (join.go) to collaboration patterns through
// collaboration.HandoffBounder, so the debate engine can bound its response
// and synthesis prompts without importing this package.

var _ collaboration.HandoffBounder = (*Orchestrator)(nil)

// BoundHandoff implements collaboration.HandoffBounder: entries with a Key
// are stored in SharedMemory (when the orchestrator has a store), every
// inline copy is cut to the policy's per-entry cap, the prompt is rendered
// through req.Render and, when it exceeds the receiver's budget, the entries
// are brought under it with the policy's overflow strategy.
func (o *Orchestrator) BoundHandoff(ctx context.Context, req *collaboration.HandoffRequest) (*collaboration.HandoffResult, error) {
	if req == nil || req.Render == nil {
		return nil, errors.New("handoff: request with Render is required")
	}
	if req.Policy == nil {
		return nil, errors.New("handoff: JoinPolicy is required")
	}

	j := newJoiner(o, req.Policy, loomv1.MergeStrategy_MERGE_STRATEGY_UNSPECIFIED, req.Pattern, req.WorkflowID, req.Task)
	j.scope = req.Scope
	j.storeType = req.Pattern + "_handoff"

	entries := make([]handoffEntry, 0, len(req.Entries))
	for _, in := range req.Entries {
		e := handoffEntry{index: in.Index, agentID: in.AgentID, full: in.Text}
		if in.Key != "" && o.sharedMemory != nil {
			if in.Stored {
				e.memKey = in.Key
			} else if err := j.store(ctx, in.Key, in.AgentID, in.Index, in.Text); err != nil {
				o.logger.Warn("failed to store hand-off entry in SharedMemory; receiver gets the inline copy only",
					zap.String("workflow_id", req.WorkflowID),
					zap.String("scope", req.Scope),
					zap.String("key", in.Key),
					zap.Error(err))
			} else {
				e.memKey = in.Key
			}
		}
		entries = append(entries, e)
	}
	branches := j.branchesFor(entries, true)

	render := func(bs []joinBranch) string {
		return req.Render(j.pieces(bs))
	}

	fit, err := j.fit(ctx, req.Receiver, branches, j.budget(req.Receiver), render)
	if err != nil {
		if errors.Is(err, ErrJoinInputOverBudget) {
			return nil, fmt.Errorf("%w: %w", collaboration.ErrHandoffOverBudget, err)
		}
		return nil, err
	}
	var storedKeys []string
	for _, e := range entries {
		if e.memKey != "" {
			storedKeys = append(storedKeys, e.memKey)
		}
	}
	return &collaboration.HandoffResult{
		Prompt:      fit.Rendered,
		InputTokens: fit.InputTokens,
		Budget:      fit.Budget,
		Overflow:    fit.Overflow,
		Stored:      fit.Stored,
		StoredKeys:  storedKeys,
		Truncated:   fit.Truncated,
		Reduced:     fit.Reduced,
		Usage:       fit.Usage,
		LLMCalls:    fit.LLMCalls,
	}, nil
}
