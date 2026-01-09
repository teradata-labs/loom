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
	"errors"
	"io"
	"testing"

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
