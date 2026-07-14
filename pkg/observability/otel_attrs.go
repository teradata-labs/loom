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
// Attributes not listed here are forwarded with a "loom." prefix so no data is dropped.
//
// Standards followed:
//   - OTel GenAI spans: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/
//   - OTel Exceptions: https://opentelemetry.io/docs/specs/semconv/exceptions/
//   - OTel RPC (for MCP): https://opentelemetry.io/docs/specs/semconv/rpc/
//   - Opik-specific: gen_ai.prompt / gen_ai.completion drive the Input/Output columns
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
	"llm.tokens.cache_read":  "gen_ai.usage.cache_read_input_tokens",
	"llm.tokens.cache_write": "gen_ai.usage.cache_creation_input_tokens",

	// Response metadata (llm span)
	"llm.stop_reason": "gen_ai.response.finish_reasons",
	"llm.cost.usd":    "gen_ai.usage.cost",

	// Input/output text for Opik UI columns — set on both llm and agent spans
	"message.preview":  "gen_ai.prompt",
	"response.preview": "gen_ai.completion",

	// Token usage (conversation span — set by agent.go)
	"conversation.tokens.input":  "gen_ai.usage.input_tokens",
	"conversation.tokens.output": "gen_ai.usage.output_tokens",
	"conversation.tokens.total":  "gen_ai.usage.total_tokens",
	"conversation.cost.usd":      "gen_ai.usage.cost",
	"conversation.stop_reason":   "gen_ai.response.finish_reasons",

	// Tool execution (OTel GenAI tool-use semconv)
	"tool.name":     "gen_ai.tool.name",
	"mcp.tool.name": "gen_ai.tool.name",
	"mcp.tool.args": "gen_ai.tool.call.arguments",

	// MCP server identity (OTel RPC semconv)
	"mcp.server.name":    "server.address",
	"mcp.server.version": "server.version",

	// Agent identity (OTel GenAI agent semconv, draft)
	"agent_id": "gen_ai.agent.id",

	// Error / exception semconv
	"error.message": "exception.message",
	"error.type":    "exception.type",
	"error.stack":   "exception.stacktrace",

	// Session / user — kept as-is so backends index them without a loom. prefix
	"session.id": "session.id",
	"user.id":    "user.id",
	"trace.id":   "trace.id",
}

// genAISystemNorm normalizes provider names to OTel GenAI spec values for gen_ai.system.
// Spec: https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/#generative-ai-request-attributes
var genAISystemNorm = map[string]string{ //nolint:gochecknoglobals
	"bedrock":      "aws.bedrock",
	"azure-openai": "azure_openai",
	"gemini":       "google.generative_ai",
	"huggingface":  "hugging_face",
	// anthropic, openai, ollama, mistral already match the spec
}

const loomAttrPrefix = "loom."

// translateAttrs sets span attributes on an OTel span, mapping Loom keys to
// OTel semantic convention keys where a mapping exists and prefixing unknowns with "loom.".
func translateAttrs(otelSpan oteltrace.Span, attrs map[string]interface{}) {
	for k, v := range attrs {
		otelKey := k
		if mapped, ok := loomToGenAI[k]; ok {
			otelKey = mapped
			// Normalize gen_ai.system values to OTel spec-defined strings.
			if otelKey == "gen_ai.system" {
				if s, ok := v.(string); ok {
					if norm, ok := genAISystemNorm[s]; ok {
						v = norm
					}
				}
			}
		} else if !strings.HasPrefix(k, loomAttrPrefix) {
			otelKey = loomAttrPrefix + k
		}
		otelSpan.SetAttributes(toOTelAttr(otelKey, v))
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
