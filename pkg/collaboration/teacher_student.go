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

// TeacherStudentOrchestrator is the executor slot for TeacherStudentPattern.
// The pattern is defined in proto and routed here, but no teaching loop
// exists yet: Execute returns ErrPatternNotImplemented so a caller gets a
// clear failure instead of a fabricated result.
type TeacherStudentOrchestrator struct {
	provider AgentProvider
}

// NewTeacherStudentOrchestrator creates the executor slot.
func NewTeacherStudentOrchestrator(provider AgentProvider) *TeacherStudentOrchestrator {
	return &TeacherStudentOrchestrator{
		provider: provider,
	}
}

// Execute always fails with ErrPatternNotImplemented. It makes no LLM call.
func (t *TeacherStudentOrchestrator) Execute(_ context.Context, config *loomv1.TeacherStudentPattern) (*loomv1.WorkflowResult, error) {
	return nil, fmt.Errorf("%w: teacher_student (objective %q)", ErrPatternNotImplemented, config.GetObjective())
}
