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
package learning

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication/interrupt"
	"github.com/teradata-labs/loom/pkg/observability"
)

// PatternReloader is an interface for pattern hot-reload functionality.
// This avoids circular dependency with pkg/patterns.
type PatternReloader interface {
	ManualReload(patternName string) error
}

// AutonomyLevel defines how autonomous the learning agent is
type AutonomyLevel int

const (
	// AutonomyManual requires human approval for all improvements
	AutonomyManual AutonomyLevel = 0

	// AutonomyHumanApproval applies improvements after human approval
	AutonomyHumanApproval AutonomyLevel = 1

	// AutonomyFull applies improvements automatically (with circuit breaker)
	AutonomyFull AutonomyLevel = 2
)

// LearningAgent implements the LearningAgentService proto interface.
// It provides autonomous self-improvement capabilities by monitoring
// pattern effectiveness and generating/applying improvements.
type LearningAgent struct {
	loomv1.UnimplementedLearningAgentServiceServer

	db               *sql.DB
	tracer           observability.Tracer
	engine           *LearningEngine
	tracker          *PatternEffectivenessTracker
	autonomyLevel    AutonomyLevel
	analysisInterval time.Duration

	// Declarative configuration (optional - set via NewLearningAgentFromConfig)
	config *loomv1.LearningAgentConfig

	// Pattern hot-reload (optional - set via SetPatternReloader)
	patternReloader PatternReloader

	// Interrupt channel (optional - set via SetInterruptChannel)
	// Enables interrupt-driven learning triggers (LEARNING_ANALYZE, etc.)
	interruptChannel *interrupt.InterruptChannel
	agentID          string // ID for interrupt handler registration

	// Circuit breaker state
	circuitBreaker *CircuitBreaker
	cbMu           sync.RWMutex

	// Analysis loop control
	stopChan chan struct{}
	wg       sync.WaitGroup
	started  bool
	mu       sync.Mutex

	// Self-trigger state
	executionCount   int64
	executionTrigger int64 // Trigger learning after N executions (0 = disabled)
	executionMu      sync.Mutex
}

// CircuitBreaker prevents runaway improvements
type CircuitBreaker struct {
	failureCount    int
	successCount    int
	lastFailureTime time.Time
	state           string // "closed", "open", "half-open"
	threshold       int    // Number of failures before opening
	cooldownPeriod  time.Duration
}

// NewLearningAgent creates a new learning agent with validation
func NewLearningAgent(
	db *sql.DB,
	tracer observability.Tracer,
	engine *LearningEngine,
	tracker *PatternEffectivenessTracker,
	autonomyLevel AutonomyLevel,
	analysisInterval time.Duration,
) (*LearningAgent, error) {
	// Validate required dependencies
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if tracer == nil {
		return nil, fmt.Errorf("tracer is required")
	}
	if engine == nil {
		return nil, fmt.Errorf("learning engine is required")
	}
	if tracker == nil {
		return nil, fmt.Errorf("pattern tracker is required")
	}

	// Set defaults
	if analysisInterval == 0 {
		analysisInterval = 1 * time.Hour
	}

	return &LearningAgent{
		db:               db,
		tracer:           tracer,
		engine:           engine,
		tracker:          tracker,
		autonomyLevel:    autonomyLevel,
		analysisInterval: analysisInterval,
		circuitBreaker: &CircuitBreaker{
			threshold:      5,
			cooldownPeriod: 30 * time.Minute,
			state:          "closed",
		},
		stopChan: make(chan struct{}),
	}, nil
}

// NewLearningAgentFromConfig creates a learning agent from proto configuration.
// This enables declarative YAML-based configuration.
func NewLearningAgentFromConfig(
	db *sql.DB,
	tracer observability.Tracer,
	engine *LearningEngine,
	tracker *PatternEffectivenessTracker,
	config *loomv1.LearningAgentConfig,
) (*LearningAgent, error) {
	// Validate required dependencies
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if tracer == nil {
		return nil, fmt.Errorf("tracer is required")
	}
	if engine == nil {
		return nil, fmt.Errorf("learning engine is required")
	}
	if tracker == nil {
		return nil, fmt.Errorf("pattern tracker is required")
	}
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	// Convert config to options
	autonomy, interval, cb, err := ToLearningAgentOptions(config)
	if err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	la := &LearningAgent{
		db:               db,
		tracer:           tracer,
		engine:           engine,
		tracker:          tracker,
		autonomyLevel:    autonomy,
		analysisInterval: interval,
		circuitBreaker:   cb,
		stopChan:         make(chan struct{}),
		config:           config,
	}

	return la, nil
}

// Start begins the autonomous analysis loop
func (la *LearningAgent) Start(ctx context.Context) error {
	la.mu.Lock()
	defer la.mu.Unlock()

	if la.started {
		return nil // Already started
	}

	_, span := la.tracer.StartSpan(ctx, "learning_agent.start")
	defer la.tracer.EndSpan(span)

	la.wg.Add(1)
	go la.analysisLoop()

	la.started = true
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Learning agent started"}
	return nil
}

// Stop gracefully stops the analysis loop
func (la *LearningAgent) Stop(ctx context.Context) error {
	la.mu.Lock()
	if !la.started {
		la.mu.Unlock()
		return nil
	}
	la.started = false
	la.mu.Unlock()

	_, span := la.tracer.StartSpan(ctx, "learning_agent.stop")
	defer la.tracer.EndSpan(span)

	close(la.stopChan)
	la.wg.Wait()

	span.Status = observability.Status{Code: observability.StatusOK, Message: "Learning agent stopped"}
	return nil
}

// SetPatternReloader sets the pattern reloader for this learning agent.
// Optional - if not set, improvements will be applied without automatic pattern reload.
// Accepts any type that implements PatternReloader interface (e.g., *patterns.HotReloader).
func (la *LearningAgent) SetPatternReloader(reloader PatternReloader) {
	la.patternReloader = reloader
}

// SetInterruptChannel sets the interrupt channel for this learning agent.
// Optional - if set, enables interrupt-driven learning triggers.
// The agentID is used to register interrupt handlers for learning signals.
// The executionTrigger specifies how many executions before auto-triggering learning (0 = disabled).
func (la *LearningAgent) SetInterruptChannel(ic *interrupt.InterruptChannel, agentID string, executionTrigger int64) error {
	la.interruptChannel = ic
	la.agentID = agentID
	la.executionTrigger = executionTrigger

	// Register interrupt handlers for learning signals
	if ic != nil {
		if err := la.registerLearningHandlers(); err != nil {
			return fmt.Errorf("failed to register learning handlers: %w", err)
		}
	}

	return nil
}

// registerLearningHandlers registers interrupt handlers for all learning signals.
// This is called automatically by SetInterruptChannel.
func (la *LearningAgent) registerLearningHandlers() error {
	if la.interruptChannel == nil {
		return fmt.Errorf("interrupt channel not set")
	}

	// Register handlers for each learning signal
	learningSignals := []interrupt.InterruptSignal{
		interrupt.SignalLearningAnalyze,
		interrupt.SignalLearningOptimize,
		interrupt.SignalLearningABTest,
		interrupt.SignalLearningProposal,
		interrupt.SignalLearningValidate,
		interrupt.SignalLearningExport,
		interrupt.SignalLearningSync,
	}

	for _, signal := range learningSignals {
		// All learning signals should wake the agent if DORMANT
		if err := la.interruptChannel.RegisterHandler(la.agentID, signal, la.handleLearningInterrupt, true); err != nil {
			return fmt.Errorf("failed to register handler for %s: %w", signal, err)
		}
	}

	return nil
}

// handleLearningInterrupt is the unified handler for all learning interrupt signals.
// It routes to the appropriate handler based on the signal type.
func (la *LearningAgent) handleLearningInterrupt(ctx context.Context, signal interrupt.InterruptSignal, payload []byte) error {
	// Start span for observability
	ctx, span := la.tracer.StartSpan(ctx, observability.SpanInterruptHandle,
		observability.WithAttribute(observability.AttrInterruptSignal, signal.String()),
		observability.WithAttribute("agent.id", la.agentID),
	)
	defer la.tracer.EndSpan(span)

	// Route to specific handler
	switch signal {
	case interrupt.SignalLearningAnalyze:
		return la.handleAnalyzeInterrupt(ctx, payload)
	case interrupt.SignalLearningOptimize:
		return la.handleOptimizeInterrupt(ctx, payload)
	case interrupt.SignalLearningABTest:
		return la.handleABTestInterrupt(ctx, payload)
	case interrupt.SignalLearningProposal:
		return la.handleProposalInterrupt(ctx, payload)
	case interrupt.SignalLearningValidate:
		return la.handleValidateInterrupt(ctx, payload)
	case interrupt.SignalLearningExport:
		return la.handleExportInterrupt(ctx, payload)
	case interrupt.SignalLearningSync:
		return la.handleSyncInterrupt(ctx, payload)
	default:
		return fmt.Errorf("unknown learning signal: %s", signal)
	}
}

// GetConfig returns the declarative configuration if set via NewLearningAgentFromConfig.
// Returns nil if the agent was created with NewLearningAgent (legacy constructor).
func (la *LearningAgent) GetConfig() *loomv1.LearningAgentConfig {
	return la.config
}

// IsEnabled returns whether this learning agent is enabled.
// If no config is set (legacy constructor), returns true by default.
func (la *LearningAgent) IsEnabled() bool {
	if la.config == nil {
		return true // Legacy behavior
	}
	return la.config.Enabled
}

// ShouldProcessDomain checks if this learning agent should process the given domain.
// If no domains are configured, processes all domains.
func (la *LearningAgent) ShouldProcessDomain(domain string) bool {
	if la.config == nil || len(la.config.Domains) == 0 {
		return true // Process all domains
	}
	for _, d := range la.config.Domains {
		if d == domain {
			return true
		}
	}
	return false
}

// IsAgentProtected checks if an agent is protected from auto-apply.
// Protected agents always require human approval regardless of autonomy level.
func (la *LearningAgent) IsAgentProtected(agentID string) bool {
	if la.config == nil || la.config.ImprovementPolicy == nil {
		return false
	}
	for _, protected := range la.config.ImprovementPolicy.ProtectedAgents {
		if protected == agentID {
			return true
		}
	}
	return false
}

// ShouldAutoApply checks if an improvement should be auto-applied based on config.
// Returns false if:
// - Autonomy level is not FULL
// - Agent is protected
// - Confidence is below threshold
// - Impact level exceeds max auto-apply impact
func (la *LearningAgent) ShouldAutoApply(improvement *loomv1.Improvement) bool {
	// Must be full autonomy
	if la.autonomyLevel != AutonomyFull {
		return false
	}

	// Check if agent is protected
	if la.IsAgentProtected(improvement.TargetAgentId) {
		return false
	}

	// Check config-based rules
	if la.config != nil && la.config.ImprovementPolicy != nil {
		policy := la.config.ImprovementPolicy

		// Check confidence threshold
		if improvement.Confidence < policy.AutoApplyMinConfidence {
			return false
		}

		// Check impact level
		if improvement.Impact > policy.MaxAutoApplyImpact {
			return false
		}
	}

	return true
}

// analysisLoop runs periodic pattern effectiveness analysis
func (la *LearningAgent) analysisLoop() {
	defer la.wg.Done()

	ticker := time.NewTicker(la.analysisInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx := context.Background()
			if err := la.runAnalysis(ctx); err != nil {
				errCtx, span := la.tracer.StartSpan(ctx, "learning_agent.analysis_error")
				span.RecordError(err)
				span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
				la.tracer.EndSpan(span)
				_ = errCtx
			}
		case <-la.stopChan:
			return
		}
	}
}

// runAnalysis performs one cycle of pattern analysis and improvement generation
func (la *LearningAgent) runAnalysis(ctx context.Context) error {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.run_analysis")
	defer la.tracer.EndSpan(span)

	// Check if enabled (for config-based agents)
	if !la.IsEnabled() {
		span.SetAttribute("enabled", false)
		return nil
	}

	// Check circuit breaker
	if !la.canProceed() {
		span.SetAttribute("circuit_breaker", "open")
		return fmt.Errorf("circuit breaker open, skipping analysis")
	}

	// Get domains to analyze
	domains := []string{"sql", "rest", "file", "document"}
	if la.config != nil && len(la.config.Domains) > 0 {
		domains = la.config.Domains
	}
	for _, domain := range domains {
		// Analyze pattern effectiveness
		_, err := la.AnalyzePatternEffectiveness(ctx, &loomv1.AnalyzePatternEffectivenessRequest{
			Domain:      domain,
			WindowHours: 24,
		})
		if err != nil {
			span.RecordError(err)
			continue
		}

		// Generate improvements
		resp, err := la.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
			Domain:       domain,
			MaxProposals: 10,
		})
		if err != nil {
			span.RecordError(err)
			continue
		}

		// Auto-apply if autonomy level and config permit
		for _, improvement := range resp.Improvements {
			if la.ShouldAutoApply(improvement) {
				_, err := la.ApplyImprovement(ctx, &loomv1.ApplyImprovementRequest{
					ImprovementId: improvement.Id,
				})
				if err != nil {
					la.recordCircuitBreakerFailure()
					span.RecordError(err)
				} else {
					la.recordCircuitBreakerSuccess()
				}
			}
		}
	}

	span.Status = observability.Status{Code: observability.StatusOK, Message: "Analysis completed"}
	return nil
}

// ============================================================================
// RPC Implementations
// ============================================================================

// AnalyzePatternEffectiveness analyzes runtime pattern performance
func (la *LearningAgent) AnalyzePatternEffectiveness(
	ctx context.Context,
	req *loomv1.AnalyzePatternEffectivenessRequest,
) (*loomv1.PatternAnalysisResponse, error) {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.analyze_pattern_effectiveness")
	defer la.tracer.EndSpan(span)

	span.SetAttribute("domain", req.Domain)
	span.SetAttribute("agent_id", req.AgentId)
	span.SetAttribute("window_hours", req.WindowHours)

	windowHours := req.WindowHours
	if windowHours == 0 {
		windowHours = 24
	}

	// Query pattern effectiveness from database
	windowStart := time.Now().Add(-time.Duration(windowHours) * time.Hour).Unix()

	query := `
		SELECT
			pattern_name, variant, domain,
			SUM(total_usages) as total_usages,
			SUM(success_count) as success_count,
			SUM(failure_count) as failure_count,
			AVG(success_rate) as avg_success_rate,
			AVG(avg_cost_usd) as avg_cost,
			AVG(avg_latency_ms) as avg_latency,
			AVG(judge_pass_rate) as avg_judge_pass_rate,
			AVG(judge_avg_score) as avg_judge_score,
			GROUP_CONCAT(judge_criterion_scores_json, '|||') as judge_criterion_scores_all,
			llm_provider, llm_model
		FROM pattern_effectiveness
		WHERE window_start >= ?
			AND domain = ?
	`
	args := []interface{}{windowStart, req.Domain}

	if req.AgentId != "" {
		query += " AND agent_id = ?"
		args = append(args, req.AgentId)
	}

	query += " GROUP BY pattern_name, variant, domain ORDER BY avg_success_rate DESC"

	rows, err := la.db.QueryContext(ctx, query, args...)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query pattern effectiveness: %w", err)
	}
	defer rows.Close()

	var patterns []*loomv1.PatternMetric
	totalUsages := int32(0)
	totalSuccess := int32(0)
	totalCost := 0.0

	for rows.Next() {
		var (
			patternName, variant, domain, llmProvider, llmModel string
			judgeCriterionScoresAll                             sql.NullString
			totalUses, successCount, failureCount               int32
			avgSuccessRate, avgCost, avgLatency                 float64
			avgJudgePassRate, avgJudgeScore                     sql.NullFloat64
		)

		err := rows.Scan(
			&patternName, &variant, &domain,
			&totalUses, &successCount, &failureCount,
			&avgSuccessRate, &avgCost, &avgLatency,
			&avgJudgePassRate, &avgJudgeScore, &judgeCriterionScoresAll,
			&llmProvider, &llmModel,
		)
		if err != nil {
			span.RecordError(err)
			continue
		}

		// Calculate confidence based on sample size
		confidence := calculateConfidence(int(totalUses))

		// Determine recommendation
		recommendation := determineRecommendation(avgSuccessRate, confidence)

		// Parse judge criterion scores (aggregate from multiple JSON strings)
		judgeCriterionScores := make(map[string]float64)
		if judgeCriterionScoresAll.Valid && judgeCriterionScoresAll.String != "" {
			// Parse concatenated JSON strings (GROUP_CONCAT separates with |||)
			jsonStrings := splitAndTrim(judgeCriterionScoresAll.String, "|||")
			criterionSums := make(map[string]float64)
			criterionCounts := make(map[string]int)

			for _, jsonStr := range jsonStrings {
				if jsonStr == "" {
					continue
				}
				var scores map[string]float64
				if err := json.Unmarshal([]byte(jsonStr), &scores); err == nil {
					for criterion, score := range scores {
						criterionSums[criterion] += score
						criterionCounts[criterion]++
					}
				}
			}

			// Calculate averages
			for criterion, sum := range criterionSums {
				count := criterionCounts[criterion]
				if count > 0 {
					judgeCriterionScores[criterion] = sum / float64(count)
				}
			}
		}

		metric := &loomv1.PatternMetric{
			PatternName:    patternName,
			Variant:        variant,
			Domain:         domain,
			TotalUsages:    totalUses,
			SuccessCount:   successCount,
			FailureCount:   failureCount,
			SuccessRate:    avgSuccessRate,
			AvgCostUsd:     avgCost,
			AvgLatencyMs:   int64(avgLatency),
			Confidence:     confidence,
			Recommendation: recommendation,
			LlmProvider:    llmProvider,
			LlmModel:       llmModel,
		}

		// Add judge metrics if available
		if avgJudgePassRate.Valid {
			metric.JudgePassRate = avgJudgePassRate.Float64
		}
		if avgJudgeScore.Valid {
			metric.JudgeAvgScore = avgJudgeScore.Float64
		}
		if len(judgeCriterionScores) > 0 {
			metric.JudgeCriterionScores = judgeCriterionScores
		}

		patterns = append(patterns, metric)

		totalUsages += totalUses
		totalSuccess += successCount
		totalCost += avgCost * float64(totalUses)
	}

	// Calculate summary statistics
	overallSuccessRate := 0.0
	if totalUsages > 0 {
		overallSuccessRate = float64(totalSuccess) / float64(totalUsages)
	}

	patternsToPromote := 0
	patternsToDeprecate := 0
	for _, p := range patterns {
		if p.Recommendation == loomv1.PatternRecommendation_PATTERN_PROMOTE {
			patternsToPromote++
		} else if p.Recommendation == loomv1.PatternRecommendation_PATTERN_REMOVE {
			patternsToDeprecate++
		}
	}

	summary := &loomv1.PatternAnalysisSummary{
		TotalPatternsAnalyzed: int32(len(patterns)),
		OverallSuccessRate:    overallSuccessRate,
		TotalCostUsd:          totalCost,
		PatternsToPromote:     int32(patternsToPromote),
		PatternsToDeprecate:   int32(patternsToDeprecate),
		AnalysisWindowHours:   windowHours,
	}

	span.SetAttribute("patterns_analyzed", len(patterns))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Analysis completed"}

	return &loomv1.PatternAnalysisResponse{
		Patterns: patterns,
		Summary:  summary,
	}, nil
}

// GenerateImprovements generates improvement proposals based on pattern analysis
func (la *LearningAgent) GenerateImprovements(
	ctx context.Context,
	req *loomv1.GenerateImprovementsRequest,
) (*loomv1.ImprovementsResponse, error) {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.generate_improvements")
	defer la.tracer.EndSpan(span)

	span.SetAttribute("domain", req.Domain)

	// Get pattern analysis first
	analysisResp, err := la.AnalyzePatternEffectiveness(ctx, &loomv1.AnalyzePatternEffectivenessRequest{
		Domain:      req.Domain,
		AgentId:     req.AgentId,
		WindowHours: 24,
	})
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to analyze patterns: %w", err)
	}

	var improvements []*loomv1.Improvement

	// Generate improvements based on pattern performance (traditional approach)
	for _, pattern := range analysisResp.Patterns {
		improvement := la.generateImprovementForPattern(pattern)
		if improvement != nil {
			improvements = append(improvements, improvement)
		}
	}

	// Generate improvements based on judge feedback (targeted approach)
	judgeImprovements := la.generateImprovementsWithJudgeFeedback(analysisResp.Patterns)
	improvements = append(improvements, judgeImprovements...)

	// Apply weighted scoring based on OptimizationGoal
	improvements = la.scoreImprovements(improvements, req.OptimizationGoal)

	// Limit to max proposals (after sorting by score)
	maxProposals := int(req.MaxProposals)
	if maxProposals == 0 {
		maxProposals = 10
	}
	if len(improvements) > maxProposals {
		improvements = improvements[:maxProposals]
	}

	// Store improvements in database
	for _, imp := range improvements {
		if err := la.storeImprovement(ctx, imp); err != nil {
			span.RecordError(err)
		}
	}

	span.SetAttribute("improvements_generated", len(improvements))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Improvements generated"}

	return &loomv1.ImprovementsResponse{
		Improvements:  improvements,
		TotalProposed: int32(len(improvements)),
	}, nil
}

// ApplyImprovement applies an improvement proposal (respects autonomy level)
func (la *LearningAgent) ApplyImprovement(
	ctx context.Context,
	req *loomv1.ApplyImprovementRequest,
) (*loomv1.ApplyImprovementResponse, error) {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.apply_improvement")
	defer la.tracer.EndSpan(span)

	span.SetAttribute("improvement_id", req.ImprovementId)
	span.SetAttribute("force", req.Force)

	// Retrieve improvement from database
	improvement, err := la.getImprovement(ctx, req.ImprovementId)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get improvement: %w", err)
	}

	// Check autonomy level
	if la.autonomyLevel == AutonomyManual && !req.Force {
		return &loomv1.ApplyImprovementResponse{
			Success:     false,
			Message:     "Manual approval required (autonomy level 0)",
			Improvement: improvement,
		}, nil
	}

	// Check circuit breaker
	if !la.canProceed() {
		return &loomv1.ApplyImprovementResponse{
			Success:     false,
			Message:     "Circuit breaker open - too many recent failures",
			Improvement: improvement,
		}, nil
	}

	// Apply the improvement (this is where actual changes would happen)
	// For now, just mark as applied
	improvement.Status = loomv1.ImprovementStatus_IMPROVEMENT_APPLIED
	improvement.AppliedAt = time.Now().UnixMilli()
	improvement.AppliedBy = "learning-agent"

	// Update in database
	if err := la.updateImprovement(ctx, improvement); err != nil {
		span.RecordError(err)
		la.recordCircuitBreakerFailure()
		return nil, fmt.Errorf("failed to update improvement: %w", err)
	}

	// Trigger pattern hot-reload if PatternReloader is configured
	if la.patternReloader != nil && improvement.TargetPattern != "" {
		span.SetAttribute("hot_reload.triggered", true)
		span.SetAttribute("hot_reload.pattern", improvement.TargetPattern)

		if err := la.patternReloader.ManualReload(improvement.TargetPattern); err != nil {
			// Log error but don't fail the operation
			// Pattern will be reloaded on next restart
			span.RecordError(err)
			span.SetAttribute("hot_reload.success", false)
			la.tracer.RecordMetric("learning_agent.hot_reload.failure", 1.0, map[string]string{
				"pattern": improvement.TargetPattern,
			})
		} else {
			span.SetAttribute("hot_reload.success", true)
			la.tracer.RecordMetric("learning_agent.hot_reload.success", 1.0, map[string]string{
				"pattern": improvement.TargetPattern,
			})
		}
	} else {
		span.SetAttribute("hot_reload.triggered", false)
	}

	la.recordCircuitBreakerSuccess()

	span.Status = observability.Status{Code: observability.StatusOK, Message: "Improvement applied"}

	return &loomv1.ApplyImprovementResponse{
		Success:     true,
		Message:     "Improvement applied successfully",
		Improvement: improvement,
	}, nil
}

// RollbackImprovement rolls back a failed improvement
func (la *LearningAgent) RollbackImprovement(
	ctx context.Context,
	req *loomv1.RollbackImprovementRequest,
) (*loomv1.RollbackImprovementResponse, error) {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.rollback_improvement")
	defer la.tracer.EndSpan(span)

	span.SetAttribute("improvement_id", req.ImprovementId)
	span.SetAttribute("reason", req.Reason)

	// Retrieve improvement
	improvement, err := la.getImprovement(ctx, req.ImprovementId)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get improvement: %w", err)
	}

	// Mark as rolled back
	improvement.Status = loomv1.ImprovementStatus_IMPROVEMENT_ROLLED_BACK

	// Update in database
	if err := la.updateImprovement(ctx, improvement); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to update improvement: %w", err)
	}

	// Trigger pattern hot-reload if PatternReloader is configured
	// This reloads the pattern from disk, restoring the previous version
	if la.patternReloader != nil && improvement.TargetPattern != "" {
		span.SetAttribute("hot_reload.triggered", true)
		span.SetAttribute("hot_reload.pattern", improvement.TargetPattern)
		span.SetAttribute("hot_reload.action", "rollback")

		if err := la.patternReloader.ManualReload(improvement.TargetPattern); err != nil {
			// Log error but don't fail the operation
			span.RecordError(err)
			span.SetAttribute("hot_reload.success", false)
			la.tracer.RecordMetric("learning_agent.hot_reload.rollback.failure", 1.0, map[string]string{
				"pattern": improvement.TargetPattern,
			})
		} else {
			span.SetAttribute("hot_reload.success", true)
			la.tracer.RecordMetric("learning_agent.hot_reload.rollback.success", 1.0, map[string]string{
				"pattern": improvement.TargetPattern,
			})
		}
	} else {
		span.SetAttribute("hot_reload.triggered", false)
	}

	span.Status = observability.Status{Code: observability.StatusOK, Message: "Improvement rolled back"}

	return &loomv1.RollbackImprovementResponse{
		Success:             true,
		Message:             "Improvement rolled back successfully",
		RestoredVersion:     "previous",
		RollbackCompletedAt: time.Now().UnixMilli(),
	}, nil
}

// GetImprovementHistory retrieves improvement history
func (la *LearningAgent) GetImprovementHistory(
	ctx context.Context,
	req *loomv1.GetImprovementHistoryRequest,
) (*loomv1.ImprovementHistoryResponse, error) {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.get_improvement_history")
	defer la.tracer.EndSpan(span)

	limit := int(req.Limit)
	if limit == 0 {
		limit = 50
	}
	offset := int(req.Offset)

	query := `
		SELECT
			id, type, description, confidence, impact,
			target_agent_id, target_pattern, domain,
			status, created_at, applied_at, applied_by
		FROM improvement_history
		WHERE 1=1
	`
	args := []interface{}{}

	if req.AgentId != "" {
		query += " AND target_agent_id = ?"
		args = append(args, req.AgentId)
	}
	if req.Domain != "" {
		query += " AND domain = ?"
		args = append(args, req.Domain)
	}
	if req.Status != loomv1.ImprovementStatus_IMPROVEMENT_STATUS_UNSPECIFIED {
		query += " AND status = ?"
		args = append(args, int32(req.Status))
	}

	query += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := la.db.QueryContext(ctx, query, args...)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query improvement history: %w", err)
	}
	defer rows.Close()

	var improvements []*loomv1.Improvement
	for rows.Next() {
		var (
			id, typeStr, description, targetAgentId, targetPattern, domain, appliedBy string
			confidence                                                                float64
			impact, status                                                            int32
			createdAt, appliedAt                                                      int64
		)

		err := rows.Scan(
			&id, &typeStr, &description, &confidence, &impact,
			&targetAgentId, &targetPattern, &domain,
			&status, &createdAt, &appliedAt, &appliedBy,
		)
		if err != nil {
			span.RecordError(err)
			continue
		}

		improvements = append(improvements, &loomv1.Improvement{
			Id:            id,
			Type:          loomv1.ImprovementType(loomv1.ImprovementType_value[typeStr]),
			Description:   description,
			Confidence:    confidence,
			Impact:        loomv1.ImpactLevel(impact),
			TargetAgentId: targetAgentId,
			TargetPattern: targetPattern,
			Domain:        domain,
			Status:        loomv1.ImprovementStatus(status),
			CreatedAt:     createdAt,
			AppliedAt:     appliedAt,
			AppliedBy:     appliedBy,
		})
	}

	// Get total count
	countQuery := "SELECT COUNT(*) FROM improvement_history WHERE 1=1"
	countArgs := []interface{}{}
	if req.AgentId != "" {
		countQuery += " AND target_agent_id = ?"
		countArgs = append(countArgs, req.AgentId)
	}
	if req.Domain != "" {
		countQuery += " AND domain = ?"
		countArgs = append(countArgs, req.Domain)
	}

	var totalCount int32
	err = la.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&totalCount)
	if err != nil {
		span.RecordError(err)
	}

	span.SetAttribute("improvements_returned", len(improvements))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "History retrieved"}

	return &loomv1.ImprovementHistoryResponse{
		Improvements: improvements,
		TotalCount:   totalCount,
	}, nil
}

// StreamPatternMetrics streams real-time pattern effectiveness metrics
func (la *LearningAgent) StreamPatternMetrics(
	req *loomv1.StreamPatternMetricsRequest,
	stream loomv1.LearningAgentService_StreamPatternMetricsServer,
) error {
	ctx := stream.Context()
	_, span := la.tracer.StartSpan(ctx, "learning_agent.stream_pattern_metrics")
	defer la.tracer.EndSpan(span)

	// Get MessageBus from PatternEffectivenessTracker
	bus := la.tracker.GetMessageBus()
	if bus == nil {
		span.Status = observability.Status{Code: observability.StatusError, Message: "MessageBus not configured"}
		return fmt.Errorf("message bus not configured")
	}

	// Subscribe to pattern effectiveness updates
	topic := "meta.pattern.effectiveness"
	subscription, err := bus.Subscribe(ctx, "learning-agent", topic, nil, 100)
	if err != nil {
		span.RecordError(err)
		span.Status = observability.Status{Code: observability.StatusError, Message: "Failed to subscribe"}
		return fmt.Errorf("failed to subscribe to message bus: %w", err)
	}
	defer func() {
		// Unsubscribe when stream ends
		_ = bus.Unsubscribe(context.Background(), subscription.ID)
	}()

	span.SetAttribute("topic", topic)
	span.SetAttribute("domain_filter", req.Domain)
	span.SetAttribute("agent_id_filter", req.AgentId)
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Streaming pattern metrics"}

	// Stream updates until context cancelled
	metricsStreamed := 0
	for {
		select {
		case <-ctx.Done():
			// Client disconnected or context cancelled
			span.SetAttribute("metrics_streamed", metricsStreamed)
			return ctx.Err()

		case busMsg := <-subscription.Channel:
			// Extract PatternMetric from payload
			var metric loomv1.PatternMetric
			if busMsg.Payload != nil {
				if value, ok := busMsg.Payload.Data.(*loomv1.MessagePayload_Value); ok {
					if err := json.Unmarshal(value.Value, &metric); err != nil {
						// Log error but continue streaming
						span.RecordError(err)
						continue
					}
				} else {
					// Skip non-value payloads
					continue
				}
			} else {
				continue
			}

			// Apply filters
			// 1. Domain filter
			if req.Domain != "" && metric.Domain != req.Domain {
				continue
			}

			// 2. Agent ID filter (from metadata)
			if req.AgentId != "" {
				agentID, ok := busMsg.Metadata["agent_id"]
				if !ok || agentID != req.AgentId {
					continue
				}
			}

			// Wrap metric in PatternMetricEvent
			event := &loomv1.PatternMetricEvent{
				Timestamp: time.Now().UnixMilli(),
				Metric:    &metric,
				EventType: loomv1.PatternMetricEventType_METRIC_UPDATE,
			}

			// Send event to client
			if err := stream.Send(event); err != nil {
				span.RecordError(err)
				span.SetAttribute("metrics_streamed", metricsStreamed)
				return err
			}

			metricsStreamed++
		}
	}
}

// TunePatterns automatically adjusts pattern parameters based on effectiveness analysis
func (la *LearningAgent) TunePatterns(
	ctx context.Context,
	req *loomv1.TunePatternsRequest,
) (*loomv1.TunePatternsResponse, error) {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.tune_patterns")
	defer la.tracer.EndSpan(span)

	span.SetAttribute("domain", req.Domain)
	span.SetAttribute("dry_run", req.DryRun)
	span.SetAttribute("strategy", req.Strategy.String())

	// Get pattern analysis first
	analysisResp, err := la.AnalyzePatternEffectiveness(ctx, &loomv1.AnalyzePatternEffectivenessRequest{
		Domain:      req.Domain,
		AgentId:     req.AgentId,
		WindowHours: 24,
	})
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to analyze patterns: %w", err)
	}

	// Calculate optimal priorities for each pattern
	tunings := la.calculatePatternTunings(analysisResp.Patterns, req.Strategy, req.OptimizationGoal, req.PatternLibraryPath, req.DimensionWeights, req.TargetDimensions)

	// If not dry run, apply the tunings to pattern files
	if !req.DryRun && req.PatternLibraryPath != "" {
		if err := la.applyPatternTunings(ctx, tunings, req.PatternLibraryPath); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to apply tunings: %w", err)
		}
	}

	// Calculate aggregate statistics
	summary := la.calculateTuningSummary(tunings)

	span.SetAttribute("patterns_analyzed", summary.PatternsAnalyzed)
	span.SetAttribute("patterns_tuned", summary.PatternsTuned)
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Pattern tuning complete"}

	return &loomv1.TunePatternsResponse{
		Tunings: tunings,
		Summary: summary,
		Applied: !req.DryRun,
	}, nil
}

// ============================================================================
// Helper Methods
// ============================================================================

func (la *LearningAgent) generateImprovementForPattern(pattern *loomv1.PatternMetric) *loomv1.Improvement {
	if pattern.Recommendation == loomv1.PatternRecommendation_PATTERN_KEEP {
		return nil
	}

	improvement := &loomv1.Improvement{
		Id:            uuid.New().String(),
		Domain:        pattern.Domain,
		TargetPattern: pattern.PatternName,
		Confidence:    pattern.Confidence,
		Status:        loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
		CreatedAt:     time.Now().UnixMilli(),
	}

	switch pattern.Recommendation {
	case loomv1.PatternRecommendation_PATTERN_PROMOTE:
		improvement.Type = loomv1.ImprovementType_IMPROVEMENT_PATTERN_ADD
		improvement.Description = fmt.Sprintf("Pattern '%s' has high success rate (%.1f%%) - recommend more usage",
			pattern.PatternName, pattern.SuccessRate*100)
		improvement.Impact = loomv1.ImpactLevel_IMPACT_MEDIUM

	case loomv1.PatternRecommendation_PATTERN_REMOVE:
		improvement.Type = loomv1.ImprovementType_IMPROVEMENT_PATTERN_REMOVE
		improvement.Description = fmt.Sprintf("Pattern '%s' has low success rate (%.1f%%) - recommend deprecation",
			pattern.PatternName, pattern.SuccessRate*100)
		improvement.Impact = loomv1.ImpactLevel_IMPACT_HIGH

	case loomv1.PatternRecommendation_PATTERN_DEMOTE:
		improvement.Type = loomv1.ImprovementType_IMPROVEMENT_PARAMETER_TUNE
		improvement.Description = fmt.Sprintf("Pattern '%s' has moderate issues (%.1f%% success) - tune parameters",
			pattern.PatternName, pattern.SuccessRate*100)
		improvement.Impact = loomv1.ImpactLevel_IMPACT_LOW

	default:
		return nil
	}

	// Set expected impact details
	improvement.Details = la.estimateImprovementDetails(pattern)

	return improvement
}

// generateImprovementsWithJudgeFeedback generates targeted improvements based on judge feedback.
// This method analyzes judge criterion scores to identify specific areas needing improvement.
func (la *LearningAgent) generateImprovementsWithJudgeFeedback(
	patterns []*loomv1.PatternMetric,
) []*loomv1.Improvement {
	var improvements []*loomv1.Improvement

	// Define thresholds for each judge dimension (0-1 scale)
	const (
		safetyThreshold  = 0.70 // Below 70% indicates safety issues
		costThreshold    = 0.75 // Below 75% indicates cost inefficiency
		qualityThreshold = 0.80 // Below 80% indicates quality issues
		domainThreshold  = 0.75 // Below 75% indicates domain compliance issues
	)

	for _, pattern := range patterns {
		// Skip patterns without judge data
		if len(pattern.JudgeCriterionScores) == 0 {
			continue
		}

		// Analyze each judge dimension for failures
		for criterion, score := range pattern.JudgeCriterionScores {
			var improvement *loomv1.Improvement
			var threshold float64
			var impactLevel loomv1.ImpactLevel

			switch criterion {
			case "safety":
				threshold = safetyThreshold
				impactLevel = loomv1.ImpactLevel_IMPACT_CRITICAL
				if score < threshold {
					improvement = &loomv1.Improvement{
						Id:            uuid.New().String(),
						Type:          loomv1.ImprovementType_IMPROVEMENT_PARAMETER_TUNE,
						Domain:        pattern.Domain,
						TargetPattern: pattern.PatternName,
						Description: fmt.Sprintf(
							"Pattern '%s' failing safety judge (score: %.1f%%, threshold: %.1f%%). "+
								"Add guardrails or validation to prevent unsafe outputs.",
							pattern.PatternName, score*100, threshold*100,
						),
						Confidence: pattern.Confidence,
						Impact:     impactLevel,
						Status:     loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
						CreatedAt:  time.Now().UnixMilli(),
					}
				}

			case "cost":
				threshold = costThreshold
				impactLevel = loomv1.ImpactLevel_IMPACT_MEDIUM
				if score < threshold {
					improvement = &loomv1.Improvement{
						Id:            uuid.New().String(),
						Type:          loomv1.ImprovementType_IMPROVEMENT_PARAMETER_TUNE,
						Domain:        pattern.Domain,
						TargetPattern: pattern.PatternName,
						Description: fmt.Sprintf(
							"Pattern '%s' failing cost efficiency judge (score: %.1f%%, threshold: %.1f%%). "+
								"Reduce prompt size, optimize token usage, or use cheaper model.",
							pattern.PatternName, score*100, threshold*100,
						),
						Confidence: pattern.Confidence,
						Impact:     impactLevel,
						Status:     loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
						CreatedAt:  time.Now().UnixMilli(),
					}
				}

			case "quality", "correctness":
				threshold = qualityThreshold
				impactLevel = loomv1.ImpactLevel_IMPACT_HIGH
				if score < threshold {
					improvement = &loomv1.Improvement{
						Id:            uuid.New().String(),
						Type:          loomv1.ImprovementType_IMPROVEMENT_TEMPLATE_ADJUST,
						Domain:        pattern.Domain,
						TargetPattern: pattern.PatternName,
						Description: fmt.Sprintf(
							"Pattern '%s' failing quality/correctness judge (score: %.1f%%, threshold: %.1f%%). "+
								"Improve prompt template, add examples, or tune parameters.",
							pattern.PatternName, score*100, threshold*100,
						),
						Confidence: pattern.Confidence,
						Impact:     impactLevel,
						Status:     loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
						CreatedAt:  time.Now().UnixMilli(),
					}
				}

			case "domain", "domain_compliance":
				threshold = domainThreshold
				impactLevel = loomv1.ImpactLevel_IMPACT_HIGH
				if score < threshold {
					improvement = &loomv1.Improvement{
						Id:            uuid.New().String(),
						Type:          loomv1.ImprovementType_IMPROVEMENT_TEMPLATE_ADJUST,
						Domain:        pattern.Domain,
						TargetPattern: pattern.PatternName,
						Description: fmt.Sprintf(
							"Pattern '%s' failing domain compliance judge (score: %.1f%%, threshold: %.1f%%). "+
								"Add domain-specific guidance or constraints (e.g., Teradata SQL hints).",
							pattern.PatternName, score*100, threshold*100,
						),
						Confidence: pattern.Confidence,
						Impact:     impactLevel,
						Status:     loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
						CreatedAt:  time.Now().UnixMilli(),
					}
				}
			}

			if improvement != nil {
				// Estimate improvement details based on criterion failure
				improvement.Details = la.estimateJudgeFeedbackImprovementDetails(pattern, criterion, score, threshold)
				improvements = append(improvements, improvement)
			}
		}

		// Also check overall judge pass rate
		if pattern.JudgePassRate > 0 && pattern.JudgePassRate < 0.70 {
			// Low overall judge pass rate indicates systemic issues
			improvement := &loomv1.Improvement{
				Id:            uuid.New().String(),
				Type:          loomv1.ImprovementType_IMPROVEMENT_PARAMETER_TUNE,
				Domain:        pattern.Domain,
				TargetPattern: pattern.PatternName,
				Description: fmt.Sprintf(
					"Pattern '%s' has low judge pass rate (%.1f%%). "+
						"Multiple evaluation failures detected - comprehensive review needed.",
					pattern.PatternName, pattern.JudgePassRate*100,
				),
				Confidence: pattern.Confidence,
				Impact:     loomv1.ImpactLevel_IMPACT_CRITICAL,
				Status:     loomv1.ImprovementStatus_IMPROVEMENT_PENDING,
				CreatedAt:  time.Now().UnixMilli(),
			}
			improvement.Details = &loomv1.ImprovementDetails{
				ExpectedSuccessRateDelta: 0.15, // Expect 15% improvement with fixes
				ExpectedCostDeltaUsd:     -0.001,
				ExpectedLatencyDeltaMs:   -50,
				Rationale: fmt.Sprintf(
					"Pattern has %.1f%% judge pass rate with avg score %.1f/100. "+
						"Targeted improvements expected to address systemic issues.",
					pattern.JudgePassRate*100, pattern.JudgeAvgScore,
				),
			}
			improvements = append(improvements, improvement)
		}
	}

	return improvements
}

// estimateJudgeFeedbackImprovementDetails estimates impact of judge feedback-based improvements
func (la *LearningAgent) estimateJudgeFeedbackImprovementDetails(
	pattern *loomv1.PatternMetric,
	criterion string,
	currentScore float64,
	threshold float64,
) *loomv1.ImprovementDetails {
	// Calculate severity (how far below threshold)
	severity := threshold - currentScore

	// Estimate improvement based on criterion type and severity
	var successRateDelta float64
	var costDelta float64
	var latencyDelta int64
	var rationale string

	switch criterion {
	case "safety":
		// Safety improvements prioritize preventing failures
		successRateDelta = min(severity*0.5, 0.20) // Up to 20% improvement
		costDelta = 0.0                            // Safety fixes don't necessarily reduce cost
		latencyDelta = 0                           // May even increase latency slightly
		rationale = fmt.Sprintf(
			"Safety score %.1f%% below threshold (%.1f%%). "+
				"Adding guardrails expected to prevent unsafe outputs and improve success rate.",
			severity*100, currentScore*100,
		)

	case "cost":
		// Cost improvements focus on efficiency
		successRateDelta = min(severity*0.2, 0.05) // Minor success rate impact
		costDelta = -pattern.AvgCostUsd * severity // Cost reduction proportional to severity
		latencyDelta = int64(-float64(pattern.AvgLatencyMs) * severity * 0.5)
		rationale = fmt.Sprintf(
			"Cost efficiency score %.1f%% below threshold (%.1f%%). "+
				"Token optimization expected to reduce costs while maintaining quality.",
			severity*100, currentScore*100,
		)

	case "quality", "correctness":
		// Quality improvements directly impact success rate
		successRateDelta = min(severity*0.8, 0.25)                // Up to 25% improvement
		costDelta = pattern.AvgCostUsd * 0.05                     // May slightly increase cost (better prompts)
		latencyDelta = int64(float64(pattern.AvgLatencyMs) * 0.1) // May increase latency
		rationale = fmt.Sprintf(
			"Quality/correctness score %.1f%% below threshold (%.1f%%). "+
				"Template improvements expected to significantly improve output quality.",
			severity*100, currentScore*100,
		)

	case "domain", "domain_compliance":
		// Domain improvements target specific requirements
		successRateDelta = min(severity*0.6, 0.20) // Up to 20% improvement
		costDelta = 0.0                            // Domain fixes don't necessarily change cost
		latencyDelta = 0
		rationale = fmt.Sprintf(
			"Domain compliance score %.1f%% below threshold (%.1f%%). "+
				"Adding domain-specific guidance expected to improve alignment with requirements.",
			severity*100, currentScore*100,
		)

	default:
		successRateDelta = min(severity*0.3, 0.10)
		costDelta = 0.0
		latencyDelta = 0
		rationale = fmt.Sprintf(
			"Criterion '%s' score %.1f%% below threshold (%.1f%%). "+
				"General improvements expected.",
			criterion, severity*100, currentScore*100,
		)
	}

	return &loomv1.ImprovementDetails{
		ExpectedSuccessRateDelta: successRateDelta,
		ExpectedCostDeltaUsd:     costDelta,
		ExpectedLatencyDeltaMs:   latencyDelta,
		Rationale:                rationale,
	}
}

// estimateImprovementDetails estimates the expected impact of an improvement
func (la *LearningAgent) estimateImprovementDetails(pattern *loomv1.PatternMetric) *loomv1.ImprovementDetails {
	// Estimate based on recommendation type
	var successRateDelta, costDelta float64
	var latencyDelta int64
	var rationale string

	switch pattern.Recommendation {
	case loomv1.PatternRecommendation_PATTERN_PROMOTE:
		// Promoting high-performing pattern
		successRateDelta = 0.05 // +5% success rate
		costDelta = -0.001      // -$0.001 cost savings
		latencyDelta = -50      // -50ms latency improvement
		rationale = fmt.Sprintf("Pattern %s has %.1f%% success rate. Increasing usage expected to improve overall metrics.",
			pattern.PatternName, pattern.SuccessRate*100)

	case loomv1.PatternRecommendation_PATTERN_REMOVE:
		// Removing low-performing pattern
		successRateDelta = 0.10                  // +10% success rate (by removing failures)
		costDelta = pattern.AvgCostUsd * -1.0    // Save pattern cost
		latencyDelta = pattern.AvgLatencyMs * -1 // Save pattern latency
		rationale = fmt.Sprintf("Pattern %s has %.1f%% success rate. Removal expected to eliminate failures.",
			pattern.PatternName, pattern.SuccessRate*100)

	case loomv1.PatternRecommendation_PATTERN_DEMOTE:
		// Tuning problematic pattern
		successRateDelta = 0.03                       // +3% success rate
		costDelta = pattern.AvgCostUsd * -0.1         // 10% cost reduction
		latencyDelta = pattern.AvgLatencyMs / 10 * -1 // 10% latency reduction
		rationale = fmt.Sprintf("Pattern %s has %.1f%% success rate. Parameter tuning expected to improve performance.",
			pattern.PatternName, pattern.SuccessRate*100)

	default:
		successRateDelta = 0.0
		costDelta = 0.0
		latencyDelta = 0
		rationale = "No specific improvement expected"
	}

	return &loomv1.ImprovementDetails{
		ExpectedSuccessRateDelta: successRateDelta,
		ExpectedCostDeltaUsd:     costDelta,
		ExpectedLatencyDeltaMs:   latencyDelta,
		Rationale:                rationale,
	}
}

// scoreImprovements applies weighted scoring to improvements based on OptimizationGoal
func (la *LearningAgent) scoreImprovements(
	improvements []*loomv1.Improvement,
	goal *loomv1.OptimizationGoal,
) []*loomv1.Improvement {
	// Extract optimization weights (default to equal if not specified)
	costWeight := 0.33
	qualityWeight := 0.33
	latencyWeight := 0.34

	if goal != nil {
		total := goal.CostWeight + goal.QualityWeight + goal.LatencyWeight
		if total > 0 {
			costWeight = goal.CostWeight / total
			qualityWeight = goal.QualityWeight / total
			latencyWeight = goal.LatencyWeight / total
		}
	}

	// Calculate score for each improvement
	type scoredImprovement struct {
		improvement *loomv1.Improvement
		score       float64
	}

	scored := make([]scoredImprovement, 0, len(improvements))
	for _, imp := range improvements {
		if imp.Details == nil {
			continue
		}

		// Normalize metrics to 0-1 range for scoring
		// Cost delta: negative is good (cost savings), so negate for scoring
		costScore := -imp.Details.ExpectedCostDeltaUsd * 1000.0 // Scale to reasonable range
		if costScore < 0 {
			costScore = 0
		}

		// Quality delta: positive is good (success rate increase)
		qualityScore := imp.Details.ExpectedSuccessRateDelta * 10.0 // Scale 0.0-1.0 range

		// Latency delta: negative is good (latency reduction), so negate
		latencyScore := float64(-imp.Details.ExpectedLatencyDeltaMs) / 100.0 // Scale to reasonable range
		if latencyScore < 0 {
			latencyScore = 0
		}

		// Calculate weighted score
		score := (costScore * costWeight) +
			(qualityScore * qualityWeight) +
			(latencyScore * latencyWeight)

		// Factor in confidence
		score *= imp.Confidence

		scored = append(scored, scoredImprovement{
			improvement: imp,
			score:       score,
		})
	}

	// Sort by score descending (highest score first)
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	// Extract sorted improvements
	result := make([]*loomv1.Improvement, len(scored))
	for i, s := range scored {
		result[i] = s.improvement
	}

	return result
}

func (la *LearningAgent) storeImprovement(ctx context.Context, improvement *loomv1.Improvement) error {
	query := `
		INSERT INTO improvement_history
			(id, type, description, confidence, impact,
			 target_agent_id, target_pattern, domain,
			 status, created_at, applied_at, applied_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := la.db.ExecContext(ctx, query,
		improvement.Id,
		improvement.Type.String(),
		improvement.Description,
		improvement.Confidence,
		int32(improvement.Impact),
		improvement.TargetAgentId,
		improvement.TargetPattern,
		improvement.Domain,
		int32(improvement.Status),
		improvement.CreatedAt,
		improvement.AppliedAt,
		improvement.AppliedBy,
	)

	return err
}

func (la *LearningAgent) getImprovement(ctx context.Context, id string) (*loomv1.Improvement, error) {
	query := `
		SELECT
			id, type, description, confidence, impact,
			target_agent_id, target_pattern, domain,
			status, created_at, applied_at, applied_by
		FROM improvement_history
		WHERE id = ?
	`

	var (
		typeStr, description, targetAgentId, targetPattern, domain, appliedBy string
		confidence                                                            float64
		impact, status                                                        int32
		createdAt, appliedAt                                                  int64
	)

	err := la.db.QueryRowContext(ctx, query, id).Scan(
		&id, &typeStr, &description, &confidence, &impact,
		&targetAgentId, &targetPattern, &domain,
		&status, &createdAt, &appliedAt, &appliedBy,
	)
	if err != nil {
		return nil, err
	}

	return &loomv1.Improvement{
		Id:            id,
		Type:          loomv1.ImprovementType(loomv1.ImprovementType_value[typeStr]),
		Description:   description,
		Confidence:    confidence,
		Impact:        loomv1.ImpactLevel(impact),
		TargetAgentId: targetAgentId,
		TargetPattern: targetPattern,
		Domain:        domain,
		Status:        loomv1.ImprovementStatus(status),
		CreatedAt:     createdAt,
		AppliedAt:     appliedAt,
		AppliedBy:     appliedBy,
	}, nil
}

func (la *LearningAgent) updateImprovement(ctx context.Context, improvement *loomv1.Improvement) error {
	query := `
		UPDATE improvement_history
		SET status = ?, applied_at = ?, applied_by = ?
		WHERE id = ?
	`

	_, err := la.db.ExecContext(ctx, query,
		int32(improvement.Status),
		improvement.AppliedAt,
		improvement.AppliedBy,
		improvement.Id,
	)

	return err
}

// ============================================================================
// Circuit Breaker
// ============================================================================

func (la *LearningAgent) canProceed() bool {
	la.cbMu.RLock()
	defer la.cbMu.RUnlock()

	cb := la.circuitBreaker

	switch cb.state {
	case "closed":
		return true

	case "open":
		// Check if cooldown period has elapsed
		if time.Since(cb.lastFailureTime) > cb.cooldownPeriod {
			// Transition to half-open
			la.cbMu.RUnlock()
			la.cbMu.Lock()
			cb.state = "half-open"
			la.cbMu.Unlock()
			la.cbMu.RLock()
			return true
		}
		return false

	case "half-open":
		return true

	default:
		return true
	}
}

func (la *LearningAgent) recordCircuitBreakerFailure() {
	la.cbMu.Lock()
	defer la.cbMu.Unlock()

	cb := la.circuitBreaker
	cb.failureCount++
	cb.lastFailureTime = time.Now()

	if cb.state == "half-open" {
		// Immediately reopen on failure in half-open state
		cb.state = "open"
	} else if cb.failureCount >= cb.threshold {
		cb.state = "open"
	}
}

func (la *LearningAgent) recordCircuitBreakerSuccess() {
	la.cbMu.Lock()
	defer la.cbMu.Unlock()

	cb := la.circuitBreaker
	cb.successCount++

	if cb.state == "half-open" && cb.successCount >= 3 {
		// Close circuit after 3 consecutive successes in half-open state
		cb.state = "closed"
		cb.failureCount = 0
		cb.successCount = 0
	}
}

// ============================================================================
// Utility Functions
// ============================================================================

func calculateConfidence(usageCount int) float64 {
	if usageCount == 0 {
		return 0.0
	}

	// Sigmoid function for confidence
	k := 0.1
	x0 := 25.0
	confidence := 1.0 / (1.0 + mathExp(-k*(float64(usageCount)-x0)))

	if usageCount < 3 {
		confidence = min(confidence, 0.3)
	}

	return confidence
}

func determineRecommendation(successRate, confidence float64) loomv1.PatternRecommendation {
	if confidence < 0.3 {
		return loomv1.PatternRecommendation_PATTERN_INVESTIGATE
	}

	if successRate >= 0.9 {
		return loomv1.PatternRecommendation_PATTERN_PROMOTE
	} else if successRate >= 0.7 {
		return loomv1.PatternRecommendation_PATTERN_KEEP
	} else if successRate >= 0.5 {
		return loomv1.PatternRecommendation_PATTERN_DEMOTE
	} else {
		return loomv1.PatternRecommendation_PATTERN_REMOVE
	}
}

func mathExp(x float64) float64 {
	// Simple exp approximation for small values
	if x < -10 {
		return 0
	} else if x > 10 {
		return 22026.4657948 // e^10
	}

	// Taylor series approximation
	result := 1.0
	term := 1.0
	for i := 1; i < 20; i++ {
		term *= x / float64(i)
		result += term
		if term < 1e-10 {
			break
		}
	}
	return result
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// calculatePatternTunings computes priority adjustments for each pattern
func (la *LearningAgent) calculatePatternTunings(
	patterns []*loomv1.PatternMetric,
	strategy loomv1.TuningStrategy,
	goal *loomv1.OptimizationGoal,
	patternLibraryPath string,
	dimensionWeights map[string]float64,
	targetDimensions []loomv1.JudgeDimension,
) []*loomv1.PatternTuning {
	tunings := make([]*loomv1.PatternTuning, 0, len(patterns))

	// Phase 6: Use dimension weights if provided (judge-based multi-dimensional tuning)
	useDimensionWeights := len(dimensionWeights) > 0 || len(targetDimensions) > 0

	// Phase 8: Track which dimension weights are actually used (for validation warning)
	usedDimensions := make(map[string]bool)
	for dimension := range dimensionWeights {
		usedDimensions[dimension] = false // Will be set to true when we find patterns with this dimension
	}

	// Default weights if not provided (legacy behavior)
	costWeight := 0.3
	qualityWeight := 0.5
	latencyWeight := 0.2
	if goal != nil {
		costWeight = goal.CostWeight
		qualityWeight = goal.QualityWeight
		latencyWeight = goal.LatencyWeight
	}

	// Determine priority adjustment magnitude based on strategy
	var maxAdjustment int32
	switch strategy {
	case loomv1.TuningStrategy_TUNING_CONSERVATIVE:
		maxAdjustment = 5
	case loomv1.TuningStrategy_TUNING_MODERATE:
		maxAdjustment = 15
	case loomv1.TuningStrategy_TUNING_AGGRESSIVE:
		maxAdjustment = 30
	default:
		maxAdjustment = 10
	}

	for _, pattern := range patterns {
		var score float64

		// Phase 6/8: Calculate dimension-weighted score using judge criterion scores if available
		// Phase 8: Supports custom dimensions (e.g., "teradata_compliance", "hipaa_compliance")
		if useDimensionWeights && len(pattern.JudgeCriterionScores) > 0 {
			// Use judge dimension scores (already normalized to 0-1)
			totalWeight := 0.0
			weightedSum := 0.0

			for dimension, dimensionScore := range pattern.JudgeCriterionScores {
				weight := dimensionWeights[dimension]
				if weight > 0 {
					weightedSum += dimensionScore * weight
					totalWeight += weight
					// Phase 8: Mark dimension as used
					usedDimensions[dimension] = true
				}
			}

			// If dimension weights were specified but don't cover all dimensions,
			// use equal weights for remaining dimensions
			if totalWeight > 0 {
				score = weightedSum / totalWeight
			} else {
				// Fallback to equal weighting if no matching dimensions
				for _, dimensionScore := range pattern.JudgeCriterionScores {
					weightedSum += dimensionScore
					totalWeight++
				}
				if totalWeight > 0 {
					score = weightedSum / totalWeight
				}
			}
		} else {
			// Legacy behavior: Calculate weighted score using basic metrics
			score = qualityWeight*pattern.SuccessRate +
				costWeight*(1.0-(pattern.AvgCostUsd/0.01)) + // Normalize cost (assume 0.01 is baseline)
				latencyWeight*(1.0-(float64(pattern.AvgLatencyMs)/1000.0)) // Normalize latency (1s baseline)
		}

		// Clamp score to 0-1
		if score < 0 {
			score = 0
		} else if score > 1 {
			score = 1
		}

		// Calculate priority adjustment: score of 0.5 = no change, >0.5 = increase, <0.5 = decrease
		priorityDelta := int32((score - 0.5) * float64(maxAdjustment) * 2)

		// Determine tuning confidence based on sample size
		var confidence loomv1.TuningConfidence
		if pattern.TotalUsages < 50 {
			confidence = loomv1.TuningConfidence_TUNING_LOW
			priorityDelta = priorityDelta / 2 // Be more conservative with low confidence
		} else if pattern.TotalUsages < 200 {
			confidence = loomv1.TuningConfidence_TUNING_MEDIUM
		} else {
			confidence = loomv1.TuningConfidence_TUNING_HIGH
		}

		// Skip if no adjustment needed
		if priorityDelta == 0 {
			continue
		}

		// Try to read current priority from file, default to 50 if not found
		currentPriority := int32(50)
		if patternLibraryPath != "" {
			if yamlPath, err := FindPatternYAMLFile(patternLibraryPath, pattern.PatternName); err == nil {
				if priority, err := GetCurrentPriority(yamlPath, pattern.PatternName); err == nil {
					currentPriority = priority
				}
			}
		}

		newPriority := currentPriority + priorityDelta

		// Clamp to valid range (0-100)
		if newPriority < 0 {
			newPriority = 0
		} else if newPriority > 100 {
			newPriority = 100
		}

		adjustments := []*loomv1.ParameterAdjustment{
			{
				ParameterName: "priority",
				OldValue:      fmt.Sprintf("%d", currentPriority),
				NewValue:      fmt.Sprintf("%d", newPriority),
				Reason: fmt.Sprintf("Pattern score: %.2f (success: %.1f%%, cost: $%.4f, latency: %dms)",
					score, pattern.SuccessRate*100, pattern.AvgCostUsd, pattern.AvgLatencyMs),
			},
		}

		// Create projected metrics (estimate improvement)
		projectedMetrics := &loomv1.PatternMetric{
			PatternName:  pattern.PatternName,
			Domain:       pattern.Domain,
			TotalUsages:  pattern.TotalUsages,
			SuccessRate:  pattern.SuccessRate + 0.02, // Estimate 2% improvement
			AvgCostUsd:   pattern.AvgCostUsd * 0.98,  // Estimate 2% cost reduction
			AvgLatencyMs: int64(float64(pattern.AvgLatencyMs) * 0.98),
			Confidence:   pattern.Confidence,
		}

		tuning := &loomv1.PatternTuning{
			PatternName:      pattern.PatternName,
			Domain:           pattern.Domain,
			Adjustments:      adjustments,
			BeforeMetrics:    pattern,
			ProjectedMetrics: projectedMetrics,
			Rationale:        fmt.Sprintf("Adjusted priority from %d to %d based on performance metrics", currentPriority, newPriority),
			Confidence:       confidence,
		}

		tunings = append(tunings, tuning)
	}

	// Phase 8: Log warning for unused dimension weights (helps catch typos in dimension names)
	if la.tracer != nil && len(dimensionWeights) > 0 {
		for dimension, used := range usedDimensions {
			if !used {
				la.tracer.RecordMetric("learning_agent.unused_dimension_weight", 1.0, map[string]string{
					"dimension": dimension,
				})
			}
		}
	}

	return tunings
}

// applyPatternTunings writes adjusted priorities back to pattern YAML files
func (la *LearningAgent) applyPatternTunings(
	ctx context.Context,
	tunings []*loomv1.PatternTuning,
	patternLibraryPath string,
) error {
	// Apply each tuning by finding the YAML file and updating it
	for _, tuning := range tunings {
		for _, adj := range tuning.Adjustments {
			if adj.ParameterName != "priority" {
				continue // Only priority is supported for now
			}

			// Find the YAML file containing this pattern
			yamlPath, err := FindPatternYAMLFile(patternLibraryPath, tuning.PatternName)
			if err != nil {
				// Log error but continue with other patterns
				if la.tracer != nil {
					la.tracer.RecordMetric("learning_agent.pattern_tuning.file_not_found", 1.0, map[string]string{
						"pattern": tuning.PatternName,
						"domain":  tuning.Domain,
					})
				}
				continue
			}

			// Parse new priority
			var newPriority int32
			_, _ = fmt.Sscanf(adj.NewValue, "%d", &newPriority)

			// Update the priority in the YAML file
			if err := UpdatePatternPriority(yamlPath, tuning.PatternName, newPriority); err != nil {
				// Log error but continue with other patterns
				if la.tracer != nil {
					la.tracer.RecordMetric("learning_agent.pattern_tuning.update_failed", 1.0, map[string]string{
						"pattern": tuning.PatternName,
						"domain":  tuning.Domain,
						"error":   err.Error(),
					})
				}
				continue
			}

			// Record successful update
			if la.tracer != nil {
				la.tracer.RecordMetric("learning_agent.pattern_tuning.updated", 1.0, map[string]string{
					"pattern":      tuning.PatternName,
					"domain":       tuning.Domain,
					"old_priority": adj.OldValue,
					"new_priority": adj.NewValue,
				})
			}

			// Trigger pattern hot-reload if configured
			if la.patternReloader != nil {
				if err := la.patternReloader.ManualReload(tuning.PatternName); err != nil {
					// Log error but don't fail the operation
					if la.tracer != nil {
						la.tracer.RecordMetric("learning_agent.pattern_tuning.reload_failed", 1.0, map[string]string{
							"pattern": tuning.PatternName,
						})
					}
				} else if la.tracer != nil {
					la.tracer.RecordMetric("learning_agent.pattern_tuning.reloaded", 1.0, map[string]string{
						"pattern": tuning.PatternName,
					})
				}
			}
		}
	}

	return nil
}

// calculateTuningSummary computes aggregate tuning statistics
func (la *LearningAgent) calculateTuningSummary(tunings []*loomv1.PatternTuning) *loomv1.TuningSummary {
	summary := &loomv1.TuningSummary{
		PatternsAnalyzed:  int32(len(tunings)),
		PatternsTuned:     int32(len(tunings)),
		PatternsPromoted:  0,
		PatternsDemoted:   0,
		PatternsUnchanged: 0,
	}

	var totalSuccessImprovement float64
	var totalCostReduction float64
	var totalLatencyReduction int64

	for _, tuning := range tunings {
		// Count promotions/demotions based on priority change
		for _, adj := range tuning.Adjustments {
			if adj.ParameterName == "priority" {
				oldPriority := parseInt32(adj.OldValue)
				newPriority := parseInt32(adj.NewValue)
				if newPriority > oldPriority {
					summary.PatternsPromoted++
				} else if newPriority < oldPriority {
					summary.PatternsDemoted++
				} else {
					summary.PatternsUnchanged++
				}
			}
		}

		// Calculate estimated improvements
		if tuning.ProjectedMetrics != nil && tuning.BeforeMetrics != nil {
			totalSuccessImprovement += (tuning.ProjectedMetrics.SuccessRate - tuning.BeforeMetrics.SuccessRate)
			totalCostReduction += (tuning.BeforeMetrics.AvgCostUsd - tuning.ProjectedMetrics.AvgCostUsd)
			totalLatencyReduction += (tuning.BeforeMetrics.AvgLatencyMs - tuning.ProjectedMetrics.AvgLatencyMs)
		}
	}

	summary.EstimatedSuccessRateImprovement = totalSuccessImprovement / float64(len(tunings))
	summary.EstimatedCostReductionUsd = totalCostReduction
	summary.EstimatedLatencyReductionMs = totalLatencyReduction / int64(len(tunings))

	return summary
}

func parseInt32(s string) int32 {
	var i int32
	_, _ = fmt.Sscanf(s, "%d", &i)
	return i
}

// splitAndTrim splits a string by separator and trims whitespace
func splitAndTrim(s string, sep string) []string {
	parts := []string{}
	for _, part := range splitString(s, sep) {
		trimmed := trimString(part)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

// splitString splits a string by separator
func splitString(s string, sep string) []string {
	if s == "" {
		return []string{}
	}
	parts := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			parts = append(parts, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// trimString removes leading and trailing whitespace
func trimString(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// ============================================================================
// Interrupt Handler Implementations
// ============================================================================

// handleAnalyzeInterrupt handles SignalLearningAnalyze interrupt.
// Triggers pattern effectiveness analysis for specified domain or all domains.
func (la *LearningAgent) handleAnalyzeInterrupt(ctx context.Context, payload []byte) error {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.handle_analyze_interrupt")
	defer la.tracer.EndSpan(span)

	// Parse payload (JSON)
	type AnalyzePayload struct {
		Domain      string `json:"domain"`
		AgentID     string `json:"agent_id"`
		WindowHours int64  `json:"window_hours"`
	}

	var p AnalyzePayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to parse analyze payload: %w", err)
		}
	}

	// Default to all domains if not specified
	domains := []string{"sql", "rest", "file", "document"}
	if p.Domain != "" {
		domains = []string{p.Domain}
	}

	windowHours := p.WindowHours
	if windowHours == 0 {
		windowHours = 24
	}

	// Analyze each domain
	for _, domain := range domains {
		_, err := la.AnalyzePatternEffectiveness(ctx, &loomv1.AnalyzePatternEffectivenessRequest{
			Domain:      domain,
			AgentId:     p.AgentID,
			WindowHours: windowHours,
		})
		if err != nil {
			span.RecordError(err)
			// Continue with other domains
		}
	}

	span.Status = observability.Status{Code: observability.StatusOK, Message: "Analysis completed"}
	return nil
}

// handleOptimizeInterrupt handles SignalLearningOptimize interrupt.
// Triggers improvement generation and optional auto-apply based on autonomy level.
func (la *LearningAgent) handleOptimizeInterrupt(ctx context.Context, payload []byte) error {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.handle_optimize_interrupt")
	defer la.tracer.EndSpan(span)

	// Parse payload (JSON)
	type OptimizePayload struct {
		Domain           string                   `json:"domain"`
		AgentID          string                   `json:"agent_id"`
		MaxProposals     int32                    `json:"max_proposals"`
		OptimizationGoal *loomv1.OptimizationGoal `json:"optimization_goal,omitempty"`
		AutoApply        bool                     `json:"auto_apply"`
	}

	var p OptimizePayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to parse optimize payload: %w", err)
		}
	}

	// Default max proposals
	maxProposals := p.MaxProposals
	if maxProposals == 0 {
		maxProposals = 10
	}

	// Generate improvements
	resp, err := la.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
		Domain:           p.Domain,
		AgentId:          p.AgentID,
		MaxProposals:     maxProposals,
		OptimizationGoal: p.OptimizationGoal,
	})
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to generate improvements: %w", err)
	}

	// Auto-apply if requested and autonomy permits
	if p.AutoApply {
		for _, improvement := range resp.Improvements {
			if la.ShouldAutoApply(improvement) {
				_, err := la.ApplyImprovement(ctx, &loomv1.ApplyImprovementRequest{
					ImprovementId: improvement.Id,
				})
				if err != nil {
					span.RecordError(err)
				}
			}
		}
	}

	span.SetAttribute("improvements_generated", len(resp.Improvements))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Optimization completed"}
	return nil
}

// handleABTestInterrupt handles SignalLearningABTest interrupt.
// Analyzes A/B test results for pattern variants and determines statistical winners.
func (la *LearningAgent) handleABTestInterrupt(ctx context.Context, payload []byte) error {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.handle_abtest_interrupt")
	defer la.tracer.EndSpan(span)

	// Parse payload (JSON)
	type ABTestPayload struct {
		PatternName       string   `json:"pattern_name"`
		Variants          []string `json:"variants"` // e.g., ["control", "treatment-a", "treatment-b"]
		Domain            string   `json:"domain"`
		AgentID           string   `json:"agent_id"`
		MinSampleSize     int32    `json:"min_sample_size"`    // Minimum usages per variant for statistical significance
		SignificanceLevel float64  `json:"significance_level"` // e.g., 0.05 for 95% confidence
		WindowHours       int64    `json:"window_hours"`       // Time window to analyze (default: 24)
	}

	var p ABTestPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to parse abtest payload: %w", err)
		}
	}

	// Defaults
	if p.MinSampleSize == 0 {
		p.MinSampleSize = 30 // Minimum for basic statistical significance
	}
	if p.SignificanceLevel == 0 {
		p.SignificanceLevel = 0.05 // 95% confidence
	}
	if p.WindowHours == 0 {
		p.WindowHours = 24
	}

	// Set span attributes
	span.SetAttribute("pattern_name", p.PatternName)
	span.SetAttribute("variants_count", len(p.Variants))
	span.SetAttribute("domain", p.Domain)
	span.SetAttribute("agent_id", p.AgentID)
	span.SetAttribute("min_sample_size", p.MinSampleSize)

	// Query pattern effectiveness for all variants
	type VariantMetrics struct {
		Variant      string
		TotalUsages  int
		SuccessRate  float64
		AvgCostUSD   float64
		AvgLatencyMS int
	}

	variants := make([]VariantMetrics, 0, len(p.Variants))

	// If no variants specified, query all variants for this pattern
	variantList := p.Variants
	if len(variantList) == 0 {
		// Query distinct variants from database
		query := `
			SELECT DISTINCT variant
			FROM pattern_effectiveness
			WHERE pattern_name = ? AND domain = ? AND agent_id = ?
			  AND window_start >= ?
			ORDER BY variant
		`
		windowStart := time.Now().Add(-time.Duration(p.WindowHours) * time.Hour).Unix()
		rows, err := la.db.QueryContext(ctx, query, p.PatternName, p.Domain, p.AgentID, windowStart)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to query variants: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var variant string
			if err := rows.Scan(&variant); err != nil {
				continue
			}
			variantList = append(variantList, variant)
		}
	}

	// Query metrics for each variant
	for _, variant := range variantList {
		query := `
			SELECT
				COALESCE(SUM(total_usages), 0) as total_usages,
				COALESCE(AVG(success_rate), 0) as avg_success_rate,
				COALESCE(AVG(avg_cost_usd), 0) as avg_cost,
				COALESCE(AVG(avg_latency_ms), 0) as avg_latency
			FROM pattern_effectiveness
			WHERE pattern_name = ? AND variant = ? AND domain = ? AND agent_id = ?
			  AND window_start >= ?
		`
		windowStart := time.Now().Add(-time.Duration(p.WindowHours) * time.Hour).Unix()

		var totalUsages int
		var avgSuccessRate, avgCost float64
		var avgLatency int

		err := la.db.QueryRowContext(ctx, query, p.PatternName, variant, p.Domain, p.AgentID, windowStart).
			Scan(&totalUsages, &avgSuccessRate, &avgCost, &avgLatency)
		if err != nil {
			span.RecordError(err)
			continue
		}

		variants = append(variants, VariantMetrics{
			Variant:      variant,
			TotalUsages:  totalUsages,
			SuccessRate:  avgSuccessRate,
			AvgCostUSD:   avgCost,
			AvgLatencyMS: avgLatency,
		})
	}

	// Check if we have sufficient sample sizes
	sufficientSamples := true
	for _, v := range variants {
		if v.TotalUsages < int(p.MinSampleSize) {
			sufficientSamples = false
			break
		}
	}

	// Find best performing variant (by success rate)
	var bestVariant *VariantMetrics
	for i := range variants {
		if bestVariant == nil || variants[i].SuccessRate > bestVariant.SuccessRate {
			bestVariant = &variants[i]
		}
	}

	// Determine statistical significance (simplified: compare success rates)
	// For production, should use proper statistical tests (chi-square, t-test, etc.)
	significantDifference := false
	if len(variants) >= 2 && bestVariant != nil {
		// Check if best variant is significantly better than others
		for _, v := range variants {
			if v.Variant == bestVariant.Variant {
				continue
			}
			// Simplified check: difference > 5% and sufficient samples
			if sufficientSamples && (bestVariant.SuccessRate-v.SuccessRate) > 0.05 {
				significantDifference = true
				break
			}
		}
	}

	// Record metrics
	if la.tracer != nil {
		la.tracer.RecordMetric("learning_agent.abtest.variants_analyzed", float64(len(variants)), map[string]string{
			"pattern_name": p.PatternName,
			"domain":       p.Domain,
		})
		if bestVariant != nil {
			la.tracer.RecordMetric("learning_agent.abtest.best_variant_success_rate", bestVariant.SuccessRate, map[string]string{
				"pattern_name": p.PatternName,
				"variant":      bestVariant.Variant,
			})
		}
	}

	// Set span status
	message := fmt.Sprintf("A/B test analyzed: %d variants", len(variants))
	if !sufficientSamples {
		message = fmt.Sprintf("%s (insufficient samples, need %d per variant)", message, p.MinSampleSize)
	} else if significantDifference && bestVariant != nil {
		message = fmt.Sprintf("%s, winner: %s (%.2f%% success rate)", message, bestVariant.Variant, bestVariant.SuccessRate*100)
	} else {
		message = fmt.Sprintf("%s (no significant difference)", message)
	}

	span.SetAttribute("sufficient_samples", sufficientSamples)
	span.SetAttribute("significant_difference", significantDifference)
	if bestVariant != nil {
		span.SetAttribute("best_variant", bestVariant.Variant)
		span.SetAttribute("best_success_rate", bestVariant.SuccessRate)
	}
	span.Status = observability.Status{Code: observability.StatusOK, Message: message}

	return nil
}

// handleProposalInterrupt handles SignalLearningProposal interrupt.
// Generates improvement proposals without applying them (for human review).
func (la *LearningAgent) handleProposalInterrupt(ctx context.Context, payload []byte) error {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.handle_proposal_interrupt")
	defer la.tracer.EndSpan(span)

	// Parse payload (JSON)
	type ProposalPayload struct {
		Domain           string                   `json:"domain"`
		AgentID          string                   `json:"agent_id"`
		MaxProposals     int32                    `json:"max_proposals"`
		OptimizationGoal *loomv1.OptimizationGoal `json:"optimization_goal,omitempty"`
	}

	var p ProposalPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to parse proposal payload: %w", err)
		}
	}

	// Default max proposals
	maxProposals := p.MaxProposals
	if maxProposals == 0 {
		maxProposals = 10
	}

	// Generate improvements (but don't apply)
	resp, err := la.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
		Domain:           p.Domain,
		AgentId:          p.AgentID,
		MaxProposals:     maxProposals,
		OptimizationGoal: p.OptimizationGoal,
	})
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to generate proposals: %w", err)
	}

	span.SetAttribute("proposals_generated", len(resp.Improvements))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Proposals generated"}
	return nil
}

// handleValidateInterrupt handles SignalLearningValidate interrupt.
// Validates an applied improvement by comparing before/after metrics.
func (la *LearningAgent) handleValidateInterrupt(ctx context.Context, payload []byte) error {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.handle_validate_interrupt")
	defer la.tracer.EndSpan(span)

	// Parse payload (JSON)
	type ValidatePayload struct {
		ImprovementID   string `json:"improvement_id"`
		ValidationHours int64  `json:"validation_hours"`
	}

	var p ValidatePayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to parse validate payload: %w", err)
		}
	}

	// Retrieve improvement
	improvement, err := la.getImprovement(ctx, p.ImprovementID)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to get improvement: %w", err)
	}

	// Get post-improvement metrics
	validationHours := p.ValidationHours
	if validationHours == 0 {
		validationHours = 24
	}

	analysisResp, err := la.AnalyzePatternEffectiveness(ctx, &loomv1.AnalyzePatternEffectivenessRequest{
		Domain:      improvement.Domain,
		AgentId:     improvement.TargetAgentId,
		WindowHours: validationHours,
	})
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to analyze post-improvement metrics: %w", err)
	}

	// Find pattern metrics in analysis results
	var validationPassed bool
	for _, pattern := range analysisResp.Patterns {
		if pattern.PatternName == improvement.TargetPattern {
			// Check if improvement met expectations
			// (This is a simple check - could be more sophisticated)
			validationPassed = pattern.SuccessRate >= 0.7 && pattern.Confidence >= 0.5
			break
		}
	}

	span.SetAttribute("improvement_id", p.ImprovementID)
	span.SetAttribute("validation_passed", validationPassed)

	if validationPassed {
		span.Status = observability.Status{Code: observability.StatusOK, Message: "Validation passed"}
	} else {
		span.Status = observability.Status{Code: observability.StatusError, Message: "Validation failed"}
		// Could trigger rollback here
	}

	return nil
}

// handleExportInterrupt handles SignalLearningExport interrupt.
// Exports learning data (pattern metrics, improvements) to external system.
func (la *LearningAgent) handleExportInterrupt(ctx context.Context, payload []byte) error {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.handle_export_interrupt")
	defer la.tracer.EndSpan(span)

	// Parse payload (JSON)
	type ExportPayload struct {
		Domain      string `json:"domain"`
		Format      string `json:"format"` // "json", "csv", "hawk"
		Destination string `json:"destination"`
		WindowHours int64  `json:"window_hours"`
	}

	var p ExportPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to parse export payload: %w", err)
		}
	}

	// Default format and window
	format := p.Format
	if format == "" {
		format = "json"
	}
	windowHours := p.WindowHours
	if windowHours == 0 {
		windowHours = 24
	}

	// Get pattern analysis data
	analysisResp, err := la.AnalyzePatternEffectiveness(ctx, &loomv1.AnalyzePatternEffectivenessRequest{
		Domain:      p.Domain,
		WindowHours: windowHours,
	})
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to get analysis data: %w", err)
	}

	// Export based on format
	switch format {
	case "json":
		// Export as JSON
		data, err := json.Marshal(analysisResp)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal analysis data: %w", err)
		}

		// Note: Destination writing is intentionally omitted for now
		// In production, this would write to p.Destination (file path, S3 URL, etc.)
		// For now, data is available for immediate consumption by the caller
		_ = data

	case "hawk":
		// Export to Hawk (already happening via tracer)
		// This is a no-op since metrics are continuously exported via tracer
		span.SetAttribute("hawk_export", "continuous")

	default:
		return fmt.Errorf("unsupported export format: %s", format)
	}

	span.SetAttribute("format", format)
	span.SetAttribute("patterns_exported", len(analysisResp.Patterns))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Export completed"}
	return nil
}

// handleSyncInterrupt handles SignalLearningSync interrupt.
// Syncs learning state with external system (e.g., shared learning DB, central learning registry).
// Supports push (export local learning), pull (import external improvements), and bidirectional sync.
func (la *LearningAgent) handleSyncInterrupt(ctx context.Context, payload []byte) error {
	ctx, span := la.tracer.StartSpan(ctx, "learning_agent.handle_sync_interrupt")
	defer la.tracer.EndSpan(span)

	// Parse payload (JSON)
	type SyncPayload struct {
		SyncDirection   string   `json:"sync_direction"`    // "push", "pull", "bidirectional"
		Domain          string   `json:"domain"`            // Domain filter (optional)
		AgentID         string   `json:"agent_id"`          // Agent ID filter (optional)
		PatternNames    []string `json:"pattern_names"`     // Specific patterns to sync (optional)
		SyncWindowHours int64    `json:"sync_window_hours"` // Time window for data to sync (default: 168 = 1 week)
	}

	var p SyncPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &p); err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to parse sync payload: %w", err)
		}
	}

	// Defaults
	if p.SyncDirection == "" {
		p.SyncDirection = "bidirectional"
	}
	if p.SyncWindowHours == 0 {
		p.SyncWindowHours = 168 // 1 week
	}

	// Set span attributes
	span.SetAttribute("sync_direction", p.SyncDirection)
	span.SetAttribute("domain", p.Domain)
	span.SetAttribute("agent_id", p.AgentID)
	span.SetAttribute("pattern_count", len(p.PatternNames))
	span.SetAttribute("sync_window_hours", p.SyncWindowHours)

	// Initialize counters
	var pushedRecords, pulledRecords, skippedRecords int

	// Push: Export local learning data to external system
	if p.SyncDirection == "push" || p.SyncDirection == "bidirectional" {
		// Query local pattern effectiveness data within time window
		query := `
			SELECT
				pattern_name, variant, domain, agent_id,
				window_start, window_end,
				total_usages, success_count, failure_count,
				success_rate, avg_cost_usd, avg_latency_ms,
				judge_pass_rate, judge_avg_score,
				llm_provider, llm_model
			FROM pattern_effectiveness
			WHERE window_start >= ?
		`
		args := []interface{}{time.Now().Add(-time.Duration(p.SyncWindowHours) * time.Hour).Unix()}

		// Add filters if specified
		if p.Domain != "" {
			query += " AND domain = ?"
			args = append(args, p.Domain)
		}
		if p.AgentID != "" {
			query += " AND agent_id = ?"
			args = append(args, p.AgentID)
		}
		if len(p.PatternNames) > 0 {
			// Build IN clause for pattern names
			placeholders := make([]string, len(p.PatternNames))
			for i := range p.PatternNames {
				placeholders[i] = "?"
				args = append(args, p.PatternNames[i])
			}
			query += fmt.Sprintf(" AND pattern_name IN (%s)", strings.Join(placeholders, ","))
		}

		query += " ORDER BY window_start DESC LIMIT 1000"

		rows, err := la.db.QueryContext(ctx, query, args...)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to query pattern effectiveness for push: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				patternName, variant, domain, agentID string
				windowStart, windowEnd                int64
				totalUsages, successCount, failCount  int
				successRate, avgCost                  float64
				avgLatency                            int
				judgePassRate, judgeAvgScore          sql.NullFloat64
				llmProvider, llmModel                 string
			)

			err := rows.Scan(
				&patternName, &variant, &domain, &agentID,
				&windowStart, &windowEnd,
				&totalUsages, &successCount, &failCount,
				&successRate, &avgCost, &avgLatency,
				&judgePassRate, &judgeAvgScore,
				&llmProvider, &llmModel,
			)
			if err != nil {
				span.RecordError(err)
				continue
			}

			// In a real implementation, this would push to external system via HTTP/gRPC
			// For now, we just count the records that would be pushed
			pushedRecords++
		}
	}

	// Pull: Import external improvements (stub - would query external API)
	if p.SyncDirection == "pull" || p.SyncDirection == "bidirectional" {
		// In a real implementation:
		// 1. Query external learning registry for improvements
		// 2. Filter by domain, agent_id, pattern_names
		// 3. Apply improvements that pass validation
		// 4. Record in improvement_history table

		// For now, just simulate pulling records
		// This would be replaced with actual HTTP/gRPC calls to external system
		pulledRecords = 0 // No external system to pull from yet
	}

	// Record metrics
	if la.tracer != nil {
		if pushedRecords > 0 {
			la.tracer.RecordMetric("learning_agent.sync.pushed_records", float64(pushedRecords), map[string]string{
				"domain":    p.Domain,
				"direction": p.SyncDirection,
			})
		}
		if pulledRecords > 0 {
			la.tracer.RecordMetric("learning_agent.sync.pulled_records", float64(pulledRecords), map[string]string{
				"domain":    p.Domain,
				"direction": p.SyncDirection,
			})
		}
	}

	// Set span status
	message := fmt.Sprintf("Sync completed: pushed=%d, pulled=%d, skipped=%d", pushedRecords, pulledRecords, skippedRecords)
	span.SetAttribute("pushed_records", pushedRecords)
	span.SetAttribute("pulled_records", pulledRecords)
	span.SetAttribute("skipped_records", skippedRecords)
	span.Status = observability.Status{Code: observability.StatusOK, Message: message}

	return nil
}

// ============================================================================
// Self-Trigger Mechanism
// ============================================================================

// RecordExecution increments the execution counter and triggers learning if threshold is reached.
// This should be called by the agent after each pattern execution.
// If executionTrigger > 0 and executionCount % executionTrigger == 0, sends SignalLearningAnalyze.
func (la *LearningAgent) RecordExecution(ctx context.Context) error {
	la.executionMu.Lock()
	la.executionCount++
	currentCount := la.executionCount
	trigger := la.executionTrigger
	interruptChannel := la.interruptChannel
	la.executionMu.Unlock()

	// Skip self-trigger if no interrupt channel configured or no trigger threshold set
	if interruptChannel == nil || trigger == 0 {
		return nil
	}

	// Check if we've hit the trigger threshold
	if currentCount%trigger == 0 {
		// Self-trigger learning analysis
		payload, _ := json.Marshal(map[string]interface{}{
			"domain":       "", // Analyze all domains
			"agent_id":     la.agentID,
			"window_hours": 24,
			"trigger":      "execution_count",
		})

		// Send to self (self-trigger)
		if err := interruptChannel.SendFrom(ctx, interrupt.SignalLearningAnalyze, la.agentID, payload, la.agentID); err != nil {
			// Log error but don't fail - self-triggering is best-effort
			if la.tracer != nil {
				la.tracer.RecordMetric("learning_agent.self_trigger.failed", 1.0, map[string]string{
					"agent_id":        la.agentID,
					"execution_count": fmt.Sprintf("%d", currentCount),
				})
			}
			return fmt.Errorf("failed to self-trigger learning: %w", err)
		}

		// Record successful self-trigger
		if la.tracer != nil {
			la.tracer.RecordMetric("learning_agent.self_trigger.success", 1.0, map[string]string{
				"agent_id":        la.agentID,
				"execution_count": fmt.Sprintf("%d", currentCount),
			})
		}
	}

	return nil
}

// GetExecutionCount returns the current execution count.
// This is useful for monitoring and testing.
func (la *LearningAgent) GetExecutionCount() int64 {
	la.executionMu.Lock()
	defer la.executionMu.Unlock()
	return la.executionCount
}

// ResetExecutionCount resets the execution counter to zero.
// This is useful for testing or when starting a new learning cycle.
func (la *LearningAgent) ResetExecutionCount() {
	la.executionMu.Lock()
	defer la.executionMu.Unlock()
	la.executionCount = 0
}
