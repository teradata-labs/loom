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

	"github.com/teradata-labs/loom/pkg/skills"
	skilltasks "github.com/teradata-labs/loom/pkg/skills/tasks"
)

// skillTaskEmitTimeout bounds one detached skill-task emit.
//
// Sizing: Manager.CreateTaskIdempotent runs three separate transactions per
// step (idempotency lookup, insert, history record) plus one per dependency
// edge, so a template costs roughly four round trips per step. That is ~35
// round trips at the DefaultMaxTasks cap of 8, and proportionally more for an
// emitter built with a higher WithEmitterMaxTasks — ~200 at a cap of 50. At a
// 2ms RTT that is under a second; 30s absorbs an order-of-magnitude worse RTT
// or a contended database and still bounds a wedged emit far below the life of
// a session.
const skillTaskEmitTimeout = 30 * time.Second

// emitSkillTasksAsync materializes the freshly-activated skill's tasks onto the
// agent's board, on its own goroutine, and returns immediately.
//
// Off the critical path by design. manage_skills(load) is a tool call inside the
// user's turn, and nothing in that turn depends on the task rows existing: the
// model receives the skill body either way, and the rows exist for the
// human-facing board. Emitting inline would add the whole transaction cost of
// the template (see skillTaskEmitTimeout) to the user's wait for a response.
//
// Every value the goroutine needs is captured here, in the caller's goroutine,
// so the goroutine reads only its own locals — no Agent field is touched
// concurrently with a turn.
//
// Callers must only invoke this for a genuinely new activation. Emission is
// idempotent via SkillIdempotencyKey, so a repeat is harmless, but it is a
// board-worth of transactions spent to re-discover rows that already exist.
func (a *Agent) emitSkillTasksAsync(ctx context.Context, sessionID string, skill *skills.Skill) {
	emitter := a.skillTaskEmitter
	if emitter == nil || skill == nil {
		return
	}
	if sessionID == "" {
		// The idempotency key is "skill:<name>|sess:<id>|step:<n>". An empty id
		// collapses every session without one onto a single key, so the second
		// such activation would adopt the first's tasks instead of getting its
		// own. Skipping is the honest outcome.
		zap.L().Debug("skill task emission skipped: no session id in context",
			zap.String("skill", skill.Name))
		return
	}

	// Same resolution the rest of the agent uses for this config (see
	// skillMenuPromptSupplement): an absent per-agent block falls back to the
	// package defaults rather than to the zero value, whose TasksEnabled=nil
	// would read the same but whose SkillTaskBoardID lookup would nil-deref.
	var skillsConfig *skills.SkillsConfig
	if a.config != nil {
		skillsConfig = a.config.SkillsConfig
	}
	if skillsConfig == nil {
		skillsConfig = skills.DefaultSkillsConfig()
	}

	// The agent-level master switch. EmitForActivation returns Source "none"
	// when this is false, so reading it from the wrong place disables emission
	// without an error anywhere.
	agentTasksEnabled := skillsConfig.EffectiveTasksEnabled()

	boardID := skillsConfig.SkillTaskBoardID
	if boardID == "" && a.taskBoardConfig != nil {
		boardID = a.taskBoardConfig.DefaultBoardId
	}

	req := skilltasks.EmitRequest{
		Skill:             skill,
		SessionID:         sessionID,
		AgentID:           a.id,
		BoardID:           boardID,
		LLM:               a.llm,
		AgentTasksEnabled: agentTasksEnabled,
	}

	// WithoutCancel drops the turn's cancellation but keeps its VALUES. That
	// distinction is the point: a store can read tenant or user identity off the
	// context to scope its writes (the Postgres task stores in downstream
	// deployments do), so a bare context.Background() would strip that scoping,
	// while a plain ctx would cancel the writes the moment the turn ends.
	emitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), skillTaskEmitTimeout)

	// Add before the go statement so a waiter that starts after this call
	// cannot observe a zero counter while the emit is still pending.
	a.skillTaskEmits.Add(1)
	go func() {
		defer a.skillTaskEmits.Done()
		defer cancel()

		res, err := emitter.EmitForActivation(emitCtx, req)
		if err != nil {
			// Never surfaced to the tool caller: the skill is already loaded and
			// its body is already on its way to the model. A board that failed
			// to fill is a logged degradation, not a failed skill load.
			zap.L().Warn("skill task emission failed",
				zap.String("skill", skill.Name),
				zap.String("session", sessionID),
				zap.String("board", boardID),
				zap.Error(err))
			return
		}
		if res == nil {
			return
		}
		zap.L().Debug("skill task emission complete",
			zap.String("skill", skill.Name),
			zap.String("session", sessionID),
			zap.String("source", res.Source),
			zap.Int("tasks", len(res.Tasks)),
			zap.Int("created", res.CreatedCount))
	}()
}

// waitForSkillTaskEmits blocks until every emit started so far has finished.
//
// The counter exists so the detached emits can be joined rather than waited
// out. Its only caller today is this package's tests, which need a
// deterministic join and not a duration; there is no Agent shutdown path yet
// for it to hang off.
func (a *Agent) waitForSkillTaskEmits() {
	a.skillTaskEmits.Wait()
}
