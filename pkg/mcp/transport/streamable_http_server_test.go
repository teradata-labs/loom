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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
)

func newTestServer(t *testing.T) *StreamableHTTPServer {
	logger := zaptest.NewLogger(t)
	server, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: func(msg []byte) ([]byte, error) {
			var req struct {
				JSONRPC string           `json:"jsonrpc"`
				ID      *json.RawMessage `json:"id"`
				Method  string           `json:"method"`
			}
			if err := json.Unmarshal(msg, &req); err != nil {
				return nil, err
			}

			// Notifications (no ID) return nil
			if req.ID == nil {
				return nil, nil
			}

			// Build response based on method
			var result interface{}
			switch req.Method {
			case "initialize":
				result = map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
					"serverInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
				}
			case "ping":
				result = map[string]interface{}{}
			default:
				result = map[string]interface{}{"status": "ok"}
			}

			resultBytes, _ := json.Marshal(result)
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result":  json.RawMessage(resultBytes),
			}
			return json.Marshal(resp)
		},
		Logger: logger,
	})
	require.NoError(t, err)
	return server
}

func TestStreamableHTTPServer_Initialize(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Should have a session ID
	sessionID := resp.Header.Get("Mcp-Session-Id")
	assert.NotEmpty(t, sessionID)

	// Should have JSON response
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(respBody), "protocolVersion")

	// Server should track the session
	assert.Equal(t, 1, srv.SessionCount())
}

func TestStreamableHTTPServer_Ping(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// First initialize to get a session
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	initResp, err := http.Post(ts.URL, "application/json", strings.NewReader(initBody))
	require.NoError(t, err)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	initResp.Body.Close()

	// Now ping with session ID
	body := `{"jsonrpc":"2.0","id":2,"method":"ping"}`
	req, err := http.NewRequest("POST", ts.URL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestStreamableHTTPServer_Notification(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Notification has no ID
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func TestStreamableHTTPServer_InvalidSession(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	req, err := http.NewRequest("POST", ts.URL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", "nonexistent-session")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestStreamableHTTPServer_DeleteSession(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create session via initialize
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	initResp, err := http.Post(ts.URL, "application/json", strings.NewReader(initBody))
	require.NoError(t, err)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	initResp.Body.Close()
	assert.Equal(t, 1, srv.SessionCount())

	// Delete session
	req, err := http.NewRequest("DELETE", ts.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 0, srv.SessionCount())
}

func TestStreamableHTTPServer_DeleteSession_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest("DELETE", ts.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", "nonexistent")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestStreamableHTTPServer_DeleteSession_NoHeader(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest("DELETE", ts.URL, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestStreamableHTTPServer_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest("PUT", ts.URL, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestStreamableHTTPServer_EmptyBody(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(""))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestStreamableHTTPServer_WrongContentType(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Post(ts.URL, "text/plain", strings.NewReader("not json"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)
}

func TestNewStreamableHTTPServer_NilHandler(t *testing.T) {
	_, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: nil,
	})
	assert.Error(t, err)
}

func TestStreamableHTTPServer_ConcurrentRequests(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create a session first
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	initResp, err := http.Post(ts.URL, "application/json", strings.NewReader(initBody))
	require.NoError(t, err)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	initResp.Body.Close()

	// Fire concurrent requests
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()

			body := `{"jsonrpc":"2.0","id":` + string(rune('0'+i%10)) + `,"method":"ping"}`
			req, _ := http.NewRequest("POST", ts.URL, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Mcp-Session-Id", sessionID)

			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}(i)
	}

	for i := 0; i < 20; i++ {
		<-done
	}

	// Session should still be valid
	assert.Equal(t, 1, srv.SessionCount())
}

func TestStreamableHTTPServer_SessionTTLExpiry(t *testing.T) {
	logger := zaptest.NewLogger(t)
	// Use a very short TTL so the cleanup goroutine fires quickly.
	srv, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: func(msg []byte) ([]byte, error) {
			var req struct {
				JSONRPC string           `json:"jsonrpc"`
				ID      *json.RawMessage `json:"id"`
				Method  string           `json:"method"`
			}
			if err := json.Unmarshal(msg, &req); err != nil {
				return nil, err
			}
			if req.ID == nil {
				return nil, nil
			}
			result := map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
			}
			resultBytes, _ := json.Marshal(result)
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result":  json.RawMessage(resultBytes),
			}
			return json.Marshal(resp)
		},
		Logger:     logger,
		SessionTTL: 2 * time.Second, // Short TTL for test; cleanup interval = 1s
	})
	require.NoError(t, err)
	defer srv.Close()

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create a session via initialize
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	initResp, err := http.Post(ts.URL, "application/json", strings.NewReader(initBody))
	require.NoError(t, err)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	initResp.Body.Close()
	require.NotEmpty(t, sessionID)
	assert.Equal(t, 1, srv.SessionCount())

	// Wait for the session to expire (TTL=2s, cleanup interval=1s, so wait 3s to be safe)
	assert.Eventually(t, func() bool {
		return srv.SessionCount() == 0
	}, 5*time.Second, 200*time.Millisecond, "session should be cleaned up after TTL expires")
}

func TestStreamableHTTPServer_SessionTTLRenewedByActivity(t *testing.T) {
	logger := zaptest.NewLogger(t)
	srv, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: func(msg []byte) ([]byte, error) {
			var req struct {
				JSONRPC string           `json:"jsonrpc"`
				ID      *json.RawMessage `json:"id"`
				Method  string           `json:"method"`
			}
			if err := json.Unmarshal(msg, &req); err != nil {
				return nil, err
			}
			if req.ID == nil {
				return nil, nil
			}
			var result interface{}
			switch req.Method {
			case "initialize":
				result = map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
					"serverInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
				}
			default:
				result = map[string]interface{}{}
			}
			resultBytes, _ := json.Marshal(result)
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result":  json.RawMessage(resultBytes),
			}
			return json.Marshal(resp)
		},
		Logger:     logger,
		SessionTTL: 3 * time.Second,
	})
	require.NoError(t, err)
	defer srv.Close()

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create session
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	initResp, err := http.Post(ts.URL, "application/json", strings.NewReader(initBody))
	require.NoError(t, err)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	initResp.Body.Close()
	require.NotEmpty(t, sessionID)

	// Keep the session alive by pinging every second for 5 seconds.
	// With a 3s TTL, the session would expire without activity.
	for i := 0; i < 5; i++ {
		time.Sleep(1 * time.Second)

		body := `{"jsonrpc":"2.0","id":2,"method":"ping"}`
		req, err := http.NewRequest("POST", ts.URL, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Session-Id", sessionID)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "session should still be alive at iteration %d", i)
	}

	// Session should still be alive because we kept touching it
	assert.Equal(t, 1, srv.SessionCount())
}

func TestStreamableHTTPServer_Close(t *testing.T) {
	logger := zaptest.NewLogger(t)
	srv, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: func(msg []byte) ([]byte, error) {
			return nil, nil
		},
		Logger:     logger,
		SessionTTL: 1 * time.Minute,
	})
	require.NoError(t, err)

	// Close should not panic and should be idempotent
	srv.Close()
	srv.Close() // second call should be safe
}

func TestStreamableHTTPServer_CloseStopsCleanup(t *testing.T) {
	logger := zaptest.NewLogger(t)
	srv, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: func(msg []byte) ([]byte, error) {
			var req struct {
				JSONRPC string           `json:"jsonrpc"`
				ID      *json.RawMessage `json:"id"`
				Method  string           `json:"method"`
			}
			if err := json.Unmarshal(msg, &req); err != nil {
				return nil, err
			}
			if req.ID == nil {
				return nil, nil
			}
			result := map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
			}
			resultBytes, _ := json.Marshal(result)
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result":  json.RawMessage(resultBytes),
			}
			return json.Marshal(resp)
		},
		Logger:     logger,
		SessionTTL: 2 * time.Second,
	})
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create a session
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	initResp, err := http.Post(ts.URL, "application/json", strings.NewReader(initBody))
	require.NoError(t, err)
	initResp.Body.Close()
	assert.Equal(t, 1, srv.SessionCount())

	// Stop cleanup before the session expires
	srv.Close()

	// Wait longer than the TTL -- since cleanup is stopped, session should persist
	time.Sleep(3 * time.Second)
	assert.Equal(t, 1, srv.SessionCount(), "session should not be cleaned up after Close()")
}

func TestStreamableHTTPServer_NoCleanupWhenTTLZero(t *testing.T) {
	// When TTL is 0 (default), no cleanup goroutine should start,
	// and sessions should persist indefinitely.
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create a session
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	initResp, err := http.Post(ts.URL, "application/json", strings.NewReader(initBody))
	require.NoError(t, err)
	initResp.Body.Close()
	assert.Equal(t, 1, srv.SessionCount())

	// Wait a bit -- session should still be there
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, 1, srv.SessionCount())

	// Close should be safe even when no cleanup goroutine was started
	srv.Close()
}

func TestStreamableHTTPServer_ExpireSessionsDirect(t *testing.T) {
	// Test the expireSessions method directly for deterministic behavior.
	logger := zaptest.NewLogger(t)
	srv, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: func(msg []byte) ([]byte, error) {
			return nil, nil
		},
		Logger:     logger,
		SessionTTL: 5 * time.Minute,
	})
	require.NoError(t, err)
	defer srv.Close()

	// Manually insert sessions with controlled lastActivity times
	now := time.Now()
	srv.mu.Lock()
	srv.sessions["fresh"] = &httpSession{
		id:           "fresh",
		lastActivity: now,
	}
	srv.sessions["stale"] = &httpSession{
		id:           "stale",
		lastActivity: now.Add(-10 * time.Minute), // 10 minutes ago, well past TTL
	}
	srv.sessions["borderline"] = &httpSession{
		id:           "borderline",
		lastActivity: now.Add(-4 * time.Minute), // 4 minutes ago, within TTL
	}
	srv.mu.Unlock()

	assert.Equal(t, 3, srv.SessionCount())

	// Run expiry check at "now"
	srv.expireSessions(now)

	// Only "stale" should be removed
	assert.Equal(t, 2, srv.SessionCount())

	srv.mu.RLock()
	_, hasFresh := srv.sessions["fresh"]
	_, hasStale := srv.sessions["stale"]
	_, hasBorderline := srv.sessions["borderline"]
	srv.mu.RUnlock()

	assert.True(t, hasFresh, "fresh session should still exist")
	assert.False(t, hasStale, "stale session should be expired")
	assert.True(t, hasBorderline, "borderline session should still exist")
}

func TestStreamableHTTPServer_ConcurrentWithCleanup(t *testing.T) {
	// Verify no race conditions between request handling and cleanup goroutine.
	logger := zaptest.NewLogger(t)
	srv, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: func(msg []byte) ([]byte, error) {
			var req struct {
				JSONRPC string           `json:"jsonrpc"`
				ID      *json.RawMessage `json:"id"`
				Method  string           `json:"method"`
			}
			if err := json.Unmarshal(msg, &req); err != nil {
				return nil, err
			}
			if req.ID == nil {
				return nil, nil
			}
			var result interface{}
			switch req.Method {
			case "initialize":
				result = map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
					"serverInfo":      map[string]interface{}{"name": "test", "version": "1.0"},
				}
			default:
				result = map[string]interface{}{}
			}
			resultBytes, _ := json.Marshal(result)
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result":  json.RawMessage(resultBytes),
			}
			return json.Marshal(resp)
		},
		Logger:     logger,
		SessionTTL: 10 * time.Second, // Long enough that sessions survive the test
	})
	require.NoError(t, err)
	defer srv.Close()

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create a session
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	initResp, err := http.Post(ts.URL, "application/json", strings.NewReader(initBody))
	require.NoError(t, err)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	initResp.Body.Close()

	// Fire concurrent requests while cleanup goroutine is running
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := `{"jsonrpc":"2.0","id":2,"method":"ping"}`
			req, _ := http.NewRequest("POST", ts.URL, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Mcp-Session-Id", sessionID)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, 1, srv.SessionCount())
}

func TestStreamableHTTPServer_DefaultSessionTTLConstant(t *testing.T) {
	// Verify the exported constant has the expected value.
	assert.Equal(t, 30*time.Minute, DefaultSessionTTL)
}

func TestWarnIfNotLocalhost(t *testing.T) {
	tests := []struct {
		name       string
		addr       string
		expectWarn bool
	}{
		{"localhost:8080", "127.0.0.1:8080", false},
		{"localhost no port", "127.0.0.1", false},
		{"ipv6 localhost", "[::1]:8080", false},
		{"localhost name", "localhost:8080", false},
		{"all interfaces", "0.0.0.0:8080", true},
		{"empty host (all)", ":8080", true},
		{"ipv6 all", "[::]:8080", true},
		{"external IP", "192.168.1.100:8080", true},
		{"public IP", "10.0.0.1:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			logger := zap.New(core)

			WarnIfNotLocalhost(logger, tt.addr)

			if tt.expectWarn {
				assert.GreaterOrEqual(t, logs.Len(), 1, "expected a warning log for addr=%s", tt.addr)
			} else {
				assert.Equal(t, 0, logs.Len(), "expected no warning for addr=%s", tt.addr)
			}
		})
	}
}

func TestWarnIfNotLocalhost_NilLogger(t *testing.T) {
	// Should not panic.
	WarnIfNotLocalhost(nil, "0.0.0.0:8080")
}
