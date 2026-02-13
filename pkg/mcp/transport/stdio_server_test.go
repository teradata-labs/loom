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

package transport

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStdioServerTransport_SendReceive(t *testing.T) {
	// Use pipes to simulate stdin/stdout
	clientToServer := strings.NewReader(`{"jsonrpc":"2.0","method":"ping","id":1}` + "\n")
	var serverToClient bytes.Buffer

	transport := NewStdioServerTransport(clientToServer, &serverToClient)

	// Receive message from "stdin"
	msg, err := transport.Receive(context.Background())
	require.NoError(t, err)
	assert.Contains(t, string(msg), `"method":"ping"`)

	// Send response to "stdout"
	resp := []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	err = transport.Send(context.Background(), resp)
	require.NoError(t, err)

	assert.Equal(t, `{"jsonrpc":"2.0","id":1,"result":{}}`+"\n", serverToClient.String())
}

func TestStdioServerTransport_ReceiveEOF(t *testing.T) {
	emptyReader := strings.NewReader("")
	var buf bytes.Buffer

	transport := NewStdioServerTransport(emptyReader, &buf)

	_, err := transport.Receive(context.Background())
	assert.ErrorIs(t, err, io.EOF)
}

func TestStdioServerTransport_ReceiveContextCancelled(t *testing.T) {
	// Use a pipe reader that blocks
	pr, pw := io.Pipe()
	defer pw.Close()
	var buf bytes.Buffer

	transport := NewStdioServerTransport(pr, &buf)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := transport.Receive(ctx)
	assert.Error(t, err)

	// Close the pipe to unblock the persistent reader goroutine so it
	// can exit cleanly. Without this, the goroutine would remain blocked
	// on ReadBytes for the duration of the test.
	pw.Close()
}

func TestStdioServerTransport_NoGoroutineLeak(t *testing.T) {
	// Verify that cancelling Receive does not leak goroutines.
	// The persistent reader design means only one goroutine exists
	// per transport regardless of how many Receive calls are cancelled.

	// Let runtime settle before measuring baseline.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	pr, pw := io.Pipe()

	var buf bytes.Buffer
	transport := NewStdioServerTransport(pr, &buf)

	// Cancel Receive many times. Under the old design, each cancellation
	// would leak one goroutine.
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := transport.Receive(ctx)
		require.Error(t, err)
	}

	// Close the pipe so the persistent reader goroutine exits.
	pw.Close()

	// Give the reader goroutine time to drain and exit.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()

	current := runtime.NumGoroutine()
	// Allow a small margin (2) for background runtime goroutines that may
	// come and go, but 50 leaked goroutines would be obvious.
	assert.LessOrEqual(t, current, baseline+2,
		"goroutine count should not grow significantly after cancelled Receive calls; "+
			"baseline=%d current=%d", baseline, current)
}

func TestStdioServerTransport_ReceiveMultipleMessages(t *testing.T) {
	input := `{"method":"initialize"}` + "\n" + `{"method":"ping"}` + "\n"
	reader := strings.NewReader(input)
	var buf bytes.Buffer

	transport := NewStdioServerTransport(reader, &buf)

	msg1, err := transport.Receive(context.Background())
	require.NoError(t, err)
	assert.Contains(t, string(msg1), "initialize")

	msg2, err := transport.Receive(context.Background())
	require.NoError(t, err)
	assert.Contains(t, string(msg2), "ping")
}

func TestStdioServerTransport_ReceiveSkipsEmptyLines(t *testing.T) {
	input := "\n\n" + `{"method":"ping"}` + "\n"
	reader := strings.NewReader(input)
	var buf bytes.Buffer

	transport := NewStdioServerTransport(reader, &buf)

	msg, err := transport.Receive(context.Background())
	require.NoError(t, err)
	assert.Contains(t, string(msg), "ping")
}

func TestStdioServerTransport_ReceiveTrimsNewlines(t *testing.T) {
	// Test with \r\n (Windows-style)
	input := `{"method":"ping"}` + "\r\n"
	reader := strings.NewReader(input)
	var buf bytes.Buffer

	transport := NewStdioServerTransport(reader, &buf)

	msg, err := transport.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, `{"method":"ping"}`, string(msg))
}

func TestStdioServerTransport_Close(t *testing.T) {
	reader := strings.NewReader("")
	var buf bytes.Buffer

	transport := NewStdioServerTransport(reader, &buf)

	err := transport.Close()
	require.NoError(t, err)

	// Send after close should fail
	err = transport.Send(context.Background(), []byte("test"))
	assert.Error(t, err)
}

func TestStdioServerTransport_ConcurrentSends(t *testing.T) {
	reader := strings.NewReader("")
	var buf bytes.Buffer

	transport := NewStdioServerTransport(reader, &buf)

	// Concurrent sends should not race
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			_ = transport.Send(context.Background(), []byte(`{"id":`+string(rune('0'+i))+`}`))
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// All messages should have been written with newlines
	output := buf.String()
	assert.Equal(t, 10, strings.Count(output, "\n"))
}

func TestStdioServerTransport_PipeBased(t *testing.T) {
	// Full pipe-based test simulating real stdio
	pr, pw := io.Pipe()
	var output bytes.Buffer

	transport := NewStdioServerTransport(pr, &output)

	// Writer goroutine (simulates client sending)
	go func() {
		_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","method":"initialize","id":1}` + "\n"))
		_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","method":"ping","id":2}` + "\n"))
		pw.Close()
	}()

	// Read first message
	msg1, err := transport.Receive(context.Background())
	require.NoError(t, err)
	assert.Contains(t, string(msg1), "initialize")

	// Send response
	err = transport.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	require.NoError(t, err)

	// Read second message
	msg2, err := transport.Receive(context.Background())
	require.NoError(t, err)
	assert.Contains(t, string(msg2), "ping")

	// Read EOF
	_, err = transport.Receive(context.Background())
	assert.ErrorIs(t, err, io.EOF)
}
