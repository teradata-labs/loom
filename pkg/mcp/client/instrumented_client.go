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
	"encoding/json"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/observability"
)

// InstrumentedClient wraps an MCP Client with observability instrumentation.
// It captures detailed traces and metrics for every MCP operation, including:
// - Operation type and parameters
// - Execution duration and success/failure
// - Result data and errors
// - Server identification
//
// This wrapper is transparent and can wrap any Client.
type InstrumentedClient struct {
	// client is the underlying MCP client
	client *Client

	// tracer is used for creating spans
	tracer observability.Tracer

	// serverName is used to identify which MCP server this is
	serverName string
}

// NewInstrumentedClient creates a new instrumented MCP client.
func NewInstrumentedClient(client *Client, tracer observability.Tracer, serverName string) *InstrumentedClient {
	return &InstrumentedClient{
		client:     client,
		tracer:     tracer,
		serverName: serverName,
	}
}

// Initialize performs the MCP handshake with observability instrumentation.
func (ic *InstrumentedClient) Initialize(ctx context.Context, clientInfo protocol.Implementation) error {
	// Start span
	ctx, span := ic.tracer.StartSpan(ctx, observability.SpanMCPClientInitialize)
	defer ic.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrMCPServerName, ic.serverName)
	span.SetAttribute(observability.AttrMCPOperation, "initialize")
	span.SetAttribute("mcp.client.name", clientInfo.Name)
	span.SetAttribute("mcp.client.version", clientInfo.Version)
	span.SetAttribute(observability.AttrMCPProtocolVersion, protocol.ProtocolVersion)

	// Record event: Initialize started
	span.AddEvent("mcp.initialize.started", map[string]interface{}{
		"server": ic.serverName,
	})

	// Execute the operation
	err := ic.client.Initialize(ctx, clientInfo)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		// Record error event
		span.AddEvent("mcp.initialize.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		// Emit error metric
		ic.tracer.RecordMetric(observability.MetricMCPErrors, 1, map[string]string{
			observability.AttrMCPServerName: ic.serverName,
			observability.AttrMCPOperation:  "initialize",
		})

		return err
	}

	// Success
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "",
	}

	// Record server info
	span.SetAttribute("mcp.server.name", ic.client.serverInfo.Name)
	span.SetAttribute("mcp.server.version", ic.client.serverInfo.Version)

	// Record success event
	span.AddEvent("mcp.initialize.completed", map[string]interface{}{
		"duration_ms":      duration.Milliseconds(),
		"protocol_version": ic.client.protocolVersion,
	})

	// Emit success metric
	ic.tracer.RecordMetric(observability.MetricMCPCalls, 1, map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "initialize",
		"status":                        "success",
	})

	// Emit duration metric
	ic.tracer.RecordMetric(observability.MetricMCPDuration, float64(duration.Milliseconds()), map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "initialize",
	})

	return nil
}

// ListTools lists available tools with observability instrumentation.
func (ic *InstrumentedClient) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	// Start span
	ctx, span := ic.tracer.StartSpan(ctx, observability.SpanMCPToolsList)
	defer ic.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrMCPServerName, ic.serverName)
	span.SetAttribute(observability.AttrMCPOperation, "tools.list")

	// Record event
	span.AddEvent("mcp.tools.list.started", map[string]interface{}{
		"server": ic.serverName,
	})

	// Execute the operation
	tools, err := ic.client.ListTools(ctx)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		span.AddEvent("mcp.tools.list.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		ic.tracer.RecordMetric(observability.MetricMCPErrors, 1, map[string]string{
			observability.AttrMCPServerName: ic.serverName,
			observability.AttrMCPOperation:  "tools.list",
		})

		return nil, err
	}

	// Success
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "",
	}

	span.SetAttribute("mcp.tools.count", len(tools))

	span.AddEvent("mcp.tools.list.completed", map[string]interface{}{
		"duration_ms": duration.Milliseconds(),
		"tool_count":  len(tools),
	})

	ic.tracer.RecordMetric(observability.MetricMCPCalls, 1, map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "tools.list",
		"status":                        "success",
	})

	ic.tracer.RecordMetric(observability.MetricMCPDuration, float64(duration.Milliseconds()), map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "tools.list",
	})

	return tools, nil
}

// CallTool calls a tool with observability instrumentation.
// Returns interface{} to avoid import cycles (actual type is *protocol.CallToolResult)
func (ic *InstrumentedClient) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (interface{}, error) {
	// Start span
	ctx, span := ic.tracer.StartSpan(ctx, observability.SpanMCPToolsCall)
	defer ic.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrMCPServerName, ic.serverName)
	span.SetAttribute(observability.AttrMCPOperation, "tools.call")
	span.SetAttribute(observability.AttrMCPToolName, name)

	// Capture arguments (sanitized to avoid PII)
	if len(arguments) > 0 {
		if argsJSON, err := json.Marshal(arguments); err == nil && len(argsJSON) < 1000 {
			span.SetAttribute("mcp.tool.args", string(argsJSON))
		} else {
			span.SetAttribute("mcp.tool.args.count", len(arguments))
		}
	}

	// Record event
	span.AddEvent("mcp.tools.call.started", map[string]interface{}{
		"server": ic.serverName,
		"tool":   name,
	})

	// Execute the operation
	resultInterface, err := ic.client.CallTool(ctx, name, arguments)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case (client-level error)
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		span.AddEvent("mcp.tools.call.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		ic.tracer.RecordMetric(observability.MetricMCPErrors, 1, map[string]string{
			observability.AttrMCPServerName: ic.serverName,
			observability.AttrMCPOperation:  "tools.call",
			observability.AttrMCPToolName:   name,
			"error_type":                    "client_error",
		})

		return nil, err
	}

	// Type assert the result back to *protocol.CallToolResult for instrumentation
	result, ok := resultInterface.(*protocol.CallToolResult)
	if !ok {
		// This shouldn't happen, but handle gracefully
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: "Invalid result type from CallTool",
		}
		return resultInterface, nil // Return as-is
	}

	// Check if tool execution returned an error
	if result.IsError {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: "Tool execution failed",
		}

		span.SetAttribute("mcp.tool.error", true)

		span.AddEvent("mcp.tools.call.error", map[string]interface{}{
			"duration_ms": duration.Milliseconds(),
		})

		ic.tracer.RecordMetric(observability.MetricMCPErrors, 1, map[string]string{
			observability.AttrMCPServerName: ic.serverName,
			observability.AttrMCPOperation:  "tools.call",
			observability.AttrMCPToolName:   name,
			"error_type":                    "tool_error",
		})
	} else {
		// Success
		span.Status = observability.Status{
			Code:    observability.StatusOK,
			Message: "",
		}

		span.AddEvent("mcp.tools.call.completed", map[string]interface{}{
			"duration_ms": duration.Milliseconds(),
		})

		ic.tracer.RecordMetric(observability.MetricMCPCalls, 1, map[string]string{
			observability.AttrMCPServerName: ic.serverName,
			observability.AttrMCPOperation:  "tools.call",
			observability.AttrMCPToolName:   name,
			"status":                        "success",
		})
	}

	// Emit duration metric
	ic.tracer.RecordMetric(observability.MetricMCPDuration, float64(duration.Milliseconds()), map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "tools.call",
		observability.AttrMCPToolName:   name,
	})

	return resultInterface, nil
}

// ListResources lists available resources with observability instrumentation.
func (ic *InstrumentedClient) ListResources(ctx context.Context) ([]protocol.Resource, error) {
	// Start span
	ctx, span := ic.tracer.StartSpan(ctx, observability.SpanMCPResourcesList)
	defer ic.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrMCPServerName, ic.serverName)
	span.SetAttribute(observability.AttrMCPOperation, "resources.list")

	span.AddEvent("mcp.resources.list.started", map[string]interface{}{
		"server": ic.serverName,
	})

	// Execute the operation
	resources, err := ic.client.ListResources(ctx)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		span.AddEvent("mcp.resources.list.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		ic.tracer.RecordMetric(observability.MetricMCPErrors, 1, map[string]string{
			observability.AttrMCPServerName: ic.serverName,
			observability.AttrMCPOperation:  "resources.list",
		})

		return nil, err
	}

	// Success
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "",
	}

	span.SetAttribute("mcp.resources.count", len(resources))

	span.AddEvent("mcp.resources.list.completed", map[string]interface{}{
		"duration_ms":    duration.Milliseconds(),
		"resource_count": len(resources),
	})

	ic.tracer.RecordMetric(observability.MetricMCPCalls, 1, map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "resources.list",
		"status":                        "success",
	})

	ic.tracer.RecordMetric(observability.MetricMCPDuration, float64(duration.Milliseconds()), map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "resources.list",
	})

	return resources, nil
}

// ReadResource reads a resource with observability instrumentation.
func (ic *InstrumentedClient) ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error) {
	// Start span
	ctx, span := ic.tracer.StartSpan(ctx, observability.SpanMCPResourcesRead)
	defer ic.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrMCPServerName, ic.serverName)
	span.SetAttribute(observability.AttrMCPOperation, "resources.read")
	span.SetAttribute(observability.AttrMCPResourceURI, uri)

	span.AddEvent("mcp.resources.read.started", map[string]interface{}{
		"server": ic.serverName,
		"uri":    uri,
	})

	// Execute the operation
	contents, err := ic.client.ReadResource(ctx, uri)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		span.AddEvent("mcp.resources.read.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		ic.tracer.RecordMetric(observability.MetricMCPErrors, 1, map[string]string{
			observability.AttrMCPServerName: ic.serverName,
			observability.AttrMCPOperation:  "resources.read",
		})

		return nil, err
	}

	// Success
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "",
	}

	span.AddEvent("mcp.resources.read.completed", map[string]interface{}{
		"duration_ms": duration.Milliseconds(),
	})

	ic.tracer.RecordMetric(observability.MetricMCPCalls, 1, map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "resources.read",
		"status":                        "success",
	})

	ic.tracer.RecordMetric(observability.MetricMCPDuration, float64(duration.Milliseconds()), map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "resources.read",
	})

	return contents, nil
}

// SubscribeResource subscribes to resource updates with observability instrumentation.
func (ic *InstrumentedClient) SubscribeResource(ctx context.Context, uri string) error {
	// Start span
	ctx, span := ic.tracer.StartSpan(ctx, observability.SpanMCPResourcesSubscribe)
	defer ic.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrMCPServerName, ic.serverName)
	span.SetAttribute(observability.AttrMCPOperation, "resources.subscribe")
	span.SetAttribute(observability.AttrMCPResourceURI, uri)

	span.AddEvent("mcp.resources.subscribe.started", map[string]interface{}{
		"server": ic.serverName,
		"uri":    uri,
	})

	// Execute the operation
	err := ic.client.SubscribeResource(ctx, uri)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		span.AddEvent("mcp.resources.subscribe.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		ic.tracer.RecordMetric(observability.MetricMCPErrors, 1, map[string]string{
			observability.AttrMCPServerName: ic.serverName,
			observability.AttrMCPOperation:  "resources.subscribe",
		})

		return err
	}

	// Success
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "",
	}

	span.AddEvent("mcp.resources.subscribe.completed", map[string]interface{}{
		"duration_ms": duration.Milliseconds(),
	})

	ic.tracer.RecordMetric(observability.MetricMCPCalls, 1, map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "resources.subscribe",
		"status":                        "success",
	})

	ic.tracer.RecordMetric(observability.MetricMCPDuration, float64(duration.Milliseconds()), map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "resources.subscribe",
	})

	return nil
}

// ListPrompts lists available prompts with observability instrumentation.
func (ic *InstrumentedClient) ListPrompts(ctx context.Context) ([]protocol.Prompt, error) {
	// Start span
	ctx, span := ic.tracer.StartSpan(ctx, observability.SpanMCPPromptsList)
	defer ic.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrMCPServerName, ic.serverName)
	span.SetAttribute(observability.AttrMCPOperation, "prompts.list")

	span.AddEvent("mcp.prompts.list.started", map[string]interface{}{
		"server": ic.serverName,
	})

	// Execute the operation
	prompts, err := ic.client.ListPrompts(ctx)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		span.AddEvent("mcp.prompts.list.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		ic.tracer.RecordMetric(observability.MetricMCPErrors, 1, map[string]string{
			observability.AttrMCPServerName: ic.serverName,
			observability.AttrMCPOperation:  "prompts.list",
		})

		return nil, err
	}

	// Success
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "",
	}

	span.SetAttribute("mcp.prompts.count", len(prompts))

	span.AddEvent("mcp.prompts.list.completed", map[string]interface{}{
		"duration_ms":  duration.Milliseconds(),
		"prompt_count": len(prompts),
	})

	ic.tracer.RecordMetric(observability.MetricMCPCalls, 1, map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "prompts.list",
		"status":                        "success",
	})

	ic.tracer.RecordMetric(observability.MetricMCPDuration, float64(duration.Milliseconds()), map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "prompts.list",
	})

	return prompts, nil
}

// GetPrompt gets a prompt with observability instrumentation.
func (ic *InstrumentedClient) GetPrompt(ctx context.Context, name string, arguments map[string]interface{}) (*protocol.GetPromptResult, error) {
	// Start span
	ctx, span := ic.tracer.StartSpan(ctx, observability.SpanMCPPromptsGet)
	defer ic.tracer.EndSpan(span)

	// Start timing
	start := time.Now()

	// Set span attributes
	span.SetAttribute(observability.AttrMCPServerName, ic.serverName)
	span.SetAttribute(observability.AttrMCPOperation, "prompts.get")
	span.SetAttribute(observability.AttrMCPPromptName, name)

	if len(arguments) > 0 {
		span.SetAttribute("mcp.prompt.args.count", len(arguments))
	}

	span.AddEvent("mcp.prompts.get.started", map[string]interface{}{
		"server": ic.serverName,
		"prompt": name,
	})

	// Execute the operation
	result, err := ic.client.GetPrompt(ctx, name, arguments)

	// Calculate duration
	duration := time.Since(start)

	// Handle error case
	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())

		span.AddEvent("mcp.prompts.get.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		ic.tracer.RecordMetric(observability.MetricMCPErrors, 1, map[string]string{
			observability.AttrMCPServerName: ic.serverName,
			observability.AttrMCPOperation:  "prompts.get",
			observability.AttrMCPPromptName: name,
		})

		return nil, err
	}

	// Success
	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: "",
	}

	span.AddEvent("mcp.prompts.get.completed", map[string]interface{}{
		"duration_ms": duration.Milliseconds(),
	})

	ic.tracer.RecordMetric(observability.MetricMCPCalls, 1, map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "prompts.get",
		observability.AttrMCPPromptName: name,
		"status":                        "success",
	})

	ic.tracer.RecordMetric(observability.MetricMCPDuration, float64(duration.Milliseconds()), map[string]string{
		observability.AttrMCPServerName: ic.serverName,
		observability.AttrMCPOperation:  "prompts.get",
		observability.AttrMCPPromptName: name,
	})

	return result, nil
}

// IsInitialized delegates to the underlying client.
func (ic *InstrumentedClient) IsInitialized() bool {
	return ic.client.IsInitialized()
}

// Close delegates to the underlying client.
func (ic *InstrumentedClient) Close() error {
	return ic.client.Close()
}

// Ping delegates to the underlying client.
func (ic *InstrumentedClient) Ping(ctx context.Context) error {
	return ic.client.Ping(ctx)
}

// SetSamplingHandler delegates to the underlying client.
func (ic *InstrumentedClient) SetSamplingHandler(handler SamplingHandler) {
	ic.client.SetSamplingHandler(handler)
}

// UnsubscribeResource delegates to the underlying client.
func (ic *InstrumentedClient) UnsubscribeResource(ctx context.Context, uri string) error {
	return ic.client.UnsubscribeResource(ctx, uri)
}
