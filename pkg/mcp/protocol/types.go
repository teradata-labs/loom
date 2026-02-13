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
// Package protocol implements MCP protocol types for the Model Context Protocol.
package protocol

// ProtocolVersion is the MCP protocol version supported by this implementation
const ProtocolVersion = "2024-11-05"

// InitializeParams contains parameters for the initialize request
type InitializeParams struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    ClientCapabilities     `json:"capabilities"`
	ClientInfo      Implementation         `json:"clientInfo"`
	Extensions      map[string]interface{} `json:"extensions,omitempty"`
}

// InitializeResult contains the server's response to initialize
type InitializeResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    ServerCapabilities     `json:"capabilities"`
	ServerInfo      Implementation         `json:"serverInfo"`
	Extensions      map[string]interface{} `json:"extensions,omitempty"`
}

// Implementation describes client or server implementation details
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCapabilities declares what the client supports
type ClientCapabilities struct {
	Roots    *RootsCapability    `json:"roots,omitempty"`
	Sampling *SamplingCapability `json:"sampling,omitempty"`
}

// ServerCapabilities declares what the server supports
type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
	Logging   *LoggingCapability   `json:"logging,omitempty"`
}

// Capability markers (empty structs indicate support)
type RootsCapability struct{}
type SamplingCapability struct{}
type ToolsCapability struct{}

type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`   // Supports subscriptions
	ListChanged bool `json:"listChanged,omitempty"` // Sends list change notifications
}

type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"` // Sends list change notifications
}

type LoggingCapability struct{}

// ToolAnnotations provides hints about tool behavior (MCP 2025-03-26)
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// Tool represents an MCP tool definition
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`           // JSON Schema
	Annotations *ToolAnnotations       `json:"annotations,omitempty"` // MCP 2025-03-26
	Meta        map[string]interface{} `json:"_meta,omitempty"`       // MCP Apps metadata
}

// ToolListResult is the response from tools/list
type ToolListResult struct {
	Tools []Tool `json:"tools"`
}

// CallToolParams contains parameters for tools/call
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// CallToolResult is the response from tools/call
type CallToolResult struct {
	Content           []Content              `json:"content"`                     // Array of content items
	IsError           bool                   `json:"isError,omitempty"`           // Deprecated, use proper errors
	StructuredContent map[string]interface{} `json:"structuredContent,omitempty"` // Structured output (MCP 2025-03-26)
}

// Content represents different types of content (text, image, resource)
type Content struct {
	Type     string       `json:"type"` // "text", "image", "resource"
	Text     string       `json:"text,omitempty"`
	Data     string       `json:"data,omitempty"`     // Base64 for images
	MimeType string       `json:"mimeType,omitempty"` // For images/resources
	Resource *ResourceRef `json:"resource,omitempty"` // For resource type
}

// ResourceRef references a resource
type ResourceRef struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
}

// Resource represents an MCP resource definition
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceListResult is the response from resources/list
type ResourceListResult struct {
	Resources []Resource `json:"resources"`
}

// ReadResourceParams contains parameters for resources/read
type ReadResourceParams struct {
	URI string `json:"uri"`
}

// ReadResourceResult is the response from resources/read
type ReadResourceResult struct {
	Contents []ResourceContents `json:"contents"`
}

// ResourceContents contains resource data
type ResourceContents struct {
	URI      string                 `json:"uri"`
	MimeType string                 `json:"mimeType,omitempty"`
	Text     string                 `json:"text,omitempty"`
	Blob     string                 `json:"blob,omitempty"` // Base64
	Meta     map[string]interface{} `json:"_meta,omitempty"`
}

// ResourceTemplate defines a dynamic resource URI template
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// Prompt represents an MCP prompt definition
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument describes a prompt parameter
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptListResult is the response from prompts/list
type PromptListResult struct {
	Prompts []Prompt `json:"prompts"`
}

// GetPromptParams contains parameters for prompts/get
type GetPromptParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// GetPromptResult is the response from prompts/get
type GetPromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// PromptMessage represents a message in a prompt
type PromptMessage struct {
	Role    string      `json:"role"`    // "user" or "assistant"
	Content interface{} `json:"content"` // Can be string or Content object
}

// SamplingParams contains parameters for sampling/createMessage
type SamplingParams struct {
	Messages       []PromptMessage        `json:"messages"`
	ModelPrefs     *ModelPreferences      `json:"modelPreferences,omitempty"`
	SystemPrompt   string                 `json:"systemPrompt,omitempty"`
	IncludeContext string                 `json:"includeContext,omitempty"` // "none", "thisServer", "allServers"
	Temperature    *float64               `json:"temperature,omitempty"`
	MaxTokens      int                    `json:"maxTokens"`
	StopSequences  []string               `json:"stopSequences,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// ModelPreferences specifies LLM selection preferences
type ModelPreferences struct {
	Hints                []ModelHint `json:"hints,omitempty"`
	CostPriority         *float64    `json:"costPriority,omitempty"`         // 0-1
	SpeedPriority        *float64    `json:"speedPriority,omitempty"`        // 0-1
	IntelligencePriority *float64    `json:"intelligencePriority,omitempty"` // 0-1
}

// ModelHint suggests model preferences
type ModelHint struct {
	Name string `json:"name,omitempty"`
}

// SamplingResult is the response from sampling/createMessage
type SamplingResult struct {
	Role       string  `json:"role"` // "assistant"
	Content    Content `json:"content"`
	Model      string  `json:"model"`
	StopReason string  `json:"stopReason,omitempty"` // "endTurn", "stopSequence", "maxTokens"
}

// Notification types

// ProgressNotification reports progress for a long-running operation
type ProgressNotification struct {
	ProgressToken string  `json:"progressToken"`
	Progress      float64 `json:"progress"`
	Total         float64 `json:"total,omitempty"`
}

// LogNotification sends log messages from server to client
type LogNotification struct {
	Level  string      `json:"level"` // "debug", "info", "warning", "error"
	Logger string      `json:"logger,omitempty"`
	Data   interface{} `json:"data"`
}

// ResourceUpdatedNotification notifies of a resource change
type ResourceUpdatedNotification struct {
	URI string `json:"uri"`
}

// ResourceListChangedNotification notifies that the resource list has changed
type ResourceListChangedNotification struct{}

// PromptListChangedNotification notifies that the prompt list has changed
type PromptListChangedNotification struct{}
