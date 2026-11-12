// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

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
	// Stored says Text is already in SharedMemory under Key (an earlier
	// hand-off of the same run put it there); the bounder references the
	// key without writing again.
	Stored bool
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
	Budget      int      // 0 when no budget applied
	Overflow    string   // none | pre_reduce | truncate
	Stored      int      // entries whose full text is in SharedMemory
	StoredKeys  []string // keys of those entries (a failed Put leaves its key out)
	Truncated   int      // entries whose inline copy was cut
	Reduced     int      // entries replaced by an LLM-reduced card
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

// SpliceTemplate fills a hand-off prompt template: {{prompt}} and
// {{branch_count}} by replacement, {{branches}} last and by splicing, so
// placeholder-like text inside an entry is never re-scanned. A template
// without {{branches}} gets the sections appended, so the receiver never runs
// blind.
func SpliceTemplate(tmpl, prompt, sections string, count int) string {
	body := strings.ReplaceAll(tmpl, "{{prompt}}", prompt)
	body = strings.ReplaceAll(body, "{{branch_count}}", strconv.Itoa(count))
	if !strings.Contains(body, "{{branches}}") {
		return body + "\n\n" + sections
	}
	return strings.Join(strings.Split(body, "{{branches}}"), sections)
}

// SharedMemoryWriteHint is the line every SharedMemory header carries so an
// agent that writes its own keys keeps them with its workflow: a key with the
// workflow prefix is the run's own (cleared by a reset_shared_memory restart,
// kept by a checkpoint); a key without it is shared with every workflow on
// the store.
func SharedMemoryWriteHint(workflowID string) string {
	return fmt.Sprintf("Keys you write with shared_memory_write should start with `%s-` so they stay with this workflow (a restart that resets SharedMemory clears them, a checkpoint keeps them); keys without that prefix are shared with every workflow on this store.\n", workflowID)
}

// SharedMemoryHeader lists the SharedMemory keys holding full entries, for a
// receiver that can call shared_memory_read, and asks it to prefix the keys
// it writes with the workflow id. unit names the entries ("branches",
// "positions"). Empty when no entry was stored.
func SharedMemoryHeader(pieces []HandoffPiece, unit, workflowID string) string {
	stored := 0
	for _, p := range pieces {
		if p.Key != "" {
			stored++
		}
	}
	if stored == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## AVAILABLE CONTEXT (SharedMemory)\n")
	b.WriteString(fmt.Sprintf("Full text of %d of %d %s is stored in SharedMemory namespace \"workflow\":\n", stored, len(pieces), unit))
	for _, p := range pieces {
		if p.Key == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("- `%s`: agent %s, %d bytes\n", p.Key, p.AgentID, p.FullSize))
	}
	b.WriteString("Use `shared_memory_read(namespace=\"workflow\", key=\"<key>\")` to fetch one whose section below is marked truncated or reduced.\n")
	if workflowID != "" {
		b.WriteString(SharedMemoryWriteHint(workflowID))
	}
	b.WriteString("\n---\n\n")
	return b.String()
}

// HandoffCleaner removes the entries a workflow stored through BoundHandoff
// once that workflow's run has ended, so a long-lived store does not grow
// with every run. The orchestrator implements it next to HandoffBounder.
type HandoffCleaner interface {
	CleanupHandoffs(ctx context.Context, workflowID string)
}

// handoffBounderOf returns the provider's HandoffBounder, or nil.
func handoffBounderOf(provider AgentProvider) HandoffBounder {
	b, _ := provider.(HandoffBounder)
	return b
}
