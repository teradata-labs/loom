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
// Package lsp provides LSP client stubs.
package lsp

import "context"

// Client is an LSP client stub.
type Client struct{}

// NewClient creates a new LSP client.
func NewClient() *Client {
	return &Client{}
}

// Initialize initializes the client.
func (c *Client) Initialize(ctx context.Context) error {
	return nil
}

// Shutdown shuts down the client.
func (c *Client) Shutdown(ctx context.Context) error {
	return nil
}

// IsConnected returns whether the client is connected.
func (c *Client) IsConnected() bool {
	return false
}

// GetStatus returns the client status.
func (c *Client) GetStatus() string {
	return "disconnected"
}

// DiagnosticSummary represents a summary of diagnostics.
type DiagnosticSummary struct {
	Errors   int
	Warnings int
	Info     int
	Hints    int
}

// GetDiagnosticSummary returns a summary of diagnostics.
func (c *Client) GetDiagnosticSummary() DiagnosticSummary {
	return DiagnosticSummary{}
}
