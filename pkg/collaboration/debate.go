// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// DebateOrchestrator manages multi-agent debates with round-by-round tracking.
type DebateOrchestrator struct {
	provider AgentProvider
	tracer   observability.Tracer
	logger   *zap.Logger
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
	}
}

// Execute runs a multi-agent debate with specified rounds and topic.
func (d *DebateOrchestrator) Execute(ctx context.Context, config *loomv1.DebatePattern) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	// Generate unique workflow ID for session tracking
	workflowID := fmt.Sprintf("debate-%s", uuid.New().String()[:8])

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

		round, err := d.executeRound(ctx, workflowID, roundNum, config, debateHistory)

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
		debateHistory = append(debateHistory, d.formatRoundHistory(ctx, workflowID, round, internalModerator))

		// Accumulate agent results and costs
		for _, position := range round.Positions {
			agentResult := &loomv1.AgentResult{
				AgentId:         position.AgentId,
				Output:          position.Position,
				ConfidenceScore: position.Confidence,
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

		synthesis, err := d.synthesizeWithModerator(ctx, workflowID, config, debateResult)

		if modSpan != nil {
			if err != nil {
				modSpan.SetAttribute("moderator.status", "failed")
				modSpan.RecordError(err)
			} else {
				modSpan.SetAttribute("moderator.status", "completed")
				modSpan.SetAttribute("moderator.synthesis_length", fmt.Sprintf("%d", len(synthesis)))
			}
		}
		d.tracer.EndSpan(modSpan)

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

	duration := time.Since(startTime)
	d.logger.Info("Debate completed",
		zap.Duration("duration", duration),
		zap.Bool("consensus_achieved", debateResult.ConsensusAchieved),
		zap.Int("total_rounds", len(debateResult.Rounds)))

	return result, nil
}

// executeRound runs a single round of debate where all agents present positions.
func (d *DebateOrchestrator) executeRound(ctx context.Context, workflowID string, roundNum int32, config *loomv1.DebatePattern, history []string) (*loomv1.DebateRound, error) {
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
		position, err := d.getAgentPosition(ctx, workflowID, agentID, contextPrompt, roundNum)
		if err != nil {
			return nil, fmt.Errorf("agent %s failed: %w", agentID, err)
		}
		positions[agentID] = position
		round.Positions = append(round.Positions, position)
	}

	// Let agents respond to each other's positions (second pass)
	if roundNum > 1 {
		for _, agentID := range config.AgentIds {
			responses, err := d.getAgentResponses(ctx, workflowID, agentID, positions, contextPrompt, roundNum)
			if err != nil {
				// Non-fatal: log and continue
				continue
			}
			positions[agentID].Responses = responses
		}
	}

	// Synthesize round
	round.Synthesis = d.synthesizeRound(round)

	// Check for consensus (simple heuristic: all agents agree)
	round.ConsensusReached = d.checkConsensus(round)

	return round, nil
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

// generatePerspectiveGuidance creates agent-specific guidance to encourage diverse viewpoints.
func (d *DebateOrchestrator) generatePerspectiveGuidance(agentID string) string {
	// Extract perspective from agent ID (e.g., "td-expert-performance" -> "performance")
	parts := strings.Split(agentID, "-")
	perspective := parts[len(parts)-1]

	// Define perspective-specific guidance
	perspectives := map[string]string{
		"performance":  "Focus on performance optimization, speed, throughput, and efficiency metrics. Consider scalability and resource utilization. Prioritize quantifiable performance gains.",
		"analytics":    "Focus on data analysis, statistical validity, insights extraction, and analytical rigor. Consider data quality, sampling strategies, and analytical methodologies.",
		"quality":      "Focus on correctness, reliability, testing strategies, and quality assurance. Consider edge cases, error handling, validation approaches, and test coverage.",
		"architecture": "Focus on system design, modularity, maintainability, and architectural patterns. Consider long-term sustainability, technical debt, and design principles.",
		"transcend":    "Focus on integration capabilities, cross-system compatibility, and interoperability. Consider API design, data exchange formats, and system boundaries.",
		"security":     "Focus on security implications, threat modeling, access control, and vulnerability assessment. Consider attack surfaces and defense in depth.",
		"cost":         "Focus on resource costs, efficiency, budget constraints, and cost-benefit analysis. Consider TCO (total cost of ownership) and ROI.",
		"user":         "Focus on user experience, usability, accessibility, and end-user impact. Consider user workflows and adoption barriers.",
		"ops":          "Focus on operational concerns, deployment, monitoring, and production readiness. Consider observability, debugging, and incident response.",
	}

	// Return specific guidance or general guidance
	if guidance, ok := perspectives[perspective]; ok {
		return fmt.Sprintf("Your perspective: %s\n%s", cases.Title(language.English).String(perspective), guidance)
	}

	// Default: encourage unique perspective based on agent name
	return fmt.Sprintf("Your perspective: %s\nApproach this problem from your unique angle, considering aspects that other agents might overlook. Avoid generic responses.", agentID)
}

// getAgentPosition gets an agent's position on the debate topic.
func (d *DebateOrchestrator) getAgentPosition(ctx context.Context, workflowID, agentID, contextPrompt string, roundNum int32) (*loomv1.AgentPosition, error) {
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

	// Add agent-specific perspective to encourage diversity
	perspectiveGuidance := d.generatePerspectiveGuidance(agentID)

	// Construct debate prompt
	prompt := fmt.Sprintf(`%s

%s

Please provide your position on this topic. Structure your response as:

POSITION: [Your clear stance/conclusion]

ARGUMENTS:
1. [First supporting argument]
2. [Second supporting argument]
3. [Third supporting argument]

CONFIDENCE: [0-100]

Be specific, evidence-based, and consider alternative perspectives.`, contextPrompt, perspectiveGuidance)

	// Execute agent with session ID for database persistence
	resp, err := a.Chat(ctx, sessionID, prompt)
	if err != nil {
		return nil, fmt.Errorf("agent execution failed: %w", err)
	}

	// Extract tool usage from response
	toolsUsed := make([]string, 0)
	toolCallCount := int32(0)
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
	}, nil
}

// getAgentResponses gets agent responses to other agents' positions.
func (d *DebateOrchestrator) getAgentResponses(ctx context.Context, workflowID, agentID string, positions map[string]*loomv1.AgentPosition, contextPrompt string, roundNum int32) (map[string]string, error) {
	a, err := d.provider.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("agent not found: %s: %w", agentID, err)
	}

	// Generate unique session ID for this agent's responses
	sessionID := fmt.Sprintf("%s-round%d-%s-response", workflowID, roundNum, agentID)

	myPosition := positions[agentID]
	responses := make(map[string]string)

	// Build prompt with other agents' positions
	otherPositions := make([]string, 0)
	for otherID, pos := range positions {
		if otherID == agentID {
			continue
		}
		positionText := fmt.Sprintf("Agent %s:\nPosition: %s\nArguments:\n%s",
			otherID, pos.Position, strings.Join(pos.Arguments, "\n"))
		otherPositions = append(otherPositions, positionText)
	}

	if len(otherPositions) == 0 {
		return responses, nil
	}

	prompt := fmt.Sprintf(`You previously took this position:
%s

Other agents have presented these positions:
%s

Provide brief responses to the key points raised by other agents. What do you agree with? What do you challenge? What new insights emerge?`,
		myPosition.Position,
		strings.Join(otherPositions, "\n\n---\n\n"))

	resp, err := a.Chat(ctx, sessionID, prompt)
	if err != nil {
		return nil, err
	}

	// For simplicity, use full response as general response
	responses["all"] = resp.Content

	return responses, nil
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
func (d *DebateOrchestrator) formatRoundHistory(ctx context.Context, workflowID string, round *loomv1.DebateRound, moderator *agent.Agent) string {
	var sb strings.Builder

	for _, pos := range round.Positions {
		// Extract key points from position using moderator for LLM-guided summarization
		summary := d.summarizePosition(ctx, workflowID, pos.AgentId, pos.Position, pos.Arguments, moderator)
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
func (d *DebateOrchestrator) summarizePosition(ctx context.Context, workflowID, agentID, position string, arguments []string, moderator *agent.Agent) string {
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
	prompt := fmt.Sprintf(`As the debate moderator, summarize this agent's position into 2-3 concise sentences (max 200 chars) that capture the core argument:

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

// synthesizeWithModerator uses a moderator agent to synthesize final consensus.
func (d *DebateOrchestrator) synthesizeWithModerator(ctx context.Context, workflowID string, config *loomv1.DebatePattern, result *loomv1.DebateResult) (string, error) {
	moderator, err := d.provider.GetAgent(ctx, config.ModeratorAgentId)
	if err != nil {
		return "", fmt.Errorf("moderator agent not found: %s: %w", config.ModeratorAgentId, err)
	}

	// Generate unique session ID for moderator synthesis
	sessionID := fmt.Sprintf("%s-moderator-synthesis", workflowID)

	// Build synthesis prompt with all rounds
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Debate Topic\n%s\n\n", config.Topic))
	sb.WriteString("# All Debate Rounds\n\n")

	for _, round := range result.Rounds {
		sb.WriteString(fmt.Sprintf("## Round %d\n", round.RoundNumber))
		for _, pos := range round.Positions {
			sb.WriteString(fmt.Sprintf("**%s** (%.0f%% confidence):\n%s\n\n",
				pos.AgentId, pos.Confidence*100, pos.Position))
			if len(pos.Arguments) > 0 {
				sb.WriteString(fmt.Sprintf("Arguments: %s\n\n", strings.Join(pos.Arguments, "; ")))
			}
		}
		sb.WriteString("\n")
	}

	prompt := fmt.Sprintf(`%s

Synthesize this debate into a clear consensus or identify the strongest position. Consider:
- Points of agreement across agents
- Strongest evidence-based arguments
- Areas of disagreement and why
- Most practical/actionable conclusion

Provide a concise synthesis that captures the essence of the debate and identifies the best path forward.`, sb.String())

	resp, err := moderator.Chat(ctx, sessionID, prompt)
	if err != nil {
		return "", fmt.Errorf("moderator synthesis failed: %w", err)
	}

	return resp.Content, nil
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
	interactionCount := int32(0)
	totalResponseLength := int32(0)
	for _, round := range result.Rounds {
		interactionCount += int32(len(round.Positions))
		for _, pos := range round.Positions {
			totalResponseLength += int32(len(pos.Position))
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
