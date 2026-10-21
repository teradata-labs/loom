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
package server

import (
	"context"
	"encoding/json"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// weaveContextMetadataKeys lists the WeaveRequest.context keys forwarded onto
// the server span's OpenInference metadata attribute and into the turn context
// so Agent.chat can include them on agent.chat.
var weaveContextMetadataKeys = []string{ //nolint:gochecknoglobals
	"experiment_id",
	"replication_id",
	"condition",
	"game_name",
	"phase",
	"call_number",
	"trial",
	"run_id",
}

const maxWeaveContextValueRunes = 256

// extractWeaveContextMetadata copies whitelisted non-empty keys from
// req.Context, bounding each value to maxWeaveContextValueRunes runes.
func extractWeaveContextMetadata(req *loomv1.WeaveRequest) map[string]string {
	raw := req.GetContext()
	if len(raw) == 0 {
		return nil
	}
	var out map[string]string
	for _, k := range weaveContextMetadataKeys {
		v, ok := raw[k]
		if !ok || v == "" {
			continue
		}
		if runes := []rune(v); len(runes) > maxWeaveContextValueRunes {
			v = string(runes[:maxWeaveContextValueRunes])
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[k] = v
	}
	return out
}

// startWeaveSpan attaches whitelisted req.Context tags to ctx and, when tracer
// is non-nil, starts the server span ("server.Weave" or "server.StreamWeave")
// before session-ownership resolution so session_store.load_session nests under
// the turn trace. The span is the root of the request's trace, so it carries the
// user's message as message.preview (bounded by observability.TruncateContent,
// exported as OpenInference input.value): trace and session views that show a
// root span's input then show what the user sent.
func startWeaveSpan(
	ctx context.Context,
	tracer observability.Tracer,
	spanName string,
	req *loomv1.WeaveRequest,
) (context.Context, *observability.Span) {
	md := extractWeaveContextMetadata(req)
	if len(md) > 0 {
		ctx = observability.ContextWithSpanMetadata(ctx, md)
	}
	if tracer == nil {
		return ctx, nil
	}
	opts := []observability.SpanOption{
		observability.WithAttribute("query.length", len(req.GetQuery())),
	}
	if q := req.GetQuery(); q != "" {
		opts = append(opts, observability.WithAttribute("message.preview", observability.TruncateContent(q)))
	}
	if req.GetSessionId() != "" {
		opts = append(opts, observability.WithAttribute(observability.AttrSessionID, req.GetSessionId()))
	}
	if req.GetAgentId() != "" {
		opts = append(opts, observability.WithAttribute("agent.id", req.GetAgentId()))
	}
	if len(md) > 0 {
		if b, err := json.Marshal(md); err == nil {
			opts = append(opts, observability.WithAttribute("metadata", string(b)))
		}
	}
	return tracer.StartSpan(ctx, spanName, opts...)
}

// turnUsage returns the turn's token and cost totals: every successful LLM
// call of the conversation loop, including a final synthesis call. Loom's own
// memory calls (graph-memory extraction, recall query and rerank) are not
// part of it; their llm.completion spans carry their cost.
func turnUsage(resp *agent.Response) agent.Usage {
	if resp == nil {
		return agent.Usage{}
	}
	return resp.TurnUsage
}

// recordWeaveSpanUsage records the completed turn's response, as
// response.preview (bounded by observability.TruncateContent, exported as
// OpenInference output.value) and its length, and the full-turn usage totals
// onto the server span.
func recordWeaveSpanUsage(span *observability.Span, resp *agent.Response) {
	if span == nil || resp == nil {
		return
	}
	usage := turnUsage(resp)
	span.SetAttribute("response.length", len(resp.Content))
	if resp.Content != "" {
		span.SetAttribute("response.preview", observability.TruncateContent(resp.Content))
	}
	span.SetAttribute("usage.input_tokens", usage.InputTokens)
	span.SetAttribute("usage.output_tokens", usage.OutputTokens)
	span.SetAttribute("usage.total_tokens", usage.TotalTokens)
	span.SetAttribute("usage.cost_usd", usage.CostUSD)
	if usage.CacheReadInputTokens > 0 {
		span.SetAttribute("usage.cache_read_input_tokens", usage.CacheReadInputTokens)
	}
	if usage.CacheCreationInputTokens > 0 {
		span.SetAttribute("usage.cache_creation_input_tokens", usage.CacheCreationInputTokens)
	}
}

// resolveWeaveTraceID returns the server span's trace ID when present, falling
// back to resp.Metadata["trace_id"] when the server has no tracer configured.
func resolveWeaveTraceID(span *observability.Span, resp *agent.Response) string {
	if span != nil && span.TraceID != "" {
		return span.TraceID
	}
	if resp != nil && resp.Metadata != nil {
		if tid, ok := resp.Metadata["trace_id"].(string); ok {
			return tid
		}
	}
	return ""
}

// buildWeaveCostInfo constructs CostInfo from the turn's accumulated usage
// (resp.TurnUsage) rather than only the final LLM call.
func buildWeaveCostInfo(provider, model string, resp *agent.Response) *loomv1.CostInfo {
	usage := turnUsage(resp)
	return &loomv1.CostInfo{
		TotalCostUsd: usage.CostUSD,
		LlmCost: &loomv1.LLMCost{
			Provider:                 provider,
			Model:                    model,
			TotalTokens:              types.SafeInt32(usage.TotalTokens),
			InputTokens:              types.SafeInt32(usage.InputTokens),
			OutputTokens:             types.SafeInt32(usage.OutputTokens),
			CostUsd:                  usage.CostUSD,
			CacheReadInputTokens:     types.SafeInt32(usage.CacheReadInputTokens),
			CacheCreationInputTokens: types.SafeInt32(usage.CacheCreationInputTokens),
		},
	}
}

// buildExecutionMetadata populates ExecutionMetadata from the agent response
// and measured turn duration.
func buildExecutionMetadata(resp *agent.Response, contextState *loomv1.ContextState, duration time.Duration) *loomv1.ExecutionMetadata {
	if resp == nil {
		return nil
	}
	// llm_calls counts the calls TurnUsage sums; "turns" counts loop
	// iterations, which happen to match it today.
	llmCalls, _ := resp.Metadata["llm_calls"].(int)
	toolExecs, ok := resp.Metadata["tool_executions"].(int)
	if !ok {
		toolExecs = len(resp.ToolExecutions)
	}
	var patternName string
	if contextState != nil {
		patternName = contextState.ActivePattern
	}
	return &loomv1.ExecutionMetadata{
		PatternName:     patternName,
		LlmCalls:        types.SafeInt32(llmCalls),
		ToolExecutions:  types.SafeInt32(toolExecs),
		TotalDurationMs: duration.Milliseconds(),
	}
}
