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
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// TestDogfood_RealWorldScenarios runs multiple real-world dogfood tests
// analyzing actual usage patterns from the Loom framework
func TestDogfood_RealWorldScenarios(t *testing.T) {
	scenarios := []struct {
		name        string
		description string
		domain      string
		patterns    []PatternUsageScenario
		goal        *loomv1.OptimizationGoal
	}{
		{
			name:        "Agent Conversation Patterns",
			description: "Analyzing agent conversation loop patterns from pkg/agent",
			domain:      "agent-runtime",
			patterns: []PatternUsageScenario{
				// Tool-based conversation patterns
				{
					Name:        "agent.conversation.with_tools",
					Description: "Agent conversations that use tool execution",
					SuccessRate: 0.92, // 92% success rate
					UsageCount:  450,
					AvgCost:     0.003, // $0.003 per conversation
					AvgLatency:  1200,  // 1200ms
					Variant:     "default",
				},
				{
					Name:        "agent.conversation.no_tools",
					Description: "Agent conversations without tools",
					SuccessRate: 0.98, // 98% success - simpler, more reliable
					UsageCount:  320,
					AvgCost:     0.001,
					AvgLatency:  600,
					Variant:     "default",
				},
				{
					Name:        "agent.conversation.multi_turn",
					Description: "Multi-turn conversations with context",
					SuccessRate: 0.85, // 85% - more complex
					UsageCount:  280,
					AvgCost:     0.005,
					AvgLatency:  2400,
					Variant:     "default",
				},
				{
					Name:        "agent.self_correction.enabled",
					Description: "Conversations with self-correction enabled",
					SuccessRate: 0.88,
					UsageCount:  150,
					AvgCost:     0.004,
					AvgLatency:  1800,
					Variant:     "experimental",
				},
				{
					Name:        "agent.self_correction.disabled",
					Description: "Conversations without self-correction",
					SuccessRate: 0.75, // Lower success without correction
					UsageCount:  100,
					AvgCost:     0.002,
					AvgLatency:  800,
					Variant:     "control",
				},
			},
			goal: &loomv1.OptimizationGoal{
				CostWeight:    0.3, // Balance cost
				QualityWeight: 0.5, // Prioritize success rate
				LatencyWeight: 0.2, // Some latency consideration
			},
		},
		{
			name:        "LLM Provider Performance",
			description: "Analyzing LLM provider patterns from pkg/llm",
			domain:      "llm-providers",
			patterns: []PatternUsageScenario{
				{
					Name:        "llm.anthropic.claude_opus",
					Description: "Claude Opus 4.5 usage",
					SuccessRate: 0.96,
					UsageCount:  800,
					AvgCost:     0.015, // Higher cost, best quality
					AvgLatency:  2000,
					Variant:     "default",
				},
				{
					Name:        "llm.anthropic.claude_sonnet",
					Description: "Claude Sonnet usage",
					SuccessRate: 0.94,
					UsageCount:  1200,
					AvgCost:     0.003, // Much cheaper
					AvgLatency:  1200,
					Variant:     "default",
				},
				{
					Name:        "llm.anthropic.claude_haiku",
					Description: "Claude Haiku usage",
					SuccessRate: 0.88,
					UsageCount:  600,
					AvgCost:     0.0003, // Very cheap
					AvgLatency:  400,
					Variant:     "default",
				},
				{
					Name:        "llm.bedrock.claude_opus",
					Description: "Bedrock Claude Opus",
					SuccessRate: 0.95,
					UsageCount:  400,
					AvgCost:     0.015,
					AvgLatency:  2200, // Slightly slower than direct Anthropic
					Variant:     "aws",
				},
				{
					Name:        "llm.ollama.local",
					Description: "Local Ollama models",
					SuccessRate: 0.72, // Lower quality but free
					UsageCount:  200,
					AvgCost:     0.0,
					AvgLatency:  3000, // Slower on local hardware
					Variant:     "local",
				},
			},
			goal: &loomv1.OptimizationGoal{
				CostWeight:    0.4, // Cost is important for LLM usage
				QualityWeight: 0.5, // Quality still most important
				LatencyWeight: 0.1, // Latency less critical
			},
		},
		{
			name:        "Pattern Library Usage",
			description: "Analyzing pattern library effectiveness from pkg/patterns",
			domain:      "pattern-library",
			patterns: []PatternUsageScenario{
				{
					Name:        "pattern.sql.query_optimization",
					Description: "SQL query optimization patterns",
					SuccessRate: 0.91,
					UsageCount:  500,
					AvgCost:     0.002,
					AvgLatency:  800,
					Variant:     "default",
				},
				{
					Name:        "pattern.sql.teradata_specific",
					Description: "Teradata-specific SQL patterns",
					SuccessRate: 0.87,
					UsageCount:  350,
					AvgCost:     0.003,
					AvgLatency:  1000,
					Variant:     "teradata",
				},
				{
					Name:        "pattern.code.review",
					Description: "Code review patterns",
					SuccessRate: 0.93,
					UsageCount:  280,
					AvgCost:     0.004,
					AvgLatency:  1500,
					Variant:     "default",
				},
				{
					Name:        "pattern.debugging.error_analysis",
					Description: "Error analysis and debugging patterns",
					SuccessRate: 0.89,
					UsageCount:  220,
					AvgCost:     0.003,
					AvgLatency:  1200,
					Variant:     "default",
				},
				{
					Name:        "pattern.vision.diagram_analysis",
					Description: "Diagram and visual analysis patterns",
					SuccessRate: 0.68, // Lower success - complex task
					UsageCount:  80,
					AvgCost:     0.008, // Higher cost with vision
					AvgLatency:  3000,
					Variant:     "experimental",
				},
			},
			goal: &loomv1.OptimizationGoal{
				CostWeight:    0.2,
				QualityWeight: 0.7, // Prioritize pattern quality
				LatencyWeight: 0.1,
			},
		},
		{
			name:        "Orchestration Workflows",
			description: "Analyzing workflow orchestration patterns from pkg/orchestration",
			domain:      "orchestration",
			patterns: []PatternUsageScenario{
				{
					Name:        "workflow.pipeline.sequential",
					Description: "Sequential pipeline workflows",
					SuccessRate: 0.95,
					UsageCount:  180,
					AvgCost:     0.01,
					AvgLatency:  5000,
					Variant:     "default",
				},
				{
					Name:        "workflow.parallel.fan_out",
					Description: "Parallel fan-out workflows",
					SuccessRate: 0.88,
					UsageCount:  120,
					AvgCost:     0.015,
					AvgLatency:  3000, // Faster due to parallelism
					Variant:     "parallel",
				},
				{
					Name:        "workflow.debate.architecture",
					Description: "Multi-agent debate workflows",
					SuccessRate: 0.82,
					UsageCount:  90,
					AvgCost:     0.025, // Expensive - multiple agents
					AvgLatency:  8000,
					Variant:     "collaboration",
				},
				{
					Name:        "workflow.conditional.routing",
					Description: "Conditional routing workflows",
					SuccessRate: 0.90,
					UsageCount:  150,
					AvgCost:     0.008,
					AvgLatency:  4000,
					Variant:     "default",
				},
			},
			goal: &loomv1.OptimizationGoal{
				CostWeight:    0.3,
				QualityWeight: 0.4,
				LatencyWeight: 0.3, // Latency matters for workflows
			},
		},
		{
			name:        "Tool Execution Patterns",
			description: "Analyzing tool execution patterns from pkg/shuttle",
			domain:      "tool-execution",
			patterns: []PatternUsageScenario{
				{
					Name:        "tool.builtin.file_read",
					Description: "Built-in file read tool",
					SuccessRate: 0.98,
					UsageCount:  600,
					AvgCost:     0.0001,
					AvgLatency:  50,
					Variant:     "default",
				},
				{
					Name:        "tool.builtin.shell_exec",
					Description: "Shell execution tool",
					SuccessRate: 0.85, // Can fail due to command errors
					UsageCount:  300,
					AvgCost:     0.0002,
					AvgLatency:  200,
					Variant:     "default",
				},
				{
					Name:        "tool.mcp.filesystem",
					Description: "MCP filesystem tools",
					SuccessRate: 0.92,
					UsageCount:  250,
					AvgCost:     0.0003,
					AvgLatency:  100,
					Variant:     "mcp",
				},
				{
					Name:        "tool.custom.sql_query",
					Description: "Custom SQL query tool",
					SuccessRate: 0.88,
					UsageCount:  400,
					AvgCost:     0.001,
					AvgLatency:  500,
					Variant:     "backend",
				},
				{
					Name:        "tool.concurrent.parallel_exec",
					Description: "Concurrent tool execution",
					SuccessRate: 0.79, // Lower due to race conditions
					UsageCount:  150,
					AvgCost:     0.0005,
					AvgLatency:  300,
					Variant:     "experimental",
				},
			},
			goal: &loomv1.OptimizationGoal{
				CostWeight:    0.1, // Tools are cheap
				QualityWeight: 0.6, // Reliability is key
				LatencyWeight: 0.3, // Speed matters for tools
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			runDogfoodScenario(t, scenario)
		})
	}
}

// PatternUsageScenario represents realistic usage data for a pattern
type PatternUsageScenario struct {
	Name        string
	Description string
	SuccessRate float64 // 0.0 to 1.0
	UsageCount  int
	AvgCost     float64 // USD
	AvgLatency  int64   // milliseconds
	Variant     string
}

func runDogfoodScenario(t *testing.T, scenario struct {
	name        string
	description string
	domain      string
	patterns    []PatternUsageScenario
	goal        *loomv1.OptimizationGoal
}) {
	ctx := context.Background()
	tracer := observability.NewNoOpTracer()

	// Setup database
	dbPath := fmt.Sprintf("/tmp/loom-dogfood-%s-%d.db", scenario.domain, time.Now().UnixNano())
	defer os.Remove(dbPath)

	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db.Close()

	err = InitSelfImprovementSchema(ctx, db, tracer)
	require.NoError(t, err)

	collector, err := NewMetricsCollector(dbPath, tracer)
	require.NoError(t, err)

	engine := NewLearningEngine(collector, tracer)

	bus := communication.NewMessageBus(nil, nil, tracer, zap.NewNop())
	tracker := NewPatternEffectivenessTracker(db, tracer, bus, 1*time.Hour, 5*time.Minute)

	err = tracker.Start(ctx)
	require.NoError(t, err)

	agent, err := NewLearningAgent(db, tracer, engine, tracker, AutonomyManual, 1*time.Hour)
	require.NoError(t, err)

	t.Logf("\n🔬 DOGFOOD ANALYSIS: %s", scenario.name)
	t.Logf("📋 Description: %s", scenario.description)
	t.Logf("🏷️  Domain: %s", scenario.domain)
	t.Logf("═══════════════════════════════════════════════════════════════")

	// Generate realistic usage data
	t.Log("\n📊 Generating realistic usage data...")
	for _, pattern := range scenario.patterns {
		successCount := int(float64(pattern.UsageCount) * pattern.SuccessRate)
		failureCount := pattern.UsageCount - successCount

		// Record successes
		for i := 0; i < successCount; i++ {
			tracker.RecordUsage(
				ctx,
				pattern.Name,
				pattern.Variant,
				scenario.domain,
				"loom-framework",
				true,
				pattern.AvgCost,
				time.Duration(pattern.AvgLatency)*time.Millisecond,
				"",
				"anthropic",
				"claude-sonnet-4",
				nil, // No judge evaluation for this test
			)
		}

		// Record failures
		for i := 0; i < failureCount; i++ {
			errorTypes := []string{"timeout", "validation_error", "llm_error", "tool_failure", "parsing_error"}
			errorType := errorTypes[i%len(errorTypes)]

			tracker.RecordUsage(
				ctx,
				pattern.Name,
				pattern.Variant,
				scenario.domain,
				"loom-framework",
				false,
				pattern.AvgCost,
				time.Duration(pattern.AvgLatency*2)*time.Millisecond, // Failures take longer
				errorType,
				"anthropic",
				"claude-sonnet-4",
				nil, // No judge evaluation for this test
			)
		}

		t.Logf("  ✅ %s: %.1f%% success (%d/%d uses)",
			pattern.Name, pattern.SuccessRate*100, successCount, pattern.UsageCount)
	}

	// Flush data
	err = tracker.Stop(ctx)
	require.NoError(t, err)

	// Analyze patterns
	t.Log("\n🔍 Learning agent analyzing patterns...")
	analysisResp, err := agent.AnalyzePatternEffectiveness(ctx, &loomv1.AnalyzePatternEffectivenessRequest{
		Domain:      scenario.domain,
		AgentId:     "loom-framework",
		WindowHours: 24,
	})
	require.NoError(t, err)
	require.NotNil(t, analysisResp)

	t.Logf("\n📈 Analysis Results (%d patterns):", len(analysisResp.Patterns))
	t.Logf("───────────────────────────────────────────────────────────────")

	// Categorize patterns by recommendation
	promote := []*loomv1.PatternMetric{}
	keep := []*loomv1.PatternMetric{}
	demote := []*loomv1.PatternMetric{}
	remove := []*loomv1.PatternMetric{}
	investigate := []*loomv1.PatternMetric{}

	for _, p := range analysisResp.Patterns {
		switch p.Recommendation {
		case loomv1.PatternRecommendation_PATTERN_PROMOTE:
			promote = append(promote, p)
		case loomv1.PatternRecommendation_PATTERN_KEEP:
			keep = append(keep, p)
		case loomv1.PatternRecommendation_PATTERN_DEMOTE:
			demote = append(demote, p)
		case loomv1.PatternRecommendation_PATTERN_REMOVE:
			remove = append(remove, p)
		case loomv1.PatternRecommendation_PATTERN_INVESTIGATE:
			investigate = append(investigate, p)
		}
	}

	// Display by category
	if len(promote) > 0 {
		t.Log("\n⬆️  PROMOTE (High performers - use more):")
		for _, p := range promote {
			t.Logf("  • %s: %.1f%% success, $%.4f cost, %dms latency",
				p.PatternName, p.SuccessRate*100, p.AvgCostUsd, p.AvgLatencyMs)
		}
	}

	if len(keep) > 0 {
		t.Log("\n✅ KEEP (Performing well):")
		for _, p := range keep {
			t.Logf("  • %s: %.1f%% success, $%.4f cost, %dms latency",
				p.PatternName, p.SuccessRate*100, p.AvgCostUsd, p.AvgLatencyMs)
		}
	}

	if len(demote) > 0 {
		t.Log("\n⬇️  DEMOTE (Needs tuning):")
		for _, p := range demote {
			t.Logf("  • %s: %.1f%% success, $%.4f cost, %dms latency",
				p.PatternName, p.SuccessRate*100, p.AvgCostUsd, p.AvgLatencyMs)
		}
	}

	if len(remove) > 0 {
		t.Log("\n❌ REMOVE (Poor performers):")
		for _, p := range remove {
			t.Logf("  • %s: %.1f%% success, $%.4f cost, %dms latency",
				p.PatternName, p.SuccessRate*100, p.AvgCostUsd, p.AvgLatencyMs)
		}
	}

	if len(investigate) > 0 {
		t.Log("\n🔍 INVESTIGATE (Needs more data):")
		for _, p := range investigate {
			t.Logf("  • %s: %.1f%% success (%d samples)",
				p.PatternName, p.SuccessRate*100, p.TotalUsages)
		}
	}

	// Generate improvement proposals
	t.Log("\n💡 Generating improvement proposals...")
	proposalsResp, err := agent.GenerateImprovements(ctx, &loomv1.GenerateImprovementsRequest{
		Domain:           scenario.domain,
		AgentId:          "loom-framework",
		MaxProposals:     10,
		OptimizationGoal: scenario.goal,
	})
	require.NoError(t, err)
	require.NotNil(t, proposalsResp)

	if len(proposalsResp.Improvements) > 0 {
		t.Logf("\n✨ Improvement Proposals (%d):", len(proposalsResp.Improvements))
		t.Logf("───────────────────────────────────────────────────────────────")

		for i, imp := range proposalsResp.Improvements {
			impactIcon := "📊"
			switch imp.Impact {
			case loomv1.ImpactLevel_IMPACT_HIGH:
				impactIcon = "🔴"
			case loomv1.ImpactLevel_IMPACT_MEDIUM:
				impactIcon = "🟡"
			case loomv1.ImpactLevel_IMPACT_LOW:
				impactIcon = "🟢"
			}

			t.Logf("\n%d. %s %s", i+1, impactIcon, imp.Description)
			t.Logf("   Type: %s | Confidence: %.0f%% | Impact: %s",
				imp.Type, imp.Confidence*100, imp.Impact)

			if imp.Details != nil {
				t.Logf("   Expected Impact:")
				t.Logf("     • Success Rate: %+.1f%%", imp.Details.ExpectedSuccessRateDelta*100)
				t.Logf("     • Cost: $%+.4f", imp.Details.ExpectedCostDeltaUsd)
				t.Logf("     • Latency: %+dms", imp.Details.ExpectedLatencyDeltaMs)
				if imp.Details.Rationale != "" {
					t.Logf("   Rationale: %s", imp.Details.Rationale)
				}
			}
		}
	} else {
		t.Log("\n  ℹ️  No improvements generated - patterns are performing well!")
	}

	// Summary statistics
	t.Log("\n📊 Summary Statistics:")
	t.Logf("───────────────────────────────────────────────────────────────")
	if analysisResp.Summary != nil {
		t.Logf("  • Total Patterns: %d", analysisResp.Summary.TotalPatternsAnalyzed)
		t.Logf("  • Overall Success Rate: %.1f%%", analysisResp.Summary.OverallSuccessRate*100)
		t.Logf("  • Total Cost: $%.4f", analysisResp.Summary.TotalCostUsd)
		t.Logf("  • Patterns to Promote: %d", analysisResp.Summary.PatternsToPromote)
		t.Logf("  • Patterns to Deprecate: %d", analysisResp.Summary.PatternsToDeprecate)
	}

	// Key insights
	t.Log("\n🎯 Key Insights:")
	t.Logf("───────────────────────────────────────────────────────────────")

	// Calculate cost efficiency (success rate per dollar)
	bestCostEfficiency := 0.0
	var bestCostPattern *loomv1.PatternMetric
	for _, p := range analysisResp.Patterns {
		if p.AvgCostUsd > 0 {
			efficiency := p.SuccessRate / p.AvgCostUsd
			if efficiency > bestCostEfficiency {
				bestCostEfficiency = efficiency
				bestCostPattern = p
			}
		}
	}
	if bestCostPattern != nil {
		t.Logf("  • Most Cost-Efficient: %s (%.0f success/$ ratio)",
			bestCostPattern.PatternName, bestCostEfficiency)
	}

	// Calculate speed champion (lowest latency with high success)
	var speedChampion *loomv1.PatternMetric
	minLatency := int64(999999)
	for _, p := range analysisResp.Patterns {
		if p.SuccessRate >= 0.85 && p.AvgLatencyMs < minLatency {
			minLatency = p.AvgLatencyMs
			speedChampion = p
		}
	}
	if speedChampion != nil {
		t.Logf("  • Fastest Reliable Pattern: %s (%dms, %.1f%% success)",
			speedChampion.PatternName, speedChampion.AvgLatencyMs, speedChampion.SuccessRate*100)
	}

	// Calculate quality leader (highest success rate with significant usage)
	var qualityLeader *loomv1.PatternMetric
	maxSuccess := 0.0
	for _, p := range analysisResp.Patterns {
		if p.TotalUsages >= 100 && p.SuccessRate > maxSuccess {
			maxSuccess = p.SuccessRate
			qualityLeader = p
		}
	}
	if qualityLeader != nil {
		t.Logf("  • Highest Quality: %s (%.1f%% success, %d uses)",
			qualityLeader.PatternName, qualityLeader.SuccessRate*100, qualityLeader.TotalUsages)
	}

	t.Log("\n═══════════════════════════════════════════════════════════════")
	t.Logf("✅ Dogfood analysis complete for %s", scenario.name)
	t.Log("═══════════════════════════════════════════════════════════════\n")
}
