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

import (
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// loomToGenAI maps Loom attribute keys to OTel semantic convention keys.
// Attributes not listed here (and not in openInferencePassthrough) are
// forwarded with a "loom." prefix so no data is dropped.
//
// Standards followed:
//   - OTel GenAI spans: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/
//   - OTel Exceptions: https://opentelemetry.io/docs/specs/semconv/exceptions/
//   - OTel RPC (for MCP): https://opentelemetry.io/docs/specs/semconv/rpc/
//   - Opik-specific: gen_ai.prompt / gen_ai.completion drive the Input/Output columns
//
// Agent-level conversation totals (conversation.tokens.*, conversation.cost.usd)
// are deliberately not mapped to gen_ai.usage.*: backends that sum usage over
// a trace would count them a second time on top of the llm.completion spans
// they aggregate. They export as loom.conversation.*.
var loomToGenAI = map[string]string{ //nolint:gochecknoglobals
	// LLM identity (REQUIRED by OTel GenAI spec)
	"llm.provider":  "gen_ai.system",
	"llm.model":     "gen_ai.request.model",
	"llm.operation": "gen_ai.operation.name",

	// LLM request parameters (RECOMMENDED)
	"llm.temperature": "gen_ai.request.temperature",
	"llm.max_tokens":  "gen_ai.request.max_tokens",

	// Token usage (llm span — set by instrumented_provider.go)
	"llm.tokens.input":       "gen_ai.usage.input_tokens",
	"llm.tokens.output":      "gen_ai.usage.output_tokens",
	"llm.tokens.total":       "gen_ai.usage.total_tokens",
	"llm.tokens.cache_read":  "gen_ai.usage.cache_read.input_tokens",
	"llm.tokens.cache_write": "gen_ai.usage.cache_creation.input_tokens",

	// Response metadata (llm span)
	"llm.stop_reason": "gen_ai.response.finish_reasons",
	"llm.cost.usd":    "gen_ai.usage.cost",

	// Input/output text for Opik UI columns — set on both llm and agent spans
	"message.preview":  "gen_ai.prompt",
	"response.preview": "gen_ai.completion",

	// Tool execution (OTel GenAI tool-use semconv)
	"tool.name":     "gen_ai.tool.name",
	"mcp.tool.name": "gen_ai.tool.name",
	"mcp.tool.args": "gen_ai.tool.call.arguments",

	// MCP server identity (OTel RPC semconv)
	"mcp.server.name":    "server.address",
	"mcp.server.version": "server.version",

	// Agent identity (OTel GenAI agent semconv, draft)
	"agent_id": "gen_ai.agent.id",
	"agent.id": "gen_ai.agent.id",

	// Error / exception semconv
	"error.message": "exception.message",
	"error.type":    "exception.type",
	"error.stack":   "exception.stacktrace",

	// Session / user — kept as-is so backends index them without a loom. prefix
	"session.id": "session.id",
	"user.id":    "user.id", // #nosec G101 -- attribute key name, not a credential
	"trace.id":   "trace.id",
}

// loomAlsoExport lists Loom keys exported under a second OTel key in
// addition to their primary one (the loomToGenAI mapping or passthrough).
var loomAlsoExport = map[string]string{ //nolint:gochecknoglobals
	// gen_ai.system is the deprecated name of gen_ai.provider.name; Phoenix
	// reads gen_ai.provider.name first. Both carry the normalized value.
	"llm.provider": "gen_ai.provider.name",
	// OpenInference agent.name (passthrough) plus the OTel GenAI key.
	"agent.name": "gen_ai.agent.name",
}

// openInferencePassthrough lists keys (exact, or prefixes ending in ".")
// that are already OpenInference or OTel names and are exported unchanged
// instead of being "loom."-prefixed.
var openInferencePassthrough = []string{ //nolint:gochecknoglobals
	"openinference.",
	"input.",
	"output.",
	"llm.token_count.",
	"llm.invocation_parameters",
	"llm.input_messages.",
	"llm.output_messages.",
	"tool.parameters",
	"tool.description",
	"tool.id",
	"retrieval.documents.",
	"graph.node.id",
	"agent.name",
	"gen_ai.",
}

// genAISystemNorm normalizes Loom provider names to OTel GenAI provider names
// (gen_ai.provider.name / gen_ai.system), the vocabulary Phoenix maps to
// OpenInference llm.provider values.
// Spec: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/#generative-ai-request-attributes
var genAISystemNorm = map[string]string{ //nolint:gochecknoglobals
	"bedrock":      "aws.bedrock",
	"azure-openai": "azure.ai.openai",
	"gemini":       "gcp.gemini",
	"mistral":      "mistral_ai",
	"huggingface":  "hugging_face",
	// anthropic, openai, ollama already match the spec
}

// OpenInference span kinds (openinference.span.kind).
const (
	oiKindLLM       = "LLM"
	oiKindAgent     = "AGENT"
	oiKindTool      = "TOOL"
	oiKindRetriever = "RETRIEVER"
	oiKindReranker  = "RERANKER"
	oiKindChain     = "CHAIN"
)

// openInferenceSpanKinds assigns an OpenInference span kind by Loom span
// name. Every other span is CHAIN. An explicit openinference.span.kind
// attribute on the span takes precedence.
var openInferenceSpanKinds = map[string]string{ //nolint:gochecknoglobals
	SpanLLMCompletion: oiKindLLM,

	"agent.chat":                     oiKindAgent,
	"agent.chat_with_progress":       oiKindAgent,
	"agent.chat_with_content_blocks": oiKindAgent,
	"agent.conversation.resume":      oiKindAgent,

	"agent.tool_execution": oiKindTool,
	"tool.execute":         oiKindTool,
	"mcp.tools.call":       oiKindTool,

	"graph_memory.recall":          oiKindRetriever,
	"memory.search_messages":       oiKindRetriever,
	"memory.rerank_search_results": oiKindReranker,
}

const (
	loomAttrPrefix          = "loom."
	attrOpenInferenceKind   = "openinference.span.kind"
	attrOIInputValue        = "input.value"
	attrOIOutputValue       = "output.value"
	attrOITokenPrompt       = "llm.token_count.prompt"
	attrOITokenCompletion   = "llm.token_count.completion"
	attrOITokenTotal        = "llm.token_count.total"
	attrOITokenCacheRead    = "llm.token_count.prompt_details.cache_read"
	attrOITokenCacheWrite   = "llm.token_count.prompt_details.cache_write"
	attrOICostTotal         = "llm.cost.total"
	attrLoomTokensInput     = "llm.tokens.input"
	attrLoomTokensOutput    = "llm.tokens.output"
	attrLoomCostUSD         = "llm.cost.usd"
	attrLoomTokensCacheRead = "llm.tokens.cache_read"
	attrLoomTokensCacheWrt  = "llm.tokens.cache_write"
	attrLoomMessagePreview  = "message.preview"
	attrLoomResponsePreview = "response.preview"
)

// exportAttributes returns the OTel attributes exported for a Loom span:
// each Loom attribute under its OTel name (or "loom."-prefixed), followed by
// the OpenInference attributes derived from the span name and attributes.
func exportAttributes(spanName string, attrs map[string]interface{}) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(attrs)+8)
	for k, v := range attrs {
		otelKey := k
		if mapped, ok := loomToGenAI[k]; ok {
			otelKey = mapped
		} else if !isPassthrough(k) && !strings.HasPrefix(k, loomAttrPrefix) {
			otelKey = loomAttrPrefix + k
		}
		if k == AttrLLMProvider {
			v = normalizeGenAISystem(v)
		}
		out = append(out, toOTelAttr(otelKey, v))
		if extra, ok := loomAlsoExport[k]; ok {
			out = append(out, toOTelAttr(extra, v))
		}
	}
	return append(out, deriveOpenInferenceAttrs(spanName, attrs)...)
}

// deriveOpenInferenceAttrs computes the OpenInference attributes Phoenix
// needs and cannot synthesize from the gen_ai.* keys: the span kind, token
// counts that include cached prompt tokens, input/output values and the
// LLM cost. Keys the span already carries are left alone.
//
// llm.provider, llm.system and llm.model_name are not set: Phoenix derives
// them from gen_ai.provider.name / gen_ai.system and gen_ai.request.model,
// and an explicit value would override its OpenInference mapping.
func deriveOpenInferenceAttrs(spanName string, attrs map[string]interface{}) []attribute.KeyValue {
	var out []attribute.KeyValue
	has := func(k string) bool { _, ok := attrs[k]; return ok }

	kind := oiKindChain
	if explicit, ok := attrs[attrOpenInferenceKind].(string); ok && explicit != "" {
		kind = explicit
	} else {
		if mapped, ok := openInferenceSpanKinds[spanName]; ok {
			kind = mapped
		}
		out = append(out, attribute.String(attrOpenInferenceKind, kind))
	}

	if s, ok := attrs[attrLoomMessagePreview].(string); ok && !has(attrOIInputValue) {
		out = append(out, attribute.String(attrOIInputValue, s))
	}
	if s, ok := attrs[attrLoomResponsePreview].(string); ok && !has(attrOIOutputValue) {
		out = append(out, attribute.String(attrOIOutputValue, s))
	}

	if kind != oiKindLLM {
		return out
	}

	// Loom's llm.tokens.input is the uncached remainder for every provider,
	// while OpenInference's prompt count includes cached reads and writes.
	// Phoenix would otherwise copy gen_ai.usage.input_tokens as the prompt.
	if input, ok := attrInt64(attrs, attrLoomTokensInput); ok && !has(attrOITokenPrompt) {
		cacheRead, _ := attrInt64(attrs, attrLoomTokensCacheRead)
		cacheWrite, _ := attrInt64(attrs, attrLoomTokensCacheWrt)
		output, _ := attrInt64(attrs, attrLoomTokensOutput)
		prompt := input + cacheRead + cacheWrite
		out = append(out,
			attribute.Int64(attrOITokenPrompt, prompt),
			attribute.Int64(attrOITokenCompletion, output),
			attribute.Int64(attrOITokenTotal, prompt+output),
		)
		if has(attrLoomTokensCacheRead) {
			out = append(out, attribute.Int64(attrOITokenCacheRead, cacheRead))
		}
		if has(attrLoomTokensCacheWrt) {
			out = append(out, attribute.Int64(attrOITokenCacheWrite, cacheWrite))
		}
	}
	if cost, ok := attrFloat64(attrs, attrLoomCostUSD); ok && !has(attrOICostTotal) {
		out = append(out, attribute.Float64(attrOICostTotal, cost))
	}
	return out
}

func isPassthrough(key string) bool {
	for _, p := range openInferencePassthrough {
		if strings.HasSuffix(p, ".") {
			if strings.HasPrefix(key, p) {
				return true
			}
		} else if key == p {
			return true
		}
	}
	return false
}

func normalizeGenAISystem(v interface{}) interface{} {
	if s, ok := v.(string); ok {
		if norm, ok := genAISystemNorm[s]; ok {
			return norm
		}
	}
	return v
}

func attrInt64(attrs map[string]interface{}, key string) (int64, bool) {
	switch v := attrs[key].(type) {
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}

func attrFloat64(attrs map[string]interface{}, key string) (float64, bool) {
	switch v := attrs[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

// toOTelAttr converts a value to a typed OTel attribute.KeyValue.
func toOTelAttr(key string, v interface{}) attribute.KeyValue {
	switch val := v.(type) {
	case string:
		return attribute.String(key, val)
	case int:
		return attribute.Int64(key, int64(val))
	case int32:
		return attribute.Int64(key, int64(val))
	case int64:
		return attribute.Int64(key, val)
	case float32:
		return attribute.Float64(key, float64(val))
	case float64:
		return attribute.Float64(key, val)
	case bool:
		return attribute.Bool(key, val)
	default:
		return attribute.String(key, fmt.Sprintf("%v", val))
	}
}

// spanKindFor returns the OTel SpanKind for a given Loom span name.
// LLM, backend, and MCP spans are modelled as client calls (outbound I/O);
// everything else is internal.
func spanKindFor(name string) oteltrace.SpanKind {
	switch {
	case strings.HasPrefix(name, "llm."):
		return oteltrace.SpanKindClient
	case strings.HasPrefix(name, "backend."):
		return oteltrace.SpanKindClient
	case strings.HasPrefix(name, "mcp."):
		return oteltrace.SpanKindClient
	default:
		return oteltrace.SpanKindInternal
	}
}
