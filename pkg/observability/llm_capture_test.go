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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestIsLLMMessageCaptureKey(t *testing.T) {
	for key, want := range map[string]bool{
		"llm.input_messages.0.message.role":                                  true,
		"llm.input_messages.12.message.tool_calls.0.tool_call.function.name": true,
		"llm.output_messages.0.message.content":                              true,
		"llm.tools.0.tool.json_schema":                                       true,
		"llm.tools.10.tool.json_schema":                                      true,
		"llm.invocation_parameters":                                          true,
		"llm.tools.count":                                                    false,
		"llm.tools.names":                                                    false,
		"llm.tools.omitted":                                                  false,
		"llm.tools.":                                                         false,
		"llm.messages.omitted":                                               false,
		"llm.messages.count":                                                 false,
		"input.value":                                                        false,
		"llm.token_count.total":                                              false,
		"llm.invocation_parameters_extra":                                    false,
	} {
		assert.Equal(t, want, IsLLMMessageCaptureKey(key), key)
	}
}

// TestExportAttributes_CapturedMessagesKeepTheirNames: the OpenInference
// message, tool and parameter keys export unprefixed; Loom's own
// llm.tools.count, llm.tools.names and the omission counters keep loom.
func TestExportAttributes_CapturedMessagesKeepTheirNames(t *testing.T) {
	kvs := exportAttributes(SpanLLMCompletion, map[string]interface{}{
		"llm.input_messages.0.message.role":     "user",
		"llm.input_messages.0.message.content":  "hi",
		"llm.output_messages.0.message.content": "hello",
		"llm.tools.0.tool.json_schema":          `{"type":"function"}`,
		"llm.invocation_parameters":             `{"model":"m"}`,
		"llm.tools.count":                       1,
		"llm.tools.names":                       []string{"web_search"},
		"llm.tools.omitted":                     2,
		"llm.messages.omitted":                  3,
	})
	keys := map[string]bool{}
	for _, kv := range kvs {
		keys[string(kv.Key)] = true
	}
	for _, k := range []string{
		"llm.input_messages.0.message.role", "llm.input_messages.0.message.content",
		"llm.output_messages.0.message.content", "llm.tools.0.tool.json_schema", "llm.invocation_parameters",
		"loom.llm.tools.count", "loom.llm.tools.names", "loom.llm.tools.omitted", "loom.llm.messages.omitted",
	} {
		assert.True(t, keys[k], "missing exported key %s", k)
	}
	assert.False(t, keys["llm.tools.count"], "Loom's tool count must not look like an OpenInference tool entry")
}

func TestOTelSpanLimits(t *testing.T) {
	assert.Equal(t, otelAttributeCountLimit, otelSpanLimits().AttributeCountLimit)
	t.Setenv("OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT", "300")
	assert.Equal(t, 300, otelSpanLimits().AttributeCountLimit, "the environment variable wins")
}

// newLimitedOTelTracer is OTelTracer as NewOTelTracer builds it (span limits,
// redaction), exporting to an InMemoryExporter.
func newLimitedOTelTracer(t *testing.T, limits *sdktrace.SpanLimits) (*OTelTracer, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	opts := []sdktrace.TracerProviderOption{sdktrace.WithSyncer(exp), sdktrace.WithSampler(sdktrace.AlwaysSample())}
	if limits != nil {
		opts = append(opts, sdktrace.WithRawSpanLimits(*limits))
	}
	provider := sdktrace.NewTracerProvider(opts...)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return &OTelTracer{
		provider: provider,
		tracer:   provider.Tracer("loom"),
		privacy:  PrivacyConfig{RedactCredentials: true, RedactPII: true},
	}, exp
}

// capturingLLMSpan emits an LLM span that recorded n input messages, one
// with a credential and an email in it, plus a tool schema with a credential
// parameter, and the usage the span's token counts come from.
func capturingLLMSpan(tr Tracer, n int) {
	_, s := tr.StartSpan(context.Background(), SpanLLMCompletion)
	for i := 0; i < n; i++ {
		s.SetAttribute(fmt.Sprintf("llm.input_messages.%d.message.role", i), "user")
		s.SetAttribute(fmt.Sprintf("llm.input_messages.%d.message.content", i), fmt.Sprintf("message %d", i))
	}
	s.SetAttribute("llm.input_messages.0.message.content", "use Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456 and mail jane.doe@example.com")
	s.SetAttribute("llm.tools.0.tool.json_schema",
		`{"type":"function","function":{"name":"web_search","parameters":{"type":"object","properties":{"api_key":{"type":"string","default":"tvly-abcdefghijklmnopqrstuvwx"},"query":{"type":"string"}}}}}`)
	s.SetAttribute("llm.tools.count", 1)
	s.SetAttribute("llm.tokens.input", 120)
	s.SetAttribute("llm.tokens.output", 30)
	s.SetAttribute("llm.tokens.total", 150)
	tr.EndSpan(s)
}

func exportedAttrs(t *testing.T, exp *tracetest.InMemoryExporter) map[string]attribute.Value {
	t.Helper()
	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	out := map[string]attribute.Value{}
	for _, kv := range spans[0].Attributes {
		out[string(kv.Key)] = kv.Value
	}
	return out
}

// TestOTelTracer_ExportsEveryCapturedMessageRedacted: a span that records
// hundreds of message attributes keeps all of them and its token counts (the
// SDK's default limit of 128 would silently drop most), and every captured
// value goes through the export redaction.
func TestOTelTracer_ExportsEveryCapturedMessageRedacted(t *testing.T) {
	limits := otelSpanLimits()
	tr, exp := newLimitedOTelTracer(t, &limits)
	capturingLLMSpan(tr, 700)
	got := exportedAttrs(t, exp)

	messages := 0
	for k := range got {
		if strings.HasPrefix(k, "llm.input_messages.") {
			messages++
		}
	}
	assert.Equal(t, 1400, messages)
	assert.Equal(t, int64(150), got["llm.token_count.total"].AsInt64())
	assert.Equal(t, int64(1), got["loom.llm.tools.count"].AsInt64())

	first := got["llm.input_messages.0.message.content"].AsString()
	assert.NotContains(t, first, "abcdefghijklmnopqrstuvwxyz123456")
	assert.NotContains(t, first, "jane.doe@example.com")
	schema := got["llm.tools.0.tool.json_schema"].AsString()
	assert.NotContains(t, schema, "tvly-abcdefghijklmnopqrstuvwx")
	assert.Contains(t, schema, `"query"`, "the rest of the schema is kept")
}

// TestOTelTracer_DefaultSDKLimitWouldDropCapturedMessages documents why
// NewOTelTracer raises the attribute limit.
func TestOTelTracer_DefaultSDKLimitWouldDropCapturedMessages(t *testing.T) {
	tr, exp := newLimitedOTelTracer(t, nil)
	capturingLLMSpan(tr, 700)
	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Greater(t, spans[0].DroppedAttributes, 0)
}

// TestEvalRunRecorder_LeavesCapturedMessagesOut: the unredacted local record
// keeps the span's other attributes but not the captured messages.
func TestEvalRunRecorder_LeavesCapturedMessagesOut(t *testing.T) {
	tr, _, path := newRecordingOTelTracer(t, PrivacyConfig{}, SpanFilterConfig{})
	_, s := tr.StartSpan(context.Background(), SpanLLMCompletion)
	s.SetAttribute("test.span", SpanLLMCompletion)
	s.SetAttribute("message.preview", "hello")
	s.SetAttribute("llm.tokens.total", 42)
	s.SetAttribute("llm.tools.count", 1)
	s.SetAttribute("llm.input_messages.0.message.content", "hello")
	s.SetAttribute("llm.output_messages.0.message.content", "hi")
	s.SetAttribute("llm.tools.0.tool.json_schema", "{}")
	s.SetAttribute("llm.invocation_parameters", "{}")
	tr.EndSpan(s)
	require.NoError(t, tr.Shutdown(context.Background()))

	cfg := readEvalRuns(t, path)[SpanLLMCompletion].Config
	assert.Equal(t, "hello", cfg["message.preview"])
	assert.Contains(t, cfg, "llm.tokens.total")
	assert.Contains(t, cfg, "llm.tools.count")
	for k := range cfg {
		assert.False(t, IsLLMMessageCaptureKey(k), "captured key %s in eval_runs", k)
	}
}

func TestWithoutLLMMessageCapture_ReturnsInputWhenNothingToDrop(t *testing.T) {
	attrs := map[string]interface{}{"a": 1}
	out := withoutLLMMessageCapture(attrs)
	out["b"] = 2
	assert.Contains(t, attrs, "b", "no copy when there is nothing to remove")
}
