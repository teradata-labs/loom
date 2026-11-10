// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

// DebateOrchestrator manages multi-agent debates with round-by-round tracking.
type DebateOrchestrator struct {
	provider AgentProvider
	tracer   observability.Tracer
	logger   *zap.Logger

	// bounder bounds the response and synthesis prompts under a
	// DebatePattern.join_policy; nil when the provider cannot (then a
	// policy is logged and ignored).
	bounder HandoffBounder

	// storedPositions remembers which position keys a run has already put
	// in SharedMemory ("<workflow-id>|<key>"), so a position carried into
	// several prompts is written once. Runs share the orchestrator, so this
	// is keyed by workflow id and forgotten when the run ends.
	storedPositions sync.Map
}

// positionStored reports whether a run already stored a position key.
func (d *DebateOrchestrator) positionStored(workflowID, key string) bool {
	_, ok := d.storedPositions.Load(workflowID + "|" + key)
	return ok
}

// markPositionsStored records the keyed entries a successful hand-off stored.
func (d *DebateOrchestrator) markPositionsStored(workflowID string, entries []HandoffEntry) {
	for _, e := range entries {
		if e.Key != "" {
			d.storedPositions.Store(workflowID+"|"+e.Key, struct{}{})
		}
	}
}

// forgetStoredPositions drops a run's bookkeeping when the run ends.
func (d *DebateOrchestrator) forgetStoredPositions(workflowID string) {
	prefix := workflowID + "|"
	d.storedPositions.Range(func(k, _ any) bool {
		if s, ok := k.(string); ok && strings.HasPrefix(s, prefix) {
			d.storedPositions.Delete(k)
		}
		return true
	})
}

// debateHandoffStats aggregates, over a debate, what bounding the round 2+
// response prompts did.
type debateHandoffStats struct {
	bounded    int // response prompts rendered through the bounder
	overBudget int // of those, prompts that needed an overflow strategy
	truncated  int
	reduced    int
}

func (s *debateHandoffStats) add(r *HandoffResult) {
	if r == nil {
		return
	}
	s.bounded++
	if r.Overflow != "none" {
		s.overBudget++
	}
	s.truncated += r.Truncated
	s.reduced += r.Reduced
}

// positionKey is the SharedMemory key holding an agent's full position for
// a round, when a join policy stores positions.
func positionKey(workflowID string, round int32, agentID string) string {
	return fmt.Sprintf("%s-round-%d-position-%s", workflowID, round, agentID)
}

// NewDebateOrchestrator creates a new debate orchestrator.
func NewDebateOrchestrator(provider AgentProvider) *DebateOrchestrator {
	return NewDebateOrchestratorWithObservability(provider, observability.NewNoOpTracer(), zap.NewNop())
}

// NewDebateOrchestratorWithObservability creates a new debate orchestrator with observability.
func NewDebateOrchestratorWithObservability(provider AgentProvider, tracer observability.Tracer, logger *zap.Logger) *DebateOrchestrator {
	return &DebateOrchestrator{
		provider: provider,
		tracer:   tracer,
		logger:   logger,
		bounder:  handoffBounderOf(provider),
	}
}

// handoffPolicy returns the join policy to bound this debate's hand-offs
// with, or nil when there is none or the provider cannot bound.
func (d *DebateOrchestrator) handoffPolicy(config *loomv1.DebatePattern) *loomv1.JoinPolicy {
	if config.GetJoinPolicy() == nil {
		return nil
	}
	if d.bounder == nil {
		return nil
	}
	return config.JoinPolicy
}

// Execute runs a multi-agent debate with specified rounds and topic.
func (d *DebateOrchestrator) Execute(ctx context.Context, config *loomv1.DebatePattern) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	// Generate unique workflow ID for session tracking
	workflowID := fmt.Sprintf("debate-%s", uuid.New().String()[:8])

	// The moderator is the join agent: a join_policy.agent_id has no meaning
	// here, and accepting it would promise a synthesis author the engine does
	// not use. The YAML loader and the builder reject it; so does the engine,
	// for patterns built directly.
	if id := config.GetJoinPolicy().GetAgentId(); id != "" {
		return nil, fmt.Errorf("debate join_policy.agent_id %q is not used: moderator_agent_id is the join agent and each debater receives its own response prompt; remove agent_id", id)
	}

	// Positions stored under a join policy are this run's artifact: drop them
	// when the run ends, however it ends.
	if cleaner, ok := d.provider.(HandoffCleaner); ok && d.handoffPolicy(config) != nil {
		defer cleaner.CleanupHandoffs(ctx, workflowID)
	}
	defer d.forgetStoredPositions(workflowID)

	// Start workflow-level span for complete observability hierarchy
	ctx, span := d.tracer.StartSpan(ctx, "workflow.debate")
	defer d.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("workflow.type", "debate")
		span.SetAttribute("workflow.name", "multi-agent-debate")
		span.SetAttribute("workflow.id", workflowID)
		span.SetAttribute("debate.topic", config.Topic)
		span.SetAttribute("debate.rounds", fmt.Sprintf("%d", config.Rounds))
		span.SetAttribute("debate.agent_count", fmt.Sprintf("%d", len(config.AgentIds)))
		span.SetAttribute("debate.agents", strings.Join(config.AgentIds, ","))
		if config.ModeratorAgentId != "" {
			span.SetAttribute("debate.moderator", config.ModeratorAgentId)
		}
	}

	d.logger.Info("Starting debate execution",
		zap.String("topic", config.Topic),
		zap.Int32("rounds", config.Rounds),
		zap.Int("agents", len(config.AgentIds)))

	if len(config.AgentIds) < 2 {
		return nil, fmt.Errorf("debate requires at least 2 agents, got %d", len(config.AgentIds))
	}

	if config.Rounds <= 0 {
		return nil, fmt.Errorf("debate requires at least 1 round, got %d", config.Rounds)
	}

	result := &loomv1.WorkflowResult{
		PatternType:  "debate",
		AgentResults: make([]*loomv1.AgentResult, 0),
		Metadata:     make(map[string]string),
		Cost:         &loomv1.WorkflowCost{AgentCostsUsd: make(map[string]float64)},
		ModelsUsed:   make(map[string]string), // Track models used by each agent
	}

	debateResult := &loomv1.DebateResult{
		Rounds:            make([]*loomv1.DebateRound, 0),
		ConsensusAchieved: false,
	}

	// Get or create internal moderator agent for summarization and synthesis
	internalModerator, err := d.getInternalModerator(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to get internal moderator: %w", err)
	}

	if config.JoinPolicy != nil && d.bounder == nil {
		d.logger.Warn("debate join_policy set but the agent provider cannot bound hand-offs; response and synthesis prompts are unbounded",
			zap.String("workflow_id", workflowID))
	}
	var responseStats debateHandoffStats

	// Track debate history for context
	debateHistory := make([]string, 0)

	// Run debate rounds with nested spans
	for roundNum := int32(1); roundNum <= config.Rounds; roundNum++ {
		// Start round-level span
		ctx, roundSpan := d.tracer.StartSpan(ctx, fmt.Sprintf("debate.round.%d", roundNum))
		if roundSpan != nil {
			roundSpan.SetAttribute("round.number", fmt.Sprintf("%d", roundNum))
			roundSpan.SetAttribute("round.agent_count", fmt.Sprintf("%d", len(config.AgentIds)))
			roundSpan.SetAttribute("round.total_rounds", fmt.Sprintf("%d", config.Rounds))
		}

		d.logger.Info("Starting debate round",
			zap.Int32("round", roundNum),
			zap.Int32("total_rounds", config.Rounds))

		round, roundStats, err := d.executeRound(ctx, workflowID, roundNum, config, debateHistory, internalModerator, result.Cost)
		responseStats.bounded += roundStats.bounded
		responseStats.overBudget += roundStats.overBudget
		responseStats.truncated += roundStats.truncated
		responseStats.reduced += roundStats.reduced

		// End round span
		if roundSpan != nil {
			if err != nil {
				roundSpan.SetAttribute("round.status", "failed")
				roundSpan.RecordError(err)
			} else {
				roundSpan.SetAttribute("round.status", "completed")
				roundSpan.SetAttribute("round.consensus_reached", fmt.Sprintf("%t", round.ConsensusReached))
				roundSpan.SetAttribute("round.positions", fmt.Sprintf("%d", len(round.Positions)))
			}
			if roundStats.bounded > 0 {
				roundSpan.SetAttribute("join.response_prompts_bounded", fmt.Sprintf("%d", roundStats.bounded))
				roundSpan.SetAttribute("join.response_prompts_over_budget", fmt.Sprintf("%d", roundStats.overBudget))
				roundSpan.SetAttribute("join.response_positions_truncated", fmt.Sprintf("%d", roundStats.truncated))
				roundSpan.SetAttribute("join.response_positions_reduced", fmt.Sprintf("%d", roundStats.reduced))
			}
		}
		d.tracer.EndSpan(roundSpan)

		if err != nil {
			d.logger.Error("Debate round failed",
				zap.Int32("round", roundNum),
				zap.Error(err))
			return nil, fmt.Errorf("round %d failed: %w", roundNum, err)
		}

		debateResult.Rounds = append(debateResult.Rounds, round)

		// Add round to history for next round's context (using moderator for summarization)
		debateHistory = append(debateHistory, d.formatRoundHistory(ctx, workflowID, round, internalModerator, result.Cost))

		// Accumulate agent results and costs
		for _, position := range round.Positions {
			agentResult := &loomv1.AgentResult{
				AgentId:         position.AgentId,
				Output:          position.Position,
				ConfidenceScore: position.Confidence,
				Cost:            positionCost(position),
				Metadata: map[string]string{
					"round":         fmt.Sprintf("%d", roundNum),
					"num_arguments": fmt.Sprintf("%d", len(position.Arguments)),
				},
			}
			result.AgentResults = append(result.AgentResults, agentResult)

			// Track model used by this agent
			if position.Model != "" {
				modelKey := fmt.Sprintf("%s (%s)", position.Model, position.Provider)
				result.ModelsUsed[position.AgentId] = modelKey
			}
		}

		// Check for consensus
		if round.ConsensusReached {
			debateResult.ConsensusAchieved = true
			debateResult.Consensus = round.Synthesis
			d.logger.Info("Consensus reached in debate",
				zap.Int32("round", roundNum))
			break
		}
	}

	// If moderator specified, synthesize final consensus
	if config.ModeratorAgentId != "" {
		// Start moderator span
		ctx, modSpan := d.tracer.StartSpan(ctx, "debate.moderator.synthesis")
		if modSpan != nil {
			modSpan.SetAttribute("moderator.agent_id", config.ModeratorAgentId)
			modSpan.SetAttribute("moderator.rounds_reviewed", fmt.Sprintf("%d", len(debateResult.Rounds)))
		}

		synthesis, synthesisFit, err := d.synthesizeWithModerator(ctx, workflowID, config, debateResult, result.Cost)

		if modSpan != nil {
			if err != nil {
				modSpan.SetAttribute("moderator.status", "failed")
				modSpan.RecordError(err)
			} else {
				modSpan.SetAttribute("moderator.status", "completed")
				modSpan.SetAttribute("moderator.synthesis_length", fmt.Sprintf("%d", len(synthesis)))
			}
			if synthesisFit != nil {
				for k, v := range synthesisFit.Metadata("join", "positions") {
					modSpan.SetAttribute(k, v)
				}
			}
		}
		d.tracer.EndSpan(modSpan)
		if synthesisFit != nil {
			for k, v := range synthesisFit.Metadata("join", "positions") {
				result.Metadata[k] = v
			}
			result.Metadata["join.mode"] = "moderator"
			result.Metadata["join.agent_id"] = config.ModeratorAgentId
		}

		if err != nil {
			return nil, fmt.Errorf("moderator synthesis failed: %w", err)
		}
		debateResult.ModeratorSynthesis = synthesis
		if !debateResult.ConsensusAchieved {
			debateResult.Consensus = synthesis
		}
	} else {
		// Use final round synthesis as consensus
		if len(debateResult.Rounds) > 0 {
			lastRound := debateResult.Rounds[len(debateResult.Rounds)-1]
			if !debateResult.ConsensusAchieved {
				debateResult.Consensus = lastRound.Synthesis
			}
		}
	}

	// Set merged output to final consensus
	result.MergedOutput = debateResult.Consensus

	// Attach debate-specific result
	result.CollaborationResult = &loomv1.WorkflowResult_DebateResult{
		DebateResult: debateResult,
	}

	// Calculate collaboration metrics
	result.Metrics = d.calculateMetrics(debateResult)

	// Set metadata
	result.Metadata["topic"] = config.Topic
	result.Metadata["rounds"] = fmt.Sprintf("%d", config.Rounds)
	result.Metadata["agent_count"] = fmt.Sprintf("%d", len(config.AgentIds))
	result.Metadata["consensus_achieved"] = fmt.Sprintf("%t", debateResult.ConsensusAchieved)
	if d.handoffPolicy(config) != nil {
		result.Metadata["debate.response_prompts_bounded"] = fmt.Sprintf("%d", responseStats.bounded)
		result.Metadata["debate.response_prompts_over_budget"] = fmt.Sprintf("%d", responseStats.overBudget)
		result.Metadata["debate.response_positions_truncated"] = fmt.Sprintf("%d", responseStats.truncated)
		result.Metadata["debate.response_positions_reduced"] = fmt.Sprintf("%d", responseStats.reduced)
	}

	duration := time.Since(startTime)
	d.logger.Info("Debate completed",
		zap.Duration("duration", duration),
		zap.Bool("consensus_achieved", debateResult.ConsensusAchieved),
		zap.Int("total_rounds", len(debateResult.Rounds)))

	return result, nil
}

// executeRound runs a single round of debate where all agents present positions.
// The returned stats describe the bounded response prompts of this round.
func (d *DebateOrchestrator) executeRound(ctx context.Context, workflowID string, roundNum int32, config *loomv1.DebatePattern, history []string, moderator *agent.Agent, cost *loomv1.WorkflowCost) (*loomv1.DebateRound, debateHandoffStats, error) {
	var stats debateHandoffStats
	round := &loomv1.DebateRound{
		RoundNumber:      roundNum,
		Positions:        make([]*loomv1.AgentPosition, 0),
		ConsensusReached: false,
	}

	// Build context with previous rounds
	contextPrompt := d.buildDebateContext(config.Topic, roundNum, history)

	// Collect positions from all agents
	positions := make(map[string]*loomv1.AgentPosition)
	for _, agentID := range config.AgentIds {
		position, err := d.getAgentPosition(ctx, workflowID, agentID, contextPrompt, roundNum, cost)
		if err != nil {
			return nil, stats, fmt.Errorf("agent %s failed: %w", agentID, err)
		}
		positions[agentID] = position
		round.Positions = append(round.Positions, position)
	}

	// Let agents respond to each other's positions (second pass)
	if roundNum > 1 {
		for _, agentID := range config.AgentIds {
			responses, fit, err := d.getAgentResponses(ctx, workflowID, agentID, config, positions, roundNum, cost)
			stats.add(fit)
			if err != nil {
				if errors.Is(err, ErrHandoffOverBudget) {
					// The policy asked for failure (or cannot fit): fatal.
					return nil, stats, fmt.Errorf("round %d response prompt for %s: %w", roundNum, agentID, err)
				}
				// A failed response turn is non-fatal: the position stands.
				d.logger.Warn("Debate response turn failed; continuing without it",
					zap.String("workflow_id", workflowID),
					zap.Int32("round", roundNum),
					zap.String("agent_id", agentID),
					zap.Error(err))
				continue
			}
			positions[agentID].Responses = responses
		}
	}

	// Synthesize round
	round.Synthesis = d.synthesizeRound(round)

	// Check for consensus: the heuristic decides unless the moderator's
	// decision layer has a live band for the site.
	round.ConsensusReached = d.decideConsensus(ctx, workflowID, roundNum, config.Topic, round, moderator)

	return round, stats, nil
}

// decideConsensus resolves a round's consensus flag (site debate.consensus).
// The internal moderator's decision layer is borrowed: with a live band the
// decider reads the positions and its verdict is used when the band is
// cleared; otherwise the confidence-mean heuristic decides and the decider's
// answer is recorded against it. Under a TIGHTEN_ONLY band (the default when
// the band leaves mode unset, sites.ConsensusDefaultMode) the decider may
// only withhold consensus (keep the debate going), never declare it; mode:
// replace lets it declare consensus too.
func (d *DebateOrchestrator) decideConsensus(ctx context.Context, workflowID string, roundNum int32, topic string, round *loomv1.DebateRound, moderator *agent.Agent) bool {
	heuristic := d.checkConsensus(round)
	if moderator == nil || moderator.DecisionRouter() == nil || len(round.Positions) < 2 {
		return heuristic
	}
	sessionID := fmt.Sprintf("%s-round%d-consensus", workflowID, roundNum)
	req, err := sites.ConsensusRequest(topic, consensusPositions(round.Positions))
	if err != nil {
		// Not asked (an over-limit topic or position included): the
		// heuristic decides.
		d.logger.Debug("decision: consensus not asked, heuristic decides", zap.Error(err))
		return heuristic
	}
	refs := sites.ConsensusReference(heuristic)

	band := moderator.DecisionRouter().Band(sites.SiteDebateConsensus)
	if band.Shadow {
		moderator.RunDecisionShadow(ctx, sessionID, req, refs)
		return heuristic
	}

	out := moderator.LiveDecide(ctx, sessionID, req)
	if out.Act() {
		reached, ok := sites.ConsensusVerdict(out.Response, out.Band)
		if !ok {
			// No usable or decisive answer: the heuristic decides.
			out.Path = loomv1.DecisionPath_DECISION_PATH_FALLBACK
		}
		if ok {
			tightenBlocked := reached && band.ModeOr(sites.ConsensusDefaultMode) == loomv1.DecisionBandMode_DECISION_BAND_MODE_TIGHTEN_ONLY
			if !tightenBlocked {
				d.logger.Info("Consensus judged by decision layer",
					zap.Int32("round", roundNum),
					zap.Bool("consensus", reached),
					zap.Bool("heuristic", heuristic),
					zap.Float64("confidence", out.Confidence),
					zap.Duration("latency", out.Latency))
				moderator.RecordDecisionAsync(ctx, sessionID, req, out, nil)
				return reached
			}
			out.Path = loomv1.DecisionPath_DECISION_PATH_FALLBACK
		}
	}
	moderator.RecordDecisionAsync(ctx, sessionID, req, out, refs)
	return heuristic
}

// consensusPositions renders a round's positions for the decider.
func consensusPositions(positions []*loomv1.AgentPosition) []sites.Position {
	out := make([]sites.Position, 0, len(positions))
	for _, p := range positions {
		if p == nil {
			continue
		}
		out = append(out, sites.Position{
			AgentID:    p.AgentId,
			Position:   p.Position,
			Arguments:  p.Arguments,
			Confidence: float64(p.Confidence),
		})
	}
	return out
}

// getInternalModerator retrieves or uses first debating agent as internal moderator.
// The internal moderator is used for summarization and synthesis tasks.
func (d *DebateOrchestrator) getInternalModerator(ctx context.Context, config *loomv1.DebatePattern) (*agent.Agent, error) {
	// If user specified a moderator, use that
	if config.ModeratorAgentId != "" {
		moderator, err := d.provider.GetAgent(ctx, config.ModeratorAgentId)
		if err != nil {
			d.logger.Warn("User-specified moderator not found, falling back to first debater",
				zap.String("moderator_id", config.ModeratorAgentId),
				zap.Error(err))
		} else {
			d.logger.Info("Using user-specified moderator for internal summarization",
				zap.String("moderator_id", config.ModeratorAgentId))
			return moderator, nil
		}
	}

	// Fallback: use first debating agent as moderator for summarization
	if len(config.AgentIds) > 0 {
		moderator, err := d.provider.GetAgent(ctx, config.AgentIds[0])
		if err != nil {
			return nil, fmt.Errorf("cannot get agent for moderator role: %w", err)
		}
		d.logger.Info("Using first debater as internal moderator",
			zap.String("moderator_id", config.AgentIds[0]))
		return moderator, nil
	}

	return nil, fmt.Errorf("no agents available for moderator role")
}

// standpointGuidance is the one instruction the debate adds about how to
// argue. The standpoint itself comes from the agent's own configuration; the
// orchestrator does not assign perspectives. An earlier version guessed a
// perspective from the agent id's suffix and told unknown agents to "approach
// this from your unique angle, considering aspects other agents might
// overlook", and under that framing two agents configured to take opposite
// sides argued the same side in 11 of 15 debates.
const standpointGuidance = `Take the standpoint set out in your own instructions and hold it. Where your instructions assign you a side, argue that side, even if you would personally choose otherwise. Present the strongest honest case for it. Other participants may argue the opposite; do not move toward their conclusions to reduce disagreement.`

// getAgentPosition gets an agent's position on the debate topic.
func (d *DebateOrchestrator) getAgentPosition(ctx context.Context, workflowID, agentID, contextPrompt string, roundNum int32, cost *loomv1.WorkflowCost) (*loomv1.AgentPosition, error) {
	// Start agent-level span with hierarchical naming
	ctx, agentSpan := d.tracer.StartSpan(ctx, fmt.Sprintf("debate.agent.%s.position", agentID))
	defer d.tracer.EndSpan(agentSpan)

	// Generate unique session ID for this agent's position
	sessionID := fmt.Sprintf("%s-round%d-%s-position", workflowID, roundNum, agentID)

	if agentSpan != nil {
		agentSpan.SetAttribute("agent.id", agentID)
		agentSpan.SetAttribute("agent.round", fmt.Sprintf("%d", roundNum))
		agentSpan.SetAttribute("agent.role", "debater")
		agentSpan.SetAttribute("agent.session_id", sessionID)
	}

	a, err := d.provider.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("agent not found: %s: %w", agentID, err)
	}

	// Get model and provider info from agent
	model := a.GetLLMModel()
	provider := a.GetLLMProviderName()

	if agentSpan != nil {
		agentSpan.SetAttribute("agent.model", model)
		agentSpan.SetAttribute("agent.provider", provider)
	}

	// Construct debate prompt. The standpoint comes from the agent's own
	// instructions; the orchestrator only asks the agent to hold it.
	prompt := fmt.Sprintf(`%s

%s

State your position on this topic. Structure your response as:

POSITION: [Your clear stance/conclusion]

ARGUMENTS:
1. [First supporting argument]
2. [Second supporting argument]
3. [Third supporting argument]

CONFIDENCE: [0-100]

Be specific and evidence-based. Anticipate the strongest objection to your position and answer it; do not soften or abandon your position to accommodate it.`, contextPrompt, standpointGuidance)

	// Execute agent with session ID for database persistence
	resp, err := a.Chat(ctx, sessionID, prompt)
	if err != nil {
		return nil, fmt.Errorf("agent execution failed: %w", err)
	}
	addDebateUsage(cost, agentID, resp.Usage)

	// Extract tool usage from response
	toolsUsed := make([]string, 0)
	toolCallCount := int32(0)          // #nosec G115 -- literal constant
	toolNames := make(map[string]bool) // deduplicate tool names
	for _, toolExec := range resp.ToolExecutions {
		toolNames[toolExec.ToolName] = true
		toolCallCount++
	}
	for toolName := range toolNames {
		toolsUsed = append(toolsUsed, toolName)
	}

	// Parse response
	position, arguments, confidence := d.parseAgentResponse(resp.Content)

	// Add comprehensive metrics to span
	if agentSpan != nil {
		agentSpan.SetAttribute("agent.confidence", fmt.Sprintf("%.2f", confidence/100.0))
		agentSpan.SetAttribute("agent.num_arguments", fmt.Sprintf("%d", len(arguments)))
		agentSpan.SetAttribute("agent.tools_used", fmt.Sprintf("%d", len(toolsUsed)))
		agentSpan.SetAttribute("agent.tool_call_count", fmt.Sprintf("%d", toolCallCount))
		if len(toolsUsed) > 0 {
			agentSpan.SetAttribute("agent.tool_names", strings.Join(toolsUsed, ","))
		}
		if resp.Thinking != "" {
			agentSpan.SetAttribute("agent.has_thinking", "true")
			agentSpan.SetAttribute("agent.thinking_length", fmt.Sprintf("%d", len(resp.Thinking)))
		} else {
			agentSpan.SetAttribute("agent.has_thinking", "false")
		}
		agentSpan.SetAttribute("agent.position_length", fmt.Sprintf("%d", len(position)))
	}

	return &loomv1.AgentPosition{
		AgentId:       agentID,
		Position:      position,
		Arguments:     arguments,
		Confidence:    float32(confidence) / 100.0,
		Responses:     make(map[string]string),
		Thinking:      resp.Thinking,
		ToolsUsed:     toolsUsed,
		ToolCallCount: toolCallCount,
		Model:         model,
		Provider:      provider,
		InputTokens:   types.SafeInt32(resp.Usage.InputTokens),
		OutputTokens:  types.SafeInt32(resp.Usage.OutputTokens),
		TotalTokens:   types.SafeInt32(resp.Usage.TotalTokens),
		CostUsd:       resp.Usage.CostUSD,
	}, nil
}

// getAgentResponses gets an agent's response to the other agents' positions
// of this round. The other positions are rendered in config.AgentIds order.
// Under a join policy (with a bounder) each is stored in SharedMemory, the
// inline copy is capped and the whole prompt is fitted to the responding
// agent's token budget; the returned HandoffResult says what that did (nil
// without a policy).
func (d *DebateOrchestrator) getAgentResponses(ctx context.Context, workflowID, agentID string, config *loomv1.DebatePattern, positions map[string]*loomv1.AgentPosition, roundNum int32, cost *loomv1.WorkflowCost) (map[string]string, *HandoffResult, error) {
	a, err := d.provider.GetAgent(ctx, agentID)
	if err != nil {
		return nil, nil, fmt.Errorf("agent not found: %s: %w", agentID, err)
	}

	// Generate unique session ID for this agent's responses
	sessionID := fmt.Sprintf("%s-round%d-%s-response", workflowID, roundNum, agentID)

	myPosition := positions[agentID]
	responses := make(map[string]string)
	policy := d.handoffPolicy(config)

	// The other agents' positions, in configured order.
	entries := make([]HandoffEntry, 0, len(positions))
	for i, otherID := range config.AgentIds {
		pos, ok := positions[otherID]
		if !ok || otherID == agentID {
			continue
		}
		entry := HandoffEntry{
			Index:   i + 1,
			AgentID: otherID,
			Text:    fmt.Sprintf("Position: %s\nArguments:\n%s", pos.Position, strings.Join(pos.Arguments, "\n")),
		}
		if policy != nil {
			entry.Key = positionKey(workflowID, roundNum, otherID)
			entry.Stored = d.positionStored(workflowID, entry.Key)
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return responses, nil, nil
	}

	render := func(pieces []HandoffPiece) string {
		others := make([]string, 0, len(pieces))
		for _, p := range pieces {
			section := fmt.Sprintf("Agent %s:\n%s", p.AgentID, p.Inline)
			if p.Notice != "" {
				section += "\n" + p.Notice
			}
			others = append(others, section)
		}
		return SharedMemoryHeader(pieces, "positions", workflowID) + fmt.Sprintf(`You previously took this position:
%s

Other agents have presented these positions:
%s

Respond briefly to the key points the other agents raised: which do you challenge, and why; which do you concede. Conceding a point is not adopting the other side's position. Hold the standpoint set out in your instructions unless those instructions tell you to change your mind on the evidence.`,
			myPosition.Position,
			strings.Join(others, "\n\n---\n\n"))
	}

	var (
		prompt string
		fit    *HandoffResult
	)
	if policy != nil {
		fit, err = d.bounder.BoundHandoff(ctx, &HandoffRequest{
			WorkflowID: workflowID,
			Pattern:    "debate",
			Scope:      fmt.Sprintf("round-%d-response-%s", roundNum, agentID),
			Receiver:   a,
			Policy:     policy,
			Task:       config.Topic,
			Entries:    entries,
			Render:     render,
		})
		if err != nil {
			return nil, nil, err
		}
		d.markPositionsStored(workflowID, entries)
		addHandoffUsage(cost, agentID, fit)
		prompt = fit.Prompt
	} else {
		prompt = render(unboundedPieces(entries))
	}

	resp, err := a.Chat(ctx, sessionID, prompt)
	if err != nil {
		return nil, fit, err
	}
	addDebateUsage(cost, agentID, resp.Usage)

	// For simplicity, use full response as general response
	responses["all"] = resp.Content

	return responses, fit, nil
}

// unboundedPieces renders entries as full, unstored pieces: the pre-policy
// behaviour.
func unboundedPieces(entries []HandoffEntry) []HandoffPiece {
	pieces := make([]HandoffPiece, len(entries))
	for i, e := range entries {
		pieces[i] = HandoffPiece{Index: e.Index, AgentID: e.AgentID, Inline: e.Text, FullSize: len(e.Text)}
	}
	return pieces
}

// parseAgentResponse extracts position, arguments, and confidence from agent output.
func (d *DebateOrchestrator) parseAgentResponse(text string) (string, []string, float64) {
	position := ""
	arguments := make([]string, 0)
	confidence := 75.0 // default

	lines := strings.Split(text, "\n")
	inArguments := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(strings.ToUpper(line), "POSITION:") {
			position = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "POSITION:"), "position:"))
		} else if strings.HasPrefix(strings.ToUpper(line), "ARGUMENTS:") {
			inArguments = true
		} else if strings.HasPrefix(strings.ToUpper(line), "CONFIDENCE:") {
			confStr := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "CONFIDENCE:"), "confidence:"))
			_, _ = fmt.Sscanf(confStr, "%f", &confidence)
			inArguments = false
		} else if inArguments && line != "" {
			// Remove bullet points/numbers
			arg := strings.TrimLeft(line, "0123456789.-*• ")
			if arg != "" {
				arguments = append(arguments, arg)
			}
		}
	}

	// Fallback: if no structured parsing, use full text as position
	if position == "" {
		position = text
	}

	return position, arguments, confidence
}

// buildDebateContext constructs the debate prompt with history.
func (d *DebateOrchestrator) buildDebateContext(topic string, roundNum int32, history []string) string {
	context := fmt.Sprintf("# Debate Topic\n\n%s\n\n# Round %d\n\n", topic, roundNum)

	if len(history) > 0 {
		context += "## Previous Rounds\n\n"
		for i, h := range history {
			context += fmt.Sprintf("### Round %d\n%s\n\n", i+1, h)
		}
	} else {
		context += "This is the opening round. Present your initial position.\n\n"
	}

	return context
}

// formatRoundHistory formats a round for inclusion in next round's context.
// It creates concise summaries instead of including full positions to keep context manageable.
// Uses the moderator agent for LLM-guided summarization.
func (d *DebateOrchestrator) formatRoundHistory(ctx context.Context, workflowID string, round *loomv1.DebateRound, moderator *agent.Agent, cost *loomv1.WorkflowCost) string {
	var sb strings.Builder

	for _, pos := range round.Positions {
		// Extract key points from position using moderator for LLM-guided summarization
		summary := d.summarizePosition(ctx, workflowID, pos.AgentId, pos.Position, pos.Arguments, moderator, cost)
		sb.WriteString(fmt.Sprintf("**Agent %s** (confidence: %.0f%%):\n%s\n\n",
			pos.AgentId, pos.Confidence*100, summary))
	}

	if round.Synthesis != "" {
		sb.WriteString(fmt.Sprintf("**Synthesis**: %s\n", round.Synthesis))
	}

	return sb.String()
}

// summarizePosition creates a concise summary of an agent's position using moderator-guided LLM summarization.
// If the position is short enough, it returns it as-is. Otherwise, it uses the moderator to create an intelligent summary.
func (d *DebateOrchestrator) summarizePosition(ctx context.Context, workflowID, agentID, position string, arguments []string, moderator *agent.Agent, cost *loomv1.WorkflowCost) string {
	// If position is already short, no need to summarize
	if len(position) <= 250 && len(arguments) <= 2 {
		summary := position
		if len(arguments) > 0 {
			summary += "\nKey points:"
			for _, arg := range arguments {
				if len(arg) > 150 {
					arg = arg[:147] + "..."
				}
				summary += fmt.Sprintf("\n- %s", arg)
			}
		}
		return summary
	}

	// Use moderator agent to create an intelligent summary
	if moderator == nil {
		if d.logger != nil {
			d.logger.Warn("No moderator available for LLM summarization, using fallback")
		}
		return d.fallbackSummary(position, arguments)
	}

	// Build summarization prompt
	prompt := fmt.Sprintf(`Summarize this debate position into 2-3 concise sentences (max 200 chars) that capture the core argument:

Agent: %s

POSITION: %s

ARGUMENTS:
%s

Provide only the summary, no preamble or commentary.`, agentID, position, d.formatArgumentsList(arguments))

	// Generate session ID for this summarization
	sessionID := fmt.Sprintf("%s-moderator-summary-%s", workflowID, uuid.New().String()[:8])

	// Call moderator LLM for summary
	resp, err := moderator.Chat(ctx, sessionID, prompt)
	if err != nil {
		if d.logger != nil {
			d.logger.Warn("Moderator summarization failed, using fallback",
				zap.String("agent_id", agentID),
				zap.Error(err))
		}
		return d.fallbackSummary(position, arguments)
	}
	addDebateUsage(cost, moderator.GetName(), resp.Usage)

	// Use LLM-generated summary
	summary := strings.TrimSpace(resp.Content)

	// Ensure summary isn't too long (safety check)
	if len(summary) > 300 {
		if idx := strings.Index(summary[250:300], ". "); idx != -1 {
			summary = summary[:250+idx+1]
		} else {
			summary = summary[:297] + "..."
		}
	}

	return summary
}

// fallbackSummary provides a simple truncation-based summary as fallback.
func (d *DebateOrchestrator) fallbackSummary(position string, arguments []string) string {
	summary := position
	if len(summary) > 250 {
		if idx := strings.Index(summary[200:250], ". "); idx != -1 {
			summary = summary[:200+idx+1]
		} else {
			summary = summary[:250] + "..."
		}
	}

	if len(arguments) > 0 {
		summary += "\nKey points:"
		for i, arg := range arguments {
			if i >= 2 {
				break
			}
			if len(arg) > 150 {
				arg = arg[:147] + "..."
			}
			summary += fmt.Sprintf("\n- %s", arg)
		}
	}

	return summary
}

// formatArgumentsList formats arguments for the summarization prompt.
func (d *DebateOrchestrator) formatArgumentsList(arguments []string) string {
	if len(arguments) == 0 {
		return "(no explicit arguments provided)"
	}
	var sb strings.Builder
	for i, arg := range arguments {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, arg))
	}
	return sb.String()
}

// synthesizeRound creates a summary of the round's positions.
// It identifies common themes and areas of divergence.
func (d *DebateOrchestrator) synthesizeRound(round *loomv1.DebateRound) string {
	if len(round.Positions) == 0 {
		return "No positions presented."
	}

	var sb strings.Builder

	// Count agents with high vs low confidence
	highConfCount := 0
	lowConfCount := 0
	totalConf := float32(0.0)

	for _, pos := range round.Positions {
		totalConf += pos.Confidence
		if pos.Confidence >= 0.75 {
			highConfCount++
		} else if pos.Confidence < 0.60 {
			lowConfCount++
		}
	}

	avgConf := totalConf / float32(len(round.Positions))

	// Summary header
	if highConfCount == len(round.Positions) {
		sb.WriteString(fmt.Sprintf("All %d agents expressed high confidence (avg %.0f%%). ", len(round.Positions), avgConf*100))
	} else if lowConfCount > len(round.Positions)/2 {
		sb.WriteString(fmt.Sprintf("Agents showed uncertainty (avg %.0f%% confidence). ", avgConf*100))
	} else {
		sb.WriteString(fmt.Sprintf("%d agents participated (avg %.0f%% confidence). ", len(round.Positions), avgConf*100))
	}

	// List concise positions
	sb.WriteString("Positions: ")
	for i, pos := range round.Positions {
		if i > 0 {
			sb.WriteString("; ")
		}
		// Get first sentence or 100 chars
		posSum := pos.Position
		if len(posSum) > 100 {
			if idx := strings.Index(posSum[:100], ". "); idx != -1 {
				posSum = posSum[:idx+1]
			} else {
				posSum = posSum[:97] + "..."
			}
		}
		sb.WriteString(fmt.Sprintf("%s (%.0f%%)", posSum, pos.Confidence*100))
	}

	return sb.String()
}

// checkConsensus checks if agents have reached consensus.
func (d *DebateOrchestrator) checkConsensus(round *loomv1.DebateRound) bool {
	if len(round.Positions) == 0 {
		return false
	}

	// Simple heuristic: consensus if all positions are very similar
	// and all confidences are high (>0.8)
	avgConfidence := float32(0.0)
	for _, pos := range round.Positions {
		avgConfidence += pos.Confidence
	}
	avgConfidence /= float32(len(round.Positions))

	// Consensus requires high confidence
	return avgConfidence >= 0.8
}

// debateSynthesisInstruction closes the moderator's synthesis prompt.
const debateSynthesisInstruction = `Synthesize this debate into a clear consensus or identify the strongest position. Consider:
- Points of agreement across agents
- Strongest evidence-based arguments
- Areas of disagreement and why
- Most practical/actionable conclusion

Provide a concise synthesis that captures the essence of the debate and identifies the best path forward.`

// synthesizeWithModerator uses a moderator agent to synthesize final
// consensus from every round's positions. Under a join policy (with a
// bounder) each position is stored in SharedMemory, the inline copy is
// capped and the whole prompt is fitted to the moderator's token budget;
// join_policy.prompt_template, when set, replaces the default wording
// ({{prompt}} = topic, {{branches}} = the rendered rounds, {{branch_count}}
// = number of positions). The returned HandoffResult is nil without a policy.
func (d *DebateOrchestrator) synthesizeWithModerator(ctx context.Context, workflowID string, config *loomv1.DebatePattern, result *loomv1.DebateResult, cost *loomv1.WorkflowCost) (string, *HandoffResult, error) {
	moderator, err := d.provider.GetAgent(ctx, config.ModeratorAgentId)
	if err != nil {
		return "", nil, fmt.Errorf("moderator agent not found: %s: %w", config.ModeratorAgentId, err)
	}

	// Generate unique session ID for moderator synthesis
	sessionID := fmt.Sprintf("%s-moderator-synthesis", workflowID)
	policy := d.handoffPolicy(config)

	// One entry per position across all rounds, in round then configured
	// order; roundOf and confidenceOf let the renderer group them.
	entries := make([]HandoffEntry, 0)
	roundOf := make(map[int]int32)
	confidenceOf := make(map[int]float32)
	for _, round := range result.Rounds {
		for _, pos := range round.Positions {
			text := pos.Position
			if len(pos.Arguments) > 0 {
				text += fmt.Sprintf("\n\nArguments: %s", strings.Join(pos.Arguments, "; "))
			}
			idx := len(entries) + 1
			entry := HandoffEntry{Index: idx, AgentID: pos.AgentId, Text: text}
			if policy != nil {
				entry.Key = positionKey(workflowID, round.RoundNumber, pos.AgentId)
				entry.Stored = d.positionStored(workflowID, entry.Key)
			}
			entries = append(entries, entry)
			roundOf[idx] = round.RoundNumber
			confidenceOf[idx] = pos.Confidence
		}
	}

	render := func(pieces []HandoffPiece) string {
		var rounds strings.Builder
		current := int32(-1)
		for _, p := range pieces {
			if r := roundOf[p.Index]; r != current {
				if current != -1 {
					rounds.WriteString("\n")
				}
				rounds.WriteString(fmt.Sprintf("## Round %d\n", r))
				current = r
			}
			rounds.WriteString(fmt.Sprintf("**%s** (%.0f%% confidence):\n%s\n", p.AgentID, confidenceOf[p.Index]*100, p.Inline))
			if p.Notice != "" {
				rounds.WriteString(p.Notice)
				rounds.WriteString("\n")
			}
			rounds.WriteString("\n")
		}
		header := SharedMemoryHeader(pieces, "positions", workflowID)
		if policy != nil && strings.TrimSpace(policy.PromptTemplate) != "" {
			return header + SpliceTemplate(policy.PromptTemplate, config.Topic, rounds.String(), len(pieces))
		}
		return header + fmt.Sprintf("# Debate Topic\n%s\n\n# All Debate Rounds\n\n%s\n%s", config.Topic, rounds.String(), debateSynthesisInstruction)
	}

	var (
		prompt string
		fit    *HandoffResult
	)
	if policy != nil {
		fit, err = d.bounder.BoundHandoff(ctx, &HandoffRequest{
			WorkflowID: workflowID,
			Pattern:    "debate",
			Scope:      "moderator-synthesis",
			Receiver:   moderator,
			Policy:     policy,
			Task:       config.Topic,
			Entries:    entries,
			Render:     render,
		})
		if err != nil {
			return "", nil, err
		}
		d.markPositionsStored(workflowID, entries)
		addHandoffUsage(cost, config.ModeratorAgentId, fit)
		prompt = fit.Prompt
	} else {
		prompt = render(unboundedPieces(entries))
	}

	resp, err := moderator.Chat(ctx, sessionID, prompt)
	if err != nil {
		return "", fit, fmt.Errorf("moderator synthesis failed: %w", err)
	}
	addDebateUsage(cost, config.ModeratorAgentId, resp.Usage)

	return resp.Content, fit, nil
}

// calculateMetrics computes collaboration quality metrics.
func (d *DebateOrchestrator) calculateMetrics(result *loomv1.DebateResult) *loomv1.CollaborationMetrics {
	if len(result.Rounds) == 0 {
		return &loomv1.CollaborationMetrics{}
	}

	// Calculate perspective diversity (variance in positions)
	diversityScore := float32(0.7) // Placeholder: would need semantic analysis

	// Calculate agreement level (average confidence)
	totalConfidence := float32(0.0)
	count := 0
	for _, round := range result.Rounds {
		for _, pos := range round.Positions {
			totalConfidence += pos.Confidence
			count++
		}
	}
	agreementLevel := float32(0.5)
	if count > 0 {
		agreementLevel = totalConfidence / float32(count)
	}

	// Calculate interaction metrics
	interactionCount := int32(0)    // #nosec G115 -- literal constant
	totalResponseLength := int32(0) // #nosec G115 -- literal constant
	for _, round := range result.Rounds {
		interactionCount += types.SafeInt32(len(round.Positions))
		for _, pos := range round.Positions {
			totalResponseLength += types.SafeInt32(len(pos.Position))
		}
	}

	avgResponseLength := int32(0)
	if interactionCount > 0 {
		avgResponseLength = totalResponseLength / interactionCount
	}

	return &loomv1.CollaborationMetrics{
		PerspectiveDiversity: diversityScore,
		AgreementLevel:       agreementLevel,
		AvgResponseLength:    avgResponseLength,
		InteractionCount:     interactionCount,
		TimeToConsensusMs:    0,   // Would need timing tracking
		ConfidenceVariance:   0.2, // Placeholder
	}
}
