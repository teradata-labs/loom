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
	"github.com/teradata-labs/loom/pkg/types"
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

	// LLM provider for re-ranking (optional, enables hybrid approach)
	llmProvider types.LLMProvider
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

// SetLLMProvider sets the LLM provider for pattern re-ranking.
// When set, enables hybrid approach: fast keyword matching + LLM re-ranking for ambiguous cases.
func (o *Orchestrator) SetLLMProvider(provider types.LLMProvider) {
	o.llmProvider = provider
}

// ClassifyIntent analyzes user message and determines intent category.
// Returns intent category and confidence score (0.0-1.0).
// Uses pluggable classifier if set, otherwise uses default keyword-based classifier.
func (o *Orchestrator) ClassifyIntent(userMessage string, ctxData map[string]interface{}) (string, float64) {
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
		span.SetAttribute("intent.category", intent)
		span.SetAttribute("intent.confidence", fmt.Sprintf("%.2f", confidence))
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	o.tracer.RecordMetric("patterns.orchestrator.classify_intent", 1.0, map[string]string{
		"intent":     intent,
		"confidence": fmt.Sprintf("%.1f", confidence*100),
	})

	return intent, confidence
}

// PlanExecution creates an execution plan based on classified intent.
// Uses pluggable planner if set, otherwise uses default generic planner.
func (o *Orchestrator) PlanExecution(intent string, userMessage string, ctxData map[string]interface{}) (*ExecutionPlan, error) {
	startTime := time.Now()
	_, span := o.tracer.StartSpan(context.Background(), "patterns.orchestrator.plan_execution")
	defer o.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("intent.category", intent)
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
			"intent": intent,
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
func (o *Orchestrator) GetRoutingRecommendation(intent string) string {
	recommendations := map[string]string{
		"schema_discovery": "For schema discovery, prefer comprehensive discovery tools with caching. " +
			"Check if schema is already cached before making expensive calls.",

		"data_quality": "For data quality assessment, consider using workflow patterns for comprehensive checks. " +
			"For single validation rules, use individual quality check tools.",

		"data_transform": "For data transformation, use ETL workflow patterns with validation gates. " +
			"Include source validation, transformation logic, and result verification.",

		"analytics": "For analytics queries, validate and estimate cost before execution. " +
			"Consider using pattern library for complex analytics (ML, time series, advanced aggregations).",

		"relationship_query": "For relationship queries, use schema inference tools with FK detection. " +
			"Results include confidence scores for inferred relationships.",

		"query_generation": "For query generation, validate syntax and estimate cost before execution. " +
			"Use patterns from library for complex query structures.",

		"document_search": "For document search, use appropriate indexing and search patterns. " +
			"Consider full-text search, vector similarity, or hybrid approaches.",

		"api_call": "For API calls, validate request structure and handle responses with proper error handling. " +
			"Use retry patterns for transient failures.",
	}

	if rec, ok := recommendations[intent]; ok {
		return rec
	}
	return "No specific routing recommendation available. Use default tool selection."
}

// RecommendPattern suggests a pattern from the library based on user message and intent.
// Returns pattern name and confidence score (0.0-1.0).
func (o *Orchestrator) RecommendPattern(userMessage string, intent string) (string, float64) {
	startTime := time.Now()
	_, span := o.tracer.StartSpan(context.Background(), "patterns.orchestrator.recommend_pattern")
	defer o.tracer.EndSpan(span)

	if span != nil {
		span.SetAttribute("intent.category", intent)
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
			"intent": intent,
			"result": "no_match",
		})
		return "", 0.0
	}

	// Score patterns based on intent match and keyword relevance
	scored := make([]scoredPattern, 0)

	// Tokenize user message for better matching
	keywords := strings.FieldsFunc(messageLower, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '-' || r == '_'
	})

	// Filter stop words
	stopWords := map[string]bool{
		"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
		"be": true, "by": true, "for": true, "from": true, "has": true, "in": true,
		"is": true, "it": true, "of": true, "on": true, "that": true, "the": true,
		"to": true, "was": true, "will": true, "with": true,
	}

	filteredKeywords := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		if !stopWords[kw] && len(kw) > 2 {
			filteredKeywords = append(filteredKeywords, kw)
		}
	}

	for _, summary := range searchResults {
		score := 0.0

		// Build searchable text
		searchText := strings.ToLower(fmt.Sprintf("%s %s %s %s",
			summary.Name, summary.Title, summary.Description, summary.BackendFunction))

		for _, useCase := range summary.UseCases {
			searchText += " " + strings.ToLower(useCase)
		}

		// Boost if intent matches pattern's declared intents or category (strong signal)
		if matchesIntent(summary.Category, summary.Intents, intent) {
			score += 0.5
		} else if intent == "" {
			// When intent is unknown, give partial boost to relevant categories
			// This helps ML, analytics, and data patterns rank higher
			categoryLower := strings.ToLower(summary.Category)
			switch categoryLower {
			case "ml", "analytics", "timeseries":
				score += 0.4 // High relevance
			case "data_quality", "etl", "data_transform":
				score += 0.3 // Medium relevance
			case "data-import", "learning", "reasoning":
				score += 0.2 // Lower relevance
			}
		}

		// Count keyword matches in searchable text
		keywordMatches := 0
		for _, keyword := range filteredKeywords {
			if strings.Contains(searchText, keyword) {
				keywordMatches++
			}
		}

		// Score based on percentage of keywords matched
		if len(filteredKeywords) > 0 {
			matchRate := float64(keywordMatches) / float64(len(filteredKeywords))
			score += matchRate * 0.5 // Up to 0.5 points for keyword matching
		}

		// Bonus for exact name match
		if strings.Contains(summary.Name, messageLower) {
			score += 0.2
		}

		// Bonus for title match
		titleLower := strings.ToLower(summary.Title)
		for _, keyword := range filteredKeywords {
			if strings.Contains(titleLower, keyword) {
				score += 0.1
				break
			}
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
			"intent": intent,
			"result": "no_scored_match",
		})
		return "", 0.0
	}

	// === HYBRID APPROACH: Decide if we need LLM re-ranking ===
	useLLM := shouldInvokeLLMReRanker(scored, intent, o.llmProvider)

	if span != nil {
		span.SetAttribute("llm_reranking.triggered", fmt.Sprintf("%t", useLLM))
	}

	var finalPattern string
	var finalConfidence float64

	if useLLM {
		// Use LLM to re-rank top candidates for better accuracy
		topN := min(5, len(scored))
		topCandidates := scored[:topN]

		if span != nil {
			span.SetAttribute("llm_reranking.candidates", fmt.Sprintf("%d", topN))
		}

		// Build summary map for LLM re-ranker
		summaries := make(map[string]PatternSummary)
		for _, s := range topCandidates {
			// Find summary in search results
			for _, summary := range searchResults {
				if summary.Name == s.name {
					summaries[s.name] = summary
					break
				}
			}
		}

		llmPattern, llmConf, err := reRankPatternsWithLLM(o.llmProvider, userMessage, topCandidates, summaries)
		if err != nil {
			// Fallback to keyword scoring on error
			if span != nil {
				span.RecordError(fmt.Errorf("LLM re-ranking failed, using keyword fallback: %w", err))
				span.SetAttribute("llm_reranking.fallback", "true")
			}
			finalPattern = scored[0].name
			finalConfidence = scored[0].score
		} else {
			finalPattern = llmPattern
			finalConfidence = llmConf
			if span != nil {
				span.SetAttribute("llm_reranking.success", "true")
			}
		}
	} else {
		// Use keyword-based scoring (fast path)
		finalPattern = scored[0].name
		finalConfidence = scored[0].score

		// Cap confidence at 0.9 (never 100% certain for keyword matching)
		if finalConfidence > 0.9 {
			finalConfidence = 0.9
		}
	}

	duration := time.Since(startTime)
	if span != nil {
		span.SetAttribute("recommendation.result", "success")
		span.SetAttribute("recommendation.pattern", finalPattern)
		span.SetAttribute("recommendation.confidence", fmt.Sprintf("%.2f", finalConfidence))
		span.SetAttribute("recommendation.candidates", fmt.Sprintf("%d", len(scored)))
		span.SetAttribute("recommendation.method", map[bool]string{true: "llm", false: "keyword"}[useLLM])
		span.SetAttribute("duration_ms", fmt.Sprintf("%.2f", duration.Seconds()*1000))
	}

	o.tracer.RecordMetric("patterns.orchestrator.recommend_pattern", 1.0, map[string]string{
		"intent":     string(intent),
		"result":     "success",
		"pattern":    finalPattern,
		"method":     map[bool]string{true: "llm", false: "keyword"}[useLLM],
		"confidence": fmt.Sprintf("%.1f", finalConfidence*100),
	})

	return finalPattern, finalConfidence
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
func defaultIntentClassifier(userMessage string, context map[string]interface{}) (string, float64) {
	messageLower := strings.ToLower(userMessage)

	// Schema discovery keywords
	schemaKeywords := []string{"what tables", "list tables", "show tables", "what columns", "schema", "table structure", "describe"}
	if containsAny(messageLower, schemaKeywords) {
		return "schema_discovery", 0.90
	}

	// Relationship query keywords
	relationshipKeywords := []string{"related", "foreign key", "relationship", "connected to", "references", "joins"}
	if containsAny(messageLower, relationshipKeywords) {
		return "relationship_query", 0.85
	}

	// Data quality keywords
	dqKeywords := []string{"data quality", "duplicates", "null", "completeness", "validate", "check quality", "integrity"}
	if containsAny(messageLower, dqKeywords) {
		return "data_quality", 0.90
	}

	// Data transform keywords
	transformKeywords := []string{"move data", "copy", "load data", "extract", "transform", "etl", "migrate", "transfer"}
	if containsAny(messageLower, transformKeywords) {
		return "data_transform", 0.85
	}

	// Analytics keywords
	analyticsKeywords := []string{"aggregate", "sum", "count", "average", "group by", "analyze", "report", "metrics", "statistics"}
	if containsAny(messageLower, analyticsKeywords) {
		return "analytics", 0.80
	}

	// Query generation keywords
	queryKeywords := []string{"write query", "generate query", "query for", "select", "find", "get data"}
	if containsAny(messageLower, queryKeywords) {
		return "query_generation", 0.75
	}

	// Document search keywords
	docKeywords := []string{"search document", "find in document", "document query", "text search", "full text"}
	if containsAny(messageLower, docKeywords) {
		return "document_search", 0.80
	}

	// API call keywords
	apiKeywords := []string{"api call", "http request", "rest api", "endpoint", "webhook"}
	if containsAny(messageLower, apiKeywords) {
		return "api_call", 0.85
	}

	return "", 0.0
}

// defaultExecutionPlanner is the default generic execution planner.
// Backends should provide custom planners for domain-specific optimization.
func defaultExecutionPlanner(intent string, userMessage string, context map[string]interface{}) (*ExecutionPlan, error) {
	plan := &ExecutionPlan{
		Intent: intent,
		Steps:  make([]PlannedStep, 0),
	}

	switch intent {
	case "schema_discovery":
		plan.Description = "Discover data schema"
		plan.Reasoning = "User wants to explore data structure. Using schema discovery tools."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "get_schema",
			Description: "Retrieve schema information",
			Params:      make(map[string]string),
		})

	case "relationship_query":
		plan.Description = "Analyze data relationships"
		plan.Reasoning = "User wants to understand data relationships. Using relationship inference."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "infer_relationships",
			Description: "Infer foreign key relationships",
			Params:      make(map[string]string),
		})

	case "data_quality":
		plan.Description = "Execute data quality assessment"
		plan.Reasoning = "User needs quality validation. Using quality check tools."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "check_quality",
			Description: "Run data quality checks",
			Params:      make(map[string]string),
		})

	case "data_transform":
		plan.Description = "Execute data transformation"
		plan.Reasoning = "User wants to transform data. Using ETL workflow."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "transform_data",
			Description: "Execute transformation pipeline",
			Params:      make(map[string]string),
		})

	case "analytics":
		plan.Description = "Generate and execute analytics"
		plan.Reasoning = "User wants analytics. Using query generation and execution."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "execute_query",
			Description: "Execute analytics query",
			Params:      make(map[string]string),
		})

	case "query_generation":
		plan.Description = "Generate query"
		plan.Reasoning = "User wants query generation. Creating query from requirements."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "generate_query",
			Description: "Generate query from natural language",
			Params:      make(map[string]string),
		})

	case "document_search":
		plan.Description = "Search documents"
		plan.Reasoning = "User wants document search. Using search tools."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "search_documents",
			Description: "Search document collection",
			Params:      make(map[string]string),
		})

	case "api_call":
		plan.Description = "Execute API call"
		plan.Reasoning = "User wants API interaction. Using HTTP client."
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "call_api",
			Description: "Execute API request",
			Params:      make(map[string]string),
		})

	case "":
		return nil, fmt.Errorf("cannot plan execution for empty intent")

	default:
		// Freeform intents get a generic plan
		plan.Description = fmt.Sprintf("Execute %s operation", intent)
		plan.Reasoning = fmt.Sprintf("User intent classified as %q. Using default execution strategy.", intent)
		plan.Steps = append(plan.Steps, PlannedStep{
			ToolName:    "execute",
			Description: fmt.Sprintf("Execute %s operation", intent),
			Params:      make(map[string]string),
		})
	}

	return plan, nil
}

// matchesIntent checks if a classified intent matches a pattern's declared intents or category.
// Uses case-insensitive comparison. Falls back to category match for backward compatibility
// with patterns that don't declare explicit intents.
func matchesIntent(category string, declaredIntents []string, classifiedIntent string) bool {
	if classifiedIntent == "" {
		return false
	}
	intentLower := strings.ToLower(classifiedIntent)

	// Check declared intents first (new freeform field)
	for _, di := range declaredIntents {
		if strings.ToLower(di) == intentLower {
			return true
		}
	}

	// Backward compatibility: match against category
	if strings.ToLower(category) == intentLower {
		return true
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
