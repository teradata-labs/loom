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

package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// scriptedProvider returns a fixed reply and records prompts.
type scriptedProvider struct {
	mu      sync.Mutex
	reply   string
	err     error
	delay   time.Duration
	usage   types.Usage
	prompts []string
}

func (p *scriptedProvider) Chat(ctx context.Context, msgs []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	p.mu.Lock()
	for _, m := range msgs {
		p.prompts = append(p.prompts, m.Content)
	}
	reply, err, delay, usage := p.reply, p.err, p.delay, p.usage
	p.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return &types.LLMResponse{Content: reply, Usage: usage}, nil
}
func (p *scriptedProvider) Name() string  { return "scripted" }
func (p *scriptedProvider) Model() string { return "scripted-1" }

func (p *scriptedProvider) lastPrompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.prompts) == 0 {
		return ""
	}
	return p.prompts[len(p.prompts)-1]
}

func fullRequest(t *testing.T) *loomv1.DecisionRequest {
	t.Helper()
	choice, err := decision.Choice("Which kind of failure?", map[string]any{
		"transient": "a retry may succeed",
		"auth":      "credentials rejected",
	}, decision.WithNoneOption("not a failure"))
	require.NoError(t, err)
	score, err := decision.Score("How severe?", "Cosmetic", "Degraded", "Blocking")
	require.NoError(t, err)
	req, err := decision.NewRequest("test.site", map[string]any{
		"tool":   "teradata:connect",
		"result": "MCP_CALL_FAILED: session_handle_budget_full",
	}, map[string]*loomv1.DecisionQuestion{
		"is_failure": decision.Noul("Did the call fail?", decision.WithCriteria("the tool did not do what was asked", nil)),
		"kind":       choice,
		"severity":   score,
	})
	require.NoError(t, err)
	return req
}

const goodReply = `{
  "is_failure": {"probability": 0.97},
  "kind": {"probabilities": {"transient": 0.05, "auth": 0.01, "none_of_these": 0.94}},
  "severity": {"probabilities": {"0": 0.02, "1": 0.12, "2": 0.86}}
}`

func TestAdapterHappyPath(t *testing.T) {
	t.Parallel()
	p := &scriptedProvider{reply: goodReply, usage: types.Usage{InputTokens: 400, OutputTokens: 60, CostUSD: 0.002}}
	a := New(p)
	assert.Equal(t, "llm:scripted", a.Name())
	assert.Equal(t, "scripted-1", a.Model())

	resp, err := a.Decide(context.Background(), fullRequest(t))
	require.NoError(t, err)
	assert.Equal(t, "scripted-1", resp.Model)
	assert.Equal(t, int64(400), resp.Usage.InputTokens)
	assert.Equal(t, int64(60), resp.Usage.OutputTokens)
	assert.InDelta(t, 0.002, resp.Usage.CostUsd, 1e-12)

	n, err := decision.NoulOf(resp, "is_failure")
	require.NoError(t, err)
	assert.InDelta(t, 0.97, n.Probability, 1e-9)
	c, err := decision.ChoiceOf(resp, "kind")
	require.NoError(t, err)
	assert.Equal(t, decision.NoneOption, c.Choice)
	assert.InDelta(t, 0.94, c.Probabilities[decision.NoneOption], 1e-9)
	s, err := decision.ScoreOf(resp, "severity")
	require.NoError(t, err)
	assert.InDelta(t, 1.84, s.Score, 1e-9)
	assert.Equal(t, "Blocking", s.Legend["2"])

	prompt := p.lastPrompt()
	for _, want := range []string{
		"teradata:connect", "session_handle_budget_full", // state reached the model verbatim
		"id: is_failure", "true_when: the tool did not do what was asked",
		"id: kind", "transient: a retry may succeed", "none_of_these: not a failure",
		"id: severity", "0: Cosmetic", "2: Blocking",
		`"probability": "<0..1>"`,
	} {
		assert.Contains(t, prompt, want)
	}
	assert.NotContains(t, prompt, "{{.", "every placeholder substituted")
}

func TestAdapterToleratesFencesAndProse(t *testing.T) {
	t.Parallel()
	for _, wrap := range []string{
		"```json\n" + goodReply + "\n```",
		"Here is the answer:\n" + goodReply + "\nHope that helps.",
		"```\n" + goodReply + "```",
	} {
		p := &scriptedProvider{reply: wrap}
		_, err := New(p).Decide(context.Background(), fullRequest(t))
		require.NoError(t, err, wrap)
	}
}

func TestAdapterRejectsMalformedReplies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		reply string
	}{
		{name: "no json", reply: "I cannot answer."},
		{name: "unknown question id", reply: `{"is_failure":{"probability":0.5},"kind":{"probabilities":{"transient":1}},"severity":{"probabilities":{"0":1}},"extra":{"probability":1}}`},
		{name: "missing question", reply: `{"is_failure":{"probability":0.5},"kind":{"probabilities":{"transient":1}}}`},
		{name: "unknown option key", reply: `{"is_failure":{"probability":0.5},"kind":{"probabilities":{"bogus":1}},"severity":{"probabilities":{"0":1}}}`},
		{name: "unknown level", reply: `{"is_failure":{"probability":0.5},"kind":{"probabilities":{"transient":1}},"severity":{"probabilities":{"7":1}}}`},
		{name: "noul out of range", reply: `{"is_failure":{"probability":1.5},"kind":{"probabilities":{"transient":1}},"severity":{"probabilities":{"0":1}}}`},
		{name: "noul missing", reply: `{"is_failure":{},"kind":{"probabilities":{"transient":1}},"severity":{"probabilities":{"0":1}}}`},
		{name: "negative probability", reply: `{"is_failure":{"probability":0.5},"kind":{"probabilities":{"transient":-1,"auth":2}},"severity":{"probabilities":{"0":1}}}`},
		{name: "all zero distribution", reply: `{"is_failure":{"probability":0.5},"kind":{"probabilities":{"transient":0}},"severity":{"probabilities":{"0":1}}}`},
		{name: "unknown field", reply: `{"is_failure":{"probability":0.5,"reason":"x"},"kind":{"probabilities":{"transient":1}},"severity":{"probabilities":{"0":1}}}`},
		{name: "not an object", reply: `[1,2,3]`},
		{name: "unbalanced", reply: `{"is_failure":{"probability":0.5`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(&scriptedProvider{reply: tt.reply}).Decide(context.Background(), fullRequest(t))
			assert.True(t, errors.Is(err, decision.ErrMalformedAnswer), "got %v", err)
		})
	}
}

func TestAdapterFillsMissingKeysWithZero(t *testing.T) {
	t.Parallel()
	// A partial distribution over known keys is accepted; missing keys are 0.
	reply := `{"is_failure":{"probability":0.5},"kind":{"probabilities":{"transient":1}},"severity":{"probabilities":{"2":1}}}`
	resp, err := New(&scriptedProvider{reply: reply}).Decide(context.Background(), fullRequest(t))
	require.NoError(t, err)
	c, err := decision.ChoiceOf(resp, "kind")
	require.NoError(t, err)
	assert.Equal(t, "transient", c.Choice)
	assert.Equal(t, 0.0, c.Probabilities["auth"])
	assert.Len(t, c.Probabilities, 3)
	assert.InDelta(t, 1, c.Confidence, 1e-9)
}

func TestAdapterProviderErrorsAndTimeout(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	_, err := New(&scriptedProvider{err: boom}).Decide(context.Background(), fullRequest(t))
	assert.True(t, errors.Is(err, boom))

	slow := &scriptedProvider{reply: goodReply, delay: time.Second}
	start := time.Now()
	_, err = New(slow, WithTimeout(10*time.Millisecond)).Decide(context.Background(), fullRequest(t))
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
	assert.Less(t, time.Since(start), 500*time.Millisecond)

	_, err = New(&scriptedProvider{reply: goodReply}).Decide(context.Background(), &loomv1.DecisionRequest{})
	assert.True(t, errors.Is(err, decision.ErrValidation))

	assert.Panics(t, func() { New(nil) })
}

// fakeRegistry serves one template.
type fakeRegistry struct {
	tmpl string
	keys []string
}

func (f *fakeRegistry) Get(_ context.Context, key string, vars map[string]interface{}) (string, error) {
	f.keys = append(f.keys, key)
	if vars != nil {
		return "", errors.New("adapter must fetch the raw template with nil vars")
	}
	if f.tmpl == "" {
		return "", errors.New("not found")
	}
	return f.tmpl, nil
}
func (f *fakeRegistry) GetWithVariant(ctx context.Context, key, _ string, vars map[string]interface{}) (string, error) {
	return f.Get(ctx, key, vars)
}
func (f *fakeRegistry) GetMetadata(context.Context, string) (*prompts.PromptMetadata, error) {
	return nil, nil
}
func (f *fakeRegistry) List(context.Context, map[string]string) ([]string, error) { return nil, nil }
func (f *fakeRegistry) Reload(context.Context) error                              { return nil }
func (f *fakeRegistry) Watch(context.Context) (<-chan prompts.PromptUpdate, error) {
	return nil, nil
}

func TestAdapterUsesRegistryTemplateWhenPresent(t *testing.T) {
	t.Parallel()
	reg := &fakeRegistry{tmpl: "CUSTOM {{.state}} | {{.questions}} | {{.schema}}"}
	p := &scriptedProvider{reply: goodReply}
	_, err := New(p, WithPromptRegistry(reg)).Decide(context.Background(), fullRequest(t))
	require.NoError(t, err)
	assert.Equal(t, []string{PromptKey}, reg.keys)
	assert.True(t, strings.HasPrefix(p.lastPrompt(), "CUSTOM "))
	assert.Contains(t, p.lastPrompt(), "teradata:connect")

	// Missing key falls back to the default template.
	reg2 := &fakeRegistry{}
	p2 := &scriptedProvider{reply: goodReply}
	_, err = New(p2, WithPromptRegistry(reg2)).Decide(context.Background(), fullRequest(t))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(p2.lastPrompt(), "Answer each question below"))
}

// TestDefaultTemplateMatchesPromptFile is the drift guard between the built-in
// fallback and prompts/decision/llm_adapter.yaml.
func TestDefaultTemplateMatchesPromptFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "prompts", "decision", "llm_adapter.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "prompt file must exist")
	// The file is "---\n<front matter>\n---\n<document>"; take the document.
	body, ok := strings.CutPrefix(string(raw), "---\n")
	require.True(t, ok, "expected leading front matter delimiter")
	parts := strings.SplitN(body, "\n---\n", 2)
	require.Len(t, parts, 2, "expected closing front matter delimiter")
	var doc struct {
		Prompts []struct {
			ID      string `yaml:"id"`
			Content string `yaml:"content"`
		} `yaml:"prompts"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(parts[1]), &doc))
	require.Len(t, doc.Prompts, 1)
	assert.Equal(t, "llm_adapter", doc.Prompts[0].ID)
	assert.Equal(t, strings.TrimSpace(DefaultTemplate), strings.TrimSpace(doc.Prompts[0].Content),
		"DefaultTemplate and prompts/decision/llm_adapter.yaml have drifted")
}

func TestExtractObject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: `{"a":1}`, want: `{"a":1}`, ok: true},
		{in: `x {"a":{"b":"}"}} y`, want: `{"a":{"b":"}"}}`, ok: true},
		{in: `{"a":"\"}"}`, want: `{"a":"\"}"}`, ok: true},
		{in: `no braces`, ok: false},
		{in: `{"a":1`, ok: false},
		{in: ``, ok: false},
	}
	for _, tt := range tests {
		got, ok := extractObject(tt.in)
		assert.Equal(t, tt.ok, ok, tt.in)
		if ok {
			assert.Equal(t, tt.want, got)
		}
	}
}

func FuzzParseAnswers(f *testing.F) {
	f.Add(goodReply)
	f.Add(`{"is_failure":{"probability":0.5}}`)
	f.Add(`{}`)
	f.Add(`{"kind":{"probabilities":{"transient":1e309}}}`)
	f.Add("```json\n{\"a\":1}\n```")
	f.Fuzz(func(t *testing.T, content string) {
		req := fuzzRequest(t)
		answers, err := ParseAnswers(req, content)
		if err != nil {
			if !errors.Is(err, decision.ErrMalformedAnswer) {
				t.Fatalf("errors must wrap ErrMalformedAnswer: %v", err)
			}
			return
		}
		// Anything accepted must pass the same check a Router applies.
		resp := &loomv1.DecisionResponse{Answers: answers}
		if err := decision.CheckAnswers(req, resp); err != nil {
			t.Fatalf("accepted answers failed CheckAnswers: %v", err)
		}
	})
}

func FuzzExtractObject(f *testing.F) {
	f.Add(`{"a":1}`)
	f.Add(`{{}}`)
	f.Add(`"{"`)
	f.Fuzz(func(t *testing.T, s string) {
		got, ok := extractObject(s)
		if ok && (len(got) < 2 || got[0] != '{' || got[len(got)-1] != '}') {
			t.Fatalf("extracted %q is not a braced object", got)
		}
	})
}

func fuzzRequest(t *testing.T) *loomv1.DecisionRequest {
	t.Helper()
	choice, err := decision.Choice("k", map[string]any{"transient": "t", "auth": "a"})
	if err != nil {
		t.Fatal(err)
	}
	score, err := decision.Score("s", "lo", "hi")
	if err != nil {
		t.Fatal(err)
	}
	req, err := decision.NewRequest("fuzz", "state", map[string]*loomv1.DecisionQuestion{
		"is_failure": decision.Noul("?"),
		"kind":       choice,
		"severity":   score,
	})
	if err != nil {
		t.Fatal(err)
	}
	return req
}
