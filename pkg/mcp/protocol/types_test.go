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
package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTool_MarshalJSON(t *testing.T) {
	tool := Tool{
		Name:        "read_file",
		Description: "Read a file from disk",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Path to the file",
				},
			},
			"required": []string{"path"},
		},
	}

	data, err := json.Marshal(tool)
	require.NoError(t, err)

	var unmarshaled Tool
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, tool.Name, unmarshaled.Name)
	assert.Equal(t, tool.Description, unmarshaled.Description)
	assert.NotNil(t, unmarshaled.InputSchema)
}

func TestResource_MarshalJSON(t *testing.T) {
	resource := Resource{
		URI:         "file:///tmp/test.txt",
		Name:        "test.txt",
		Description: "A test file",
		MimeType:    "text/plain",
	}

	data, err := json.Marshal(resource)
	require.NoError(t, err)

	var unmarshaled Resource
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, resource.URI, unmarshaled.URI)
	assert.Equal(t, resource.Name, unmarshaled.Name)
	assert.Equal(t, resource.Description, unmarshaled.Description)
	assert.Equal(t, resource.MimeType, unmarshaled.MimeType)
}

func TestPrompt_MarshalJSON(t *testing.T) {
	prompt := Prompt{
		Name:        "code_review",
		Description: "Review code for quality",
		Arguments: []PromptArgument{
			{
				Name:        "language",
				Description: "Programming language",
				Required:    true,
			},
		},
	}

	data, err := json.Marshal(prompt)
	require.NoError(t, err)

	var unmarshaled Prompt
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, prompt.Name, unmarshaled.Name)
	assert.Equal(t, prompt.Description, unmarshaled.Description)
	assert.Len(t, unmarshaled.Arguments, 1)
	assert.Equal(t, "language", unmarshaled.Arguments[0].Name)
	assert.True(t, unmarshaled.Arguments[0].Required)
}

func TestInitializeParams_MarshalJSON(t *testing.T) {
	params := InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    ClientCapabilities{},
		ClientInfo: Implementation{
			Name:    "test-client",
			Version: "1.0.0",
		},
	}

	data, err := json.Marshal(params)
	require.NoError(t, err)

	var unmarshaled InitializeParams
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, params.ProtocolVersion, unmarshaled.ProtocolVersion)
	assert.Equal(t, params.ClientInfo.Name, unmarshaled.ClientInfo.Name)
	assert.Equal(t, params.ClientInfo.Version, unmarshaled.ClientInfo.Version)
}

func TestInitializeResult_MarshalJSON(t *testing.T) {
	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{},
			Resources: &ResourcesCapability{
				Subscribe: true,
			},
		},
		ServerInfo: Implementation{
			Name:    "test-server",
			Version: "2.0.0",
		},
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var unmarshaled InitializeResult
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, result.ProtocolVersion, unmarshaled.ProtocolVersion)
	assert.Equal(t, result.ServerInfo.Name, unmarshaled.ServerInfo.Name)
	assert.NotNil(t, unmarshaled.Capabilities.Resources)
	assert.True(t, unmarshaled.Capabilities.Resources.Subscribe)
}

func TestCallToolResult_MarshalJSON(t *testing.T) {
	tests := []struct {
		name   string
		result CallToolResult
	}{
		{
			name: "text content",
			result: CallToolResult{
				Content: []Content{
					{
						Type: "text",
						Text: "Hello, world!",
					},
				},
			},
		},
		{
			name: "error result",
			result: CallToolResult{
				IsError: true,
				Content: []Content{
					{
						Type: "text",
						Text: "Error occurred",
					},
				},
			},
		},
		{
			name: "multiple contents",
			result: CallToolResult{
				Content: []Content{
					{
						Type: "text",
						Text: "Part 1",
					},
					{
						Type: "text",
						Text: "Part 2",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.result)
			require.NoError(t, err)

			var unmarshaled CallToolResult
			err = json.Unmarshal(data, &unmarshaled)
			require.NoError(t, err)

			assert.Equal(t, tt.result.IsError, unmarshaled.IsError)
			assert.Len(t, unmarshaled.Content, len(tt.result.Content))

			for i := range tt.result.Content {
				assert.Equal(t, tt.result.Content[i].Type, unmarshaled.Content[i].Type)
				assert.Equal(t, tt.result.Content[i].Text, unmarshaled.Content[i].Text)
			}
		})
	}
}

func TestResourceContents_MarshalJSON(t *testing.T) {
	contents := ResourceContents{
		URI:      "file:///tmp/test.txt",
		MimeType: "text/plain",
		Text:     "File contents",
	}

	data, err := json.Marshal(contents)
	require.NoError(t, err)

	var unmarshaled ResourceContents
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, contents.URI, unmarshaled.URI)
	assert.Equal(t, contents.MimeType, unmarshaled.MimeType)
	assert.Equal(t, contents.Text, unmarshaled.Text)
}

func TestGetPromptResult_MarshalJSON(t *testing.T) {
	result := GetPromptResult{
		Description: "Code review prompt",
		Messages: []PromptMessage{
			{
				Role: "user",
				Content: Content{
					Type: "text",
					Text: "Please review this code",
				},
			},
		},
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var unmarshaled GetPromptResult
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, result.Description, unmarshaled.Description)
	assert.Len(t, unmarshaled.Messages, 1)
	assert.Equal(t, "user", unmarshaled.Messages[0].Role)
}

func TestSamplingParams_MarshalJSON(t *testing.T) {
	costPriority := 0.5
	speedPriority := 0.5

	params := SamplingParams{
		Messages: []PromptMessage{
			{
				Role: "user",
				Content: Content{
					Type: "text",
					Text: "Hello",
				},
			},
		},
		ModelPrefs: &ModelPreferences{
			Hints: []ModelHint{
				{
					Name: "claude-3-5-sonnet",
				},
			},
			CostPriority:  &costPriority,
			SpeedPriority: &speedPriority,
		},
		SystemPrompt: "You are a helpful assistant",
		MaxTokens:    1000,
	}

	data, err := json.Marshal(params)
	require.NoError(t, err)

	var unmarshaled SamplingParams
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Len(t, unmarshaled.Messages, 1)
	assert.NotNil(t, unmarshaled.ModelPrefs)
	assert.Equal(t, params.SystemPrompt, unmarshaled.SystemPrompt)
	assert.Equal(t, params.MaxTokens, unmarshaled.MaxTokens)
}

func TestProtocolVersionConstant(t *testing.T) {
	assert.Equal(t, "2024-11-05", ProtocolVersion)
}

func TestJSONRPCVersionConstant(t *testing.T) {
	assert.Equal(t, "2.0", JSONRPCVersion)
}

func TestToolListResult(t *testing.T) {
	result := ToolListResult{
		Tools: []Tool{
			{
				Name:        "tool1",
				Description: "First tool",
			},
			{
				Name:        "tool2",
				Description: "Second tool",
			},
		},
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var unmarshaled ToolListResult
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Len(t, unmarshaled.Tools, 2)
	assert.Equal(t, "tool1", unmarshaled.Tools[0].Name)
	assert.Equal(t, "tool2", unmarshaled.Tools[1].Name)
}

func TestResourceListResult(t *testing.T) {
	result := ResourceListResult{
		Resources: []Resource{
			{
				URI:  "file:///tmp/file1.txt",
				Name: "file1.txt",
			},
			{
				URI:  "file:///tmp/file2.txt",
				Name: "file2.txt",
			},
		},
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var unmarshaled ResourceListResult
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Len(t, unmarshaled.Resources, 2)
	assert.Equal(t, "file1.txt", unmarshaled.Resources[0].Name)
	assert.Equal(t, "file2.txt", unmarshaled.Resources[1].Name)
}

func TestPromptListResult(t *testing.T) {
	result := PromptListResult{
		Prompts: []Prompt{
			{
				Name:        "prompt1",
				Description: "First prompt",
			},
		},
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var unmarshaled PromptListResult
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Len(t, unmarshaled.Prompts, 1)
	assert.Equal(t, "prompt1", unmarshaled.Prompts[0].Name)
}

func TestReadResourceResult(t *testing.T) {
	result := ReadResourceResult{
		Contents: []ResourceContents{
			{
				URI:  "file:///tmp/test.txt",
				Text: "Resource content",
			},
		},
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var unmarshaled ReadResourceResult
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Len(t, unmarshaled.Contents, 1)
	assert.Equal(t, "file:///tmp/test.txt", unmarshaled.Contents[0].URI)
	assert.Equal(t, "Resource content", unmarshaled.Contents[0].Text)
}
