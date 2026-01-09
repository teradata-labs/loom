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

func TestRequestID_MarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		id       *RequestID
		expected string
	}{
		{
			name:     "string ID",
			id:       NewStringRequestID("test-123"),
			expected: `"test-123"`,
		},
		{
			name:     "number ID",
			id:       NewNumericRequestID(42),
			expected: `42`,
		},
		{
			name:     "nil ID",
			id:       nil,
			expected: `null`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.id)
			require.NoError(t, err)
			assert.JSONEq(t, tt.expected, string(data))
		})
	}
}

func TestRequestID_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantStr *string
		wantNum *int64
		wantErr bool
	}{
		{
			name:    "string ID",
			input:   `"test-123"`,
			wantStr: stringPtr("test-123"),
			wantNum: nil,
		},
		{
			name:    "number ID",
			input:   `42`,
			wantStr: nil,
			wantNum: int64Ptr(42),
		},
		{
			name:    "null ID",
			input:   `null`,
			wantStr: stringPtr(""), // JSON null unmarshals to empty string
			wantNum: nil,
		},
		{
			name:    "invalid type",
			input:   `true`,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			input:   `{invalid}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var id RequestID
			err := json.Unmarshal([]byte(tt.input), &id)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Compare values, not pointers
			if tt.wantStr != nil {
				require.NotNil(t, id.Str)
				assert.Equal(t, *tt.wantStr, *id.Str)
			} else {
				assert.Nil(t, id.Str)
			}

			if tt.wantNum != nil {
				require.NotNil(t, id.Num)
				assert.Equal(t, *tt.wantNum, *id.Num)
			} else {
				assert.Nil(t, id.Num)
			}
		})
	}
}

func TestRequest_MarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		request  *Request
		expected string
	}{
		{
			name: "request with string ID",
			request: &Request{
				JSONRPC: JSONRPCVersion,
				ID:      NewStringRequestID("req-1"),
				Method:  "initialize",
				Params:  json.RawMessage(`{"protocolVersion":"2024-11-05"}`),
			},
			expected: `{
				"jsonrpc": "2.0",
				"id": "req-1",
				"method": "initialize",
				"params": {"protocolVersion":"2024-11-05"}
			}`,
		},
		{
			name: "request with number ID",
			request: &Request{
				JSONRPC: JSONRPCVersion,
				ID:      NewNumericRequestID(1),
				Method:  "tools/list",
				Params:  json.RawMessage(`{}`),
			},
			expected: `{
				"jsonrpc": "2.0",
				"id": 1,
				"method": "tools/list",
				"params": {}
			}`,
		},
		{
			name: "notification (no ID)",
			request: &Request{
				JSONRPC: JSONRPCVersion,
				Method:  "notifications/initialized",
			},
			expected: `{
				"jsonrpc": "2.0",
				"method": "notifications/initialized"
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.request)
			require.NoError(t, err)
			assert.JSONEq(t, tt.expected, string(data))
		})
	}
}

func TestResponse_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantResp *Response
		wantErr  bool
	}{
		{
			name: "success response",
			input: `{
				"jsonrpc": "2.0",
				"id": "req-1",
				"result": {"tools": []}
			}`,
			wantResp: &Response{
				JSONRPC: JSONRPCVersion,
				ID:      NewStringRequestID("req-1"),
				Result:  json.RawMessage(`{"tools": []}`),
			},
		},
		{
			name: "error response",
			input: `{
				"jsonrpc": "2.0",
				"id": 1,
				"error": {
					"code": -32600,
					"message": "Invalid Request"
				}
			}`,
			wantResp: &Response{
				JSONRPC: JSONRPCVersion,
				ID:      NewNumericRequestID(1),
				Error: &Error{
					Code:    InvalidRequest,
					Message: "Invalid Request",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp Response
			err := json.Unmarshal([]byte(tt.input), &resp)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantResp.JSONRPC, resp.JSONRPC)
			assert.Equal(t, tt.wantResp.ID, resp.ID)

			if tt.wantResp.Result != nil {
				assert.JSONEq(t, string(tt.wantResp.Result), string(resp.Result))
			}

			if tt.wantResp.Error != nil {
				require.NotNil(t, resp.Error)
				assert.Equal(t, tt.wantResp.Error.Code, resp.Error.Code)
				assert.Equal(t, tt.wantResp.Error.Message, resp.Error.Message)
			}
		})
	}
}

func TestError_MarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		expected string
	}{
		{
			name: "basic error",
			err: &Error{
				Code:    MethodNotFound,
				Message: "Method not found",
			},
			expected: `{
				"code": -32601,
				"message": "Method not found"
			}`,
		},
		{
			name: "error with data",
			err:  NewError(InvalidParams, "Invalid params", map[string]interface{}{"field": "name"}),
			expected: `{
				"code": -32602,
				"message": "Invalid params",
				"data": {"field": "name"}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.err)
			require.NoError(t, err)
			assert.JSONEq(t, tt.expected, string(data))
		})
	}
}

func TestErrorCodes(t *testing.T) {
	// Verify standard JSON-RPC error codes
	assert.Equal(t, -32700, ParseError)
	assert.Equal(t, -32600, InvalidRequest)
	assert.Equal(t, -32601, MethodNotFound)
	assert.Equal(t, -32602, InvalidParams)
	assert.Equal(t, -32603, InternalError)
}

func TestNewStringRequestID(t *testing.T) {
	id := NewStringRequestID("test-id")
	require.NotNil(t, id)
	require.NotNil(t, id.Str)
	assert.Equal(t, "test-id", *id.Str)
	assert.Nil(t, id.Num)
}

func TestNewNumericRequestID(t *testing.T) {
	id := NewNumericRequestID(123)
	require.NotNil(t, id)
	require.NotNil(t, id.Num)
	assert.Equal(t, int64(123), *id.Num)
	assert.Nil(t, id.Str)
}

func TestRequestID_String(t *testing.T) {
	tests := []struct {
		name     string
		id       *RequestID
		expected string
	}{
		{
			name:     "string ID",
			id:       NewStringRequestID("test-123"),
			expected: "test-123",
		},
		{
			name:     "number ID",
			id:       NewNumericRequestID(42),
			expected: "42",
		},
		{
			name:     "nil ID",
			id:       &RequestID{},
			expected: "null",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.id.String()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		expected string
	}{
		{
			name: "error without data",
			err: &Error{
				Code:    -32600,
				Message: "Invalid Request",
			},
			expected: "JSON-RPC error -32600: Invalid Request",
		},
		{
			name:     "error with data",
			err:      NewError(-32602, "Invalid params", map[string]string{"field": "name"}),
			expected: "JSON-RPC error -32602: Invalid params (data: {\"field\":\"name\"})",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.err.Error()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Helper functions
func stringPtr(s string) *string {
	return &s
}

func int64Ptr(i int64) *int64 {
	return &i
}
