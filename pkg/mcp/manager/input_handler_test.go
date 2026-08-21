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
package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

type fakePrompter struct {
	accept  bool
	content map[string]interface{}
	err     error

	sawMessage string
	sawSchema  map[string]interface{}
}

func (f *fakePrompter) PromptElicitation(_ context.Context, message string, schema map[string]interface{}) (bool, map[string]interface{}, error) {
	f.sawMessage = message
	f.sawSchema = schema
	return f.accept, f.content, f.err
}

func elicitationRequests() protocol.InputRequests {
	return protocol.InputRequests{
		"github_login": {
			Method: "elicitation/create",
			Params: json.RawMessage(`{"message":"Provide username","requestedSchema":{"type":"object"}}`),
		},
	}
}

func TestElicitationHandlerAccept(t *testing.T) {
	p := &fakePrompter{accept: true, content: map[string]interface{}{"name": "octocat"}}
	handler := NewElicitationInputHandler(p)

	responses, err := handler(context.Background(), elicitationRequests())
	require.NoError(t, err)
	require.Contains(t, responses, "github_login")

	var result struct {
		Action  string                 `json:"action"`
		Content map[string]interface{} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(responses["github_login"], &result))
	assert.Equal(t, "accept", result.Action)
	assert.Equal(t, "octocat", result.Content["name"])
	assert.Equal(t, "Provide username", p.sawMessage)
	assert.NotNil(t, p.sawSchema)
}

func TestElicitationHandlerDecline(t *testing.T) {
	p := &fakePrompter{accept: false}
	handler := NewElicitationInputHandler(p)

	responses, err := handler(context.Background(), elicitationRequests())
	require.NoError(t, err)

	var result struct {
		Action  string                 `json:"action"`
		Content map[string]interface{} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(responses["github_login"], &result))
	assert.Equal(t, "decline", result.Action)
	assert.Nil(t, result.Content, "decline carries no content")
}

func TestElicitationHandlerPrompterErrorAborts(t *testing.T) {
	p := &fakePrompter{err: fmt.Errorf("gate unavailable")}
	handler := NewElicitationInputHandler(p)

	_, err := handler(context.Background(), elicitationRequests())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gate unavailable")
}

func TestElicitationHandlerRejectsOtherMethods(t *testing.T) {
	handler := NewElicitationInputHandler(&fakePrompter{accept: true})

	_, err := handler(context.Background(), protocol.InputRequests{
		"llm": {Method: "sampling/createMessage", Params: json.RawMessage(`{}`)},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sampling/createMessage")
}
