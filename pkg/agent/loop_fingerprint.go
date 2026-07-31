// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// loopFingerprintVersion prefixes every fingerprint. Adding, removing, or
// renaming a field in LoopFingerprint MUST bump this version — fingerprints
// with different versions are never comparable, so old and new eval runs are
// never silently compared across a field-list change. Planned v2: the
// output-verification (behavior.output_policy) and max_cost_usd fields, once
// those configuration surfaces merge.
const loopFingerprintVersion = "v1"

// LoopFingerprint computes a stable identifier for the shape of the agent's
// conversation loop: the stop conditions, budgets, retry policy, and
// self-healing switches that Harness-Bench-style evaluation must attribute
// results to. Model and provider are deliberately excluded — they are
// emitted as separate span attributes (llm.provider / llm.model), and the
// fingerprint identifies the loop, not the model. Attribution = model attrs
// + loop fingerprint together.
//
// The field list is explicit (no reflection) so the hash is deterministic by
// construction. Returns the versioned hash ("v1:<sha256-hex>") and the
// contributing fields for debugging.
func LoopFingerprint(cfg *Config) (string, map[string]string) {
	if cfg == nil {
		return "", nil
	}

	fields := map[string]string{
		"max_turns":                 strconv.Itoa(cfg.MaxTurns),
		"max_tool_executions":       strconv.Itoa(cfg.MaxToolExecutions),
		"max_iterations":            strconv.Itoa(cfg.MaxIterations),
		"output_token_cb_threshold": strconv.Itoa(cfg.OutputTokenCBThreshold),
		"max_context_tokens":        strconv.Itoa(cfg.MaxContextTokens),
		"reserved_output_tokens":    strconv.Itoa(cfg.ReservedOutputTokens),
		"enable_self_healing":       strconv.FormatBool(cfg.EnableSelfHealing),
		"retry_enabled":             strconv.FormatBool(cfg.Retry.Enabled),
		"retry_max_retries":         strconv.Itoa(cfg.Retry.MaxRetries),
		"retry_initial_delay":       cfg.Retry.InitialDelay.String(),
		"retry_max_delay":           cfg.Retry.MaxDelay.String(),
		"retry_multiplier":          strconv.FormatFloat(cfg.Retry.Multiplier, 'g', -1, 64),
	}

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%s\n", k, fields[k])
	}

	sum := sha256.Sum256([]byte(sb.String()))
	return loopFingerprintVersion + ":" + hex.EncodeToString(sum[:]), fields
}

// getLoopFingerprint returns the agent's cached loop fingerprint, computing
// it on first use. Lazy computation (not construction-time) guarantees the
// fingerprint reflects post-defaulting *effective* config values; hot reload
// rebuilds the Agent, so the cache cannot go stale.
func (a *Agent) getLoopFingerprint() string {
	a.loopFingerprintOnce.Do(func() {
		a.loopFingerprint, _ = LoopFingerprint(a.config)
	})
	return a.loopFingerprint
}
