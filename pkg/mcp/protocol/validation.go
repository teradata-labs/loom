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
// Package protocol provides validation utilities for MCP protocol.
package protocol

import (
	"fmt"

	"github.com/xeipuuv/gojsonschema"
)

// ValidateToolArguments validates tool arguments against JSON Schema
func ValidateToolArguments(tool Tool, arguments map[string]interface{}) error {
	if len(tool.InputSchema) == 0 {
		return nil // No schema = no validation
	}

	schemaLoader := gojsonschema.NewGoLoader(tool.InputSchema)
	argsLoader := gojsonschema.NewGoLoader(arguments)

	result, err := gojsonschema.Validate(schemaLoader, argsLoader)
	if err != nil {
		return fmt.Errorf("schema validation failed: %w", err)
	}

	if !result.Valid() {
		errors := make([]string, len(result.Errors()))
		for i, err := range result.Errors() {
			errors[i] = err.String()
		}
		return fmt.Errorf("invalid arguments: %v", errors)
	}

	return nil
}

// ValidateRequest validates a JSON-RPC request
func ValidateRequest(req *Request) error {
	if req.JSONRPC != JSONRPCVersion {
		return fmt.Errorf("invalid jsonrpc version: %s (expected %s)", req.JSONRPC, JSONRPCVersion)
	}

	if req.Method == "" {
		return fmt.Errorf("method is required")
	}

	return nil
}

// ValidateResponse validates a JSON-RPC response
func ValidateResponse(resp *Response) error {
	if resp.JSONRPC != JSONRPCVersion {
		return fmt.Errorf("invalid jsonrpc version: %s (expected %s)", resp.JSONRPC, JSONRPCVersion)
	}

	if resp.ID == nil {
		return fmt.Errorf("response ID is required")
	}

	// Exactly one of Result or Error must be present
	hasResult := len(resp.Result) > 0
	hasError := resp.Error != nil

	if hasResult == hasError {
		return fmt.Errorf("response must have exactly one of result or error")
	}

	return nil
}
