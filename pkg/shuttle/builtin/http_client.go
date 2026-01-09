// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// HTTPClientTool provides HTTP request capabilities for agents.
// Apple-style: It just works with sensible defaults.
type HTTPClientTool struct {
	client *http.Client
}

// NewHTTPClientTool creates a new HTTP client tool with sensible defaults.
func NewHTTPClientTool() *HTTPClientTool {
	return &HTTPClientTool{
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (t *HTTPClientTool) Name() string {
	return "http_request"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/rest_api.yaml).
// This fallback is used only when prompts are not configured.
func (t *HTTPClientTool) Description() string {
	return `Makes HTTP requests to APIs and websites. Supports GET, POST, PUT, DELETE, PATCH methods.
Returns response body, status code, and headers. Automatically handles JSON content.

Use this tool to:
- Fetch data from REST APIs
- Call web services
- Download content from URLs
- Send data to HTTP endpoints`
}

func (t *HTTPClientTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for HTTP request",
		map[string]*shuttle.JSONSchema{
			"url": shuttle.NewStringSchema("The URL to request (required)").
				WithFormat("uri"),
			"method": shuttle.NewStringSchema("HTTP method (default: GET)").
				WithEnum("GET", "POST", "PUT", "DELETE", "PATCH").
				WithDefault("GET"),
			"headers": shuttle.NewObjectSchema(
				"HTTP headers to send (e.g., {\"Content-Type\": \"application/json\"})",
				nil,
				nil,
			),
			"body": shuttle.NewStringSchema("Request body (for POST/PUT/PATCH)"),
			"timeout_seconds": shuttle.NewNumberSchema("Request timeout in seconds (default: 30)").
				WithDefault(30),
		},
		[]string{"url"},
	)
}

func (t *HTTPClientTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Extract parameters with sensible defaults
	url, ok := params["url"].(string)
	if !ok || url == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "url is required",
				Suggestion: "Provide a valid URL (e.g., https://api.example.com/data)",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	method := "GET"
	if m, ok := params["method"].(string); ok {
		method = strings.ToUpper(m)
	}

	// Create request
	var body io.Reader
	if bodyStr, ok := params["body"].(string); ok && bodyStr != "" {
		body = strings.NewReader(bodyStr)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_REQUEST",
				Message:    fmt.Sprintf("Failed to create request: %v", err),
				Suggestion: "Check the URL format and method",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Set headers
	if headers, ok := params["headers"].(map[string]interface{}); ok {
		for key, val := range headers {
			if valStr, ok := val.(string); ok {
				req.Header.Set(key, valStr)
			}
		}
	}

	// Default headers if not set
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Loom-Agent/1.0")
	}

	// Execute request
	resp, err := t.client.Do(req)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "REQUEST_FAILED",
				Message:    fmt.Sprintf("HTTP request failed: %v", err),
				Retryable:  true,
				Suggestion: "Check network connectivity and URL accessibility",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "READ_FAILED",
				Message: fmt.Sprintf("Failed to read response: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Try to parse as JSON (common case)
	var jsonData interface{}
	isJSON := false
	if json.Valid(respBody) {
		json.Unmarshal(respBody, &jsonData)
		isJSON = true
	}

	// Build result
	result := map[string]interface{}{
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"headers":     resp.Header,
	}

	if isJSON {
		result["body"] = jsonData
		result["body_type"] = "json"
	} else {
		result["body"] = string(respBody)
		result["body_type"] = "text"
	}

	success := resp.StatusCode >= 200 && resp.StatusCode < 300

	return &shuttle.Result{
		Success: success,
		Data:    result,
		Metadata: map[string]interface{}{
			"url":         url,
			"method":      method,
			"status_code": resp.StatusCode,
			"is_json":     isJSON,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *HTTPClientTool) Backend() string {
	return "" // Backend-agnostic
}
