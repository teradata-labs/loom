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
package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/builder"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestToolCredentialsAreRedactedOnExport runs a turn whose tool calls carry
// credentials in their arguments and results, and checks every span the turn
// records: before redaction the credentials are there (tool span input and
// output, the LLM span that made the calls, the follow-up LLM span that reads
// the results); after the export redaction (observability.RedactSpan with the
// settings looms serve uses) none of them is.
func TestToolCredentialsAreRedactedOnExport(t *testing.T) {
	const (
		bearer = "planted123bearer456token"
		tavily = "tvly-planted0123456789abcdef"
		cookie = "sid=planted789cookie"
	)
	tracer := observability.NewMockTracer()
	llmProvider := &mockLLMProvider{
		name:  "test-llm",
		model: "test-model",
		responses: []agent.LLMResponse{
			{StopReason: "tool_use", ToolCalls: []llmtypes.ToolCall{
				{ID: "call_http", Name: "http_request", Input: map[string]interface{}{
					"url":     "https://api.example.com/v1/items",
					"headers": map[string]interface{}{"Authorization": "Bearer " + bearer, "Accept": "application/json"},
				}},
				{ID: "call_search", Name: "web_search", Input: map[string]interface{}{
					"query": "AI conferences", "api_key": tavily,
				}},
			}},
			{Content: "done", StopReason: "end_turn"},
		},
	}
	httpTool := &shuttle.MockTool{MockName: "http_request", MockBackend: "test",
		MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: map[string]interface{}{
				"status_code": 200,
				"headers":     map[string][]string{"Set-Cookie": {cookie + "; Path=/"}, "Content-Type": {"application/json"}},
				"body":        `{"items":[]}`,
			}}, nil
		}}
	searchTool := &shuttle.MockTool{MockName: "web_search", MockBackend: "test",
		MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: "no results"}, nil
		}}

	cfg := agent.DefaultConfig()
	cfg.Name = "credential-agent"
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := builder.NewInstrumentedAgent(&mockBackend{}, llmProvider, tracer, agent.WithConfig(cfg))
	ag.RegisterTool(httpTool)
	ag.RegisterTool(searchTool)

	_, err := ag.Chat(context.Background(), "credential-session", "fetch the items and search")
	require.NoError(t, err)

	spans := tracer.GetSpans()
	require.NotEmpty(t, spans)
	dump := func(s *observability.Span) string {
		return fmt.Sprintf("%v %v %v", s.Attributes, s.Events, s.Status)
	}

	// The path is exercised: unredacted spans hold every planted credential.
	var raw strings.Builder
	for _, s := range spans {
		raw.WriteString(dump(s))
	}
	for _, secret := range []string{bearer, tavily, cookie} {
		require.Contains(t, raw.String(), secret, "the turn's spans must carry %q before redaction, or the test proves nothing", secret)
	}

	export := observability.PrivacyConfig{RedactCredentials: true, RedactPII: true} // as looms serve configures OTLP export
	for _, s := range spans {
		out := dump(observability.RedactSpan(s, export))
		for _, secret := range []string{bearer, tavily, cookie} {
			assert.NotContains(t, out, secret, "span %s exports %q", s.Name, secret)
		}
	}

	// What is not a credential stays readable.
	for _, s := range tracer.GetSpansByName("agent.tool_execution") {
		if s.Attributes[observability.AttrToolName] != "http_request" {
			continue
		}
		in := fmt.Sprint(observability.RedactSpan(s, export).Attributes["input.value"])
		assert.Contains(t, in, `"url":"https://api.example.com/v1/items"`)
		assert.Contains(t, in, `"Accept":"application/json"`)
		assert.Contains(t, in, observability.RedactedValue)
	}
}
