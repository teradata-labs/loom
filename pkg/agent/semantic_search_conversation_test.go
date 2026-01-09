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
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	"github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestSemanticSearch_RealLLMConversation tests semantic search with a real multi-turn conversation
// between two agents, validated by judge agents to ensure topic adherence.
//
// To run this test with Anthropic:
//
//	export ANTHROPIC_API_KEY=your_key
//	go test -run TestSemanticSearch_RealLLMConversation -v -timeout 15m
//
// To run this test with AWS Bedrock:
//
//	export AWS_REGION=us-east-1
//	export AWS_ACCESS_KEY_ID=your_key
//	export AWS_SECRET_ACCESS_KEY=your_secret
//	# OR use AWS profile:
//	export AWS_PROFILE=your_profile
//	go test -run TestSemanticSearch_RealLLMConversation -v -timeout 15m
//
// This test will:
// 1. Create two agents (interviewer + expert) that converse about 6 technical topics
// 2. Use judge agents to validate each response is on-topic
// 3. Generate ~100-120 messages across all topics
// 4. Test semantic search can find relevant messages for each topic
// 5. Measure search accuracy, latency, and topic separation
func TestSemanticSearch_RealLLMConversation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping long-running real LLM conversation test")
	}

	// Create local random generator for tool failures
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Create LLM provider (Anthropic or Bedrock)
	llmProvider, providerName, err := createLLMProvider()
	if err != nil {
		t.Skipf("Skipping real LLM test - %v", err)
	}
	t.Logf("Using LLM provider: %s", providerName)

	// Setup database
	tmpDB := t.TempDir() + "/real_conversation.db"
	defer os.Remove(tmpDB)

	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(tmpDB, tracer)
	require.NoError(t, err)
	defer store.Close()

	// Create memory with store
	memory := NewMemory()
	memory.store = store
	memory.SetTracer(tracer)

	sessionID := "real-llm-conversation"
	session := memory.GetOrCreateSession(sessionID)

	// Set LLM provider for semantic search reranking
	memory.SetLLMProvider(llmProvider)

	ctx := context.Background()

	// Define conversation topics with ground truth keywords for validation
	topics := []ConversationTopic{
		{
			Name:         "Database Optimization",
			InitialQuery: "Explain database query optimization techniques and best practices for improving SQL performance.",
			GroundTruthKeywords: []string{
				"index", "query", "database", "SQL", "performance", "optimization",
				"execution plan", "analyze", "EXPLAIN", "cache", "B-tree",
			},
			RejectKeywords: []string{
				"authentication", "OAuth", "JWT", "microservices", "Docker",
				"testing", "unit test", "React", "frontend",
			},
		},
		{
			Name:         "API Authentication",
			InitialQuery: "Describe modern API authentication methods, focusing on OAuth 2.0, JWT, and API keys.",
			GroundTruthKeywords: []string{
				"authentication", "OAuth", "JWT", "token", "API key", "bearer",
				"authorization", "scope", "refresh token", "access token",
			},
			RejectKeywords: []string{
				"database", "SQL", "index", "Docker", "container",
				"cache", "Redis", "microservices",
			},
		},
		{
			Name:         "Caching Strategies",
			InitialQuery: "What are effective caching strategies for web applications, including cache invalidation patterns?",
			GroundTruthKeywords: []string{
				"cache", "Redis", "Memcached", "CDN", "invalidation", "TTL",
				"eviction", "LRU", "write-through", "write-back", "cache-aside",
			},
			RejectKeywords: []string{
				"authentication", "OAuth", "JWT", "Docker", "Kubernetes",
				"testing", "unit test",
			},
		},
		{
			Name:         "Error Handling Patterns",
			InitialQuery: "Explain error handling best practices in distributed systems, including retry strategies and circuit breakers.",
			GroundTruthKeywords: []string{
				"error", "exception", "retry", "circuit breaker", "fallback",
				"timeout", "backoff", "exponential", "idempotent", "fault tolerance",
			},
			RejectKeywords: []string{
				"database", "SQL", "cache", "Redis", "authentication",
				"OAuth", "JWT",
			},
		},
		{
			Name:         "Microservices Architecture",
			InitialQuery: "Describe microservices architecture principles, service discovery, and inter-service communication.",
			GroundTruthKeywords: []string{
				"microservices", "service", "discovery", "load balancer", "API gateway",
				"distributed", "container", "Docker", "orchestration", "gRPC", "REST",
			},
			RejectKeywords: []string{
				"database", "SQL", "cache", "Redis", "authentication",
				"testing", "unit test",
			},
		},
		{
			Name:         "Testing Methodologies",
			InitialQuery: "Explain different software testing approaches: unit testing, integration testing, and end-to-end testing.",
			GroundTruthKeywords: []string{
				"testing", "unit test", "integration test", "end-to-end", "mock",
				"assertion", "coverage", "TDD", "BDD", "test case", "fixture",
			},
			RejectKeywords: []string{
				"database", "SQL", "cache", "microservices", "Docker",
				"authentication", "OAuth",
			},
		},
	}

	// Get segmented memory for semantic search
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "SegmentedMemory not available")

	// Create judge for validating responses
	judge := NewTopicJudge(llmProvider)

	// Create tools for agents to use during conversation
	tools := createConversationTools(rng)

	// Statistics tracking
	stats := &ConversationStats{
		TopicScores:      make(map[string][]float64),
		OffTopicCount:    make(map[string]int),
		TotalMessages:    0,
		ToolCallCount:    0,
		ToolErrorCount:   0,
		ToolSuccessCount: 0,
		ToolErrorsByType: make(map[string]int),
		SearchMetrics:    make(map[string]*SearchMetrics),
		ConversationTime: time.Now(),
	}

	t.Logf("Starting real LLM agent-to-agent conversation test with %d topics", len(topics))
	t.Logf("Agent A and Agent B will take turns, using tools and searching each other's history")

	// Conduct conversation for each topic
	for idx, topic := range topics {
		t.Logf("\n=== Topic %d/%d: %s ===", idx+1, len(topics), topic.Name)

		conversationHistory := []types.Message{}
		offTopicStrikes := 0
		maxOffTopicStrikes := 3

		// Agent A starts the conversation with a question
		agentAMessage := types.Message{
			Role:    "user",
			Content: fmt.Sprintf("Agent A: %s", topic.InitialQuery),
		}
		conversationHistory = append(conversationHistory, agentAMessage)

		// Save to database
		dbMsg := Message{
			Role:      "user",
			Content:   agentAMessage.Content,
			Timestamp: time.Now(),
		}
		err = store.SaveMessage(ctx, sessionID, dbMsg)
		require.NoError(t, err)
		stats.TotalMessages++

		// Agents alternate for multiple turns
		turnsPerTopic := 8  // Each agent gets 4 turns
		currentAgent := "B" // Agent B responds first

		for turn := 0; turn < turnsPerTopic && offTopicStrikes < maxOffTopicStrikes; turn++ {
			t.Logf("  Turn %d/%d (Agent %s responding)", turn+1, turnsPerTopic, currentAgent)

			// Every 3rd turn, the agent searches previous conversation for relevant context
			if turn > 0 && turn%3 == 0 {
				searchQuery := fmt.Sprintf("tool calls and results about %s", topic.Name)
				t.Logf("    🔍 Agent %s searching for: %q", currentAgent, searchQuery)

				searchResults, searchErr := segMem.SearchMessages(ctx, searchQuery, 3)
				if searchErr == nil && len(searchResults) > 0 {
					t.Logf("    📚 Found %d relevant messages from previous turns", len(searchResults))

					// Add search results as context
					contextSummary := "\n[Context from previous discussion:"
					for i, msg := range searchResults {
						preview := msg.Content
						if len(preview) > 100 {
							preview = preview[:100] + "..."
						}
						contextSummary += fmt.Sprintf("\n- Message %d: %s", i+1, preview)
					}
					contextSummary += "]"

					// Prepend context to the last message
					lastMsg := &conversationHistory[len(conversationHistory)-1]
					lastMsg.Content = contextSummary + "\n\n" + lastMsg.Content
				}
			}

			// Get LLM response with tools available
			assistantResponse, err := llmProvider.Chat(ctx, conversationHistory, tools)
			require.NoError(t, err, "LLM call failed on turn %d", turn)

			// Handle tool use loop (agent may make multiple tool calls)
			maxToolIterations := 5
			toolIteration := 0
			for assistantResponse.StopReason == "tool_use" && toolIteration < maxToolIterations {
				t.Logf("    🔧 Tool use detected (iteration %d)", toolIteration+1)

				// Add assistant's tool use message to history
				assistantMessage := types.Message{
					Role:      "assistant",
					Content:   assistantResponse.Content,
					ToolCalls: assistantResponse.ToolCalls,
				}
				conversationHistory = append(conversationHistory, assistantMessage)

				// Save tool call message to database
				dbMsg := Message{
					Role:      "assistant",
					Content:   assistantResponse.Content,
					ToolCalls: assistantResponse.ToolCalls,
					Timestamp: time.Now(),
				}
				err = store.SaveMessage(ctx, sessionID, dbMsg)
				require.NoError(t, err)
				stats.TotalMessages++
				stats.ToolCallCount++

				// Execute tools and collect results
				for _, toolCall := range assistantResponse.ToolCalls {
					t.Logf("      → Calling tool: %s", toolCall.Name)
					result := executeTestTool(toolCall, tools)

					// Track tool success/failure statistics
					if result.Success {
						stats.ToolSuccessCount++
					} else {
						stats.ToolErrorCount++
						if result.Error != nil {
							stats.ToolErrorsByType[result.Error.Code]++
							t.Logf("      ⚠️  Tool error: %s - %s", result.Error.Code, result.Error.Message)
							if result.Error.Suggestion != "" {
								t.Logf("         💡 Suggestion: %s", result.Error.Suggestion)
							}
						}
					}

					// Format result content
					var content string
					if result.Success {
						content = fmt.Sprintf("%v", result.Data)
					} else if result.Error != nil {
						content = fmt.Sprintf("Error: %s - %s", result.Error.Code, result.Error.Message)
					} else {
						content = "Tool execution failed"
					}

					// Add tool result to conversation history
					toolResultMessage := types.Message{
						Role:       "tool",
						Content:    content,
						ToolUseID:  toolCall.ID,
						ToolResult: result,
					}
					conversationHistory = append(conversationHistory, toolResultMessage)

					// Save tool result to database
					dbToolResultMsg := Message{
						Role:       "tool",
						Content:    content,
						ToolUseID:  toolCall.ID,
						ToolResult: result,
						Timestamp:  time.Now(),
					}
					err = store.SaveMessage(ctx, sessionID, dbToolResultMsg)
					require.NoError(t, err)
					stats.TotalMessages++
				}

				// Continue conversation with tool results
				assistantResponse, err = llmProvider.Chat(ctx, conversationHistory, tools)
				require.NoError(t, err)

				toolIteration++
			}

			// Final assistant message (no tool use) - labeled with current agent
			responseWithLabel := fmt.Sprintf("Agent %s: %s", currentAgent, assistantResponse.Content)
			assistantMessage := types.Message{
				Role:    "assistant",
				Content: responseWithLabel,
			}
			conversationHistory = append(conversationHistory, assistantMessage)

			// Save to database
			dbMsg := Message{
				Role:      "assistant",
				Content:   responseWithLabel,
				Timestamp: time.Now(),
			}
			err = store.SaveMessage(ctx, sessionID, dbMsg)
			require.NoError(t, err)
			stats.TotalMessages++

			// Judge validates response is on-topic
			judgment, err := judge.EvaluateResponse(ctx, topic, assistantResponse.Content)
			require.NoError(t, err, "Judge evaluation failed")

			stats.TopicScores[topic.Name] = append(stats.TopicScores[topic.Name], judgment.RelevanceScore)

			t.Logf("    Agent %s judge score: %.2f/10", currentAgent, judgment.RelevanceScore)

			// Switch to the other agent for next turn
			var nextAgent string
			if currentAgent == "A" {
				nextAgent = "B"
			} else {
				nextAgent = "A"
			}

			// Prepare next agent's message
			if turn < turnsPerTopic-1 { // Don't add follow-up on last turn
				var nextMessage types.Message

				if judgment.RelevanceScore < 5.0 {
					offTopicStrikes++
					stats.OffTopicCount[topic.Name]++
					t.Logf("    ⚠️  Off-topic response detected (strike %d/%d)", offTopicStrikes, maxOffTopicStrikes)

					// Next agent steers conversation back on track
					correctionPrompt := fmt.Sprintf(
						"Agent %s: Let's refocus on %s specifically. %s",
						nextAgent,
						topic.Name,
						topic.InitialQuery,
					)
					nextMessage = types.Message{
						Role:    "user",
						Content: correctionPrompt,
					}
				} else {
					// Next agent asks a follow-up question
					followUpPrompt := fmt.Sprintf(
						"Agent %s: Can you elaborate more on %s? Please provide specific examples.",
						nextAgent,
						topic.Name,
					)
					nextMessage = types.Message{
						Role:    "user",
						Content: followUpPrompt,
					}
				}

				conversationHistory = append(conversationHistory, nextMessage)

				// Save next agent's question to database
				dbNextMsg := Message{
					Role:      "user",
					Content:   nextMessage.Content,
					Timestamp: time.Now(),
				}
				err = store.SaveMessage(ctx, sessionID, dbNextMsg)
				require.NoError(t, err)
				stats.TotalMessages++
			}

			// Switch current agent for next iteration
			currentAgent = nextAgent

			// Rate limiting to avoid API throttling
			time.Sleep(500 * time.Millisecond)
		}

		if offTopicStrikes >= maxOffTopicStrikes {
			t.Logf("  ⚠️  Topic ended early due to repeated off-topic responses")
		}

		// Calculate average relevance score for this topic
		avgScore := calculateAverage(stats.TopicScores[topic.Name])
		t.Logf("  Topic completed: Avg relevance score: %.2f/10, Off-topic: %d/%d messages",
			avgScore, stats.OffTopicCount[topic.Name], len(stats.TopicScores[topic.Name]))

		// Brief pause between topics
		time.Sleep(1 * time.Second)
	}

	t.Logf("\n=== Conversation Complete ===")
	t.Logf("Total messages: %d", stats.TotalMessages)
	t.Logf("Conversation duration: %s", time.Since(stats.ConversationTime))

	// Print per-topic statistics
	t.Logf("\n=== Per-Topic Conversation Quality ===")
	for _, topic := range topics {
		scores := stats.TopicScores[topic.Name]
		if len(scores) > 0 {
			avgScore := calculateAverage(scores)
			minScore := calculateMin(scores)
			maxScore := calculateMax(scores)
			t.Logf("  %s:", topic.Name)
			t.Logf("    Avg relevance: %.2f/10 (min: %.2f, max: %.2f)", avgScore, minScore, maxScore)
			t.Logf("    Off-topic: %d/%d (%.1f%%)", stats.OffTopicCount[topic.Name], len(scores),
				float64(stats.OffTopicCount[topic.Name])/float64(len(scores))*100)
		}
	}

	// Now test semantic search across all topics
	t.Logf("\n=== Testing Semantic Search ===")
	require.True(t, segMem.IsSwapEnabled(), "Swap should be enabled")

	// Test search for each topic
	for _, topic := range topics {
		t.Run(fmt.Sprintf("Search_%s", sanitizeTestName(topic.Name)), func(t *testing.T) {
			metrics := &SearchMetrics{
				TopicName: topic.Name,
			}
			stats.SearchMetrics[topic.Name] = metrics

			// Construct search query
			searchQuery := extractSearchQuery(topic.InitialQuery)
			t.Logf("  Query: %s", searchQuery)

			// Execute semantic search
			start := time.Now()
			results, err := segMem.SearchMessages(ctx, searchQuery, 10)
			metrics.Latency = time.Since(start)
			require.NoError(t, err)

			t.Logf("  Found %d results in %dms", len(results), metrics.Latency.Milliseconds())

			assert.Greater(t, len(results), 0, "Should find results for topic: %s", topic.Name)

			// Validate top 5 results are on-topic
			topN := min(5, len(results))
			for i := 0; i < topN; i++ {
				msg := results[i]

				// Check for ground truth keywords
				containsRelevant := containsAnyKeyword(msg.Content, topic.GroundTruthKeywords)
				containsIrrelevant := containsAnyKeyword(msg.Content, topic.RejectKeywords)

				if containsRelevant {
					metrics.RelevantResults++
					t.Logf("    ✅ Result %d: Relevant (contains topic keywords)", i+1)
				}

				if containsIrrelevant {
					metrics.OffTopicResults++
					t.Logf("    ❌ Result %d: Off-topic (contains wrong topic keywords)", i+1)
					t.Logf("       Preview: %s", truncateString(msg.Content, 100))
				}
			}

			// Calculate precision
			metrics.Precision = float64(metrics.RelevantResults) / float64(topN)
			t.Logf("  Precision (top-%d): %.2f (relevant: %d, off-topic: %d)",
				topN, metrics.Precision, metrics.RelevantResults, metrics.OffTopicResults)

			// Validate precision threshold
			assert.GreaterOrEqual(t, metrics.Precision, 0.6,
				"Search precision should be >= 60%% for topic: %s", topic.Name)

			// Validate latency
			assert.Less(t, metrics.Latency.Milliseconds(), int64(200),
				"Search latency should be < 200ms, got %dms", metrics.Latency.Milliseconds())

			// Use judge to validate top result
			if len(results) > 0 {
				topResult := results[0]
				judgment, err := judge.EvaluateResponse(ctx, topic, topResult.Content)
				require.NoError(t, err)

				metrics.JudgeScore = judgment.RelevanceScore
				t.Logf("  Judge score for top result: %.2f/10 - %s",
					judgment.RelevanceScore, judgment.Reasoning)

				assert.GreaterOrEqual(t, judgment.RelevanceScore, 5.0,
					"Top search result should be relevant (score >= 5.0)")
			}
		})
	}

	// Test tool call retrieval specifically
	t.Run("Search_ToolCalls", func(t *testing.T) {
		t.Logf("\n=== Testing Tool Call Retrieval ===")
		t.Logf("Verifying that agents can find each other's tool calls via semantic search")

		// Search for messages containing tool calls
		toolSearchQueries := []string{
			"tool calls about calculations",
			"database query results",
			"performance analysis",
			"code examples",
			"documentation search results",
		}

		toolCallsFound := 0
		for _, query := range toolSearchQueries {
			t.Logf("  Searching: %q", query)
			results, err := segMem.SearchMessages(ctx, query, 5)
			require.NoError(t, err)

			// Check if any results contain tool-related content
			for _, msg := range results {
				// Check if message has tool calls or tool results
				if strings.Contains(msg.Content, "tool") ||
					strings.Contains(msg.Content, "calculation") ||
					strings.Contains(msg.Content, "query") ||
					strings.Contains(msg.Content, "performance") {
					toolCallsFound++
					t.Logf("    ✅ Found tool-related message: %s", truncateString(msg.Content, 80))
					break
				}
			}
		}

		t.Logf("  Found %d tool-related messages across %d queries", toolCallsFound, len(toolSearchQueries))
		assert.Greater(t, toolCallsFound, 0, "Should find at least some tool-related messages")

		// Search specifically for messages from each agent
		for _, agentName := range []string{"A", "B"} {
			query := fmt.Sprintf("Agent %s tool calls", agentName)
			results, err := segMem.SearchMessages(ctx, query, 5)
			require.NoError(t, err)

			agentMsgsFound := 0
			for _, msg := range results {
				if strings.Contains(msg.Content, fmt.Sprintf("Agent %s:", agentName)) {
					agentMsgsFound++
				}
			}
			t.Logf("  Agent %s messages found: %d", agentName, agentMsgsFound)
		}
	})

	// Print final search quality summary
	t.Logf("\n=== Semantic Search Quality Summary ===")
	var totalPrecision float64
	var totalLatency time.Duration
	var totalJudgeScore float64
	validMetrics := 0

	for _, topic := range topics {
		metrics := stats.SearchMetrics[topic.Name]
		if metrics != nil {
			t.Logf("  %s:", topic.Name)
			t.Logf("    Precision: %.2f%%", metrics.Precision*100)
			t.Logf("    Latency: %dms", metrics.Latency.Milliseconds())
			t.Logf("    Judge score: %.2f/10", metrics.JudgeScore)

			totalPrecision += metrics.Precision
			totalLatency += metrics.Latency
			totalJudgeScore += metrics.JudgeScore
			validMetrics++
		}
	}

	if validMetrics > 0 {
		avgPrecision := totalPrecision / float64(validMetrics)
		avgLatency := totalLatency / time.Duration(validMetrics)
		avgJudgeScore := totalJudgeScore / float64(validMetrics)

		t.Logf("\n=== Overall Averages ===")
		t.Logf("  Total tool calls: %d", stats.ToolCallCount)
		t.Logf("  Tool success: %d (%.1f%%)", stats.ToolSuccessCount,
			float64(stats.ToolSuccessCount)/float64(stats.ToolCallCount)*100)
		t.Logf("  Tool errors: %d (%.1f%%)", stats.ToolErrorCount,
			float64(stats.ToolErrorCount)/float64(stats.ToolCallCount)*100)

		if len(stats.ToolErrorsByType) > 0 {
			t.Logf("  Error breakdown:")
			for errorCode, count := range stats.ToolErrorsByType {
				t.Logf("    - %s: %d", errorCode, count)
			}
		}

		t.Logf("  Tool calls per message: %.2f", float64(stats.ToolCallCount)/float64(stats.TotalMessages))
		t.Logf("  Precision: %.2f%%", avgPrecision*100)
		t.Logf("  Latency: %dms", avgLatency.Milliseconds())
		t.Logf("  Judge score: %.2f/10", avgJudgeScore)

		// Overall quality assertions
		assert.GreaterOrEqual(t, avgPrecision, 0.65,
			"Average search precision should be >= 65%%")
		assert.Less(t, avgLatency.Milliseconds(), int64(150),
			"Average search latency should be < 150ms")
	}

	// Test cross-topic separation (database query should NOT return auth results)
	t.Run("CrossTopicSeparation", func(t *testing.T) {
		t.Logf("  Testing topic isolation...")

		// Search for database topic
		dbResults, err := segMem.SearchMessages(ctx, "SQL query optimization", 10)
		require.NoError(t, err)

		// Top results should not contain authentication keywords
		authContamination := 0
		for i := 0; i < min(5, len(dbResults)); i++ {
			if containsAnyKeyword(dbResults[i].Content, []string{"OAuth", "JWT", "authentication", "bearer token"}) {
				authContamination++
				t.Logf("    ⚠️  Result %d contains authentication terms (cross-topic contamination)", i+1)
			}
		}

		contaminationRate := float64(authContamination) / float64(min(5, len(dbResults)))
		t.Logf("  Cross-topic contamination rate: %.1f%%", contaminationRate*100)

		assert.Less(t, contaminationRate, 0.3,
			"Cross-topic contamination should be < 30%%")
	})

	// Save conversation statistics
	statsJSON, _ := json.MarshalIndent(stats, "", "  ")
	statsFile := tmpDB + ".stats.json"
	_ = os.WriteFile(statsFile, statsJSON, 0644)
	t.Logf("\n=== Conversation statistics saved to: %s ===", statsFile)
}

// ConversationTopic defines a topic for agent conversation with validation criteria.
type ConversationTopic struct {
	Name                string
	InitialQuery        string
	GroundTruthKeywords []string // Keywords that should appear in on-topic messages
	RejectKeywords      []string // Keywords that indicate off-topic drift
}

// TopicJudgment represents a judge's evaluation of a response.
type TopicJudgment struct {
	IsOnTopic      bool
	RelevanceScore float64 // 0-10 scale
	Reasoning      string
}

// TopicJudge evaluates whether responses stay on-topic.
type TopicJudge struct {
	llm LLMProvider
}

// NewTopicJudge creates a new topic judge with the given LLM.
func NewTopicJudge(llm LLMProvider) *TopicJudge {
	return &TopicJudge{llm: llm}
}

// EvaluateResponse judges whether a response is on-topic.
func (j *TopicJudge) EvaluateResponse(ctx context.Context, topic ConversationTopic, response string) (*TopicJudgment, error) {
	prompt := fmt.Sprintf(`You are a judge evaluating whether a response stays on the topic: "%s"

Original question: %s

Response to evaluate:
%s

Evaluate this response on a scale of 0-10:
- 10: Completely on-topic, directly addresses the subject
- 7-9: Mostly on-topic with minor tangents
- 4-6: Partially relevant but significant drift
- 0-3: Off-topic or unrelated

Respond with JSON:
{
  "score": 8.5,
  "reasoning": "Brief explanation of your score"
}`, topic.Name, topic.InitialQuery, response)

	messages := []types.Message{
		{Role: "user", Content: prompt},
	}

	llmResponse, err := j.llm.Chat(ctx, messages, nil)
	if err != nil {
		return nil, fmt.Errorf("judge LLM call failed: %w", err)
	}

	// Parse JSON response
	var result struct {
		Score     float64 `json:"score"`
		Reasoning string  `json:"reasoning"`
	}

	// Try to extract JSON from response (handle markdown code blocks)
	content := llmResponse.Content
	if start := strings.Index(content, "{"); start != -1 {
		if end := strings.LastIndex(content, "}"); end != -1 {
			content = content[start : end+1]
		}
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		// Fallback: if JSON parsing fails, assume moderate relevance
		return &TopicJudgment{
			IsOnTopic:      true,
			RelevanceScore: 6.0,
			Reasoning:      "Judge response parsing failed, assuming moderate relevance",
		}, nil
	}

	return &TopicJudgment{
		IsOnTopic:      result.Score >= 5.0,
		RelevanceScore: result.Score,
		Reasoning:      result.Reasoning,
	}, nil
}

// ConversationStats tracks statistics about the conversation and search quality.
type ConversationStats struct {
	TopicScores      map[string][]float64      // Per-topic relevance scores from judge
	OffTopicCount    map[string]int            // Count of off-topic responses per topic
	TotalMessages    int                       // Total messages in conversation
	ToolCallCount    int                       // Total tool calls made during conversation
	ToolErrorCount   int                       // Total tool errors encountered
	ToolSuccessCount int                       // Total successful tool calls
	ToolErrorsByType map[string]int            // Count of errors by error code
	SearchMetrics    map[string]*SearchMetrics // Search quality per topic
	ConversationTime time.Time                 // When conversation started
}

// SearchMetrics tracks semantic search quality for a topic.
type SearchMetrics struct {
	TopicName       string
	Latency         time.Duration
	RelevantResults int     // Number of relevant results in top-N
	OffTopicResults int     // Number of off-topic results in top-N
	Precision       float64 // Precision = relevant / total
	JudgeScore      float64 // Judge's evaluation of top result
}

// Helper functions

func containsAnyKeyword(content string, keywords []string) bool {
	lower := strings.ToLower(content)
	for _, kw := range keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

func extractSearchQuery(query string) string {
	// Remove question words and extract core topic
	query = strings.TrimPrefix(query, "Explain ")
	query = strings.TrimPrefix(query, "Describe ")
	query = strings.TrimPrefix(query, "What are ")
	query = strings.TrimPrefix(query, "How do ")

	// Take first sentence or first 50 chars
	if idx := strings.Index(query, "."); idx > 0 && idx < 100 {
		query = query[:idx]
	} else if len(query) > 50 {
		query = query[:50]
	}

	return strings.TrimSpace(query)
}

func sanitizeTestName(name string) string {
	// Replace spaces and special chars for test name
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	return name
}

func calculateAverage(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func calculateMin(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	minVal := values[0]
	for _, v := range values {
		if v < minVal {
			minVal = v
		}
	}
	return minVal
}

func calculateMax(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	maxVal := values[0]
	for _, v := range values {
		if v > maxVal {
			maxVal = v
		}
	}
	return maxVal
}

// createLLMProvider creates an LLM provider based on available environment variables.
// Checks for Anthropic API key first, then AWS Bedrock credentials.
// Returns the provider, provider name, and error if neither is available.
func createLLMProvider() (LLMProvider, string, error) {
	// Try Anthropic first
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		cfg := anthropic.Config{
			APIKey:      apiKey,
			Model:       "claude-3-5-sonnet-20241022",
			MaxTokens:   4096,
			Temperature: 1.0,
		}
		llm := anthropic.NewClient(cfg)
		return llm, "Anthropic Claude 3.5 Sonnet", nil
	}

	// Try AWS Bedrock
	awsRegion := os.Getenv("AWS_REGION")
	if awsRegion == "" {
		awsRegion = os.Getenv("AWS_DEFAULT_REGION")
	}

	// Check if AWS credentials are available (either explicit or via profile)
	hasExplicitCreds := os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != ""
	hasProfile := os.Getenv("AWS_PROFILE") != ""

	if (hasExplicitCreds || hasProfile) && awsRegion != "" {
		cfg := bedrock.Config{
			Region:          awsRegion,
			AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
			Profile:         os.Getenv("AWS_PROFILE"),
			ModelID:         "anthropic.claude-3-5-sonnet-20241022-v2:0",
			MaxTokens:       4096,
			Temperature:     1.0,
		}

		llm, err := bedrock.NewClient(cfg)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create Bedrock client: %w", err)
		}
		return llm, "AWS Bedrock (Claude 3.5 Sonnet)", nil
	}

	return nil, "", fmt.Errorf("no LLM provider configured - set ANTHROPIC_API_KEY or AWS credentials")
}

// shouldToolFail returns true 1 out of 6 times to simulate random tool failures.
func shouldToolFail(rng *rand.Rand) bool {
	return rng.Intn(6) == 0
}

// createConversationTools creates mock tools for agents to use during conversation.
// These tools simulate realistic operations that would be used during technical discussions.
// Tools randomly fail 1 out of 6 times to test error handling and recovery.
func createConversationTools(rng *rand.Rand) []shuttle.Tool {
	return []shuttle.Tool{
		&shuttle.MockTool{
			MockName:        "calculate",
			MockDescription: "Perform mathematical calculations. Useful for performance analysis, cost estimation, or capacity planning.",
			MockSchema: shuttle.NewObjectSchema("",
				map[string]*shuttle.JSONSchema{
					"expression": shuttle.NewStringSchema("Mathematical expression to evaluate (e.g., '1000 * 0.002' for cost calculation)"),
				},
				[]string{"expression"},
			),
			MockExecute: func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
				// Random failure 1 out of 6 times
				if shouldToolFail(rng) {
					return &shuttle.Result{
						Success: false,
						Error: &shuttle.Error{
							Code:       "CALCULATION_ERROR",
							Message:    "Failed to perform calculation due to internal error",
							Retryable:  true,
							Suggestion: "Please try the calculation again with a simpler expression",
						},
					}, nil
				}

				expression, _ := params["expression"].(string)
				return &shuttle.Result{
					Success: true,
					Data:    fmt.Sprintf("Calculation result for '%s': 2.0 (mock result)", expression),
				}, nil
			},
		},
		&shuttle.MockTool{
			MockName:        "query_database",
			MockDescription: "Execute a SQL query to retrieve or analyze data. Returns mock query results.",
			MockSchema: shuttle.NewObjectSchema("",
				map[string]*shuttle.JSONSchema{
					"query": shuttle.NewStringSchema("SQL query to execute"),
				},
				[]string{"query"},
			),
			MockExecute: func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
				// Random failure 1 out of 6 times
				if shouldToolFail(rng) {
					return &shuttle.Result{
						Success: false,
						Error: &shuttle.Error{
							Code:       "DATABASE_CONNECTION_ERROR",
							Message:    "Failed to connect to database: connection timeout after 5 seconds",
							Retryable:  true,
							Suggestion: "Check database connectivity and retry. Consider using connection pooling.",
						},
					}, nil
				}

				query, _ := params["query"].(string)
				return &shuttle.Result{
					Success: true,
					Data: fmt.Sprintf(`Query executed successfully. Sample results:
- Row 1: id=1, name="example", value=42
- Row 2: id=2, name="test", value=100
Query: %s
Rows returned: 2 (mock data)`, query),
				}, nil
			},
		},
		&shuttle.MockTool{
			MockName:        "search_documentation",
			MockDescription: "Search technical documentation for information on a specific topic.",
			MockSchema: shuttle.NewObjectSchema("",
				map[string]*shuttle.JSONSchema{
					"query":    shuttle.NewStringSchema("Search query for documentation"),
					"category": shuttle.NewStringSchema("Documentation category (api, guide, reference)"),
				},
				[]string{"query"},
			),
			MockExecute: func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
				// Random failure 1 out of 6 times
				if shouldToolFail(rng) {
					return &shuttle.Result{
						Success: false,
						Error: &shuttle.Error{
							Code:       "SEARCH_SERVICE_UNAVAILABLE",
							Message:    "Documentation search service is temporarily unavailable (503 Service Unavailable)",
							Retryable:  true,
							Suggestion: "The search service is experiencing high load. Please retry in a few seconds.",
						},
					}, nil
				}

				query, _ := params["query"].(string)
				category, _ := params["category"].(string)
				return &shuttle.Result{
					Success: true,
					Data: fmt.Sprintf(`Documentation search results for "%s" in %s:
- Result 1: Best practices guide (relevance: 95%%)
- Result 2: API reference documentation (relevance: 87%%)
- Result 3: Tutorial examples (relevance: 72%%)`, query, category),
				}, nil
			},
		},
		&shuttle.MockTool{
			MockName:        "get_code_example",
			MockDescription: "Retrieve a code example for a specific programming pattern or technique.",
			MockSchema: shuttle.NewObjectSchema("",
				map[string]*shuttle.JSONSchema{
					"pattern":  shuttle.NewStringSchema("Programming pattern name (e.g., 'retry with exponential backoff')"),
					"language": shuttle.NewStringSchema("Programming language (go, python, javascript, etc.)"),
				},
				[]string{"pattern"},
			),
			MockExecute: func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
				// Random failure 1 out of 6 times
				if shouldToolFail(rng) {
					return &shuttle.Result{
						Success: false,
						Error: &shuttle.Error{
							Code:       "EXAMPLE_NOT_FOUND",
							Message:    "No code example found matching the requested pattern",
							Retryable:  false,
							Suggestion: "Try searching with a more common pattern name or check the pattern library documentation.",
						},
					}, nil
				}

				pattern, _ := params["pattern"].(string)
				language, _ := params["language"].(string)
				return &shuttle.Result{
					Success: true,
					Data: fmt.Sprintf(`Code example for "%s" in %s:

// Mock example code
func example() {
    // Implementation of %s pattern
    // This is a simulated code example
}`, pattern, language, pattern),
				}, nil
			},
		},
		&shuttle.MockTool{
			MockName:        "analyze_performance",
			MockDescription: "Analyze performance metrics for a given scenario.",
			MockSchema: shuttle.NewObjectSchema("",
				map[string]*shuttle.JSONSchema{
					"scenario": shuttle.NewStringSchema("Performance scenario to analyze"),
					"metrics": shuttle.NewArraySchema("Metrics to analyze (latency, throughput, cpu, memory)",
						shuttle.NewStringSchema("")),
				},
				[]string{"scenario"},
			),
			MockExecute: func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
				// Random failure 1 out of 6 times
				if shouldToolFail(rng) {
					return &shuttle.Result{
						Success: false,
						Error: &shuttle.Error{
							Code:       "METRICS_COLLECTION_FAILED",
							Message:    "Failed to collect performance metrics: monitoring agent not responding",
							Retryable:  true,
							Suggestion: "Verify the monitoring agent is running and restart it if necessary. Then retry the analysis.",
						},
					}, nil
				}

				scenario, _ := params["scenario"].(string)
				return &shuttle.Result{
					Success: true,
					Data: fmt.Sprintf(`Performance analysis for "%s":
- Latency: 45ms (p50), 120ms (p99)
- Throughput: 1000 req/s
- CPU Usage: 35%% average
- Memory: 512MB allocated, 380MB in use
Recommendations: Consider caching for improved latency`, scenario),
				}, nil
			},
		},
	}
}

// executeTestTool executes a tool and returns the result.
func executeTestTool(toolCall types.ToolCall, availableTools []shuttle.Tool) *shuttle.Result {
	// Find the tool
	var tool shuttle.Tool
	for _, t := range availableTools {
		if t.Name() == toolCall.Name {
			tool = t
			break
		}
	}

	if tool == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "TOOL_NOT_FOUND",
				Message: fmt.Sprintf("Tool '%s' not found", toolCall.Name),
			},
		}
	}

	// toolCall.Input is already a map[string]interface{}, no need to unmarshal
	result, err := tool.Execute(context.Background(), toolCall.Input)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "EXECUTION_ERROR",
				Message: fmt.Sprintf("Tool execution failed: %v", err),
			},
		}
	}

	return result
}
