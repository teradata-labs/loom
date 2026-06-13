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

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// startSpinner shows a subtle "weaving…" activity indicator on stderr while the
// agent does its pre-generation work (memory, skills, planning) before the
// first token arrives. It returns a stop function that is idempotent and
// erases the indicator line. When stderr is not a terminal (piped/redirected),
// it is a no-op so logs and captured output stay clean.
func startSpinner() (stop func()) {
	if fi, _ := os.Stderr.Stat(); fi == nil || fi.Mode()&os.ModeCharDevice == 0 {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		frames := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
		start := time.Now()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		i := 0
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fmt.Fprintf(os.Stderr, "\r\033[2K%c weaving… %.0fs", frames[i%len(frames)], time.Since(start).Seconds())
				i++
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(done)
			fmt.Fprint(os.Stderr, "\r\033[2K") // erase the spinner line
		})
	}
}

// chatRemoteURL enables remote-MCP chat: instead of gRPC to a local looms,
// messages go to a deployed loom-mcp Streamable-HTTP edge (e.g. on Fly) as
// loom_weave tool calls, authenticated with the stored `loom login` token.
var chatRemoteURL string

func init() {
	chatCmd.Flags().StringVar(&chatRemoteURL, "remote", "",
		"Chat via a remote HTTP-MCP endpoint (e.g. https://loom-dreambase.fly.dev/) instead of gRPC; uses the 'loom login' token")
}

// remoteChat drives an interactive (or one-shot) conversation against a
// remote loom-mcp Streamable-HTTP endpoint. Each turn is a loom_weave
// tools/call with SSE progress streamed live; session_id from the first
// response is threaded into later turns for conversation continuity.
func remoteChat(baseURL, message string) error {
	token, err := loomBearerToken()
	if err != nil {
		return fmt.Errorf("load login token: %w", err)
	}
	if token == "" {
		return fmt.Errorf("not signed in: run 'loom login' first (the remote endpoint requires a bearer token)")
	}

	rc := &remoteClient{baseURL: strings.TrimRight(baseURL, "/") + "/", token: token,
		hc: &http.Client{}}
	if err := rc.initialize(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Connected to %s (MCP session %s)\n", baseURL, rc.mcpSession)

	turn := func(q string) error {
		// Render the agent's answer as it streams. Each message is the
		// cumulative partial text for the current LLM call; print the new
		// suffix live. A message that doesn't extend the current segment marks
		// a new segment (e.g. the response resuming after a tool call) — start
		// it on a fresh line.
		var shown string
		streamed := false

		// Activity indicator for the pre-token wait (the agent does memory,
		// skill, and planning work before generation begins). Cleared the
		// instant the first token arrives. Not shown when stdout is piped.
		stopSpinner := startSpinner()

		onText := func(cumulative string) {
			if !streamed {
				stopSpinner()
			}
			streamed = true
			switch {
			case shown == "":
				fmt.Print(cumulative)
			case strings.HasPrefix(cumulative, shown):
				fmt.Print(cumulative[len(shown):])
			default:
				fmt.Print("\n" + cumulative)
			}
			shown = cumulative
		}

		answer, err := rc.weave(q, onText)
		stopSpinner() // idempotent: covers error / sync-fallback paths where no token arrived
		if err != nil {
			if streamed {
				fmt.Println()
			}
			return err
		}
		switch {
		case !streamed:
			// Synchronous fallback (server didn't stream): print the answer.
			fmt.Printf("\n%s\n", answer)
		case answer != "" && answer != shown:
			// Final differs from what streamed (e.g. multi-segment); show the
			// authoritative final answer on its own.
			fmt.Printf("\n\n%s\n", answer)
		default:
			fmt.Println()
		}
		return nil
	}

	if message != "" {
		return turn(message)
	}

	// Interactive REPL on a TTY; otherwise read stdin wholesale as one message.
	if fi, _ := os.Stdin.Stat(); fi != nil && fi.Mode()&os.ModeCharDevice == 0 {
		data, _ := io.ReadAll(os.Stdin)
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			return fmt.Errorf("message cannot be empty")
		}
		return turn(msg)
	}

	fmt.Fprintln(os.Stderr, `Type a message and press Enter ("exit" or Ctrl-D to quit).`)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Fprint(os.Stderr, "\nyou> ")
		if !in.Scan() {
			fmt.Fprintln(os.Stderr)
			return in.Err()
		}
		q := strings.TrimSpace(in.Text())
		switch q {
		case "":
			continue
		case "exit", "quit":
			return nil
		}
		if err := turn(q); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
}

// remoteClient is a minimal Streamable-HTTP MCP client for the chat path. It
// deliberately streams the POST response body line-by-line so progress
// notifications render live (the shared pkg/mcp transport buffers the whole
// SSE stream before parsing, which would defeat live progress).
type remoteClient struct {
	baseURL      string
	token        string
	hc           *http.Client
	mcpSession   string // Mcp-Session-Id header value
	weaveSession string // loom session_id threaded across turns
	reqID        int
}

func (rc *remoteClient) post(body []byte, accept string) (*http.Response, error) {
	// chat --timeout bounds each turn end-to-end (default 5m; SSE weaves can
	// legitimately run for minutes). The cancel is owned by the response body:
	// callers must Close() it, which releases the context resources too.
	ctx, cancel := context.WithTimeout(context.Background(), chatTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rc.baseURL, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+rc.token)
	if rc.mcpSession != "" {
		req.Header.Set("Mcp-Session-Id", rc.mcpSession)
	}
	resp, err := rc.hc.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	// Tie context cleanup to body Close so SSE reads stay live after return.
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("endpoint rejected the token (401): run 'loom login' to refresh your session")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return resp, nil
}

// cancelOnClose releases the request context when the response body closes.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func (rc *remoteClient) initialize() error {
	rc.reqID++
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": rc.reqID, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "loom-chat-remote", "version": "1"},
		},
	})
	resp, err := rc.post(body, "application/json")
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rc.mcpSession = resp.Header.Get("Mcp-Session-Id")
	var out struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("initialize: decode: %w", err)
	}
	if out.Error != nil {
		return fmt.Errorf("initialize: %s", out.Error.Message)
	}
	return nil
}

// weave runs one loom_weave call, invoking onText with the agent's cumulative
// answer text for each streamed event, and returns the final answer text.
func (rc *remoteClient) weave(query string, onText func(string)) (string, error) {
	rc.reqID++
	args := map[string]any{"query": query}
	if rc.weaveSession != "" {
		args["session_id"] = rc.weaveSession
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": rc.reqID, "method": "tools/call",
		"params": map[string]any{
			"name":      "loom_weave",
			"arguments": args,
			"_meta":     map[string]any{"progressToken": fmt.Sprintf("chat-%d", rc.reqID)},
		},
	})
	resp, err := rc.post(body, "text/event-stream")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// Synchronous JSON fallback (e.g. server without a stream handler).
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		return rc.consumeFinal(data)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var msg struct {
			Method string `json:"method"`
			Params struct {
				Message string `json:"message"`
			} `json:"params"`
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			continue
		}
		if msg.Method == "notifications/progress" {
			// The server forwards the agent's cumulative answer text in the
			// message field; bare-progress events (no message) carry only the
			// monotonic counter and are not rendered as text.
			if onText != nil && msg.Params.Message != "" {
				onText(msg.Params.Message)
			}
			continue
		}
		if len(msg.ID) > 0 {
			return rc.consumeFinal([]byte(payload))
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("stream read: %w", err)
	}
	return "", fmt.Errorf("stream ended without a final response")
}

// consumeFinal extracts the answer text from a tools/call response and, when a
// content block carries the protojson WeaveResponse, captures sessionId so the
// next turn continues the same loom session.
func (rc *remoteClient) consumeFinal(data []byte) (string, error) {
	var out struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("decode final response: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("server error: %s", out.Error.Message)
	}
	var answer string
	for _, c := range out.Result.Content {
		if c.Type != "text" {
			continue
		}
		// A content block may be the protojson WeaveResponse; harvest sessionId.
		var meta struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal([]byte(c.Text), &meta) == nil && meta.SessionID != "" {
			rc.weaveSession = meta.SessionID
			continue
		}
		if answer == "" {
			answer = c.Text
		}
	}
	if out.Result.IsError {
		return "", fmt.Errorf("tool error: %s", answer)
	}
	if answer == "" {
		return "", fmt.Errorf("response contained no text content")
	}
	return answer, nil
}
