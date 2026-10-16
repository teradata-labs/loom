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
	"context"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func attrMap(t *testing.T, kvs []attribute.KeyValue) map[string]interface{} {
	t.Helper()
	m := make(map[string]interface{}, len(kvs))
	for _, kv := range kvs {
		key := string(kv.Key)
		if _, dup := m[key]; dup {
			t.Errorf("attribute %q exported more than once", key)
		}
		m[key] = kv.Value.AsInterface()
	}
	return m
}

// TestExportAttributes_Golden is the exported-attribute contract Phoenix
// relies on: the exact attribute set for one span of each kind.
func TestExportAttributes_Golden(t *testing.T) {
	tests := []struct {
		name  string
		span  string
		attrs map[string]interface{}
		want  map[string]interface{}
	}{
		{
			name: "agent span exports no usage, kind AGENT",
			span: "agent.chat",
			attrs: map[string]interface{}{
				"llm.provider":               "bedrock",
				"llm.model":                  "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
				"message.preview":            "Read the note",
				"response.preview":           "heliotrope",
				"session.id":                 "smoke-1",
				"agent.name":                 "phoenix-smoke",
				"conversation.tokens.input":  8,
				"conversation.tokens.output": 86,
				"conversation.tokens.total":  94,
				"conversation.cost.usd":      0.0056718,
				"conversation.turns":         2,
			},
			want: map[string]interface{}{
				"gen_ai.system":                   "aws.bedrock",
				"gen_ai.provider.name":            "aws.bedrock",
				"gen_ai.request.model":            "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
				"gen_ai.prompt":                   "Read the note",
				"gen_ai.completion":               "heliotrope",
				"session.id":                      "smoke-1",
				"agent.name":                      "phoenix-smoke",
				"gen_ai.agent.name":               "phoenix-smoke",
				"loom.conversation.tokens.input":  int64(8),
				"loom.conversation.tokens.output": int64(86),
				"loom.conversation.tokens.total":  int64(94),
				"loom.conversation.cost.usd":      0.0056718,
				"loom.conversation.turns":         int64(2),
				"openinference.span.kind":         "AGENT",
				"input.value":                     "Read the note",
				"output.value":                    "heliotrope",
			},
		},
		{
			name: "LLM span prompt count includes cached tokens",
			span: "llm.completion",
			attrs: map[string]interface{}{
				"llm.provider":          "anthropic",
				"llm.model":             "claude-sonnet-4-5",
				"llm.operation":         "chat",
				"llm.tokens.input":      100,
				"llm.tokens.output":     20,
				"llm.tokens.total":      120,
				"llm.tokens.cache_read": 900,
				"llm.cost.usd":          0.01,
				"message.preview":       "prompt",
				"response.preview":      "answer",
			},
			want: map[string]interface{}{
				"gen_ai.system":                             "anthropic",
				"gen_ai.provider.name":                      "anthropic",
				"gen_ai.request.model":                      "claude-sonnet-4-5",
				"gen_ai.operation.name":                     "chat",
				"gen_ai.usage.input_tokens":                 int64(100),
				"gen_ai.usage.output_tokens":                int64(20),
				"gen_ai.usage.total_tokens":                 int64(120),
				"gen_ai.usage.cache_read.input_tokens":      int64(900),
				"gen_ai.usage.cost":                         0.01,
				"gen_ai.prompt":                             "prompt",
				"gen_ai.completion":                         "answer",
				"openinference.span.kind":                   "LLM",
				"input.value":                               "prompt",
				"output.value":                              "answer",
				"llm.token_count.prompt":                    int64(1000),
				"llm.token_count.completion":                int64(20),
				"llm.token_count.total":                     int64(1020),
				"llm.token_count.prompt_details.cache_read": int64(900),
				"llm.cost.total":                            0.01,
			},
		},
		{
			name: "tool span",
			span: "agent.tool_execution",
			attrs: map[string]interface{}{
				"tool_name":         "file_read",
				"success":           true,
				"execution_time_ms": int64(5),
			},
			want: map[string]interface{}{
				"loom.tool_name":          "file_read",
				"loom.success":            true,
				"loom.execution_time_ms":  int64(5),
				"openinference.span.kind": "TOOL",
			},
		},
		{
			name: "memory recall span",
			span: "graph_memory.recall",
			attrs: map[string]interface{}{
				"recall.outcome":  "injected",
				"recall.injected": 2,
				"session.id":      "smoke-1",
			},
			want: map[string]interface{}{
				"loom.recall.outcome":     "injected",
				"loom.recall.injected":    int64(2),
				"session.id":              "smoke-1",
				"openinference.span.kind": "RETRIEVER",
			},
		},
		{
			name: "server span",
			span: "server.Weave",
			attrs: map[string]interface{}{
				"session.id":   "smoke-1",
				"agent.id":     "b508685f-f62a-424e-8a32-0d23e408cf70",
				"query.length": 42,
			},
			want: map[string]interface{}{
				"session.id":              "smoke-1",
				"gen_ai.agent.id":         "b508685f-f62a-424e-8a32-0d23e408cf70",
				"loom.query.length":       int64(42),
				"openinference.span.kind": "CHAIN",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := attrMap(t, exportAttributes(tc.span, tc.attrs))
			if !reflect.DeepEqual(got, tc.want) {
				for k, v := range tc.want {
					if gv, ok := got[k]; !ok {
						t.Errorf("missing %s = %v", k, v)
					} else if !reflect.DeepEqual(gv, v) {
						t.Errorf("%s = %v (%T), want %v (%T)", k, gv, gv, v, v)
					}
				}
				for k, v := range got {
					if _, ok := tc.want[k]; !ok {
						t.Errorf("unexpected %s = %v", k, v)
					}
				}
			}
		})
	}
}

func TestExportAttributes_Precedence(t *testing.T) {
	t.Run("explicit span kind is not overridden", func(t *testing.T) {
		got := attrMap(t, exportAttributes("agent.chat", map[string]interface{}{
			"openinference.span.kind": "TOOL",
		}))
		if got["openinference.span.kind"] != "TOOL" {
			t.Errorf("kind = %v, want TOOL", got["openinference.span.kind"])
		}
	})
	t.Run("explicit input.value wins over message.preview", func(t *testing.T) {
		got := attrMap(t, exportAttributes("agent.tool_execution", map[string]interface{}{
			"input.value":     `{"path":"a.txt"}`,
			"message.preview": "ignored for input.value",
		}))
		if got["input.value"] != `{"path":"a.txt"}` {
			t.Errorf("input.value = %v", got["input.value"])
		}
	})
	t.Run("no token counts without llm.tokens.input", func(t *testing.T) {
		got := attrMap(t, exportAttributes("llm.completion", map[string]interface{}{"llm.model": "m"}))
		for k := range got {
			if strings.HasPrefix(k, "llm.token_count.") {
				t.Errorf("unexpected %s", k)
			}
		}
	})
	t.Run("provider fields Phoenix derives are not set", func(t *testing.T) {
		got := attrMap(t, exportAttributes("llm.completion", map[string]interface{}{
			"llm.provider": "bedrock", "llm.model": "m", "llm.tokens.input": 1,
		}))
		for _, k := range []string{"llm.provider", "llm.system", "llm.model_name"} {
			if _, ok := got[k]; ok {
				t.Errorf("%s is set; Phoenix derives it from gen_ai.* and an explicit value overrides its mapping", k)
			}
		}
	})
}

func TestOpenInferenceSpanKind(t *testing.T) {
	tests := map[string]string{
		"llm.completion":               "LLM",
		"llm.tokenize":                 "CHAIN", // not an LLM call
		"agent.chat_with_progress":     "AGENT",
		"tool.execute":                 "TOOL",
		"mcp.tools.call":               "TOOL",
		"memory.search_messages":       "RETRIEVER",
		"memory.rerank_search_results": "RERANKER",
		"agent.conversation_loop":      "CHAIN",
		"session_store.load_session":   "CHAIN",
	}
	for name, want := range tests {
		got := attrMap(t, exportAttributes(name, nil))["openinference.span.kind"]
		if got != want {
			t.Errorf("%s: kind = %v, want %s", name, got, want)
		}
	}
}

func TestExportAttributes_PrefixesNonOpenInferenceMetadata(t *testing.T) {
	// fabric's instrumented backend sets metadata.keys to a count; it must not
	// surface as OpenInference metadata.
	got := attrMap(t, exportAttributes("backend.execute", map[string]interface{}{"metadata.keys": 3}))
	if _, ok := got["loom.metadata.keys"]; !ok {
		t.Errorf("metadata.keys exported as %v, want loom.metadata.keys", got)
	}
}

// TestOTelTracerExportsOpenInference checks the Phase 1 S1.2 done criteria
// end to end through EndSpan.
func TestOTelTracerExportsOpenInference(t *testing.T) {
	tr, exp := newInMemoryOTelTracer(t)
	tr.privacy = PrivacyConfig{RedactCredentials: true, RedactPII: true}

	ctx, agentSpan := tr.StartSpan(context.Background(), "agent.chat")
	agentSpan.SetAttribute("conversation.tokens.input", 8)
	agentSpan.SetAttribute("conversation.cost.usd", 0.0056)
	agentSpan.SetAttribute("message.preview", "call me at 555-123-4567")

	_, llmSpan := tr.StartSpan(ctx, "llm.completion")
	llmSpan.SetAttribute("llm.tokens.input", 100)
	llmSpan.SetAttribute("llm.tokens.output", 5)
	llmSpan.SetAttribute("llm.tokens.cache_read", 900)
	tr.EndSpan(llmSpan)
	tr.EndSpan(agentSpan)

	exported := map[string]map[attribute.Key]attribute.Value{}
	for _, s := range exp.GetSpans() {
		m := map[attribute.Key]attribute.Value{}
		for _, kv := range s.Attributes {
			m[kv.Key] = kv.Value
		}
		exported[s.Name] = m
	}

	agent := exported["agent.chat"]
	if got := agent["openinference.span.kind"].AsString(); got != "AGENT" {
		t.Errorf("agent.chat kind = %q, want AGENT", got)
	}
	for k := range agent {
		if strings.HasPrefix(string(k), "gen_ai.usage.") || strings.HasPrefix(string(k), "llm.token_count.") {
			t.Errorf("agent.chat exports %s; trace token totals would be counted twice", k)
		}
	}
	if got := agent["input.value"].AsString(); strings.Contains(got, "555-123-4567") {
		t.Errorf("input.value %q was not redacted", got)
	}

	llm := exported["llm.completion"]
	checks := map[attribute.Key]int64{
		"llm.token_count.prompt":                    1000,
		"llm.token_count.prompt_details.cache_read": 900,
		"gen_ai.usage.cache_read.input_tokens":      900,
	}
	for k, want := range checks {
		if got := llm[k].AsInt64(); got != want {
			t.Errorf("llm.completion %s = %d, want %d", k, got, want)
		}
	}
	if _, ok := llm["gen_ai.usage.cache_read_input_tokens"]; ok {
		t.Error("llm.completion still exports the misspelled gen_ai.usage.cache_read_input_tokens")
	}
}
