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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the gRPC Loom client with TUI-friendly methods.
type Client struct {
	conn   *grpc.ClientConn
	client loomv1.LoomServiceClient
	addr   string
}

// Config holds client configuration.
type Config struct {
	ServerAddr string        // Default: localhost:9090
	Timeout    time.Duration // Default: 30s

	// TLS configuration
	TLSEnabled    bool   // Enable TLS connection
	TLSInsecure   bool   // Skip TLS certificate verification (for self-signed certs)
	TLSCAFile     string // Path to CA certificate file
	TLSServerName string // Override TLS server name (for testing)
}

// NewClient creates a new Loom client.
func NewClient(cfg Config) (*Client, error) {
	if cfg.ServerAddr == "" {
		cfg.ServerAddr = "localhost:9090"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	// Determine credentials
	var creds credentials.TransportCredentials
	if cfg.TLSEnabled {
		tlsConfig, err := createTLSConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create TLS config: %w", err)
		}
		creds = credentials.NewTLS(tlsConfig)
	} else {
		creds = insecure.NewCredentials()
	}

	// Connect to server
	conn, err := grpc.NewClient(cfg.ServerAddr,
		grpc.WithTransportCredentials(creds),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create client for %s: %w", cfg.ServerAddr, err)
	}

	return &Client{
		conn:   conn,
		client: loomv1.NewLoomServiceClient(conn),
		addr:   cfg.ServerAddr,
	}, nil
}

// createTLSConfig creates a TLS configuration based on the provided config.
func createTLSConfig(cfg Config) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// If insecure mode, skip certificate verification
	if cfg.TLSInsecure {
		tlsConfig.InsecureSkipVerify = true
		return tlsConfig, nil
	}

	// Load system root CAs
	certPool, err := x509.SystemCertPool()
	if err != nil {
		// Fall back to empty pool
		certPool = x509.NewCertPool()
	}

	// Load custom CA certificate if provided
	if cfg.TLSCAFile != "" {
		caCert, err := os.ReadFile(cfg.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}
		if !certPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
	}

	tlsConfig.RootCAs = certPool

	// Override server name if specified (useful for testing)
	if cfg.TLSServerName != "" {
		tlsConfig.ServerName = cfg.TLSServerName
	}

	return tlsConfig, nil
}

// Close closes the client connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Weave sends a query and returns the response.
func (c *Client) Weave(ctx context.Context, query string, sessionID string, agentID string) (*loomv1.WeaveResponse, error) {
	req := &loomv1.WeaveRequest{
		Query:     query,
		SessionId: sessionID,
		AgentId:   agentID,
	}

	return c.client.Weave(ctx, req)
}

// StreamWeave sends a query and streams the response.
// The progressFn is called for each progress update.
func (c *Client) StreamWeave(ctx context.Context, query string, sessionID string, agentID string, progressFn func(*loomv1.WeaveProgress)) error {
	req := &loomv1.WeaveRequest{
		Query:     query,
		SessionId: sessionID,
		AgentId:   agentID,
	}

	stream, err := c.client.StreamWeave(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to start stream: %w", err)
	}

	for {
		progress, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("stream error: %w", err)
		}

		if progressFn != nil {
			progressFn(progress)
		}
	}

	return nil
}

// SubscribeToSession subscribes to real-time updates for a session.
// Returns a stream that receives updates when new messages arrive.
// The updateFn is called for each session update.
func (c *Client) SubscribeToSession(ctx context.Context, sessionID string, agentID string, updateFn func(*loomv1.SessionUpdate)) error {
	req := &loomv1.SubscribeToSessionRequest{
		SessionId: sessionID,
		AgentId:   agentID,
	}

	stream, err := c.client.SubscribeToSession(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to subscribe to session: %w", err)
	}

	for {
		update, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("subscription error: %w", err)
		}

		if updateFn != nil {
			updateFn(update)
		}
	}

	return nil
}

// CreateSession creates a new session.
func (c *Client) CreateSession(ctx context.Context, name string, agentID string) (*loomv1.Session, error) {
	req := &loomv1.CreateSessionRequest{
		Name:    name,
		AgentId: agentID,
	}

	return c.client.CreateSession(ctx, req)
}

// GetSession retrieves a session.
func (c *Client) GetSession(ctx context.Context, sessionID string) (*loomv1.Session, error) {
	req := &loomv1.GetSessionRequest{
		SessionId: sessionID,
	}

	return c.client.GetSession(ctx, req)
}

// ListSessions lists all sessions.
func (c *Client) ListSessions(ctx context.Context, limit, offset int32) ([]*loomv1.Session, error) {
	req := &loomv1.ListSessionsRequest{
		Limit:  limit,
		Offset: offset,
	}

	resp, err := c.client.ListSessions(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Sessions, nil
}

// DeleteSession deletes a session.
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	req := &loomv1.DeleteSessionRequest{
		SessionId: sessionID,
	}

	_, err := c.client.DeleteSession(ctx, req)
	return err
}

// GetConversationHistory retrieves conversation history for a session.
func (c *Client) GetConversationHistory(ctx context.Context, sessionID string) ([]*loomv1.Message, error) {
	req := &loomv1.GetConversationHistoryRequest{
		SessionId: sessionID,
	}

	resp, err := c.client.GetConversationHistory(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Messages, nil
}

// ListTools lists available tools.
func (c *Client) ListTools(ctx context.Context) ([]*loomv1.ToolDefinition, error) {
	req := &loomv1.ListToolsRequest{}

	resp, err := c.client.ListTools(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Tools, nil
}

// ListToolsByBackend lists tools filtered by backend/server name.
func (c *Client) ListToolsByBackend(ctx context.Context, backend string) ([]*loomv1.ToolDefinition, error) {
	req := &loomv1.ListToolsRequest{
		Backend: backend,
	}

	resp, err := c.client.ListTools(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Tools, nil
}

// ListMCPServerTools lists tools from a specific MCP server.
// This queries the MCP server directly through the manager, not the agent's tool registry.
func (c *Client) ListMCPServerTools(ctx context.Context, serverName string) ([]*loomv1.ToolDefinition, error) {
	req := &loomv1.ListMCPServerToolsRequest{
		ServerName: serverName,
	}

	resp, err := c.client.ListMCPServerTools(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Tools, nil
}

// GetHealth checks server health.
func (c *Client) GetHealth(ctx context.Context) (*loomv1.HealthStatus, error) {
	req := &loomv1.GetHealthRequest{}

	return c.client.GetHealth(ctx, req)
}

// ListAgents lists all available agents from the server.
func (c *Client) ListAgents(ctx context.Context) ([]*loomv1.AgentInfo, error) {
	req := &loomv1.ListAgentsRequest{}

	resp, err := c.client.ListAgents(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Agents, nil
}

// CreatePattern creates a new pattern at runtime for an agent.
func (c *Client) CreatePattern(ctx context.Context, req *loomv1.CreatePatternRequest) (*loomv1.CreatePatternResponse, error) {
	return c.client.CreatePattern(ctx, req)
}

// StreamPatternUpdates streams pattern update events in real-time.
// The eventFn is called for each pattern update event (create, modify, delete).
// This call blocks until the context is cancelled or an error occurs.
func (c *Client) StreamPatternUpdates(ctx context.Context, agentID, category string, eventFn func(*loomv1.PatternUpdateEvent)) error {
	req := &loomv1.StreamPatternUpdatesRequest{
		AgentId:  agentID,
		Category: category,
	}

	stream, err := c.client.StreamPatternUpdates(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to start pattern updates stream: %w", err)
	}

	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("stream error: %w", err)
		}

		if eventFn != nil {
			eventFn(event)
		}
	}

	return nil
}

// ServerAddr returns the server address.
func (c *Client) ServerAddr() string {
	return c.addr
}

// ============================================================================
// Tri-Modal Communication Methods
// ============================================================================

// Point-to-Point (Unicast) Communication

// SendAsync sends a message asynchronously (fire-and-forget).
func (c *Client) SendAsync(ctx context.Context, req *loomv1.SendAsyncRequest) (*loomv1.SendAsyncResponse, error) {
	return c.client.SendAsync(ctx, req)
}

// SendAndReceive sends a message and waits for response (RPC-style).
func (c *Client) SendAndReceive(ctx context.Context, req *loomv1.SendAndReceiveRequest) (*loomv1.SendAndReceiveResponse, error) {
	return c.client.SendAndReceive(ctx, req)
}

// Broadcast Bus (Pub/Sub) Communication

// Publish publishes a message to a topic on the broadcast bus.
func (c *Client) Publish(ctx context.Context, req *loomv1.PublishRequest) (*loomv1.PublishResponse, error) {
	return c.client.Publish(ctx, req)
}

// Subscribe subscribes to messages on a topic pattern and streams them.
func (c *Client) Subscribe(ctx context.Context, req *loomv1.SubscribeRequest, msgFn func(*loomv1.BusMessage)) error {
	stream, err := c.client.Subscribe(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("stream error: %w", err)
		}

		if msgFn != nil {
			msgFn(msg)
		}
	}

	return nil
}

// PutSharedMemory writes or updates a value in shared memory.
func (c *Client) PutSharedMemory(ctx context.Context, req *loomv1.PutSharedMemoryRequest) (*loomv1.PutSharedMemoryResponse, error) {
	return c.client.PutSharedMemory(ctx, req)
}

// GetSharedMemory retrieves a value from shared memory.
func (c *Client) GetSharedMemory(ctx context.Context, req *loomv1.GetSharedMemoryRequest) (*loomv1.GetSharedMemoryResponse, error) {
	return c.client.GetSharedMemory(ctx, req)
}

// DeleteSharedMemory removes a value from shared memory.
func (c *Client) DeleteSharedMemory(ctx context.Context, req *loomv1.DeleteSharedMemoryRequest) (*loomv1.DeleteSharedMemoryResponse, error) {
	return c.client.DeleteSharedMemory(ctx, req)
}

// ListSharedMemoryKeys lists keys matching a pattern in a namespace.
func (c *Client) ListSharedMemoryKeys(ctx context.Context, req *loomv1.ListSharedMemoryKeysRequest) (*loomv1.ListSharedMemoryKeysResponse, error) {
	return c.client.ListSharedMemoryKeys(ctx, req)
}

// GetSharedMemoryStats retrieves statistics for a namespace.
func (c *Client) GetSharedMemoryStats(ctx context.Context, req *loomv1.GetSharedMemoryStatsRequest) (*loomv1.SharedMemoryStats, error) {
	return c.client.GetSharedMemoryStats(ctx, req)
}

// WatchSharedMemory watches for changes to keys and streams updates.
func (c *Client) WatchSharedMemory(ctx context.Context, req *loomv1.WatchSharedMemoryRequest) (loomv1.LoomService_WatchSharedMemoryClient, error) {
	return c.client.WatchSharedMemory(ctx, req)
}

// ListAvailableModels lists all available LLM models/providers.
func (c *Client) ListAvailableModels(ctx context.Context) ([]*loomv1.ModelInfo, error) {
	resp, err := c.client.ListAvailableModels(ctx, &loomv1.ListAvailableModelsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}
	return resp.Models, nil
}

// SwitchModel switches the LLM model/provider for a session.
func (c *Client) SwitchModel(ctx context.Context, sessionID, provider, model string) (*loomv1.SwitchModelResponse, error) {
	req := &loomv1.SwitchModelRequest{
		SessionId:       sessionID,
		Provider:        provider,
		Model:           model,
		PreserveContext: true,
	}
	resp, err := c.client.SwitchModel(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to switch model: %w", err)
	}
	return resp, nil
}

// RequestToolPermission requests user permission to execute a tool.
func (c *Client) RequestToolPermission(ctx context.Context, req *loomv1.ToolPermissionRequest) (*loomv1.ToolPermissionResponse, error) {
	resp, err := c.client.RequestToolPermission(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to request tool permission: %w", err)
	}
	return resp, nil
}

// ListMCPServers lists all configured MCP servers.
func (c *Client) ListMCPServers(ctx context.Context, req *loomv1.ListMCPServersRequest) (*loomv1.ListMCPServersResponse, error) {
	resp, err := c.client.ListMCPServers(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCP servers: %w", err)
	}
	return resp, nil
}

// GetMCPServer retrieves a specific MCP server.
func (c *Client) GetMCPServer(ctx context.Context, req *loomv1.GetMCPServerRequest) (*loomv1.MCPServerInfo, error) {
	resp, err := c.client.GetMCPServer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCP server: %w", err)
	}
	return resp, nil
}

// AddMCPServer adds a new MCP server configuration.
func (c *Client) AddMCPServer(ctx context.Context, req *loomv1.AddMCPServerRequest) (*loomv1.AddMCPServerResponse, error) {
	resp, err := c.client.AddMCPServer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to add MCP server: %w", err)
	}
	return resp, nil
}

// UpdateMCPServer updates an existing MCP server configuration.
func (c *Client) UpdateMCPServer(ctx context.Context, req *loomv1.UpdateMCPServerRequest) (*loomv1.MCPServerInfo, error) {
	resp, err := c.client.UpdateMCPServer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to update MCP server: %w", err)
	}
	return resp, nil
}

// DeleteMCPServer removes an MCP server.
func (c *Client) DeleteMCPServer(ctx context.Context, req *loomv1.DeleteMCPServerRequest) (*loomv1.DeleteMCPServerResponse, error) {
	resp, err := c.client.DeleteMCPServer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to delete MCP server: %w", err)
	}
	return resp, nil
}

// RestartMCPServer restarts a running MCP server.
func (c *Client) RestartMCPServer(ctx context.Context, req *loomv1.RestartMCPServerRequest) (*loomv1.MCPServerInfo, error) {
	resp, err := c.client.RestartMCPServer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to restart MCP server: %w", err)
	}
	return resp, nil
}

// HealthCheckMCPServers checks health of all MCP servers.
func (c *Client) HealthCheckMCPServers(ctx context.Context, req *loomv1.HealthCheckMCPServersRequest) (*loomv1.HealthCheckMCPServersResponse, error) {
	resp, err := c.client.HealthCheckMCPServers(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to health check MCP servers: %w", err)
	}
	return resp, nil
}

// TestMCPServerConnection tests an MCP server connection without persisting.
func (c *Client) TestMCPServerConnection(ctx context.Context, req *loomv1.TestMCPServerConnectionRequest) (*loomv1.TestMCPServerConnectionResponse, error) {
	resp, err := c.client.TestMCPServerConnection(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to test MCP server connection: %w", err)
	}
	return resp, nil
}

// ============================================================================
// Weaver Conflict Resolution Methods
// ============================================================================

// ============================================================================
// Artifact Management Methods
// ============================================================================

// ListArtifacts lists artifacts with optional filtering.
func (c *Client) ListArtifacts(ctx context.Context, source, contentType string, tags []string, limit, offset int32, includeDeleted bool) ([]*loomv1.Artifact, int32, error) {
	req := &loomv1.ListArtifactsRequest{
		Source:         source,
		ContentType:    contentType,
		Tags:           tags,
		Limit:          limit,
		Offset:         offset,
		IncludeDeleted: includeDeleted,
	}

	resp, err := c.client.ListArtifacts(ctx, req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list artifacts: %w", err)
	}

	return resp.Artifacts, resp.TotalCount, nil
}

// GetArtifact retrieves artifact metadata by ID or name.
func (c *Client) GetArtifact(ctx context.Context, id, name string) (*loomv1.Artifact, error) {
	req := &loomv1.GetArtifactRequest{
		Id:   id,
		Name: name,
	}

	resp, err := c.client.GetArtifact(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact: %w", err)
	}

	return resp.Artifact, nil
}

// UploadArtifact uploads a file to artifacts storage.
// Returns the created artifact metadata.
func (c *Client) UploadArtifact(ctx context.Context, name string, content []byte, source, sourceAgentID, purpose string, tags []string) (*loomv1.Artifact, error) {
	req := &loomv1.UploadArtifactRequest{
		Name:          name,
		Content:       content,
		Source:        source,
		SourceAgentId: sourceAgentID,
		Purpose:       purpose,
		Tags:          tags,
	}

	resp, err := c.client.UploadArtifact(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to upload artifact: %w", err)
	}

	return resp.Artifact, nil
}

// UploadArtifactFromFile reads a file and uploads it to artifacts storage.
// This is a convenience method that wraps UploadArtifact.
func (c *Client) UploadArtifactFromFile(ctx context.Context, filePath, purpose string, tags []string) (*loomv1.Artifact, error) {
	// Check if path is a directory
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	if fileInfo.IsDir() {
		return nil, fmt.Errorf("cannot upload directory: %s. To upload multiple files, create a tar/zip archive first (e.g., tar -czf archive.tar.gz %s)", filePath, filePath)
	}

	// Read file content (Clean path to prevent path traversal - gosec G304)
	content, err := os.ReadFile(filepath.Clean(filePath))
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Extract filename from path
	_, filename := "", filePath
	for i := len(filePath) - 1; i >= 0; i-- {
		if filePath[i] == '/' || filePath[i] == '\\' {
			filename = filePath[i+1:]
			break
		}
	}

	// Upload with "user" source
	return c.UploadArtifact(ctx, filename, content, "user", "", purpose, tags)
}

// DeleteArtifact deletes an artifact (soft or hard delete).
func (c *Client) DeleteArtifact(ctx context.Context, id string, hardDelete bool) error {
	req := &loomv1.DeleteArtifactRequest{
		Id:         id,
		HardDelete: hardDelete,
	}

	_, err := c.client.DeleteArtifact(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to delete artifact: %w", err)
	}

	return nil
}

// SearchArtifacts performs full-text search on artifacts.
func (c *Client) SearchArtifacts(ctx context.Context, query string, limit int32) ([]*loomv1.Artifact, error) {
	req := &loomv1.SearchArtifactsRequest{
		Query: query,
		Limit: limit,
	}

	resp, err := c.client.SearchArtifacts(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to search artifacts: %w", err)
	}

	return resp.Artifacts, nil
}

// GetArtifactContent reads artifact file content.
// The encoding parameter can be "text" or "base64" (auto-detected if empty).
// maxSizeMB limits the size of content that can be read (default: 10MB).
func (c *Client) GetArtifactContent(ctx context.Context, id, encoding string, maxSizeMB int64) ([]byte, string, error) {
	req := &loomv1.GetArtifactContentRequest{
		Id:        id,
		Encoding:  encoding,
		MaxSizeMb: maxSizeMB,
	}

	resp, err := c.client.GetArtifactContent(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get artifact content: %w", err)
	}

	return resp.Content, resp.Encoding, nil
}

// GetArtifactStats retrieves storage statistics.
func (c *Client) GetArtifactStats(ctx context.Context) (*loomv1.GetArtifactStatsResponse, error) {
	req := &loomv1.GetArtifactStatsRequest{}

	resp, err := c.client.GetArtifactStats(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact stats: %w", err)
	}

	return resp, nil
}

// ListUIApps lists all available UI apps from the server.
func (c *Client) ListUIApps(ctx context.Context) ([]*loomv1.UIApp, error) {
	resp, err := c.client.ListUIApps(ctx, &loomv1.ListUIAppsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list UI apps: %w", err)
	}
	return resp.Apps, nil
}

// GetServerConfig retrieves the current server configuration.
func (c *Client) GetServerConfig(ctx context.Context) (*loomv1.ServerConfig, error) {
	resp, err := c.client.GetServerConfig(ctx, &loomv1.GetServerConfigRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to get server config: %w", err)
	}
	return resp, nil
}

// AnswerClarificationQuestion provides an answer to a clarification question asked by an agent.
// This is used by the TUI to respond to EventQuestionAsked progress events.
func (c *Client) AnswerClarificationQuestion(ctx context.Context, sessionID, questionID, answer, agentID string) error {
	req := &loomv1.AnswerClarificationRequest{
		SessionId:  sessionID,
		QuestionId: questionID,
		Answer:     answer,
		AgentId:    agentID,
	}

	resp, err := c.client.AnswerClarificationQuestion(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to answer clarification question: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("answer not accepted: %s", resp.Error)
	}

	return nil
}

// AnswerClarificationQuestionWithRetry sends an answer to a clarification question with retry logic.
// Retries up to maxRetries times with exponential backoff on transient failures.
// Useful for handling network issues and temporary server unavailability.
func (c *Client) AnswerClarificationQuestionWithRetry(ctx context.Context, sessionID, questionID, answer, agentID string, maxRetries int) error {
	if maxRetries <= 0 {
		maxRetries = 3 // Default to 3 retries
	}

	var lastErr error
	baseDelay := 100 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := c.AnswerClarificationQuestion(ctx, sessionID, questionID, answer, agentID)
		if err == nil {
			return nil
		}

		lastErr = err

		// Don't retry on validation errors or non-transient errors
		if strings.Contains(err.Error(), "validation") ||
			strings.Contains(err.Error(), "not accepted") ||
			strings.Contains(err.Error(), "not found") {
			return err
		}

		// Check if context cancelled before retrying
		if ctx.Err() != nil {
			return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		}

		// Calculate exponential backoff delay
		if attempt < maxRetries-1 {
			// Use uint directly to avoid int->uint conversion (gosec G115).
			// attempt is always non-negative (loop starts at 0) and capped at 30.
			var shift uint
			if attempt > 0 && attempt <= 30 {
				shift = uint(attempt) // #nosec G115 -- non-negative and bounded
			}
			delay := time.Duration(1<<shift) * baseDelay // 100ms, 200ms, 400ms, ...
			if delay > 5*time.Second {
				delay = 5 * time.Second // Cap at 5 seconds
			}

			select {
			case <-time.After(delay):
				// Continue to next retry
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during backoff: %w", ctx.Err())
			}
		}
	}

	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}
