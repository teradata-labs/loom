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
// Package protocol implements the Model Context Protocol (MCP) JSON-RPC 2.0 layer.
// This package provides types and utilities for MCP protocol communication.
package protocol

import (
	"encoding/json"
	"fmt"
)

// JSONRPCVersion is the required version string for JSON-RPC 2.0
const JSONRPCVersion = "2.0"

// Request represents a JSON-RPC 2.0 request
type Request struct {
	JSONRPC string          `json:"jsonrpc"`          // Must be "2.0"
	ID      *RequestID      `json:"id,omitempty"`     // Null for notifications
	Method  string          `json:"method"`           // Method name
	Params  json.RawMessage `json:"params,omitempty"` // Method-specific params
}

// RequestID can be string, number, or null per JSON-RPC 2.0 spec
type RequestID struct {
	Str *string
	Num *int64
}

// MarshalJSON implements json.Marshaler for RequestID
func (r *RequestID) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	if r.Str != nil {
		return json.Marshal(r.Str)
	}
	if r.Num != nil {
		return json.Marshal(r.Num)
	}
	return []byte("null"), nil
}

// UnmarshalJSON implements json.Unmarshaler for RequestID
func (r *RequestID) UnmarshalJSON(data []byte) error {
	// Try string
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		r.Str = &s
		return nil
	}

	// Try number
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		r.Num = &n
		return nil
	}

	// Null is valid
	if string(data) == "null" {
		return nil
	}

	return fmt.Errorf("invalid request ID: %s", data)
}

// String returns a string representation of the RequestID
func (r *RequestID) String() string {
	if r == nil {
		return "null"
	}
	if r.Str != nil {
		return *r.Str
	}
	if r.Num != nil {
		return fmt.Sprintf("%d", *r.Num)
	}
	return "null"
}

// Response represents a JSON-RPC 2.0 response
type Response struct {
	JSONRPC string          `json:"jsonrpc"`          // Must be "2.0"
	ID      *RequestID      `json:"id"`               // Must match request
	Result  json.RawMessage `json:"result,omitempty"` // Success result
	Error   *Error          `json:"error,omitempty"`  // Error (mutually exclusive with Result)
}

// Error represents a JSON-RPC 2.0 error
type Error struct {
	Code    int             `json:"code"`           // Error code
	Message string          `json:"message"`        // Human-readable message
	Data    json.RawMessage `json:"data,omitempty"` // Additional error info
}

// Standard JSON-RPC error codes
const (
	ParseError     = -32700 // Invalid JSON
	InvalidRequest = -32600 // Invalid JSON-RPC
	MethodNotFound = -32601 // Method doesn't exist
	InvalidParams  = -32602 // Invalid parameters
	InternalError  = -32603 // Internal error
	ServerError    = -32000 // Server-specific error (to -32099)
)

// NewError creates a standard JSON-RPC error
func NewError(code int, message string, data interface{}) *Error {
	e := &Error{
		Code:    code,
		Message: message,
	}
	if data != nil {
		dataJSON, err := json.Marshal(data)
		if err == nil {
			e.Data = dataJSON
		}
	}
	return e
}

// Implement error interface for Error
func (e *Error) Error() string {
	if e.Data != nil {
		return fmt.Sprintf("JSON-RPC error %d: %s (data: %s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// NewStringRequestID creates a RequestID from a string
func NewStringRequestID(s string) *RequestID {
	return &RequestID{Str: &s}
}

// NewNumericRequestID creates a RequestID from a number
func NewNumericRequestID(n int64) *RequestID {
	return &RequestID{Num: &n}
}
