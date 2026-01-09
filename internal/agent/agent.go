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
// Package agent provides agent types compatible with Crush's interface.
package agent

import "context"

// AgentToolName is the name of the agent tool.
const AgentToolName = "agent"

// AgentInfo represents information about an available agent
type AgentInfo struct {
	ID     string
	Name   string
	Status string
}

// Coordinator defines the agent coordinator interface.
type Coordinator interface {
	Run(ctx context.Context, sessionID, prompt string, attachments ...interface{}) (interface{}, error)
	IsBusy(agentID string) bool
	IsSessionBusy(sessionID string) bool
	GetAgentID() string
	Cancel()
	CancelAll()
	ClearQueue(sessionID string)
	UpdateModels(ctx context.Context) error
	Summarize(ctx context.Context, sessionID string) error
	QueuedPrompts() int
	QueuedPromptsList(sessionID string) []string
	ListAgents(ctx context.Context) ([]AgentInfo, error)
}

// ErrRequestCancelled is returned when a request is cancelled.
var ErrRequestCancelled = &RequestCancelledError{}

// RequestCancelledError indicates a request was cancelled.
type RequestCancelledError struct{}

func (e *RequestCancelledError) Error() string {
	return "request cancelled"
}

// AgentParams contains agent tool parameters.
type AgentParams struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model,omitempty"`
}
