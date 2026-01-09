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
package observability

// Standard span names for consistency across Loom.
// Use these constants instead of hardcoding strings.
const (
	// Agent spans
	SpanAgentConversation   = "agent.conversation"
	SpanAgentToolSelection  = "agent.tool_selection"
	SpanAgentPatternMatch   = "agent.pattern_match"
	SpanAgentSelfCorrection = "agent.self_correction"

	// LLM spans
	SpanLLMCompletion = "llm.completion"
	SpanLLMTokenize   = "llm.tokenize" // #nosec G101 -- not a credential, just span name

	// Tool (shuttle) spans
	SpanToolExecute  = "tool.execute"
	SpanToolValidate = "tool.validate"

	// Backend (fabric) spans
	SpanBackendQuery   = "backend.query"
	SpanBackendConnect = "backend.connect"

	// Guardrail spans
	SpanGuardrailCheck = "guardrail.check"

	// Pattern spans
	SpanPatternLoad   = "pattern.load"
	SpanPatternRender = "pattern.render"

	// MCP spans
	SpanMCPClientInitialize   = "mcp.client.initialize"
	SpanMCPToolsList          = "mcp.tools.list"
	SpanMCPToolsCall          = "mcp.tools.call"
	SpanMCPResourcesList      = "mcp.resources.list"
	SpanMCPResourcesRead      = "mcp.resources.read"
	SpanMCPResourcesSubscribe = "mcp.resources.subscribe"
	SpanMCPPromptsList        = "mcp.prompts.list"
	SpanMCPPromptsGet         = "mcp.prompts.get"
	SpanMCPSamplingCreate     = "mcp.sampling.create"

	// Workflow orchestration spans
	SpanWorkflowExecution    = "workflow.execution"
	SpanDebateExecution      = "workflow.debate"
	SpanDebateRound          = "workflow.debate.round"
	SpanAgentExecution       = "workflow.agent.execution"
	SpanForkJoinExecution    = "workflow.fork_join"
	SpanPipelineExecution    = "workflow.pipeline"
	SpanParallelExecution    = "workflow.parallel"
	SpanConditionalExecution = "workflow.conditional"
	SpanMergeStrategy        = "workflow.merge"

	// Collaboration pattern spans
	SpanSwarmExecution           = "collaboration.swarm"
	SpanPairProgrammingExecution = "collaboration.pair_programming"
	SpanTeacherStudentExecution  = "collaboration.teacher_student"

	// Judge evaluation spans
	SpanJudgeEvaluation        = "judge.evaluation"
	SpanJudgeOrchestration     = "judge.orchestration"
	SpanJudgeAggregation       = "judge.aggregation"
	SpanHawkJudgeVerdictExport = "hawk.judge_verdict_export"

	// Teleprompter (DSPy-style) spans
	SpanTeleprompterCompile   = "teleprompter.compile"
	SpanTeleprompterMetric    = "teleprompter.metric"
	SpanTeleprompterBootstrap = "teleprompter.bootstrap"

	// Interrupt channel spans (4th communication channel)
	SpanInterruptSend      = "interrupt.send"
	SpanInterruptBroadcast = "interrupt.broadcast"
	SpanInterruptHandle    = "interrupt.handle"
	SpanInterruptEnqueue   = "interrupt.enqueue"
	SpanInterruptRetry     = "interrupt.retry"
)

// Standard metric names for consistency.
const (
	// Agent metrics
	MetricAgentConversations        = "agent.conversations.total"
	MetricAgentConversationDuration = "agent.conversation.duration"
	MetricAgentSelfCorrections      = "agent.self_corrections.total"

	// LLM metrics
	MetricLLMCalls        = "llm.calls.total"
	MetricLLMLatency      = "llm.latency"
	MetricLLMTokensInput  = "llm.tokens.input"  // #nosec G101 -- not a credential, just metric name
	MetricLLMTokensOutput = "llm.tokens.output" // #nosec G101 -- not a credential, just metric name
	MetricLLMCost         = "llm.cost"
	MetricLLMErrors       = "llm.errors.total"

	// Streaming metrics
	MetricLLMStreamingTTFT       = "llm.streaming.ttft_ms"
	MetricLLMStreamingThroughput = "llm.streaming.throughput"
	MetricLLMStreamingChunks     = "llm.streaming.chunks.total"

	// Tool metrics
	MetricToolExecutions = "tool.executions.total"
	MetricToolDuration   = "tool.duration"
	MetricToolErrors     = "tool.errors.total"

	// Guardrail metrics
	MetricGuardrailChecks = "guardrail.checks.total"
	MetricGuardrailBlocks = "guardrail.blocks.total"

	// MCP metrics
	MetricMCPCalls    = "mcp.calls.total"
	MetricMCPDuration = "mcp.duration"
	MetricMCPErrors   = "mcp.errors.total"

	// Interrupt channel metrics (4th communication channel)
	MetricInterruptSent      = "interrupt.sent.total"
	MetricInterruptDelivered = "interrupt.delivered.total"
	MetricInterruptDropped   = "interrupt.dropped.total"
	MetricInterruptQueued    = "interrupt.queued.total"
	MetricInterruptRetried   = "interrupt.retried.total"
	MetricInterruptLatency   = "interrupt.latency_ms"
	MetricInterruptQueueSize = "interrupt.queue.size"
)

// Standard attribute names for consistency.
// Use these constants for span and event attributes.
const (
	// Session/User context
	AttrSessionID = "session.id"
	AttrUserID    = "user.id"
	AttrTraceID   = "trace.id"
	AttrSpanID    = "span.id"

	// LLM attributes
	AttrLLMProvider    = "llm.provider"
	AttrLLMModel       = "llm.model"
	AttrLLMTemperature = "llm.temperature"
	AttrLLMMaxTokens   = "llm.max_tokens" // #nosec G101 -- not a credential, just attribute name

	// Streaming attributes
	AttrLLMStreaming  = "llm.streaming"
	AttrLLMTTFT       = "llm.ttft_ms"
	AttrLLMThroughput = "llm.streaming.throughput"

	// Tool attributes
	AttrToolName = "tool.name"
	AttrToolArgs = "tool.args"

	// Backend attributes
	AttrBackendType = "backend.type"
	AttrBackendHost = "backend.host"

	// Error attributes
	AttrErrorType    = "error.type"
	AttrErrorMessage = "error.message"
	AttrErrorStack   = "error.stack"

	// Prompt attributes
	AttrPromptKey     = "prompt.key"
	AttrPromptVariant = "prompt.variant"
	AttrPromptVersion = "prompt.version"

	// Pattern attributes
	AttrPatternName     = "pattern.name"
	AttrPatternCategory = "pattern.category"

	// MCP attributes
	AttrMCPServerName      = "mcp.server.name"
	AttrMCPOperation       = "mcp.operation"
	AttrMCPToolName        = "mcp.tool.name"
	AttrMCPResourceURI     = "mcp.resource.uri"
	AttrMCPPromptName      = "mcp.prompt.name"
	AttrMCPProtocolVersion = "mcp.protocol.version"

	// Judge attributes
	AttrJudgeName        = "judge.name"
	AttrJudgeCriticality = "judge.criticality"
	AttrJudgeVerdict     = "judge.verdict"
	AttrJudgeScore       = "judge.score"

	// Interrupt channel attributes (4th communication channel)
	AttrInterruptSignal    = "interrupt.signal"
	AttrInterruptPriority  = "interrupt.priority"
	AttrInterruptTarget    = "interrupt.target"
	AttrInterruptSender    = "interrupt.sender"
	AttrInterruptPath      = "interrupt.path"      // "fast" or "slow"
	AttrInterruptDelivered = "interrupt.delivered" // boolean
	AttrInterruptRetries   = "interrupt.retries"   // retry count
	AttrInterruptQueueID   = "interrupt.queue.id"  // persistent queue ID
)
