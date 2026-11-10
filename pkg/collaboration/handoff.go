// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"errors"
	"strconv"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/types"
)

// A hand-off carries several agents' outputs into one prompt that a single
// agent receives. In a debate that is each agent's "respond to the others"
// prompt and the moderator's synthesis prompt. The bounding itself (store
// the full text in SharedMemory, cap the inline copy, fit the whole prompt
// to the receiver's token budget) lives in pkg/orchestration, which this
// package cannot import; the orchestrator offers it through HandoffBounder,
// the way it offers agents through AgentProvider.

// HandoffEntry is one agent's full output carried into the prompt.
type HandoffEntry struct {
	Index   int    // 1-based position in the hand-off
	AgentID string // producer
	Text    string // full output
	Key     string // SharedMemory key to store Text under; "" = do not store
}

// HandoffPiece is an entry as the receiving prompt renders it.
type HandoffPiece struct {
	Index    int
	AgentID  string
	Inline   string // bounded copy of the full text (the full text when nothing was cut)
	Notice   string // "" when Inline is the full text; else what was cut and how to fetch the rest
	Key      string // SharedMemory key holding the full text; "" when not stored
	FullSize int    // bytes of the full text
}

// HandoffRequest asks a HandoffBounder for a prompt whose entries fit the
// receiver.
type HandoffRequest struct {
	WorkflowID string
	Pattern    string       // pattern name for SharedMemory metadata ("debate")
	Scope      string       // names this hand-off in SharedMemory metadata, reduce sessions and logs
	Receiver   *agent.Agent // the agent that will receive the prompt: its context window sets the budget, its compressor LLM pre-reduces
	Policy     *loomv1.JoinPolicy
	Task       string // what the receiver does with the hand-off; guides pre-reduce. May be empty.
	Entries    []HandoffEntry
	// Render builds the complete prompt from the bounded pieces. It is
	// called once to measure the prompt and again after an overflow
	// strategy ran, so it must be a pure function of its input.
	Render func(pieces []HandoffPiece) string
}

// HandoffResult is the bounded prompt and what bounding it did and cost.
type HandoffResult struct {
	Prompt      string
	InputTokens int
	Budget      int    // 0 when no budget applied
	Overflow    string // none | pre_reduce | truncate
	Stored      int    // entries whose full text is in SharedMemory
	Truncated   int    // entries whose inline copy was cut
	Reduced     int    // entries replaced by an LLM-reduced card
	Usage       types.Usage
	LLMCalls    int32 // pre-reduce calls made
}

// Metadata renders the result for result metadata and span attributes
// under prefix (e.g. "join") with unit naming the entries (e.g. "positions").
func (r *HandoffResult) Metadata(prefix, unit string) map[string]string {
	return map[string]string{
		prefix + ".input_tokens":           strconv.Itoa(r.InputTokens),
		prefix + ".budget_tokens":          strconv.Itoa(r.Budget),
		prefix + ".overflow":               r.Overflow,
		prefix + "." + unit + "_stored":    strconv.Itoa(r.Stored),
		prefix + "." + unit + "_truncated": strconv.Itoa(r.Truncated),
		prefix + "." + unit + "_reduced":   strconv.Itoa(r.Reduced),
	}
}

// ErrHandoffOverBudget is returned by a HandoffBounder when the prompt
// exceeds the policy's token budget and overflow_strategy is FAIL, or when
// the budget is too small for any overflow strategy to make it fit. A
// pattern treats it as fatal even where a plain chat failure is not.
var ErrHandoffOverBudget = errors.New("hand-off exceeds token budget")

// HandoffBounder bounds a hand-off. The orchestrator implements it; a
// collaboration pattern discovers it through a type assertion on its
// AgentProvider, as SupportsEphemeralAgents does for AgentFactory.
type HandoffBounder interface {
	BoundHandoff(ctx context.Context, req *HandoffRequest) (*HandoffResult, error)
}

// handoffBounderOf returns the provider's HandoffBounder, or nil.
func handoffBounderOf(provider AgentProvider) HandoffBounder {
	b, _ := provider.(HandoffBounder)
	return b
}
