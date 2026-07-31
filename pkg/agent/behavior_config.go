// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// maxOutputVerificationRetries caps behavior.output_policy retry attempts
// regardless of the configured retry_policy.max_retries.
const maxOutputVerificationRetries = 10

// OutputVerificationConfig is the runtime form of BehaviorConfig.output_policy:
// final-output verification for the agent conversation loop. Nil disables
// verification. Only output_schema and acceptance_criteria are supported in
// this context; see ApplyBehaviorConfig for the rejection rules.
type OutputVerificationConfig struct {
	// Schema is a JSON Schema applied to the final output (deterministic,
	// no LLM call). Empty skips structural validation.
	Schema string

	// AcceptanceCriteria is evaluated by one no-tools LLM call with a strict
	// PASS / FAIL: <reason> verdict. Supports the {{output}} placeholder.
	// Empty skips semantic validation.
	AcceptanceCriteria string

	// MaxRetries is the number of feedback-and-continue retries after a
	// failed verification (capped at maxOutputVerificationRetries).
	MaxRetries int

	// IncludeValidValues includes the schema / criteria text in the retry
	// feedback (mirrors OutputRetryPolicy.include_valid_values).
	IncludeValidValues bool

	// FeedbackTemplate optionally overrides the default retry feedback.
	// Variables: {{error}}, {{previous_output}}, {{attempt}}, {{max_retries}}.
	FeedbackTemplate string

	// CooldownMs waits between retries (context-aware, never a bare sleep).
	CooldownMs int
}

// ApplyBehaviorConfig maps a proto BehaviorConfig onto an agent Config.
// It is the single proto→runtime mapping used by the registry and server
// construction paths, and it preserves the legacy zero-value defaulting those
// paths applied: max_turns 0→25, max_tool_executions 0→50,
// output_token_cb_threshold 0→8 (-1 stays -1 = disabled).
//
// A nil BehaviorConfig applies only the defaulting. Errors are returned for
// output_policy fields that are unsupported in the agent loop, with pointers
// to the workflow-stage OutputPolicy where they are supported.
func ApplyBehaviorConfig(cfg *Config, b *loomv1.BehaviorConfig) error {
	if b != nil {
		if b.MaxTurns > 0 {
			cfg.MaxTurns = int(b.MaxTurns)
		}
		if b.MaxToolExecutions > 0 {
			cfg.MaxToolExecutions = int(b.MaxToolExecutions)
		}
		if b.MaxIterations > 0 {
			cfg.MaxIterations = int(b.MaxIterations)
		}
		if t := b.GetOutputTokenCbThreshold(); t != 0 {
			cfg.OutputTokenCBThreshold = int(t)
		}

		if b.Patterns != nil {
			pc := DefaultPatternConfig()
			pc.Enabled = b.Patterns.Enabled
			pc.EnableTracking = b.Patterns.EnableTracking
			pc.UseLLMClassifier = b.Patterns.UseLlmClassifier
			if b.Patterns.MinConfidence > 0 {
				pc.MinConfidence = float64(b.Patterns.MinConfidence)
			}
			if b.Patterns.MaxPatternsPerTurn > 0 {
				pc.MaxPatternsPerTurn = int(b.Patterns.MaxPatternsPerTurn)
			}
			cfg.PatternConfig = pc
		}

		if b.OutputPolicy != nil {
			ov, err := outputVerificationFromPolicy(b.OutputPolicy)
			if err != nil {
				return err
			}
			cfg.OutputVerification = ov
		}
	}

	// Legacy zero-value defaulting, preserved from the pre-consolidation
	// mapping sites (registry.go and cmd_serve.go).
	if cfg.MaxTurns == 0 {
		cfg.MaxTurns = 25
	}
	if cfg.MaxToolExecutions == 0 {
		cfg.MaxToolExecutions = 50
	}
	if cfg.OutputTokenCBThreshold == 0 {
		cfg.OutputTokenCBThreshold = 8
	}

	return nil
}

// outputVerificationFromPolicy converts an OutputPolicy into the agent-loop
// runtime config, rejecting fields that require registry/orchestration access
// unavailable inside the conversation loop.
func outputVerificationFromPolicy(p *loomv1.OutputPolicy) (*OutputVerificationConfig, error) {
	if p.ValidatorAgentId != "" {
		return nil, fmt.Errorf("behavior.output_policy.validator_agent_id is not supported in the agent loop; use a workflow-stage output_policy instead")
	}
	if p.JudgeConfigId != "" {
		return nil, fmt.Errorf("behavior.output_policy.judge_config_id is not supported in the agent loop; use a workflow-stage output_policy instead")
	}
	if p.OutputSchema == "" && p.AcceptanceCriteria == "" {
		return nil, fmt.Errorf("behavior.output_policy requires output_schema and/or acceptance_criteria")
	}

	ov := &OutputVerificationConfig{
		Schema:             p.OutputSchema,
		AcceptanceCriteria: p.AcceptanceCriteria,
	}

	if rp := p.RetryPolicy; rp != nil {
		switch rp.SessionMode {
		case loomv1.RetrySessionMode_RETRY_SESSION_MODE_UNSPECIFIED,
			loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE:
			// UNSPECIFIED maps to CONTINUE: the live conversation is the session.
		default:
			return nil, fmt.Errorf("behavior.output_policy.retry_policy.session_mode %s is not supported in the agent loop (the live conversation is the session); use CONTINUE or leave unset, or move the policy to a workflow stage", rp.SessionMode)
		}

		ov.MaxRetries = int(rp.MaxRetries)
		if ov.MaxRetries > maxOutputVerificationRetries {
			ov.MaxRetries = maxOutputVerificationRetries
		}
		if ov.MaxRetries < 0 {
			ov.MaxRetries = 0
		}
		ov.IncludeValidValues = rp.IncludeValidValues
		ov.FeedbackTemplate = rp.FeedbackTemplate
		ov.CooldownMs = int(rp.CooldownMs)
	}

	return ov, nil
}
