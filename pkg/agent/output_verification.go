// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"strings"
	"time"

	outputval "github.com/teradata-labs/loom/pkg/validation/output"
	"go.uber.org/zap"
)

// verificationOutcome is the terminal disposition of output verification for
// one conversation, surfaced in Response.Metadata["output_verification"].
type verificationOutcome string

const (
	verificationPassed       verificationOutcome = "passed"
	verificationFailed       verificationOutcome = "failed"
	verificationInconclusive verificationOutcome = "judge_inconclusive"
)

// verificationState tracks output-verification bookkeeping across
// conversation-loop iterations (mirrors the hygieneRetries counter).
type verificationState struct {
	attempts  int
	lastError string
}

// runOutputVerification verifies a terminal response against
// Config.OutputVerification. On failure with retries remaining it injects a
// feedback user message into the session (the caller must `continue` the
// conversation loop) — the same inject-and-continue shape as
// runEndOfTurnHygiene. Verification never turns a completed conversation
// into an error: exhaustion and inconclusive judge verdicts degrade
// gracefully with metadata.
func (a *Agent) runOutputVerification(ctx Context, session *Session, content string, state *verificationState) (retry bool, outcome verificationOutcome) {
	ov := a.config.OutputVerification
	if ov == nil || session == nil {
		return false, ""
	}

	failure, failureIsSchema, inconclusive := a.verifyOutput(ctx, ov, content)
	if inconclusive {
		return false, verificationInconclusive
	}
	if failure == "" {
		return false, verificationPassed
	}

	state.lastError = failure
	if state.attempts >= ov.MaxRetries {
		return false, verificationFailed
	}

	// Cooldown between retries — context-aware, never a bare sleep.
	if ov.CooldownMs > 0 {
		select {
		case <-ctx.Done():
			return false, verificationFailed
		case <-time.After(time.Duration(ov.CooldownMs) * time.Millisecond):
		}
	}

	requirement := ov.Schema
	if !failureIsSchema {
		requirement = ov.AcceptanceCriteria
	}
	feedback := outputval.BuildRetryFeedback(outputval.FeedbackParams{
		PreviousOutput:      content,
		Failure:             failure,
		Requirement:         requirement,
		RequirementIsSchema: failureIsSchema,
		IncludeValidValues:  ov.IncludeValidValues,
		Attempt:             state.attempts + 1,
		MaxRetries:          ov.MaxRetries,
		Template:            ov.FeedbackTemplate,
	})

	msg := Message{
		Role:      "user",
		Content:   feedback,
		AgentID:   a.id,
		Timestamp: time.Now(),
	}
	session.AddMessage(ctx, msg)
	if err := a.memory.PersistMessage(ctx, session.ID, msg); err != nil {
		zap.L().Warn("failed to persist output-verification feedback message",
			zap.String("session", session.ID),
			zap.Error(err))
	}
	state.attempts++

	// Semantic boundary for streaming clients: the previous generation was
	// rejected and a replacement is coming.
	emitProgress(ctx, StageSelfCorrection, 0, "Output verification failed; revising response", "")

	return true, ""
}

// verifyOutput runs the verifier hierarchy: output_schema (deterministic,
// free) first, then acceptance_criteria (one no-tools LLM call). Returns the
// failure description ("" = pass), whether the failure came from the schema,
// and whether the criteria judge was inconclusive (fail-open: malformed
// verdicts and judge call errors never count as validation failures and
// never burn a retry).
func (a *Agent) verifyOutput(ctx Context, ov *OutputVerificationConfig, content string) (failure string, failureIsSchema bool, inconclusive bool) {
	if ov.Schema != "" {
		if _, err := outputval.ValidateJSONSchema(content, ov.Schema); err != nil {
			return err.Error(), true, false
		}
	}

	if ov.AcceptanceCriteria == "" {
		return "", false, false
	}

	verdictResp, err := a.chatWithRetry(stripProgressCallback(ctx), []Message{{
		Role:      "user",
		Content:   buildCriteriaPrompt(ov.AcceptanceCriteria, content),
		Timestamp: time.Now(),
	}}, nil)
	if err != nil {
		zap.L().Warn("output-verification criteria evaluation failed; treating as inconclusive",
			zap.Error(err))
		return "", false, true
	}

	pass, reason, ok := outputval.ParseVerdict(verdictResp.Content)
	if !ok {
		zap.L().Warn("output-verification verdict malformed; treating as inconclusive",
			zap.String("verdict_preview", preview(verdictResp.Content, 120)))
		return "", false, true
	}
	if pass {
		return "", false, false
	}
	if reason == "" {
		reason = "output did not satisfy the acceptance criteria"
	}
	return "acceptance criteria not met: " + reason, false, false
}

// buildCriteriaPrompt renders the acceptance-criteria evaluation prompt.
// The documented {{output}} placeholder inlines the output into the criteria
// text; otherwise the output is appended in a delimited block.
func buildCriteriaPrompt(criteria, content string) string {
	var sb strings.Builder
	if strings.Contains(criteria, "{{output}}") {
		sb.WriteString(strings.ReplaceAll(criteria, "{{output}}", content))
	} else {
		sb.WriteString("Evaluate whether the OUTPUT satisfies every criterion.\n\nCRITERIA:\n")
		sb.WriteString(criteria)
		sb.WriteString("\n\nOUTPUT:\n---\n")
		sb.WriteString(content)
		sb.WriteString("\n---")
	}
	sb.WriteString("\n\nRespond with exactly `PASS` on the first line if every criterion is satisfied, or `FAIL: <reason>` if not.")
	return sb.String()
}

// stripProgressCallback returns a Context identical to ctx but without a
// progress callback, so internal evaluation calls are never streamed to the
// client as partial content.
func stripProgressCallback(ctx Context) Context {
	return &agentContext{
		Context:          ctx,
		session:          ctx.Session(),
		tracer:           ctx.Tracer(),
		progressCallback: nil,
	}
}

// preview returns at most n characters of s for log/event payloads.
func preview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
