// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// PairProgrammingOrchestrator is the executor slot for PairProgrammingPattern.
// The pattern is defined in proto and routed here, but no driver/navigator
// loop exists yet: Execute returns ErrPatternNotImplemented so a caller gets
// a clear failure instead of a fabricated result.
type PairProgrammingOrchestrator struct {
	provider AgentProvider
}

// NewPairProgrammingOrchestrator creates the executor slot.
func NewPairProgrammingOrchestrator(provider AgentProvider) *PairProgrammingOrchestrator {
	return &PairProgrammingOrchestrator{
		provider: provider,
	}
}

// Execute always fails with ErrPatternNotImplemented. It makes no LLM call.
func (p *PairProgrammingOrchestrator) Execute(_ context.Context, config *loomv1.PairProgrammingPattern) (*loomv1.WorkflowResult, error) {
	return nil, fmt.Errorf("%w: pair_programming (task %q)", ErrPatternNotImplemented, config.GetTask())
}
