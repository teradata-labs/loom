// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
)

// patternLoadRecorder receives pattern-load notifications from the
// load_pattern tool so usage can be attributed to conversation outcomes.
type patternLoadRecorder interface {
	RecordPatternLoaded(sessionID, patternName string)
}

// RecordPatternLoaded records that a pattern was loaded during a session's
// in-flight conversation. Names are deduplicated per session; the bucket is
// drained (attributed exactly once) at the end of the Chat that follows.
func (a *Agent) RecordPatternLoaded(sessionID, patternName string) {
	if sessionID == "" || patternName == "" {
		return
	}
	a.loadedPatternsMu.Lock()
	defer a.loadedPatternsMu.Unlock()
	if a.loadedPatterns == nil {
		a.loadedPatterns = make(map[string][]string)
	}
	for _, existing := range a.loadedPatterns[sessionID] {
		if existing == patternName {
			return
		}
	}
	a.loadedPatterns[sessionID] = append(a.loadedPatterns[sessionID], patternName)
}

// takeLoadedPatterns drains the session's loaded-pattern bucket. Drain-on-read
// gives per-Chat attribution windows and prevents unbounded growth; with
// concurrent Chats on the same session, patterns attribute to whichever Chat
// drains first (best-effort, documented).
func (a *Agent) takeLoadedPatterns(sessionID string) []string {
	a.loadedPatternsMu.Lock()
	defer a.loadedPatternsMu.Unlock()
	loaded := a.loadedPatterns[sessionID]
	delete(a.loadedPatterns, sessionID)
	return loaded
}

// recordPatternEffectiveness attributes the whole-conversation outcome to
// every pattern loaded during this Chat, feeding the pattern effectiveness
// tracker (wired via SetPatternTracker; no-op when none is attached). Returns
// the pattern names recorded so the caller can annotate its span.
//
// Success semantics: the conversation completed without error AND the final
// output was not rejected by output verification AND the cost ceiling did not
// trip (the latter two read forward-compatible metadata keys and are inert
// until those features merge). When multiple patterns were loaded, each
// receives the full conversation cost — summing cost across patterns
// double-counts by design; the metric answers "what do conversations using
// this pattern cost/succeed like".
func (a *Agent) recordPatternEffectiveness(ctx context.Context, sessionID string, chatErr error, meta map[string]interface{}, costUSD float64, latency time.Duration) []string {
	if a.orchestrator == nil {
		return nil
	}
	if pc := a.config.PatternConfig; pc != nil && !pc.EnableTracking {
		return nil
	}

	loaded := a.takeLoadedPatterns(sessionID)
	if len(loaded) == 0 {
		return nil
	}

	success := chatErr == nil
	errorType := classifyChatError(chatErr)
	if success && meta != nil {
		if ov, ok := meta["output_verification"].(string); ok && ov == "failed" {
			success = false
			errorType = "output_verification_failed"
		}
		if hit, ok := meta["cost_limit_hit"].(bool); ok && hit {
			success = false
			errorType = "cost_limit_hit"
		}
	}

	a.mu.RLock()
	provider := a.llm.Name()
	model := a.llm.Model()
	a.mu.RUnlock()

	for _, name := range loaded {
		a.orchestrator.RecordPatternUsage(ctx, name, a.config.Name, success, costUSD, latency, errorType, provider, model)
	}

	zap.L().Debug("pattern effectiveness recorded",
		zap.Strings("patterns", loaded),
		zap.Bool("success", success),
		zap.String("session_id", sessionID))

	return loaded
}

// classifyChatError maps a conversation error to a coarse errorType for the
// effectiveness tracker. Empty string means no error.
func classifyChatError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "llm_error"
	}
}
