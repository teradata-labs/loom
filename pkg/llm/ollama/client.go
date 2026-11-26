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
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Client implements the LLMProvider interface for Ollama.
type Client struct {
	endpoint    string
	model       string
	httpClient  *http.Client
	maxTokens   int
	temperature float64
	seed        *int64 // nil = omit "seed" from options (Ollama samples randomly)
	toolMode    ToolMode
	rateLimiter *llm.RateLimiter

	// Auto tool-mode probe cache (see resolveToolMode). Guarded by probeMu:
	// the client is shared across concurrent sessions.
	probeMu      sync.Mutex
	probeDone    bool             // a probe succeeded; probeResult holds its answer
	probeResult  toolModeDecision // valid when probeDone
	probeRetryAt time.Time        // after a failed probe, no new probe before this time
	probeLastErr error            // the failed probe's error, reported during the backoff
	probeBackoff time.Duration    // how long a failed probe is remembered
	probeWait    chan struct{}    // non-nil while a probe runs; closed when it ends
}

// fallbackToolSupportedModels is used when the /api/show probe fails (e.g. Ollama
// is unreachable at client creation time). Prefer the dynamic probe.
var fallbackToolSupportedModels = map[string]bool{
	"llama3.3":      true,
	"llama3.2":      true,
	"llama3.1":      true,
	"qwen2.5":       true,
	"qwen2.5-coder": true,
	"qwen3":         true,
	"qwen3-coder":   true,
	"mistral":       true,
	"mistral-small": true,
	"mixtral":       true,
	"deepseek-r1":   true,
	"functionary":   true,
	"command-r":     true,
	"phi4":          true,
	"gemma4":        true,
	"gpt-oss":       true,
}

// ToolMode defines how tools are handled.
type ToolMode string

const (
	// ToolModeAuto automatically detects if the model supports native tool calling
	ToolModeAuto ToolMode = "auto"
	// ToolModeNative uses Ollama's native tool calling API (requires Ollama v0.12.3+)
	ToolModeNative ToolMode = "native"
	// ToolModePrompt uses prompt engineering to simulate tool calling
	ToolModePrompt ToolMode = "prompt"
)

// Config holds configuration for the Ollama client.
type Config struct {
	Endpoint          string        // Default: http://localhost:11434
	Model             string        // Required: e.g., llama3.1, mistral, qwen2.5-coder
	MaxTokens         int           // Default: model-aware (4096 for 7B/8B, 6144 for 13B-32B, 8192 for 70B+)
	Temperature       float64       // Default: 0.8
	Timeout           time.Duration // Response header timeout. Default: 300s. Total generation time is bounded by caller context.
	ToolMode          ToolMode      // Default: auto (detect native support)
	RateLimiterConfig llm.RateLimiterConfig

	// Seed pins Ollama's sampling seed for reproducible generations.
	//
	// It is a pointer, not a plain int64, because Ollama treats seed 0 as a
	// usable seed value rather than "unset". nil therefore means "do not send
	// a seed at all" (Ollama keeps sampling randomly), while a non-nil pointer
	// to 0 means "seed with 0". Mirrors the optional int64 seed field on the
	// loom.v1.LLMConfig proto.
	Seed *int64
}

// getDefaultMaxTokens returns intelligent max_tokens based on model name.
// Smaller models (7B-8B) benefit from shorter outputs, while larger models can handle more.
func getDefaultMaxTokens(model string) int {
	modelLower := strings.ToLower(model)

	// Large models (70B+ parameters) - full capacity
	// These are explicit large models
	if strings.Contains(modelLower, "70b") || strings.Contains(modelLower, "72b") ||
		strings.Contains(modelLower, "405b") ||
		strings.Contains(modelLower, "claude") || strings.Contains(modelLower, "gpt-4") {
		return 8192 // 8K for large models
	}

	// Medium models (13B-32B parameters) - balanced outputs
	if strings.Contains(modelLower, "13b") || strings.Contains(modelLower, "14b") ||
		strings.Contains(modelLower, "20b") || strings.Contains(modelLower, "30b") ||
		strings.Contains(modelLower, "32b") || strings.Contains(modelLower, "34b") {
		return 6144 // 6K for medium models
	}

	// Small models (7B-8B parameters) or unknown - conservative default
	// This includes: llama3.1:8b, qwen2.5:7b, gemma, phi, and base names without size
	return 4096 // 4K for small models (safe default)
}

// NewClient creates a new Ollama client.
func NewClient(cfg Config) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://localhost:11434"
	}
	if cfg.Model == "" {
		cfg.Model = "llama3.1"
	}
	if cfg.MaxTokens == 0 {
		// Use model-aware default instead of fixed 4096
		cfg.MaxTokens = getDefaultMaxTokens(cfg.Model)
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = 0.8
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 300 * time.Second
	}
	if cfg.ToolMode == "" {
		cfg.ToolMode = ToolModeAuto
	}

	// Initialize rate limiter if enabled
	var rateLimiter *llm.RateLimiter
	if cfg.RateLimiterConfig.Enabled {
		rateLimiter = llm.SharedRateLimiter("ollama|"+cfg.Endpoint, cfg.RateLimiterConfig)
	}

	// Copy the seed value rather than retaining the caller's pointer, so later
	// mutation of the caller's variable cannot race with in-flight requests.
	var seed *int64
	if cfg.Seed != nil {
		s := *cfg.Seed
		seed = &s
	}

	return &Client{
		endpoint:     cfg.Endpoint,
		model:        cfg.Model,
		maxTokens:    cfg.MaxTokens,
		temperature:  cfg.Temperature,
		seed:         seed,
		toolMode:     cfg.ToolMode,
		rateLimiter:  rateLimiter,
		probeBackoff: defaultProbeBackoff,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ResponseHeaderTimeout: cfg.Timeout,
				IdleConnTimeout:       90 * time.Second,
				MaxIdleConns:          10,
				MaxIdleConnsPerHost:   2,
			},
		},
	}
}

// Name returns the provider name.
func (c *Client) Name() string {
	return "ollama"
}

// Model returns the model identifier.
func (c *Client) Model() string {
	return c.model
}

// Sources of a tool-mode decision, reported in the response metadata
// ("tool_mode_source"), the debug log and the llm.ollama.tool_mode_source
// span attribute.
const (
	toolModeSourceConfig       = "config"        // ToolMode set to native or prompt
	toolModeSourceCapabilities = "capabilities"  // /api/show capabilities list included or left out "tools"
	toolModeSourceTemplate     = "template"      // older Ollama: /api/show template checked for tool directives
	toolModeSourceFallbackList = "fallback_list" // probe failed; the model matched fallbackToolSupportedModels
	toolModeSourceProbeFailed  = "probe_failed"  // probe failed; the model is not in the fallback list
	toolModeSourceNoTools      = "no_tools"      // no tools and no tool history: not probed, nothing to decide
)

// probeTimeout bounds one /api/show probe. Ollama can take several seconds to
// answer while a model is loading or generating. The caller's context still
// applies: a cancelled request stops its probe.
const probeTimeout = 30 * time.Second

// defaultProbeBackoff is how long a failed probe is remembered. Requests in
// that window use the fallback list without probing, so a hung or busy
// /api/show doesn't add a probe to every request.
const defaultProbeBackoff = 30 * time.Second

// toolModeDecision is the tool mode chosen for one request.
type toolModeDecision struct {
	native   bool
	source   string // one of the toolModeSource* constants
	cached   bool   // taken from an earlier probe or a failure backoff, without probing
	probeErr error  // why the probe failed (sources fallback_list and probe_failed)
}

// mode returns "native" or "prompt".
func (d toolModeDecision) mode() string {
	if d.native {
		return string(ToolModeNative)
	}
	return string(ToolModePrompt)
}

// resolveToolMode decides once per request whether tools go native or in the
// prompt. Every part of the request (history conversion, the tools block, the
// response metadata) uses the one decision, so a probe that fails and then
// succeeds mid-request can't mix the two modes.
//
// In auto mode a successful /api/show probe is cached for the client's
// lifetime. A failed probe falls back to fallbackToolSupportedModels and is
// remembered for probeBackoff; the first request after that probes again. A
// probe stopped by the caller's own cancellation is not remembered.
func (c *Client) resolveToolMode(ctx context.Context) toolModeDecision {
	switch c.toolMode {
	case ToolModeNative:
		return toolModeDecision{native: true, source: toolModeSourceConfig}
	case ToolModePrompt:
		return toolModeDecision{native: false, source: toolModeSourceConfig}
	}

	// The client is shared across sessions: the cache is read and written
	// under probeMu. The probe itself runs unlocked so a slow /api/show never
	// blocks a caller that could otherwise give up on its own context.
	// Concurrent first requests share one probe: the rest wait for it, each
	// still free to give up on its own context.
	c.probeMu.Lock()
	for {
		if c.probeDone {
			d := c.probeResult
			c.probeMu.Unlock()
			d.cached = true
			return d
		}
		if time.Now().Before(c.probeRetryAt) {
			d := c.fallbackDecision(c.probeLastErr)
			c.probeMu.Unlock()
			d.cached = true
			return d
		}
		if c.probeWait == nil {
			break
		}
		wait := c.probeWait
		c.probeMu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			d := c.fallbackDecision(ctx.Err())
			d.cached = true
			return d
		}
		c.probeMu.Lock()
	}
	done := make(chan struct{})
	c.probeWait = done
	c.probeMu.Unlock()

	native, source, err := c.probeToolSupport(ctx)

	c.probeMu.Lock()
	c.probeWait = nil
	close(done)
	if err == nil {
		c.probeDone = true
		c.probeResult = toolModeDecision{native: native, source: source}
	} else if ctx.Err() == nil {
		// A probe stopped by the caller's own cancellation isn't remembered:
		// a waiting request probes again.
		c.probeRetryAt = time.Now().Add(c.probeBackoff)
		c.probeLastErr = err
	}
	c.probeMu.Unlock()

	if err != nil {
		d := c.fallbackDecision(err)
		zap.L().Debug("ollama tool mode: /api/show probe failed, using fallback list",
			zap.String("model", c.model),
			zap.String("tool_mode", d.mode()),
			zap.String("source", d.source),
			zap.Duration("retry_after", c.probeBackoff),
			zap.Error(err))
		return d
	}

	d := toolModeDecision{native: native, source: source}
	zap.L().Debug("ollama tool mode: probed /api/show",
		zap.String("model", c.model),
		zap.String("tool_mode", d.mode()),
		zap.String("source", d.source))
	return d
}

// toolModeWithoutTools is the decision for a request with no tools and no
// tool calls or results in its history. Both modes send it the same way, so
// it isn't worth a probe: a request with nothing to do with tools never waits
// for a slow /api/show.
func (c *Client) toolModeWithoutTools() toolModeDecision {
	switch c.toolMode {
	case ToolModeNative:
		return toolModeDecision{native: true, source: toolModeSourceConfig}
	case ToolModePrompt:
		return toolModeDecision{native: false, source: toolModeSourceConfig}
	}
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	if c.probeDone {
		d := c.probeResult
		d.cached = true
		return d
	}
	return toolModeDecision{native: false, source: toolModeSourceNoTools}
}

// usesTools reports whether a request offers tools or carries tool calls or
// results in its history.
func usesTools(messages []llmtypes.Message, tools []shuttle.Tool) bool {
	if len(tools) > 0 {
		return true
	}
	for _, m := range messages {
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// fallbackDecision matches the model against fallbackToolSupportedModels by
// prefix, for when the probe failed.
func (c *Client) fallbackDecision(probeErr error) toolModeDecision {
	for baseModel := range fallbackToolSupportedModels {
		if strings.HasPrefix(c.model, baseModel) {
			return toolModeDecision{native: true, source: toolModeSourceFallbackList, probeErr: probeErr}
		}
	}
	return toolModeDecision{native: false, source: toolModeSourceProbeFailed, probeErr: probeErr}
}

// recordToolMode puts the request's tool-mode decision on the current span
// (set by the instrumented provider wrapper), so a trace shows why tools were
// or weren't sent.
func recordToolMode(ctx context.Context, d toolModeDecision) {
	span := observability.SpanFromContext(ctx)
	if span == nil {
		return
	}
	span.SetAttribute("llm.ollama.tool_mode", d.mode())
	span.SetAttribute("llm.ollama.tool_mode_source", d.source)
	span.SetAttribute("llm.ollama.tool_mode_cached", d.cached)
	if d.probeErr != nil {
		span.SetAttribute("llm.ollama.tool_probe_error", d.probeErr.Error())
	}
}

// probeToolSupport queries Ollama's /api/show endpoint to check whether the
// model lists the "tools" capability or, on older Ollama, whether its
// template contains tool-handling directives (e.g. {{- if .Tools }}).
// Returns whether the model supports tools and which field said so
// (toolModeSourceCapabilities or toolModeSourceTemplate), or the error that
// stopped the probe.
func (c *Client) probeToolSupport(ctx context.Context) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]string{"name": c.model})
	if err != nil {
		return false, "", fmt.Errorf("marshal /api/show request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/api/show", bytes.NewReader(body))
	if err != nil {
		return false, "", fmt.Errorf("create /api/show request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("/api/show: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("/api/show: status %d", resp.StatusCode)
	}

	var showResp struct {
		Template     string   `json:"template"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&showResp); err != nil {
		return false, "", fmt.Errorf("decode /api/show response: %w", err)
	}

	// Ollama (v0.6.4+) lists what the model can do, "tools" among them. Prefer
	// it: models on the MLX runner report a bare "{{ .Prompt }}" template
	// even when they call tools natively.
	if len(showResp.Capabilities) > 0 {
		return slices.Contains(showResp.Capabilities, "tools"), toolModeSourceCapabilities, nil
	}

	// Older Ollama: models with tool support include tool-handling blocks in their template
	hasTools := strings.Contains(showResp.Template, ".Tools") ||
		strings.Contains(showResp.Template, "tools")
	return hasTools, toolModeSourceTemplate, nil
}

// Chat sends a conversation to Ollama and returns the response.
// Internally uses streaming so that the response header timeout applies to
// first-token arrival rather than total generation time.
func (c *Client) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return c.ChatStream(ctx, messages, tools, nil)
}

// convertTools converts shuttle.Tool to Ollama tool format.
// Tool names are sanitized for provider compatibility.
// nameMap, when non-nil, receives this request's sanitized → original names.
func (c *Client) convertTools(tools []shuttle.Tool, nameMap map[string]string) []ollamaTool {
	ollamaTools := make([]ollamaTool, len(tools))
	for i, tool := range tools {
		originalName := tool.Name()
		sanitizedName := llm.SanitizeToolName(originalName)
		if nameMap != nil {
			nameMap[sanitizedName] = originalName
		}
		// Sanitize a copy: the tool (and its schema) is shared by every session
		// that uses it, so the request must not write into it.
		schema := sanitizeSchemaTypes(tool.InputSchema())
		ollamaTools[i] = ollamaTool{
			Type: "function",
			Function: ollamaFunction{
				Name:        sanitizedName,
				Description: tool.Description(),
				Parameters:  schema,
			},
		}
	}
	return ollamaTools
}

// sanitizeSchemaTypes returns a copy of schema in which every type field is
// non-empty (MCP tools may omit types, which some models reject). The input
// is never written: tool schemas are shared across concurrent requests.
// Properties and Items are copied recursively; leaf fields that are not
// rewritten (Enum, Default, bounds) keep pointing at the original values.
func sanitizeSchemaTypes(schema *shuttle.JSONSchema) *shuttle.JSONSchema {
	if schema == nil {
		return nil
	}
	cp := *schema
	cp.Type = llm.InferSchemaType(schema)
	if schema.Properties != nil {
		cp.Properties = make(map[string]*shuttle.JSONSchema, len(schema.Properties))
		for name, prop := range schema.Properties {
			cp.Properties[name] = sanitizeSchemaTypes(prop)
		}
	}
	if schema.Items != nil {
		cp.Items = sanitizeSchemaTypes(schema.Items)
	}
	return &cp
}

// convertMessages converts agent messages to Ollama format. native is the
// request's tool mode (resolveToolMode): it decides whether assistant tool
// calls are echoed and whether tool results use the "tool" role.
func (c *Client) convertMessages(messages []llmtypes.Message, native bool) []ollamaMessage {
	var apiMessages []ollamaMessage
	// Tool call ID → name sent to the model, so each result can name its tool.
	toolNames := make(map[string]string)
	// Names of the latest assistant message's calls not yet answered by a
	// result without an ID. Ollama before v0.12.10 returns tool calls with no
	// ID; such results are paired with the calls by position.
	var pending []string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// Ollama supports system messages
			apiMessages = append(apiMessages, ollamaMessage{
				Role:    "system",
				Content: msg.Content,
			})

		case "user":
			// Check if message has ContentBlocks (multi-modal content with images)
			if len(msg.ContentBlocks) > 0 {
				var textParts []string
				var images []string

				// Extract text and images from content blocks
				for _, block := range msg.ContentBlocks {
					switch block.Type {
					case "text":
						textParts = append(textParts, block.Text)
					case "image":
						if block.Image != nil && block.Image.Source.Type == "base64" {
							// Ollama expects base64 images in the images array
							images = append(images, block.Image.Source.Data)
						}
					}
				}

				// Combine text parts
				content := strings.Join(textParts, "\n")

				apiMessages = appendUserOrCoalesceOllama(apiMessages, content, images)
			} else {
				// Fallback to plain text (backward compatible)
				apiMessages = appendUserOrCoalesceOllama(apiMessages, msg.Content, nil)
			}

		case "assistant":
			apiMsg := ollamaMessage{
				Role:    msg.Role,
				Content: msg.Content,
			}
			// Echo the model's own tool calls. Without them the history shows
			// tool results nobody asked for, and the model repeats the call.
			if native {
				pending = pending[:0]
				for _, tc := range msg.ToolCalls {
					name := llm.SanitizeToolName(tc.Name)
					if tc.ID != "" {
						toolNames[tc.ID] = name
					}
					pending = append(pending, name)
					args := tc.Input
					if args == nil {
						args = map[string]interface{}{}
					}
					apiMsg.ToolCalls = append(apiMsg.ToolCalls, ollamaToolCall{
						ID:       tc.ID,
						Type:     "function",
						Function: ollamaFunctionCall{Name: name, Arguments: args},
					})
				}
			}
			apiMessages = append(apiMessages, apiMsg)

		case "tool":
			if native {
				// Native tool format (Ollama v0.12.3+) — separate role, safe
				// alongside a following user-role sidecar (no consecutive
				// same-role messages).
				name := toolNames[msg.ToolUseID]
				if msg.ToolUseID == "" && len(pending) > 0 {
					name, pending = pending[0], pending[1:]
				}
				apiMessages = append(apiMessages, ollamaMessage{
					Role:       "tool",
					Content:    msg.Content,
					ToolName:   name,
					ToolCallID: msg.ToolUseID,
				})
			} else {
				// Fallback path: tool_result inlined as a user message. Route
				// through the coalesce helper so a following user-role sidecar
				// (e.g. manage_skills's skill body) folds into the same
				// message instead of producing consecutive user messages.
				apiMessages = appendUserOrCoalesceOllama(apiMessages, fmt.Sprintf("Tool result: %s", msg.Content), nil)
			}
		}
	}

	return apiMessages
}

// appendUserOrCoalesceOllama appends user content to the last user message in
// the slice when that last message is user-role; otherwise it appends a new
// user message. Prevents consecutive user-role wire messages, which some
// Ollama models reject. Also lets a tool_result (in the non-native fallback
// path) and an immediately-following user text (e.g. the manage_skills
// skill-body sidecar) share a single user message.
func appendUserOrCoalesceOllama(msgs []ollamaMessage, content string, images []string) []ollamaMessage {
	if content == "" && len(images) == 0 {
		return msgs
	}
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		if msgs[n-1].Content != "" && content != "" {
			msgs[n-1].Content = msgs[n-1].Content + "\n\n" + content
		} else if content != "" {
			msgs[n-1].Content = content
		}
		if len(images) > 0 {
			msgs[n-1].Images = append(msgs[n-1].Images, images...)
		}
		return msgs
	}
	return append(msgs, ollamaMessage{
		Role:    "user",
		Content: content,
		Images:  images,
	})
}

// cleanJSONString removes common formatting issues from JSON strings.
func (c *Client) cleanJSONString(s string) string {
	// Trim whitespace
	s = strings.TrimSpace(s)

	// Strip surrounding backticks (common in Ollama responses)
	if len(s) >= 2 && s[0] == '`' && s[len(s)-1] == '`' {
		s = s[1 : len(s)-1]
	}

	// Strip "json" language marker after opening backticks
	if len(s) >= 4 && strings.HasPrefix(s, "json") {
		// Check if followed by newline or whitespace
		if len(s) > 4 && (s[4] == '\n' || s[4] == '\r' || s[4] == ' ' || s[4] == '\t') {
			s = strings.TrimSpace(s[4:])
		}
	}

	return s
}

// ChatStream implements token-by-token streaming for Ollama.
// This method uses Ollama's /api/chat endpoint with stream=true to stream tokens
// as they are generated. The tokenCallback is called for each token received.
func (c *Client) ChatStream(ctx context.Context, messages []llmtypes.Message,
	tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {

	// 1. Decide the tool mode once for the whole request, then build the
	// request body from that one decision.
	toolMode := c.toolModeWithoutTools()
	if usesTools(messages, tools) {
		toolMode = c.resolveToolMode(ctx)
	}
	recordToolMode(ctx, toolMode)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	apiMessages := c.convertMessages(messages, toolMode.native)

	req := chatRequest{
		Model:    c.model,
		Messages: apiMessages,
		Stream:   true, // Enable streaming
		Options: map[string]interface{}{
			"temperature": c.temperature,
			"num_predict": c.maxTokens,
		},
	}

	// Only send "seed" when one was configured. Ollama reads seed 0 as a real
	// seed, so an unset seed must be omitted entirely rather than sent as 0.
	if c.seed != nil {
		req.Options["seed"] = *c.seed
	}

	// Add tools if native support is available. The sanitized → original
	// name map is per request: the client is shared across sessions.
	nameMap := make(map[string]string)
	if toolMode.native && len(tools) > 0 {
		req.Tools = c.convertTools(tools, nameMap)
	}

	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// sendOnce builds and sends ONE fresh HTTP request per attempt. It must
	// construct a new http.Request each time — a consumed body cannot be
	// re-sent, so a retry of a request built once outside the closure would go
	// out empty. It also surfaces HTTP 429 as an ERROR carrying any
	// server-specified wait (Retry-After and friends): httpClient.Do returns
	// nil error for any HTTP status, so without this the rate limiter's retry
	// never sees throttling and 429s go straight to the caller un-retried.
	sendOnce := func(ctx context.Context) (interface{}, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/api/chat", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, llm.NewThrottleError(
				fmt.Errorf("API error (status 429): %s", string(respBody)),
				llm.RetryAfterFromHeaders(resp.Header))
		}
		// 500/502/503/504/529 (llm.IsTransientStatus; on Ollama typically a
		// model loading or being evicted on a shared server, or a fronting
		// gateway's 502/503/504): retried by the rate limiter under the
		// throttle budget, as for every other HTTP provider; status known
		// before any content.
		if llm.IsTransientStatus(resp.StatusCode) {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, llm.MaxErrorBodyBytes))
			_ = resp.Body.Close()
			return nil, llm.NewTransientError(
				fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody)),
				resp.StatusCode, llm.RetryAfterFromHeaders(resp.Header))
		}
		return resp, nil
	}

	// 2. Send request with rate limiting if enabled
	var httpResp *http.Response
	if c.rateLimiter != nil {
		result, err := c.rateLimiter.Do(ctx, sendOnce)
		if err != nil {
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}
		httpResp = result.(*http.Response)
	} else {
		result, err := sendOnce(ctx)
		if err != nil {
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}
		httpResp = result.(*http.Response)
	}
	defer func() { _ = httpResp.Body.Close() }()

	// Check status code before streaming
	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, llm.WrapHTTPStatus(httpResp.StatusCode,
			fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody)))
	}

	// 3. Process JSON lines stream (newline-delimited JSON)
	var contentBuffer strings.Builder
	var toolCalls []llmtypes.ToolCall
	var lastResponse chatResponse // Keep track of final response for metadata
	tokenCount := 0

	scanner := bufio.NewScanner(httpResp.Body)
	// A gateway may coalesce a whole response (or an error echoing the request)
	// into one SSE line; the Scanner default 64KB line cap aborts the stream.
	// Grown on demand, so steady-state memory is unchanged.
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()

		// Parse JSON line
		var chunk chatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			// Skip malformed lines but continue processing
			continue
		}

		// Extract token from message content
		if chunk.Message.Content != "" {
			token := chunk.Message.Content
			contentBuffer.WriteString(token)
			tokenCount++

			// Call token callback (non-blocking)
			if tokenCallback != nil {
				tokenCallback(token)
			}
		}

		// Extract tool calls if present. They never reach tokenCallback
		// (text only), so report them as stream activity.
		if len(chunk.Message.ToolCalls) > 0 {
			for _, tc := range chunk.Message.ToolCalls {
				// Parse function arguments
				var params map[string]interface{}
				var argsLen int
				switch args := tc.Function.Arguments.(type) {
				case string:
					// Report the raw argument JSON as received; a string that
					// fails to parse still has a real size.
					argsLen = len(args)
					cleanedArgs := c.cleanJSONString(args)
					if err := json.Unmarshal([]byte(cleanedArgs), &params); err != nil {
						params = make(map[string]interface{})
					}
				case map[string]interface{}:
					params = args
					// Re-marshal to approximate the JSON the model produced;
					// Ollama parsed it for us, so the original bytes are gone.
					if argsJSON, err := json.Marshal(args); err == nil {
						argsLen = len(argsJSON)
					}
				default:
					params = make(map[string]interface{})
				}

				toolCalls = append(toolCalls, llmtypes.ToolCall{
					ID:    tc.ID,
					Name:  llm.ReverseToolName(nameMap, tc.Function.Name),
					Input: params,
				})
				// Ollama delivers each tool call complete in one chunk, so the
				// progress event reports the full argument size at once.
				llmtypes.NotifyToolInputProgress(ctx, llmtypes.ToolInputProgress{
					Index:      len(toolCalls) - 1,
					ToolCallID: tc.ID,
					ToolName:   llm.ReverseToolName(nameMap, tc.Function.Name),
					Bytes:      argsLen,
				})
			}
			llmtypes.NotifyStreamActivity(ctx)
		}

		// Keep final response for metadata
		if chunk.Done {
			lastResponse = chunk
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("error reading stream: SSE line exceeded the 8MB cap: %w", err)
		}
		return nil, fmt.Errorf("error reading stream: %w", err)
	}

	// 4. Build final response
	return &llmtypes.LLMResponse{
		Content:    contentBuffer.String(),
		ToolCalls:  toolCalls,
		StopReason: "stop",
		Usage: llmtypes.Usage{
			InputTokens:  lastResponse.PromptEvalCount,
			OutputTokens: lastResponse.EvalCount,
			TotalTokens:  lastResponse.PromptEvalCount + lastResponse.EvalCount,
			CostUSD:      0, // Ollama is free (local)
		},
		Metadata: map[string]interface{}{
			"model":            lastResponse.Model,
			"eval_duration":    lastResponse.EvalDuration,
			"native_tools":     toolMode.native,
			"tool_mode":        string(c.toolMode),
			"tool_mode_source": toolMode.source,
			"streaming":        true,
		},
	}, nil
}

// HealthCheck checks that the Ollama server is reachable and that the
// configured model is available. Uses /api/tags to avoid triggering model
// loading during startup preflight.
func (c *Client) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to query ollama tags: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("health check failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("failed to parse tags response: %w", err)
	}

	model := c.model
	if containsStringValue(payload, model) {
		return nil
	}

	if strings.HasSuffix(model, ":latest") {
		baseModel := strings.TrimSuffix(model, ":latest")
		if containsStringValue(payload, baseModel) {
			return nil
		}
	}

	if !strings.HasSuffix(model, ":latest") {
		if containsStringValue(payload, model+":latest") {
			return nil
		}
	}

	return fmt.Errorf("model %q not found in ollama tags response", model)
}

func containsStringValue(value interface{}, target string) bool {
	switch v := value.(type) {
	case string:
		return v == target
	case []interface{}:
		for _, item := range v {
			if containsStringValue(item, target) {
				return true
			}
		}
	case map[string]interface{}:
		for _, item := range v {
			if containsStringValue(item, target) {
				return true
			}
		}
	}
	return false
}

// Ollama API types

type chatRequest struct {
	Model    string                 `json:"model"`
	Messages []ollamaMessage        `json:"messages"`
	Stream   bool                   `json:"stream"`
	Tools    []ollamaTool           `json:"tools,omitempty"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

type ollamaTool struct {
	Type     string         `json:"type"`
	Function ollamaFunction `json:"function"`
}

type ollamaFunction struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Parameters  *shuttle.JSONSchema `json:"parameters"`
}

type ollamaMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Images     []string         `json:"images,omitempty"` // Base64-encoded images for vision models
	ToolCalls  []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName   string           `json:"tool_name,omitempty"`    // Tool role: which tool produced this result
	ToolCallID string           `json:"tool_call_id,omitempty"` // Tool role: the call this result answers (Ollama v0.12.10+)
}

type ollamaToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string      `json:"name"`
	Arguments interface{} `json:"arguments"` // Can be string or map
}

type chatResponse struct {
	Model           string        `json:"model"`
	CreatedAt       string        `json:"created_at"`
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	TotalDuration   int64         `json:"total_duration"`
	LoadDuration    int64         `json:"load_duration"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
	EvalDuration    int64         `json:"eval_duration"`
}

// Ensure Client implements LLMProvider interface.
var _ llmtypes.LLMProvider = (*Client)(nil)

// Ensure Client implements StreamingLLMProvider interface.
var _ llmtypes.StreamingLLMProvider = (*Client)(nil)
