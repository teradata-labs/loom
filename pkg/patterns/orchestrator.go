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
package patterns

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/metaagent/learning"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Orchestrator performs intent classification and execution planning.
// It's the top-level routing layer that determines which tools/patterns to use.
type Orchestrator struct {
	library *Library
	tracer  observability.Tracer
	tracker *learning.PatternEffectivenessTracker

	// Pluggable intent classifier (backend-specific)
	intentClassifier IntentClassifierFunc

	// Pluggable execution planner (backend-specific)
	executionPlanner ExecutionPlannerFunc
}

// NewOrchestrator creates a new orchestrator with the given library.
func NewOrchestrator(library *Library) *Orchestrator {
	return &Orchestrator{
		library:          library,
		tracer:           observability.NewNoOpTracer(),
		intentClassifier: defaultIntentClassifier,
		executionPlanner: defaultExecutionPlanner,
	}
}

// WithTracer sets the observability tracer for the orchestrator.
func (o *Orchestrator) WithTracer(tracer observability.Tracer) *Orchestrator {
	o.tracer = tracer
	return o
}

// WithTracker sets the pattern effectiveness tracker for the orchestrator.
// When set, the orchestrator will record pattern usage metrics after execution.
func (o *Orchestrator) WithTracker(tracker *learning.PatternEffectivenessTracker) *Orchestrator {
	o.tracker = tracker
	return o
}

// GetLibrary returns the pattern library.
func (o *Orchestrator) GetLibrary() *Library {
	return o.library
}

// SetIntentClassifier sets a custom intent classifier function.
// Backends can provide domain-specific classifiers for better accuracy.
func (o *Orchestrator) SetIntentClassifier(classifier IntentClassifierFunc) {
	o.intentClassifier = classifier
}

// SetExecutionPlanner sets a custom execution planner function.
// Backends can provide domain-specific planners for optimized execution.
func (o *Orchestrator) SetExecutionPlanner(planner ExecutionPlannerFunc) {
	o.executionPlanner = planner
}

// ClassifyIntent analyzes user message and determines intent category.
// Returns intent category and confidence score (0.0-1.0).
// Uses pluggable classifier if set, otherwise uses default keyword-based classifier.
func (o *Orchestrator) ClassifyIntent(userMessage string, ctxData map[string]interface{}) (IntentCategory, float64) {
	startTime := time.Now()
	_, span := o.tracer.StartSpan(context.Background(), "patterns.orchestrator.classify_intent")
	defer o.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("message.length", fmt.Sprintf("%d", len(userMessage)))
		span.SetAttribute("context.keys", fmt.Sprintf("%d", len(ctxData)))
	}

	intent, confidence := o.intentClassifier(userMessage, ctxData)

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("intent.category", string(intent))
		span.SetAttribute("intent.confidence", fmt.Sprintf("%.2f", confidence))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	o.tracer.RecordMetric("patterns.orchestrator.classify_intent", 1.0, map[string]string{
		"intent":     string(intent),
		"confidence": fmt.Sprintf("%.1f", confidence*100),
	})

	return intent, confidence
}

// PlanExecution creates an execution plan based on classified intent.
// Uses pluggable planner if set, otherwise uses default generic planner.
func (o *Orchestrator) PlanExecution(intent IntentCategory, userMessage string, ctxData map[string]interface{}) (*ExecutionPlan, error) {
	startTime := time.Now()
	_, span := o.tracer.StartSpan(context.Background(), "patterns.orchestrator.plan_execution")
	defer o.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("intent.category", string(intent))
		span.SetAttribute("message.length", fmt.Sprintf("%d", len(userMessage)))
		span.SetAttribute("context.keys", fmt.Sprintf("%d", len(ctxData)))
	}

	plan, err := o.executionPlanner(intent, userMessage, ctxData)

	duration := time.Since(startTime)
	if err != nil {
		if span != nil {
			span.SetAttribute("plan.result", "error")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
			span.RecordError(err)
		}
		o.tracer.RecordMetric("patterns.orchestrator.plan_execution", 1.0, map[string]string{
			"intent": string(intent),
			"result": "error",
		})
		return nil, err
	}

	if span != nil {
		span.SetAttribute("plan.result", "success")
		span.SetAttribute("plan.step_count", fmt.Sprintf("%d", len(plan.Steps)))
		span.SetAttribute("plan.description", plan.Description)
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	o.tracer.RecordMetric("patterns.orchestrator.plan_execution", 1.0, map[string]string{
		"intent":     string(intent),
		"result":     "success",
		"step_count": fmt.Sprintf("%d", len(plan.Steps)),
	})

	return plan, nil
}

// GetRoutingRecommendation provides intelligent routing suggestions.
// This helps the LLM choose the most efficient tool/pattern combination.
func (o *Orchestrator) GetRoutingRecommendation(intent IntentCategory) string {
	recommendations := map[IntentCategory]string{
		IntentSchemaDiscovery: "For schema discovery, prefer comprehensive discovery tools with caching. " +
			"Check if schema is already cached before making expensive calls.",

		IntentDataQuality: "For data quality assessment, consider using workflow patterns for comprehensive checks. " +
			"For single validation rules, use individual quality check tools.",

		IntentDataTransform: "For data transformation, use ETL workflow patterns with validation gates. " +
			"Include source validation, transformation logic, and result verification.",

		IntentAnalytics: "For analytics queries, validate and estimate cost before execution. " +
			"Consider using pattern library for complex analytics (ML, time series, advanced aggregations).",

		IntentRelationshipQuery: "For relationship queries, use schema inference tools with FK detection. " +
			"Results include confidence scores for inferred relationships.",

		IntentQueryGeneration: "For query generation, validate syntax and estimate cost before execution. " +
			"Use patterns from library for complex query structures.",

		IntentDocumentSearch: "For document search, use appropriate indexing and search patterns. " +
			"Consider full-text search, vector similarity, or hybrid approaches.",

		IntentAPICall: "For API calls, validate request structure and handle responses with proper error handling. " +
			"Use retry patterns for transient failures.",
	}

	if rec, ok := recommendations[intent]; ok {
		return rec
	}
	return "No specific routing recommendation available. Use default tool selection."
}

// RecommendPattern suggests a pattern from the library based on user message and intent.
// Returns pattern name and confidence score (0.0-1.0).
func (o *Orchestrator) RecommendPattern(userMessage string, intent IntentCategory) (string, float64) {
	startTime := time.Now()
	_, span := o.tracer.StartSpan(context.Background(), "patterns.orchestrator.recommend_pattern")
	defer o.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("intent.category", string(intent))
		span.SetAttribute("message.length", fmt.Sprintf("%d", len(userMessage)))
	}

	messageLower := strings.ToLower(userMessage)

	// Search for patterns matching the user's keywords
	searchResults := o.library.Search(userMessage)

	if span != nil {
		span.SetAttribute("search.result_count", fmt.Sprintf("%d", len(searchResults)))
	}

	if len(searchResults) == 0 {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("recommendation.result", "no_match")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		o.tracer.RecordMetric("patterns.orchestrator.recommend_pattern", 1.0, map[string]string{
			"intent": string(intent),
			"result": "no_match",
		})
		return "", 0.0
	}

	// Score patterns based on intent match and keyword relevance
	type scoredPattern struct {
		name  string
		score float64
	}

	scored := make([]scoredPattern, 0)

	for _, summary := range searchResults {
		score := 0.0

		// Boost if category matches intent
		if matchesIntent(summary.Category, intent) {
			score += 0.4
		}

		// Boost if use cases mention keywords
		for _, useCase := range summary.UseCases {
			if strings.Contains(strings.ToLower(useCase), messageLower) {
				score += 0.3
				break
			}
		}

		// Boost if title matches keywords
		if strings.Contains(strings.ToLower(summary.Title), messageLower) {
			score += 0.2
		}

		// Boost if description matches keywords
		if strings.Contains(strings.ToLower(summary.Description), messageLower) {
			score += 0.1
		}

		if score > 0 {
			scored = append(scored, scoredPattern{name: summary.Name, score: score})
		}
	}

	if len(scored) == 0 {
		duration := time.Since(startTime)
		if span != nil {
			span.SetAttribute("recommendation.result", "no_scored_match")
			span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
		}
		o.tracer.RecordMetric("patterns.orchestrator.recommend_pattern", 1.0, map[string]string{
			"intent": string(intent),
			"result": "no_scored_match",
		})
		return "", 0.0
	}

	// Return highest scoring pattern
	best := scored[0]
	for _, s := range scored[1:] {
		if s.score > best.score {
			best = s
		}
	}

	// Cap confidence at 0.9 (never 100% certain)
	if best.score > 0.9 {
		best.score = 0.9
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("recommendation.result", "success")
		span.SetAttribute("recommendation.pattern", best.name)
		span.SetAttribute("recommendation.confidence", fmt.Sprintf("%.2f", best.score))
		span.SetAttribute("recommendation.candidates", fmt.Sprintf("%d", len(scored)))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	o.tracer.RecordMetric("patterns.orchestrator.recommend_pattern", 1.0, map[string]string{
		"intent":     string(intent),
		"result":     "success",
		"pattern":    best.name,
		"confidence": fmt.Sprintf("%.1f", best.score*100),
	})

	return best.name, best.score
}

// RecordPatternUsage records pattern usage metrics to the effectiveness tracker.
// This should be called after a pattern is executed to capture success/failure, cost, latency, etc.
//
// Parameters:
//   - ctx: Context containing pattern metadata (variant, domain via context helpers)
//   - patternName: Name of the pattern that was executed
//   - agentID: ID of the agent that executed the pattern
//   - success: Whether execution succeeded
//   - costUSD: Cost of execution in USD
//   - latency: Execution duration
//   - errorType: Type of error if failed (empty if success)
//   - llmProvider: LLM provider used (e.g., "anthropic", "bedrock")
//   - llmModel: LLM model used (e.g., "claude-3-5-sonnet-20241022")
//
// The method extracts variant and domain from context using GetPatternMetadata().
// If no tracker is configured, this is a no-op.
func (o *Orchestrator) RecordPatternUsage(
	ctx context.Context,
	patternName string,
	agentID string,
	success bool,
	costUSD float64,
	latency time.Duration,
	errorType string,
	llmProvider string,
	llmModel string,
) {
	if o.tracker == nil {
		return // No tracker configured
	}

	_, span := o.tracer.StartSpan(ctx, "patterns.orchestrator.record_usage")
	defer o.tracer.EndSpan(span)

	// Extract pattern metadata from context
	metadata := GetPatternMetadata(ctx)
	variant := metadata.Variant
	domain := metadata.Domain

	// Use defaults if not in context
	if variant == "" {
		variant = "default"
	}
	if domain == "" {
		domain = "unknown"
	}

	if span != nil {
		span.SetAttribute("pattern.name", patternName)
		span.SetAttribute("pattern.variant", variant)
		span.SetAttribute("pattern.domain", domain)
		span.SetAttribute("agent.id", agentID)
		span.SetAttribute("success", success)
		span.SetAttribute("cost_usd", fmt.Sprintf("%.6f", costUSD))
		span.SetAttribute("latency_ms", fmt.Sprintf("%.2f", latency.Seconds()*1000))
		if errorType != "" {
			span.SetAttribute("error.type", errorType)
		}
		span.SetAttribute("llm.provider", llmProvider)
		span.SetAttribute("llm.model", llmModel)
	}

	// Record usage to tracker
	o.tracker.RecordUsage(
		ctx,
		patternName,
		variant,
		domain,
		agentID,
		success,
		costUSD,
		latency,
		errorType,
		llmProvider,
		llmModel,
		nil, // No judge result for pattern orchestrator usage
	)

	o.tracer.RecordMetric("patterns.orchestrator.usage_recorded", 1.0, map[string]string{
		"pattern": patternName,
		"variant": variant,
		"success": fmt.Sprintf("%t", success),
	})
}

// defaultIntentClassifier is the default keyword-based intent classifier.
// Backends should provide custom classifiers for better accuracy.
func defaultIntentClassifier(userMessage string, context map[string]interface{}) (IntentCategory, float64) {
	messageLower := strings.ToLower(userMessage)

	// Schema discovery keywords
	schemaKeywords := []string{"what tables", "list tables", "show tables", "what columns", "schema", "table structure", "describe"}
	if containsAny(messageLower, schemaKeywords) {
		return IntentSchemaDiscovery, 0.90
	}

	// Relationship query keywords
	relationshipKeywords := []string{"related", "foreign key", "relationship", "connected to", "references", "joins"}
	if containsAny(messageLower, relationshipKeywords) {
		return IntentRelationshipQuery, 0.85
	}

	// Data quality keywords
	dqKeywords := []string{"data quality", "duplicates", "null", "completeness", "validate", "check quality", "integrity"}
	if containsAny(messageLower, dqKeywords) {
		return IntentDataQuality, 0.90
	}

	// Data transform keywords
	transformKeywords := []string{"move data", "copy", "load data", "extract", "transform", "etl", "migrate", "transfer"}
	if containsAny(messageLower, transformKeywords) {
		return IntentDataTransform, 0.85
	}

	// Analytics keywords
	analyticsKeywords := []string{"aggregate", "sum", "count", "average", "group by", "analyze", "report", "metrics", "statistics"}
	if containsAny(messageLower, analyticsKeywords) {
		return IntentAnalytics, 0.80
	}

	// Query generation keywords
	queryKeywords := []string{"write query", "generate query", "query for", "select", "find", "get data"}
	if containsAny(messageLower, queryKeywords) {
		return IntentQueryGeneration, 0.75
	}

	// Document search keywords
	docKeywords := []string{"search document", "find in document", "document query", "text search", "full text"}
	if containsAny(messageLower, docKeywords) {
		return IntentDocumentSearch, 0.80
	}

	// API call keywords
	apiKeywords := []string{"api call", "http request", "rest api", "endpoint", "webhook"}
	if containsAny(messageLower, apiKeywords) {
		return IntentAPICall, 0.85
	}

	return IntentUnknown, 0.0
}

// defaultExecutionPlanner is the default generic execution planner.
// Backends should provide custom planners for domain-specific optimization.
func defaultExecutionPlanner(intent IntentCategory, userMessage string, context map[string]interface{}) (*ExecutionPlan, error) {
	plan := &ExecutionPlan{
		Intent: intent,
		Steps:  make([]PlannedStep, 0),
	}

	switch intent {
	case IntentSchemaDiscovery:
		plan.Description = "Discover data schema"
		plan.Reasoning = "User wants to explore data structure. Using schema discovery tools."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "get_schema",
			Description: "Retrieve schema information",
			Params:      make(map[string]string),
		})

	case IntentRelationshipQuery:
		plan.Description = "Analyze data relationships"
		plan.Reasoning = "User wants to understand data relationships. Using relationship inference."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "infer_relationships",
			Description: "Infer foreign key relationships",
			Params:      make(map[string]string),
		})

	case IntentDataQuality:
		plan.Description = "Execute data quality assessment"
		plan.Reasoning = "User needs quality validation. Using quality check tools."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "check_quality",
			Description: "Run data quality checks",
			Params:      make(map[string]string),
		})

	case IntentDataTransform:
		plan.Description = "Execute data transformation"
		plan.Reasoning = "User wants to transform data. Using ETL workflow."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "transform_data",
			Description: "Execute transformation pipeline",
			Params:      make(map[string]string),
		})

	case IntentAnalytics:
		plan.Description = "Generate and execute analytics"
		plan.Reasoning = "User wants analytics. Using query generation and execution."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "execute_query",
			Description: "Execute analytics query",
			Params:      make(map[string]string),
		})

	case IntentQueryGeneration:
		plan.Description = "Generate query"
		plan.Reasoning = "User wants query generation. Creating query from requirements."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "generate_query",
			Description: "Generate query from natural language",
			Params:      make(map[string]string),
		})

	case IntentDocumentSearch:
		plan.Description = "Search documents"
		plan.Reasoning = "User wants document search. Using search tools."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "search_documents",
			Description: "Search document collection",
			Params:      make(map[string]string),
		})

	case IntentAPICall:
		plan.Description = "Execute API call"
		plan.Reasoning = "User wants API interaction. Using HTTP client."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "call_api",
			Description: "Execute API request",
			Params:      make(map[string]string),
		})

	default:
		return nil, fmt.Errorf("cannot plan execution for unknown intent")
	}

	return plan, nil
}

// matchesIntent checks if a pattern category matches an intent.
func matchesIntent(category string, intent IntentCategory) bool {
	categoryLower := strings.ToLower(category)
	intentStr := strings.ToLower(string(intent))

	// Direct match
	if categoryLower == intentStr {
		return true
	}

	// Fuzzy matches
	switch intent {
	case IntentAnalytics:
		return categoryLower == "analytics" || categoryLower == "aggregation" || categoryLower == "reporting"
	case IntentDataQuality:
		return categoryLower == "data_quality" || categoryLower == "validation" || categoryLower == "quality"
	case IntentDataTransform:
		return categoryLower == "etl" || categoryLower == "transform" || categoryLower == "data_transform"
	case IntentSchemaDiscovery:
		return categoryLower == "schema" || categoryLower == "metadata" || categoryLower == "discovery"
	}

	return false
}

// containsAny checks if string contains any of the keywords.
func containsAny(s string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(s, keyword) {
			return true
		}
	}
	return false
}
