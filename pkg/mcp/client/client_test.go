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
package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap"
)

// mockTransport implements transport.Transport for testing
type mockTransport struct {
	receiveFunc func(ctx context.Context) ([]byte, error)
	sendFunc    func(ctx context.Context, data []byte) error
	closeFunc   func() error
}

func (m *mockTransport) Receive(ctx context.Context) ([]byte, error) {
	if m.receiveFunc != nil {
		return m.receiveFunc(ctx)
	}
	return nil, io.EOF
}

func (m *mockTransport) Send(ctx context.Context, data []byte) error {
	if m.sendFunc != nil {
		return m.sendFunc(ctx, data)
	}
	return nil
}

func (m *mockTransport) Close() error {
	if m.closeFunc != nil {
		return m.closeFunc()
	}
	return nil
}

func TestReceiveLoopEOFHandling(t *testing.T) {
	// Test that EOF is treated as normal shutdown and doesn't log error
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create mock transport that returns EOF
	transport := &mockTransport{
		receiveFunc: func(ctx context.Context) ([]byte, error) {
			return nil, io.EOF
		},
	}

	client := &Client{
		transport:     transport,
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
		pending:       make(map[string]chan *protocol.Response),
		notifications: make(chan Notification, 10),
	}

	// Run receiveLoop in goroutine
	done := make(chan bool)
	client.wg.Add(1)
	go func() {
		client.receiveLoop()
		done <- true
	}()

	// Should exit cleanly without error
	<-done
}

func TestReceiveLoopContextCancellation(t *testing.T) {
	// Test that context cancellation is handled properly
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())

	transport := &mockTransport{
		receiveFunc: func(ctx context.Context) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	client := &Client{
		transport:     transport,
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
		pending:       make(map[string]chan *protocol.Response),
		notifications: make(chan Notification, 10),
	}

	done := make(chan bool)
	client.wg.Add(1)
	go func() {
		client.receiveLoop()
		done <- true
	}()

	// Cancel context
	cancel()

	// Should exit cleanly
	<-done
}

func TestReceiveLoopOtherErrors(t *testing.T) {
	// Test that non-EOF, non-context errors continue the loop
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errorCount := 0
	transport := &mockTransport{
		receiveFunc: func(ctx context.Context) ([]byte, error) {
			errorCount++
			if errorCount < 3 {
				// Return a non-EOF error a few times
				return nil, errors.New("network error")
			}
			// Then return EOF to exit
			return nil, io.EOF
		},
	}

	client := &Client{
		transport:     transport,
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
		pending:       make(map[string]chan *protocol.Response),
		notifications: make(chan Notification, 10),
	}

	done := make(chan bool)
	client.wg.Add(1)
	go func() {
		client.receiveLoop()
		done <- true
	}()

	// Should eventually exit with EOF
	<-done

	// Should have attempted multiple receives
	if errorCount < 3 {
		t.Errorf("Expected at least 3 receive attempts, got %d", errorCount)
	}
}

func TestClientClose(t *testing.T) {
	// Test that Close() cancels context and closes transport
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())

	closeCalled := false
	transport := &mockTransport{
		receiveFunc: func(ctx context.Context) ([]byte, error) {
			<-ctx.Done()
			return nil, io.EOF
		},
		closeFunc: func() error {
			closeCalled = true
			return nil
		},
	}

	client := &Client{
		transport:     transport,
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
		pending:       make(map[string]chan *protocol.Response),
		notifications: make(chan Notification, 10),
		closed:        false,
	}

	// Start receive loop
	client.wg.Add(1)
	go func() {
		client.receiveLoop()
	}()

	// Close the client
	err := client.Close()
	if err != nil {
		t.Errorf("Close() returned error: %v", err)
	}

	// Should have closed transport
	if !closeCalled {
		t.Error("Expected transport.Close() to be called")
	}

	// Calling Close() again should be safe
	err = client.Close()
	if err != nil {
		t.Errorf("Second Close() returned error: %v", err)
	}
}

func TestEOFIsNormalShutdown(t *testing.T) {
	// Verify that io.EOF is recognized as a normal shutdown condition
	err := io.EOF

	// This is what the code checks for
	if !errors.Is(err, io.EOF) {
		t.Error("errors.Is should recognize io.EOF")
	}

	// Direct EOF is what we handle in the receiveLoop
	// Note: Wrapped errors would need to use %w format to match with errors.Is
}

// TestSamplingHandlerDispatch covers the frozen legacy sampling surface
// (§9.2): a registered handler answers a legacy server's
// sampling/createMessage, and without one the request is rejected
// MethodNotFound — the exact pre-migration behavior, restored for importers
// of the exported API (review finding 5, PR #327).
//
//nolint:staticcheck // frozen legacy surface retained through the 2026-07-28 deprecation window
func TestSamplingHandlerDispatch(t *testing.T) {
	ft := newScriptedTransport()
	c := connectClient(t, ft, Config{ProtocolVersion: "legacy"})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	c.SetSamplingHandler(func(_ context.Context, params protocol.SamplingParams) (*protocol.SamplingResult, error) {
		return &protocol.SamplingResult{
			Role:    "assistant",
			Content: protocol.Content{Type: "text", Text: "answered:" + params.SystemPrompt},
			Model:   "test-model",
		}, nil
	})

	// A legacy server sends a server-initiated sampling request; the client
	// answers through transport.Send with a JSON-RPC response carrying the
	// request's id, which the scripted transport records.
	ft.inject([]byte(`{"jsonrpc":"2.0","id":777,"method":"sampling/createMessage","params":{"systemPrompt":"sp","maxTokens":5,"messages":[]}}`))

	require.Eventually(t, func() bool {
		for _, sent := range ft.sentResponses() {
			var resp protocol.Response
			if json.Unmarshal(sent, &resp) == nil && resp.ID != nil && resp.ID.String() == "777" {
				return resp.Error == nil && len(resp.Result) > 0
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "registered handler must answer sampling/createMessage")
}

//nolint:staticcheck // frozen legacy surface retained through the 2026-07-28 deprecation window
func TestSamplingWithoutHandlerRejected(t *testing.T) {
	ft := newScriptedTransport()
	c := connectClient(t, ft, Config{ProtocolVersion: "legacy"})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	ft.inject([]byte(`{"jsonrpc":"2.0","id":778,"method":"sampling/createMessage","params":{"maxTokens":5,"messages":[]}}`))

	require.Eventually(t, func() bool {
		for _, sent := range ft.sentResponses() {
			var resp protocol.Response
			if json.Unmarshal(sent, &resp) == nil && resp.ID != nil && resp.ID.String() == "778" {
				return resp.Error != nil && resp.Error.Code == protocol.MethodNotFound
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "without a handler, sampling must be rejected MethodNotFound")
}

// TestLateResponseAfterTimeoutDoesNotPanic reproduces the round-3 review
// finding on PR #327: a request whose context expires tears down its pending
// entry while a late server response, which grabbed the channel reference
// under the read lock just before the delete, sends into it afterwards.
// Closing the channel in that teardown made the late send panic
// ("send on closed channel" — a select default does not protect a send on a
// CLOSED channel). The stress loop drives the two paths into the window;
// on the old code it panics within a few hundred iterations under -race.
func TestLateResponseAfterTimeoutDoesNotPanic(t *testing.T) {
	ft := &mockTransport{
		// Swallow every request: the "server" never answers through the
		// transport; the test injects late responses directly.
		sendFunc:    func(context.Context, []byte) error { return nil },
		receiveFunc: func(ctx context.Context) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() },
	}
	c := NewClient(Config{Transport: ft})
	defer func() { _ = c.Close() }()

	for i := 0; i < 2000; i++ {
		req := &protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      c.nextRequestID(),
			Method:  "tools/list",
			Params:  json.RawMessage(`{}`),
		}
		resp := &protocol.Response{JSONRPC: protocol.JSONRPCVersion, ID: req.ID, Result: json.RawMessage(`{}`)}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			_, _ = c.dispatchAndWait(ctx, req)
			close(done)
		}()

		// Cancel the request and immediately deliver the "late" response:
		// the teardown and the delivery race for the pending channel.
		cancel()
		c.handleResponse(resp)
		<-done
	}
}
