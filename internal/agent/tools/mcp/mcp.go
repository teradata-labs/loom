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
// Package mcp provides MCP types compatible with Crush's interface.
package mcp

import "context"

// EventType represents the type of MCP event.
type EventType int

const (
	EventStateChanged EventType = iota
	EventPromptsListChanged
	EventToolsListChanged
)

// Event represents an MCP event.
type Event struct {
	Type EventType
	Name string
}

// RefreshPrompts refreshes prompts for an MCP server.
func RefreshPrompts(ctx context.Context, name string) {
	// Stub - MCP handled differently in Loom
}

// RefreshTools refreshes tools for an MCP server.
func RefreshTools(ctx context.Context, name string) {
	// Stub - MCP handled differently in Loom
}
