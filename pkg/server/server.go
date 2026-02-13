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
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/llm/factory"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"github.com/teradata-labs/loom/pkg/shuttle/metadata"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// DefaultProgressBufferSize is the default buffer size for progress event channels
	DefaultProgressBufferSize = 10
)

// Server implements the LoomService gRPC server.
type Server struct {
	loomv1.UnimplementedLoomServiceServer

	agent         *agent.Agent
	sessionStore  *agent.SessionStore
	factory       *factory.ProviderFactory
	modelRegistry *factory.ModelRegistry
}

// NewServer creates a new LoomService server.
func NewServer(ag *agent.Agent, store *agent.SessionStore) *Server {
	return &Server{
		agent:         ag,
		sessionStore:  store,
		factory:       nil, // Set via SetProviderFactory
		modelRegistry: factory.NewModelRegistry(),
	}
}

// SetProviderFactory sets the LLM provider factory for dynamic model switching.
func (s *Server) SetProviderFactory(f *factory.ProviderFactory) {
	s.factory = f
}

// Weave executes a user query using the agent.
func (s *Server) Weave(ctx context.Context, req *loomv1.WeaveRequest) (*loomv1.WeaveResponse, error) {
	if req.Query == "" {
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}

	// Get or create session
	sessionID := req.SessionId
	if sessionID == "" {
		sessionID = GenerateSessionID()
	}

	// Execute agent chat
	resp, err := s.agent.Chat(ctx, sessionID, req.Query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "agent execution failed: %v", err)
	}

	// Convert response to proto format
	return &loomv1.WeaveResponse{
		Text:      resp.Content,
		SessionId: sessionID,
		Cost: &loomv1.CostInfo{
			LlmCost: &loomv1.LLMCost{
				Provider:     s.agent.GetLLMProviderName(),
				Model:        s.agent.GetLLMModel(),
				InputTokens:  types.SafeInt32(resp.Usage.InputTokens),
				OutputTokens: types.SafeInt32(resp.Usage.OutputTokens),
				CostUsd:      resp.Usage.CostUSD,
			},
			TotalCostUsd: resp.Usage.CostUSD,
		},
	}, nil
}

// StreamWeave streams agent execution progress.
func (s *Server) StreamWeave(req *loomv1.WeaveRequest, stream loomv1.LoomService_StreamWeaveServer) error {
	// Validate query
	if req.Query == "" {
		return status.Error(codes.InvalidArgument, "query cannot be empty")
	}

	// Generate session ID if not provided
	sessionID := req.SessionId
	if sessionID == "" {
		sessionID = GenerateSessionID()
	}

	// Channel to receive agent result
	type agentResult struct {
		resp *agent.Response
		err  error
	}
	resultChan := make(chan agentResult, 1)

	// Channel to receive progress events
	progressChan := make(chan agent.ProgressEvent, DefaultProgressBufferSize)

	// Create progress callback that sends events to channel
	progressCallback := func(event agent.ProgressEvent) {
		select {
		case progressChan <- event:
		case <-stream.Context().Done():
			// Context cancelled, stop sending
		}
	}

	// Execute agent with progress callback
	go func() {
		resp, err := s.agent.ChatWithProgress(stream.Context(), sessionID, req.Query, progressCallback)
		resultChan <- agentResult{resp: resp, err: err}
		close(progressChan) // Signal no more progress events
	}()

	// Stream progress events to client
	var finalResult *agentResult
	agentDone := false

	for {
		// If agent is done and progress channel is closed, process result
		if agentDone && progressChan == nil {
			break
		}

		select {
		case event, ok := <-progressChan:
			// Receive progress event from agent
			if !ok {
				// Channel closed, all progress events delivered
				progressChan = nil
				continue
			}

			// Convert agent progress to proto format
			protoProgress := &loomv1.WeaveProgress{
				Stage:          convertAgentStageToProto(event.Stage),
				Progress:       event.Progress,
				Message:        event.Message,
				ToolName:       event.ToolName,
				Timestamp:      event.Timestamp.Unix(),
				PartialContent: event.PartialContent,
				IsTokenStream:  event.IsTokenStream,
				TokenCount:     event.TokenCount,
				TtftMs:         event.TTFT,
			}

			// Include HITL request info if present
			if event.HITLRequest != nil {
				protoProgress.HitlRequest = convertHITLRequestToProto(event.HITLRequest)
			}

			// Send to client
			if err := stream.Send(protoProgress); err != nil {
				return err
			}

		case result := <-resultChan:
			// Agent finished - save result
			finalResult = &result
			agentDone = true
			// Continue to drain remaining progress events
			// The for loop condition (line 112) will exit when progressChan is also nil

		case <-stream.Context().Done():
			// Client cancelled
			return stream.Context().Err()
		}
	}

	// Process final agent result
	if finalResult == nil {
		return status.Error(codes.Internal, "no result received from agent")
	}

	if finalResult.err != nil {
		return status.Errorf(codes.Internal, "agent error: %v", finalResult.err)
	}

	// Send final completion event with result
	resp := finalResult.resp
	completionProgress := &loomv1.WeaveProgress{
		Stage:     loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED,
		Progress:  100,
		Message:   "Query completed successfully",
		Timestamp: time.Now().Unix(),
		PartialResult: &loomv1.ExecutionResult{
			Type:     "text",
			DataJson: resp.Content,
		},
	}

	return stream.Send(completionProgress)
}

// convertAgentStageToProto converts agent.ExecutionStage to proto ExecutionStage.
func convertAgentStageToProto(stage agent.ExecutionStage) loomv1.ExecutionStage {
	switch stage {
	case agent.StagePatternSelection:
		return loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION
	case agent.StageSchemaDiscovery:
		return loomv1.ExecutionStage_EXECUTION_STAGE_SCHEMA_DISCOVERY
	case agent.StageLLMGeneration:
		return loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION
	case agent.StageToolExecution:
		return loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION
	case agent.StageHumanInTheLoop:
		return loomv1.ExecutionStage_EXECUTION_STAGE_HUMAN_IN_THE_LOOP
	case agent.StageGuardrailCheck:
		return loomv1.ExecutionStage_EXECUTION_STAGE_GUARDRAIL_CHECK
	case agent.StageSelfCorrection:
		return loomv1.ExecutionStage_EXECUTION_STAGE_SELF_CORRECTION
	case agent.StageCompleted:
		return loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED
	case agent.StageFailed:
		return loomv1.ExecutionStage_EXECUTION_STAGE_FAILED
	default:
		return loomv1.ExecutionStage_EXECUTION_STAGE_UNSPECIFIED
	}
}

// convertHITLRequestToProto converts agent.HITLRequestInfo to proto HITLRequestInfo.
func convertHITLRequestToProto(info *agent.HITLRequestInfo) *loomv1.HITLRequestInfo {
	if info == nil {
		return nil
	}

	// Serialize context to JSON
	var contextJSON string
	if len(info.Context) > 0 {
		jsonBytes, err := json.Marshal(info.Context)
		if err == nil {
			contextJSON = string(jsonBytes)
		}
	}

	return &loomv1.HITLRequestInfo{
		RequestId:      info.RequestID,
		Question:       info.Question,
		RequestType:    info.RequestType,
		Priority:       info.Priority,
		TimeoutSeconds: types.SafeInt32(int(info.Timeout.Seconds())),
		ContextJson:    contextJSON,
	}
}

// CreateSession creates a new conversation session.
func (s *Server) CreateSession(ctx context.Context, req *loomv1.CreateSessionRequest) (*loomv1.Session, error) {
	sessionID := GenerateSessionID()

	// Create session without sending a message to the LLM
	session := s.agent.CreateSession(sessionID)

	return ConvertSession(session), nil
}

// GetSession retrieves session details.
func (s *Server) GetSession(ctx context.Context, req *loomv1.GetSessionRequest) (*loomv1.Session, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	session, ok := s.agent.GetSession(req.SessionId)
	if !ok {
		return nil, status.Error(codes.NotFound, "session not found")
	}

	return ConvertSession(session), nil
}

// ListSessions lists all sessions.
func (s *Server) ListSessions(ctx context.Context, req *loomv1.ListSessionsRequest) (*loomv1.ListSessionsResponse, error) {
	sessions := s.agent.ListSessions()

	protoSessions := make([]*loomv1.Session, len(sessions))
	for i, sess := range sessions {
		protoSessions[i] = ConvertSession(sess)
	}

	return &loomv1.ListSessionsResponse{
		Sessions: protoSessions,
	}, nil
}

// DeleteSession deletes a session.
func (s *Server) DeleteSession(ctx context.Context, req *loomv1.DeleteSessionRequest) (*loomv1.DeleteSessionResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	s.agent.DeleteSession(req.SessionId)

	// Also delete from persistent store
	if s.sessionStore != nil {
		if err := s.sessionStore.DeleteSession(ctx, req.SessionId); err != nil {
			// Log but don't fail
			zap.L().Warn("failed to delete session from persistent store",
				zap.String("session_id", req.SessionId),
				zap.Error(err))
		}
	}

	return &loomv1.DeleteSessionResponse{
		Success: true,
	}, nil
}

// GetConversationHistory retrieves conversation history.
func (s *Server) GetConversationHistory(ctx context.Context, req *loomv1.GetConversationHistoryRequest) (*loomv1.ConversationHistory, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	session, ok := s.agent.GetSession(req.SessionId)
	if !ok {
		return nil, status.Error(codes.NotFound, "session not found")
	}

	messages := session.GetMessages()
	protoMessages := make([]*loomv1.Message, len(messages))
	for i, msg := range messages {
		protoMessages[i] = ConvertMessage(&msg)
	}

	return &loomv1.ConversationHistory{
		SessionId: req.SessionId,
		Messages:  protoMessages,
	}, nil
}

// ListTools lists all registered tools.
// If req.Backend is specified, only tools for that backend are returned.
func (s *Server) ListTools(ctx context.Context, req *loomv1.ListToolsRequest) (*loomv1.ListToolsResponse, error) {
	// Get tools from agent's registry, optionally filtered by backend
	var tools []shuttle.Tool
	if req.Backend != "" {
		tools = s.agent.RegisteredToolsByBackend(req.Backend)
	} else {
		tools = s.agent.RegisteredTools()
	}

	protoTools := make([]*loomv1.ToolDefinition, len(tools))
	for i, tool := range tools {
		protoTools[i] = ConvertTool(tool)
	}

	return &loomv1.ListToolsResponse{
		Tools: protoTools,
	}, nil
}

// RegisterTool dynamically registers a new tool.
func (s *Server) RegisterTool(ctx context.Context, req *loomv1.RegisterToolRequest) (*loomv1.RegisterToolResponse, error) {
	return nil, status.Error(codes.Unimplemented, "dynamic tool registration not yet implemented")
}

// LoadPatterns loads pattern definitions.
func (s *Server) LoadPatterns(ctx context.Context, req *loomv1.LoadPatternsRequest) (*loomv1.LoadPatternsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "pattern loading not yet implemented")
}

// ListPatterns lists available patterns.
func (s *Server) ListPatterns(ctx context.Context, req *loomv1.ListPatternsRequest) (*loomv1.ListPatternsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "pattern listing not yet implemented")
}

// GetPattern retrieves a specific pattern.
func (s *Server) GetPattern(ctx context.Context, req *loomv1.GetPatternRequest) (*loomv1.Pattern, error) {
	return nil, status.Error(codes.Unimplemented, "get pattern not yet implemented")
}

// GetTrace retrieves execution trace.
func (s *Server) GetTrace(ctx context.Context, req *loomv1.GetTraceRequest) (*loomv1.Trace, error) {
	return nil, status.Error(codes.Unimplemented, "trace retrieval not yet implemented")
}

// GetHealth performs a health check.
func (s *Server) GetHealth(ctx context.Context, req *loomv1.GetHealthRequest) (*loomv1.HealthStatus, error) {
	return &loomv1.HealthStatus{
		Status:  "healthy",
		Version: "0.1.0",
	}, nil
}

// SwitchModel switches the LLM model/provider for a session.
func (s *Server) SwitchModel(ctx context.Context, req *loomv1.SwitchModelRequest) (*loomv1.SwitchModelResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.Provider == "" {
		return nil, status.Error(codes.InvalidArgument, "provider is required")
	}
	if req.Model == "" {
		return nil, status.Error(codes.InvalidArgument, "model is required")
	}

	// Get previous model info
	previousModel := &loomv1.ModelInfo{
		Id:       s.agent.GetLLMModel(),
		Name:     s.agent.GetLLMModel(),
		Provider: s.agent.GetLLMProviderName(),
	}

	// Check if factory is configured
	if s.factory == nil {
		return nil, status.Error(codes.FailedPrecondition, "model switching not available: provider factory not configured")
	}

	// Create new LLM provider using factory
	newProviderIface, err := s.factory.CreateProvider(req.Provider, req.Model)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to create provider: %v", err)
	}

	// Type assert to agent.LLMProvider
	newProvider, ok := newProviderIface.(agent.LLMProvider)
	if !ok {
		return nil, status.Error(codes.Internal, "failed to cast provider to LLMProvider interface")
	}

	// Switch the agent's LLM provider
	s.agent.SetLLMProvider(newProvider)

	// Get new model info
	newModel := &loomv1.ModelInfo{
		Id:       req.Model,
		Name:     req.Model,
		Provider: req.Provider,
	}

	return &loomv1.SwitchModelResponse{
		PreviousModel: previousModel,
		NewModel:      newModel,
		Success:       true,
	}, nil
}

// ListAvailableModels lists all available LLM models/providers.
func (s *Server) ListAvailableModels(ctx context.Context, req *loomv1.ListAvailableModelsRequest) (*loomv1.ListAvailableModelsResponse, error) {
	// Use factory to determine which models are actually available
	var models []*loomv1.ModelInfo
	if s.factory != nil {
		// Dynamic: only return models from configured providers
		models = s.modelRegistry.GetAvailableModels(s.factory)
	} else {
		// Fallback: return all models (legacy behavior)
		models = s.modelRegistry.GetAllModels()
	}

	// Apply provider filter if specified
	if req.ProviderFilter != "" {
		filtered := make([]*loomv1.ModelInfo, 0)
		for _, m := range models {
			if m.Provider == req.ProviderFilter {
				filtered = append(filtered, m)
			}
		}
		models = filtered
	}

	return &loomv1.ListAvailableModelsResponse{
		Models:     models,
		TotalCount: types.SafeInt32(len(models)),
	}, nil
}

// ListAvailableModelsLegacy is the old static implementation (deprecated).
func (s *Server) ListAvailableModelsLegacy(ctx context.Context, req *loomv1.ListAvailableModelsRequest) (*loomv1.ListAvailableModelsResponse, error) {
	// Static list of all supported models (kept for reference)
	models := []*loomv1.ModelInfo{
		// Anthropic Claude models
		{
			Id:                  "claude-sonnet-4.5",
			Name:                "Claude Sonnet 4.5",
			Provider:            "anthropic",
			Capabilities:        []string{"text", "vision", "tool-use"},
			ContextWindow:       200000,
			CostPer_1MInputUsd:  3.0,
			CostPer_1MOutputUsd: 15.0,
			Available:           true,
		},
		{
			Id:                  "claude-3-5-sonnet-20241022",
			Name:                "Claude 3.5 Sonnet",
			Provider:            "anthropic",
			Capabilities:        []string{"text", "vision", "tool-use"},
			ContextWindow:       200000,
			CostPer_1MInputUsd:  3.0,
			CostPer_1MOutputUsd: 15.0,
			Available:           true,
		},
		{
			Id:                  "claude-3-opus-20240229",
			Name:                "Claude 3 Opus",
			Provider:            "anthropic",
			Capabilities:        []string{"text", "vision", "tool-use"},
			ContextWindow:       200000,
			CostPer_1MInputUsd:  15.0,
			CostPer_1MOutputUsd: 75.0,
			Available:           true,
		},
		// AWS Bedrock models
		{
			Id:                  "anthropic.claude-sonnet-4-5-v1",
			Name:                "Claude Sonnet 4.5 (Bedrock)",
			Provider:            "bedrock",
			Capabilities:        []string{"text", "vision", "tool-use"},
			ContextWindow:       200000,
			CostPer_1MInputUsd:  3.0,
			CostPer_1MOutputUsd: 15.0,
			Available:           true,
		},
		{
			Id:                  "anthropic.claude-3-5-sonnet-20241022-v2:0",
			Name:                "Claude 3.5 Sonnet (Bedrock)",
			Provider:            "bedrock",
			Capabilities:        []string{"text", "vision", "tool-use"},
			ContextWindow:       200000,
			CostPer_1MInputUsd:  3.0,
			CostPer_1MOutputUsd: 15.0,
			Available:           true,
		},
		// Ollama local models
		{
			Id:                  "llama3.1",
			Name:                "Llama 3.1 (Ollama)",
			Provider:            "ollama",
			Capabilities:        []string{"text", "tool-use"},
			ContextWindow:       128000,
			CostPer_1MInputUsd:  0.0,
			CostPer_1MOutputUsd: 0.0,
			Available:           true,
		},
		{
			Id:                  "llama3.2",
			Name:                "Llama 3.2 (Ollama)",
			Provider:            "ollama",
			Capabilities:        []string{"text", "tool-use"},
			ContextWindow:       128000,
			CostPer_1MInputUsd:  0.0,
			CostPer_1MOutputUsd: 0.0,
			Available:           true,
		},
		{
			Id:                  "qwen2.5",
			Name:                "Qwen 2.5 (Ollama)",
			Provider:            "ollama",
			Capabilities:        []string{"text", "tool-use"},
			ContextWindow:       128000,
			CostPer_1MInputUsd:  0.0,
			CostPer_1MOutputUsd: 0.0,
			Available:           true,
		},
		// OpenAI models
		{
			Id:                  "gpt-4o",
			Name:                "GPT-4o",
			Provider:            "openai",
			Capabilities:        []string{"text", "vision", "tool-use"},
			ContextWindow:       128000,
			CostPer_1MInputUsd:  2.5,
			CostPer_1MOutputUsd: 10.0,
			Available:           true,
		},
		{
			Id:                  "gpt-4-turbo",
			Name:                "GPT-4 Turbo",
			Provider:            "openai",
			Capabilities:        []string{"text", "vision", "tool-use"},
			ContextWindow:       128000,
			CostPer_1MInputUsd:  10.0,
			CostPer_1MOutputUsd: 30.0,
			Available:           true,
		},
		// Azure OpenAI models
		{
			Id:                  "gpt-4o",
			Name:                "GPT-4o (Azure)",
			Provider:            "azureopenai",
			Capabilities:        []string{"text", "vision", "tool-use"},
			ContextWindow:       128000,
			CostPer_1MInputUsd:  2.5,
			CostPer_1MOutputUsd: 10.0,
			Available:           true,
		},
		// Google Gemini models
		{
			Id:                  "gemini-2.0-flash-exp",
			Name:                "Gemini 2.0 Flash",
			Provider:            "gemini",
			Capabilities:        []string{"text", "vision", "tool-use"},
			ContextWindow:       1000000,
			CostPer_1MInputUsd:  0.0,
			CostPer_1MOutputUsd: 0.0,
			Available:           true,
		},
		{
			Id:                  "gemini-1.5-pro",
			Name:                "Gemini 1.5 Pro",
			Provider:            "gemini",
			Capabilities:        []string{"text", "vision", "tool-use"},
			ContextWindow:       2000000,
			CostPer_1MInputUsd:  1.25,
			CostPer_1MOutputUsd: 5.0,
			Available:           true,
		},
		// Mistral models
		{
			Id:                  "mistral-large-latest",
			Name:                "Mistral Large",
			Provider:            "mistral",
			Capabilities:        []string{"text", "tool-use"},
			ContextWindow:       128000,
			CostPer_1MInputUsd:  2.0,
			CostPer_1MOutputUsd: 6.0,
			Available:           true,
		},
		{
			Id:                  "mistral-small-latest",
			Name:                "Mistral Small",
			Provider:            "mistral",
			Capabilities:        []string{"text", "tool-use"},
			ContextWindow:       32000,
			CostPer_1MInputUsd:  0.2,
			CostPer_1MOutputUsd: 0.6,
			Available:           true,
		},
		// HuggingFace models
		{
			Id:                  "meta-llama/Llama-3.1-70B-Instruct",
			Name:                "Llama 3.1 70B (HuggingFace)",
			Provider:            "huggingface",
			Capabilities:        []string{"text", "tool-use"},
			ContextWindow:       128000,
			CostPer_1MInputUsd:  0.0,
			CostPer_1MOutputUsd: 0.0,
			Available:           true,
		},
	}

	// Apply filters if provided
	filtered := models
	if req.ProviderFilter != "" {
		var temp []*loomv1.ModelInfo
		for _, m := range filtered {
			if m.Provider == req.ProviderFilter {
				temp = append(temp, m)
			}
		}
		filtered = temp
	}

	return &loomv1.ListAvailableModelsResponse{
		Models:     filtered,
		TotalCount: types.SafeInt32(len(filtered)),
	}, nil
}

// RequestToolPermission requests user permission to execute a tool.
func (s *Server) RequestToolPermission(ctx context.Context, req *loomv1.ToolPermissionRequest) (*loomv1.ToolPermissionResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.ToolName == "" {
		return nil, status.Error(codes.InvalidArgument, "tool_name is required")
	}

	// TODO: Implement permission request mechanism
	// This should:
	// 1. Store the permission request
	// 2. Send notification to client (via streaming or callback)
	// 3. Wait for user response with timeout
	// 4. Return the user's decision
	return nil, status.Error(codes.Unimplemented, "tool permission requests not yet implemented")
}

// Helper functions

// ConvertSession converts an agent.Session to proto format.
func ConvertSession(s *agent.Session) *loomv1.Session {
	return &loomv1.Session{
		Id:                s.ID,
		CreatedAt:         s.CreatedAt.Unix(),
		UpdatedAt:         s.UpdatedAt.Unix(),
		State:             "active",
		TotalCostUsd:      s.TotalCostUSD,
		ConversationCount: s.MessageCount(),
	}
}

// ConvertMessage converts an agent.Message to proto format.
func ConvertMessage(m *agent.Message) *loomv1.Message {
	return &loomv1.Message{
		Id:        m.ID,
		Role:      m.Role,
		Content:   m.Content,
		Timestamp: m.Timestamp.Unix(),
	}
}

// ConvertTool converts a shuttle.Tool to proto format with rich metadata.
// Attempts to load metadata from YAML files; falls back to basic info if not found.
// Logs warnings if metadata loading fails but continues with basic tool definition.
func ConvertTool(t shuttle.Tool) *loomv1.ToolDefinition {
	def := &loomv1.ToolDefinition{
		Name:        t.Name(),
		Description: t.Description(),
	}

	// Try to load rich metadata for builtin tools
	meta, err := builtin.LoadMetadata(t.Name())
	if err != nil {
		// Log error but continue with basic definition
		fmt.Fprintf(os.Stderr, "Warning: failed to load metadata for tool %s: %v\n", t.Name(), err)
		return def
	}
	if meta == nil {
		// No metadata file found (not an error - tool may not have metadata yet)
		return def
	}

	// Populate rich metadata fields
	def.Category = meta.Category
	def.Capabilities = meta.Capabilities
	def.Keywords = meta.Keywords
	def.Providers = meta.Providers
	def.BestPractices = meta.BestPractices

	// Convert use cases
	def.UseCases = convertUseCases(meta.UseCases)

	// Convert conflicts
	def.Conflicts = convertConflicts(meta.Conflicts)

	// Convert alternatives
	def.Alternatives = convertAlternatives(meta.Alternatives)

	// Convert complements
	def.Complements = convertComplements(meta.Complements)

	// Convert examples
	def.Examples = convertExamples(meta.Examples)

	// Convert prerequisites
	def.Prerequisites = convertPrerequisites(meta.Prerequisites)

	// Convert common errors
	def.CommonErrors = convertCommonErrors(meta.CommonErrors)

	// Convert rate limit
	if meta.RateLimit != nil {
		def.RateLimit = convertRateLimit(meta.RateLimit)
	}

	return def
}

// Helper functions to convert metadata types to proto types

func convertUseCases(useCases []metadata.UseCase) []*loomv1.ToolUseCase {
	result := make([]*loomv1.ToolUseCase, len(useCases))
	for i, uc := range useCases {
		result[i] = &loomv1.ToolUseCase{
			Title:     uc.Title,
			WhenToUse: uc.WhenToUse,
			Example:   uc.Example,
			NotFor:    uc.NotFor,
		}
	}
	return result
}

func convertConflicts(conflicts []metadata.Conflict) []*loomv1.ToolConflict {
	result := make([]*loomv1.ToolConflict, len(conflicts))
	for i, c := range conflicts {
		result[i] = &loomv1.ToolConflict{
			ToolName:        c.ToolName,
			Reason:          c.Reason,
			WhenPreferThis:  c.WhenPreferThis,
			WhenPreferOther: c.WhenPreferOther,
			Severity:        c.Severity,
		}
	}
	return result
}

func convertAlternatives(alternatives []metadata.Alternative) []*loomv1.ToolAlternative {
	result := make([]*loomv1.ToolAlternative, len(alternatives))
	for i, a := range alternatives {
		result[i] = &loomv1.ToolAlternative{
			ToolName: a.ToolName,
			When:     a.When,
			Benefits: a.Benefits,
		}
	}
	return result
}

func convertComplements(complements []metadata.Complement) []*loomv1.ToolComplement {
	result := make([]*loomv1.ToolComplement, len(complements))
	for i, c := range complements {
		result[i] = &loomv1.ToolComplement{
			ToolName: c.ToolName,
			Scenario: c.Scenario,
			Example:  c.Example,
		}
	}
	return result
}

func convertExamples(examples []metadata.Example) []*loomv1.ToolExample {
	result := make([]*loomv1.ToolExample, len(examples))
	for i, e := range examples {
		// Marshal input/output to JSON strings
		inputJSON, _ := json.Marshal(e.Input)
		outputJSON, _ := json.Marshal(e.Output)

		// Combine name and description for proto description field
		desc := e.Name
		if e.Description != "" {
			desc = desc + ": " + e.Description
		}
		if e.Explanation != "" {
			desc = desc + " (" + e.Explanation + ")"
		}

		result[i] = &loomv1.ToolExample{
			Description: desc,
			Input:       string(inputJSON),
			Output:      string(outputJSON),
		}
	}
	return result
}

func convertPrerequisites(prerequisites []metadata.Prerequisite) []*loomv1.ToolPrerequisite {
	result := make([]*loomv1.ToolPrerequisite, len(prerequisites))
	for i, p := range prerequisites {
		result[i] = &loomv1.ToolPrerequisite{
			Name:        p.Name,
			RequiredFor: p.RequiredFor,
			EnvVars:     p.EnvVars,
			HowToGet:    p.HowToGet,
			Fallback:    p.Fallback,
		}
	}
	return result
}

func convertCommonErrors(errors []metadata.CommonError) []*loomv1.ToolCommonError {
	result := make([]*loomv1.ToolCommonError, len(errors))
	for i, e := range errors {
		result[i] = &loomv1.ToolCommonError{
			Error:    e.Error,
			Cause:    e.Cause,
			Solution: e.Solution,
		}
	}
	return result
}

func convertRateLimit(rateLimit *metadata.RateLimit) *loomv1.RateLimitInfo {
	// The proto RateLimitInfo has fixed fields (requests_per_minute, requests_per_hour)
	// but metadata.RateLimit is flexible (map of provider -> limit per month)
	// For now, return basic info with notes about provider-specific limits

	notes := rateLimit.Notes
	if len(rateLimit.Limits) > 0 {
		if notes != "" {
			notes += ". "
		}
		notes += "Provider limits: "
		first := true
		for provider, limit := range rateLimit.Limits {
			if !first {
				notes += ", "
			}
			notes += fmt.Sprintf("%s: %d/month", provider, limit)
			first = false
		}
	}

	return &loomv1.RateLimitInfo{
		RequestsPerMinute: 0, // Not specified in metadata
		RequestsPerHour:   0, // Not specified in metadata
		Notes:             notes,
	}
}

// GenerateSessionID generates a new session ID.
func GenerateSessionID() string {
	return fmt.Sprintf("sess_%s", uuid.New().String()[:8])
}
