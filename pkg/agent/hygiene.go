// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/skills/hygiene"
)

// runEndOfTurnHygiene audits the active skill's tasks for hygiene
// violations and applies the configured policy. The retryCount pointer is
// incremented when a REQUIRE_FIX retry is requested so the outer loop can
// cap retries without leaking state across turns.
//
// Returns (retry, outcome). retry=true tells the caller to re-enter the
// conversation loop after the synthetic fixup message has been appended
// to the session. outcome is non-nil when the auditor actually ran and
// produced a result; the caller surfaces it into Response.Metadata.
//
// Hygiene failures are treated as non-fatal: any error during audit or
// enforcement is logged and the call returns (false, nil) so the agent
// can still hand control back to the user with whatever state it has.
func (a *Agent) runEndOfTurnHygiene(ctx context.Context, session *Session, retryCount *int) (bool, *hygiene.EnforcementOutcome) {
	if a.hygieneAuditor == nil || a.hygieneEnforcer == nil || session == nil {
		return false, nil
	}

	cfg := a.resolveHygieneConfig()
	if !a.hygieneAuditor.Enabled(cfg) {
		return false, nil
	}

	report, err := a.hygieneAuditor.Audit(ctx, session.ID, cfg)
	if err != nil {
		zap.L().Warn("end-of-turn hygiene audit failed; skipping",
			zap.String("session", session.ID),
			zap.Error(err))
		return false, nil
	}
	if !report.HasViolations() {
		return false, nil
	}

	maxRetries := a.hygieneAuditor.ResolveMaxRetries(cfg)
	outcome, err := a.hygieneEnforcer.Enforce(ctx, report, *retryCount, maxRetries)
	if err != nil {
		zap.L().Warn("end-of-turn hygiene enforcement failed; surfacing partial outcome",
			zap.String("session", session.ID),
			zap.Error(err))
		return false, outcome
	}

	if outcome != nil && outcome.ShouldRetry && outcome.InjectionMessage != "" {
		// Append the synthetic message to the session so the LLM sees it
		// on the next turn. The role is "user" because the LLM treats it
		// as input to act on, not as a system directive.
		msg := Message{
			Role:      "user",
			Content:   outcome.InjectionMessage,
			AgentID:   a.id,
			Timestamp: time.Now(),
		}
		session.AddMessage(ctx, msg)
		if err := a.memory.PersistMessage(ctx, session.ID, msg); err != nil {
			zap.L().Warn("failed to persist hygiene fixup message",
				zap.String("session", session.ID),
				zap.Error(err))
		}
		*retryCount++
		return true, outcome
	}

	return false, outcome
}

// resolveHygieneConfig returns the HygieneConfig from the agent's
// SkillsConfig, or nil when no skills config is wired. Nil is the signal
// to the auditor to apply defaults (enabled, REQUIRE_FIX, max_retries=2).
func (a *Agent) resolveHygieneConfig() *loomv1.HygieneConfig {
	if a.config == nil || a.config.SkillsConfig == nil {
		return nil
	}
	return a.config.SkillsConfig.Hygiene
}
