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
package gemini

// GenerateContentRequest represents a request to generate content.
type GenerateContentRequest struct {
	Contents         []Content        `json:"contents"`
	Tools            []Tool           `json:"tools,omitempty"`
	GenerationConfig GenerationConfig `json:"generationConfig,omitempty"`
}

// GenerateContentResponse represents a response from the API.
type GenerateContentResponse struct {
	Candidates    []Candidate   `json:"candidates,omitempty"`
	UsageMetadata UsageMetadata `json:"usageMetadata,omitempty"`
	Error         *APIError     `json:"error,omitempty"`
}

// Content represents a conversational turn (message).
type Content struct {
	Role  string `json:"role"` // "user", "model", or "function"
	Parts []Part `json:"parts"`
}

// Part represents a piece of content (text, function call, function response, or inline data).
type Part struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *InlineData       `json:"inlineData,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

// InlineData represents inline data like images.
type InlineData struct {
	MimeType string `json:"mimeType"` // "image/jpeg", "image/png", etc.
	Data     string `json:"data"`     // Base64-encoded data
}

// FunctionCall represents a function call request from the model.
type FunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// FunctionResponse represents a function execution result.
type FunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

// Tool represents a set of function declarations available to the model.
type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations"`
}

// FunctionDeclaration defines a function that the model can call.
type FunctionDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  Schema `json:"parameters,omitempty"`
}

// Schema represents a JSON schema for function parameters.
type Schema struct {
	Type        string            `json:"type"`
	Description string            `json:"description,omitempty"`
	Properties  map[string]Schema `json:"properties,omitempty"`
	Items       *Schema           `json:"items,omitempty"`
	Enum        []interface{}     `json:"enum,omitempty"`
	Required    []string          `json:"required,omitempty"`
}

// GenerationConfig controls generation behavior.
type GenerationConfig struct {
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
	TopP            float64 `json:"topP,omitempty"`
	TopK            int     `json:"topK,omitempty"`
}

// Candidate represents a generated response candidate.
type Candidate struct {
	Content      Content `json:"content"`
	FinishReason string  `json:"finishReason"`
	Index        int     `json:"index"`
}

// UsageMetadata contains token usage information.
type UsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// APIError represents an error from the Gemini API.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}
