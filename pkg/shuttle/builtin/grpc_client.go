// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/jhump/protoreflect/dynamic/grpcdynamic"
	"github.com/jhump/protoreflect/grpcreflect"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	reflectpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
)

// GRPCClientTool provides gRPC client capabilities for calling other gRPC services.
// Apple-style: Uses reflection for zero-config calls to any gRPC service.
type GRPCClientTool struct {
	connections map[string]*grpc.ClientConn
}

// NewGRPCClientTool creates a new gRPC client tool.
func NewGRPCClientTool() *GRPCClientTool {
	return &GRPCClientTool{
		connections: make(map[string]*grpc.ClientConn),
	}
}

func (t *GRPCClientTool) Name() string {
	return "grpc_call"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/grpc.yaml).
// This fallback is used only when prompts are not configured.
func (t *GRPCClientTool) Description() string {
	return `Calls gRPC services using server reflection. No proto files needed!
Automatically discovers service methods and constructs requests.

Use this tool to:
- Call other microservices
- Query gRPC APIs
- Integrate with gRPC-based systems
- Test gRPC endpoints

The tool uses gRPC reflection to automatically understand the service schema.`
}

func (t *GRPCClientTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for gRPC call",
		map[string]*shuttle.JSONSchema{
			"address": shuttle.NewStringSchema("gRPC server address (e.g., 'localhost:9090')"),
			"service": shuttle.NewStringSchema("Service name (e.g., 'loom.v1.LoomService')"),
			"method":  shuttle.NewStringSchema("Method name (e.g., 'Weave' or 'GetHealth')"),
			"request": shuttle.NewObjectSchema("Request parameters as JSON object", nil, nil),
			"timeout_seconds": shuttle.NewNumberSchema("Call timeout in seconds (default: 30)").
				WithDefault(30),
			"tls": shuttle.NewBooleanSchema("Use TLS connection (default: false)").
				WithDefault(false),
		},
		[]string{"address", "service", "method"},
	)
}

func (t *GRPCClientTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Extract parameters
	address, ok := params["address"].(string)
	if !ok || address == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "address is required",
				Suggestion: "Provide gRPC server address (e.g., 'localhost:9090')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	service, ok := params["service"].(string)
	if !ok || service == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "service is required",
				Suggestion: "Provide service name (e.g., 'loom.v1.LoomService')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	method, ok := params["method"].(string)
	if !ok || method == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "method is required",
				Suggestion: "Provide method name (e.g., 'Weave')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	request := params["request"]
	if request == nil {
		request = make(map[string]interface{})
	}

	timeout := 30 * time.Second
	if t, ok := params["timeout_seconds"].(float64); ok {
		timeout = time.Duration(t) * time.Second
	}

	// Get or create connection
	conn, err := t.getConnection(address, params)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "CONNECTION_FAILED",
				Message:    fmt.Sprintf("Failed to connect to %s: %v", address, err),
				Retryable:  true,
				Suggestion: "Check that the gRPC server is running and accessible",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Create reflection client
	refClient := grpcreflect.NewClient(ctx, reflectpb.NewServerReflectionClient(conn))
	defer refClient.Reset()

	// Resolve service
	svcDesc, err := refClient.ResolveService(service)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "SERVICE_NOT_FOUND",
				Message:    fmt.Sprintf("Service '%s' not found: %v", service, err),
				Suggestion: "Check service name spelling and ensure reflection is enabled",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Find method
	var methodDesc *desc.MethodDescriptor
	for _, m := range svcDesc.GetMethods() {
		if m.GetName() == method {
			methodDesc = m
			break
		}
	}

	if methodDesc == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "METHOD_NOT_FOUND",
				Message: fmt.Sprintf("Method '%s' not found in service '%s'", method, service),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Create dynamic request
	reqMsg := dynamic.NewMessage(methodDesc.GetInputType())

	// Populate request from JSON
	if request != nil {
		reqJSON, err := json.Marshal(request)
		if err != nil {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "INVALID_REQUEST",
					Message: fmt.Sprintf("Failed to marshal request: %v", err),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}

		if err := reqMsg.UnmarshalJSON(reqJSON); err != nil {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "INVALID_REQUEST",
					Message: fmt.Sprintf("Failed to populate request: %v", err),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}
	}

	// Make the call
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stub := grpcdynamic.NewStub(conn)
	respMsg, err := stub.InvokeRpc(callCtx, methodDesc, reqMsg)

	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "CALL_FAILED",
				Message:    fmt.Sprintf("gRPC call failed: %v", err),
				Retryable:  true,
				Suggestion: "Check request parameters and service availability",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Convert response to JSON
	// respMsg is proto.Message interface, but runtime type is *dynamic.Message
	dynamicMsg, ok := respMsg.(*dynamic.Message)
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "RESPONSE_TYPE_ERROR",
				Message: "Response is not a dynamic message",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	jsonBytes, err := dynamicMsg.MarshalJSON()
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "RESPONSE_MARSHAL_FAILED",
				Message: fmt.Sprintf("Failed to marshal response: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	var respData interface{}
	json.Unmarshal(jsonBytes, &respData)

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"response": respData,
			"service":  service,
			"method":   method,
		},
		Metadata: map[string]interface{}{
			"address": address,
			"service": service,
			"method":  method,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *GRPCClientTool) Backend() string {
	return "" // Backend-agnostic
}

// getConnection gets or creates a gRPC connection.
func (t *GRPCClientTool) getConnection(address string, params map[string]interface{}) (*grpc.ClientConn, error) {
	// Check if we already have a connection
	if conn, exists := t.connections[address]; exists {
		return conn, nil
	}

	// Create new connection
	opts := []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(50 * 1024 * 1024)), // 50MB
	}

	// Check TLS
	useTLS := false
	if tls, ok := params["tls"].(bool); ok {
		useTLS = tls
	}

	if !useTLS {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.Dial(address, opts...)
	if err != nil {
		return nil, err
	}

	t.connections[address] = conn
	return conn, nil
}

// Close closes all connections (call on shutdown).
func (t *GRPCClientTool) Close() {
	for _, conn := range t.connections {
		conn.Close()
	}
	t.connections = make(map[string]*grpc.ClientConn)
}
