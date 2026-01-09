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
package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateToolArguments(t *testing.T) {
	tests := []struct {
		name      string
		tool      Tool
		arguments map[string]interface{}
		wantErr   bool
	}{
		{
			name: "valid arguments",
			tool: Tool{
				Name: "read_file",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{
							"type": "string",
						},
					},
					"required": []interface{}{"path"},
				},
			},
			arguments: map[string]interface{}{
				"path": "/tmp/file.txt",
			},
			wantErr: false,
		},
		{
			name: "missing required field",
			tool: Tool{
				Name: "read_file",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{
							"type": "string",
						},
					},
					"required": []interface{}{"path"},
				},
			},
			arguments: map[string]interface{}{
				"other": "value",
			},
			wantErr: true,
		},
		{
			name: "wrong type",
			tool: Tool{
				Name: "read_file",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"count": map[string]interface{}{
							"type": "number",
						},
					},
				},
			},
			arguments: map[string]interface{}{
				"count": "not a number",
			},
			wantErr: true,
		},
		{
			name: "no schema - always valid",
			tool: Tool{
				Name:        "any_tool",
				InputSchema: nil,
			},
			arguments: map[string]interface{}{
				"anything": "goes",
			},
			wantErr: false,
		},
		{
			name: "empty schema - always valid",
			tool: Tool{
				Name:        "any_tool",
				InputSchema: map[string]interface{}{},
			},
			arguments: map[string]interface{}{
				"anything": "goes",
			},
			wantErr: false,
		},
		{
			name: "complex nested schema",
			tool: Tool{
				Name: "complex_tool",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"config": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"enabled": map[string]interface{}{
									"type": "boolean",
								},
								"count": map[string]interface{}{
									"type":    "integer",
									"minimum": float64(1),
									"maximum": float64(100),
								},
							},
							"required": []interface{}{"enabled"},
						},
					},
					"required": []interface{}{"config"},
				},
			},
			arguments: map[string]interface{}{
				"config": map[string]interface{}{
					"enabled": true,
					"count":   50,
				},
			},
			wantErr: false,
		},
		{
			name: "complex nested schema - out of range",
			tool: Tool{
				Name: "complex_tool",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"count": map[string]interface{}{
							"type":    "integer",
							"minimum": float64(1),
							"maximum": float64(100),
						},
					},
				},
			},
			arguments: map[string]interface{}{
				"count": 150, // Out of range
			},
			wantErr: true,
		},
		{
			name: "array validation",
			tool: Tool{
				Name: "array_tool",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"items": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type": "string",
							},
						},
					},
				},
			},
			arguments: map[string]interface{}{
				"items": []interface{}{"a", "b", "c"},
			},
			wantErr: false,
		},
		{
			name: "array validation - wrong item type",
			tool: Tool{
				Name: "array_tool",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"items": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type": "string",
							},
						},
					},
				},
			},
			arguments: map[string]interface{}{
				"items": []interface{}{"a", 123, "c"}, // 123 is not a string
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolArguments(tt.tool, tt.arguments)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     *Request
		wantErr bool
	}{
		{
			name: "valid request",
			req: &Request{
				JSONRPC: JSONRPCVersion,
				ID:      NewStringRequestID("test-1"),
				Method:  "initialize",
				Params:  json.RawMessage(`{}`),
			},
			wantErr: false,
		},
		{
			name: "valid notification (no ID)",
			req: &Request{
				JSONRPC: JSONRPCVersion,
				Method:  "notifications/initialized",
			},
			wantErr: false,
		},
		{
			name: "invalid jsonrpc version",
			req: &Request{
				JSONRPC: "1.0",
				ID:      NewStringRequestID("test-1"),
				Method:  "initialize",
			},
			wantErr: true,
		},
		{
			name: "missing method",
			req: &Request{
				JSONRPC: JSONRPCVersion,
				ID:      NewStringRequestID("test-1"),
				Method:  "",
			},
			wantErr: true,
		},
		{
			name: "empty method",
			req: &Request{
				JSONRPC: JSONRPCVersion,
				ID:      NewStringRequestID("test-1"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRequest(tt.req)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateResponse(t *testing.T) {
	tests := []struct {
		name    string
		resp    *Response
		wantErr bool
	}{
		{
			name: "valid success response",
			resp: &Response{
				JSONRPC: JSONRPCVersion,
				ID:      NewStringRequestID("test-1"),
				Result:  json.RawMessage(`{"success": true}`),
			},
			wantErr: false,
		},
		{
			name: "valid error response",
			resp: &Response{
				JSONRPC: JSONRPCVersion,
				ID:      NewNumericRequestID(1),
				Error: &Error{
					Code:    InternalError,
					Message: "Internal error",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid jsonrpc version",
			resp: &Response{
				JSONRPC: "1.0",
				ID:      NewStringRequestID("test-1"),
				Result:  json.RawMessage(`{}`),
			},
			wantErr: true,
		},
		{
			name: "missing ID",
			resp: &Response{
				JSONRPC: JSONRPCVersion,
				Result:  json.RawMessage(`{}`),
			},
			wantErr: true,
		},
		{
			name: "both result and error",
			resp: &Response{
				JSONRPC: JSONRPCVersion,
				ID:      NewStringRequestID("test-1"),
				Result:  json.RawMessage(`{}`),
				Error: &Error{
					Code:    InternalError,
					Message: "Error",
				},
			},
			wantErr: true,
		},
		{
			name: "neither result nor error",
			resp: &Response{
				JSONRPC: JSONRPCVersion,
				ID:      NewStringRequestID("test-1"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateResponse(tt.resp)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateToolArguments_RealWorldSchemas(t *testing.T) {
	// Test with a real-world filesystem tool schema
	filesystemTool := Tool{
		Name:        "read_file",
		Description: "Read a file from the filesystem",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Path to the file",
					"minLength":   float64(1),
				},
				"encoding": map[string]interface{}{
					"type":        "string",
					"description": "File encoding",
					"enum":        []interface{}{"utf-8", "ascii", "base64"},
					"default":     "utf-8",
				},
			},
			"required": []interface{}{"path"},
		},
	}

	tests := []struct {
		name      string
		arguments map[string]interface{}
		wantErr   bool
	}{
		{
			name: "valid with required only",
			arguments: map[string]interface{}{
				"path": "/tmp/file.txt",
			},
			wantErr: false,
		},
		{
			name: "valid with optional",
			arguments: map[string]interface{}{
				"path":     "/tmp/file.txt",
				"encoding": "utf-8",
			},
			wantErr: false,
		},
		{
			name: "invalid encoding enum",
			arguments: map[string]interface{}{
				"path":     "/tmp/file.txt",
				"encoding": "invalid",
			},
			wantErr: true,
		},
		{
			name: "empty path",
			arguments: map[string]interface{}{
				"path": "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolArguments(filesystemTool, tt.arguments)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateRequest_ErrorMessages(t *testing.T) {
	// Test that error messages are descriptive
	req := &Request{
		JSONRPC: "1.0",
		Method:  "test",
	}

	err := ValidateRequest(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid jsonrpc version")
	assert.Contains(t, err.Error(), "1.0")
	assert.Contains(t, err.Error(), "2.0")
}

func TestValidateResponse_ErrorMessages(t *testing.T) {
	// Test that error messages are descriptive
	resp := &Response{
		JSONRPC: JSONRPCVersion,
		ID:      NewStringRequestID("test-1"),
		// Both result and error present
		Result: json.RawMessage(`{}`),
		Error: &Error{
			Code:    -1,
			Message: "error",
		},
	}

	err := ValidateResponse(resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}
