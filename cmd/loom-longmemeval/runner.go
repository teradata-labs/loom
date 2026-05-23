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

//go:build fts5

package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

// RunMode determines how conversation history is processed.
type RunMode string

const (
	// ModeIngest feeds sessions through the agent via Weave so the agent
	// builds up memory (graph memory, conversation history, etc.), then
	// asks the question in a separate session.
	ModeIngest RunMode = "ingest"

	// ModeMultiSession creates a separate agent session per haystack session,
	// ending each one before starting the next. Messages are persisted to the
	// DB and become searchable via conversation_memory. The question is asked
	// in a fresh session — the agent must use graph memory + conversation_memory
	// to recall across prior sessions. This is the most faithful simulation of
	// how Loom works in production.
	ModeMultiSession RunMode = "multi-session"

	// ModeContextStuffing puts all session history directly into one
	// Weave call as prompt context. Baseline comparison.
	ModeContextStuffing RunMode = "context-stuffing"
)

// RunConfig holds configuration for a benchmark run.
type RunConfig struct {
	Mode        RunMode
	ServerAddr  string
	AgentID     string // target agent in multi-agent server (empty = default)
	Concurrency int
	Verbose     bool
	Isolate     bool // create a fresh agent per entry for graph memory isolation
}

// EntryResult holds the result of evaluating a single entry.
type EntryResult struct {
	QuestionID   string        `json:"question_id"`
	Question     string        `json:"question"`
	Hypothesis   string        `json:"hypothesis"`
	QuestionType string        `json:"question_type"`
	GroundTruth  string        `json:"ground_truth,omitempty"`
	Duration     time.Duration `json:"duration_ms"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	Sessions     int           `json:"sessions_ingested"`
	Error        string        `json:"error,omitempty"`
}

// Runner orchestrates the benchmark execution against a running Loom server.
type Runner struct {
	config    RunConfig
	logger    *zap.Logger
	client    loomv1.LoomServiceClient
	conn      *grpc.ClientConn
	baseAgent *loomv1.AgentConfig // cached config for isolation mode
}

// NewRunner creates a new benchmark runner that connects to a Loom gRPC server.
func NewRunner(cfg RunConfig, logger *zap.Logger) (*Runner, error) {
	conn, err := grpc.NewClient(cfg.ServerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", cfg.ServerAddr, err)
	}

	client := loomv1.NewLoomServiceClient(conn)

	// Verify connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.ListSessions(ctx, &loomv1.ListSessionsRequest{})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("server health check failed at %s: %w", cfg.ServerAddr, err)
	}

	logger.Info("connected to Loom server", zap.String("addr", cfg.ServerAddr))

	r := &Runner{
		config: cfg,
		logger: logger,
		client: client,
		conn:   conn,
	}

	// In isolate mode, build a base agent config for creating temp agents.
	// Each temp agent gets its own graph memory scope — no cross-entry contamination.
	if cfg.Isolate {
		r.baseAgent = &loomv1.AgentConfig{
			SystemPrompt: "You are a helpful assistant with excellent memory. " +
				"Pay close attention to dates, events, preferences, and factual details " +
				"mentioned in conversations. When asked about past conversations, " +
				"use your memory tools to recall relevant information.",
			Memory: &loomv1.MemoryConfig{
				GraphMemory: &loomv1.GraphMemoryConfig{
					Enabled:                       true,
					EnableExtraction:              true,
					ExtractionCadence:             1,
					ConversationExtractionCadence: 1,
					MaxEntitiesPerExtraction:      15,
					ContextBudgetPercent:          30,
					ExtractionTimeoutSeconds:      60,
					ExtractionWindowMessages:      30,
				},
			},
			Behavior: &loomv1.BehaviorConfig{
				MaxTurns:          50,
				MaxToolExecutions: 100,
			},
		}
		logger.Info("isolate mode: will create fresh agents per entry")
	}

	return r, nil
}

// Close closes the gRPC connection.
func (r *Runner) Close() error {
	return r.conn.Close()
}

// Run executes the benchmark across all entries.
func (r *Runner) Run(ctx context.Context, entries []Entry, resultCh chan<- EntryResult) error {
	sem := make(chan struct{}, r.config.Concurrency)
	var wg sync.WaitGroup
	var completed atomic.Int64
	total := len(entries)

loop:
	for i, entry := range entries {
		select {
		case <-ctx.Done():
			break loop
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(idx int, e Entry) {
			defer wg.Done()
			defer func() { <-sem }()

			result := r.runEntry(ctx, e)
			done := completed.Add(1)
			r.logger.Info("entry completed",
				zap.String("question_id", e.QuestionID),
				zap.String("type", e.QuestionType),
				zap.Int64("progress", done),
				zap.Int("total", total),
				zap.Duration("duration", result.Duration),
				zap.String("error", result.Error),
			)

			select {
			case resultCh <- result:
			case <-ctx.Done():
			}
		}(i, entry)
	}

	wg.Wait()
	return ctx.Err()
}

// runEntry evaluates a single LongMemEval entry.
func (r *Runner) runEntry(ctx context.Context, entry Entry) EntryResult {
	start := time.Now()
	result := EntryResult{
		QuestionID:   entry.QuestionID,
		Question:     entry.Question,
		QuestionType: entry.QuestionType,
		GroundTruth:  string(entry.Answer),
	}

	sessions, err := entry.SortedSessions()
	if err != nil {
		result.Error = fmt.Sprintf("sort sessions: %v", err)
		result.Duration = time.Since(start)
		return result
	}
	result.Sessions = len(sessions)

	// In isolate mode, create a temporary agent for this entry so graph
	// memory is completely scoped — no cross-entry contamination.
	var tempAgentID string
	if r.config.Isolate && r.baseAgent != nil {
		id, err := r.createTempAgent(ctx, entry.QuestionID)
		if err != nil {
			result.Error = fmt.Sprintf("create temp agent: %v", err)
			result.Duration = time.Since(start)
			return result
		}
		tempAgentID = id
		defer r.deleteTempAgent(ctx, tempAgentID)
	}

	// Use temp agent if available, otherwise the configured agent.
	agentID := r.config.AgentID
	if tempAgentID != "" {
		agentID = tempAgentID
	}

	switch r.config.Mode {
	case ModeIngest:
		result = r.runIngestWith(ctx, entry, sessions, result, agentID)
	case ModeMultiSession:
		result = r.runMultiSessionWith(ctx, entry, sessions, result, agentID)
	case ModeContextStuffing:
		result = r.runContextStuffingWith(ctx, entry, sessions, result, agentID)
	default:
		result.Error = fmt.Sprintf("unknown mode: %s", r.config.Mode)
	}

	result.Duration = time.Since(start)
	return result
}

// createTempAgent creates an ephemeral agent cloned from the base config.
// The agent gets its own graph memory store, ensuring full isolation.
func (r *Runner) createTempAgent(ctx context.Context, questionID string) (string, error) {
	// Clone via proto marshal/unmarshal to avoid copying the embedded
	// MessageState / sync.Mutex by value (govet copylocks).
	cfg := proto.Clone(r.baseAgent).(*loomv1.AgentConfig)
	cfg.Name = fmt.Sprintf("lme-tmp-%s", questionID)
	cfg.Description = fmt.Sprintf("LongMemEval temp agent for %s", questionID)

	info, err := r.client.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{
		Config: cfg,
	})
	if err != nil {
		return "", err
	}
	r.logger.Debug("created temp agent", zap.String("id", info.Id), zap.String("question", questionID))
	return info.Id, nil
}

// deleteTempAgent removes an ephemeral agent and its graph memory.
func (r *Runner) deleteTempAgent(ctx context.Context, agentID string) {
	_, err := r.client.DeleteAgent(ctx, &loomv1.DeleteAgentRequest{
		AgentId: agentID,
		Force:   true,
	})
	if err != nil {
		r.logger.Warn("failed to delete temp agent", zap.String("id", agentID), zap.Error(err))
	}
}

// weave sends a Weave RPC and accumulates token usage into result.
func (r *Runner) weave(ctx context.Context, sessionID, query, agentID string, result *EntryResult) (*loomv1.WeaveResponse, error) {
	req := &loomv1.WeaveRequest{
		Query:          query,
		SessionId:      sessionID,
		TimeoutSeconds: 120,
		AgentId:        agentID,
	}

	resp, err := r.client.Weave(ctx, req)
	if err != nil {
		return nil, err
	}

	if resp.Cost != nil && resp.Cost.LlmCost != nil {
		result.InputTokens += int(resp.Cost.LlmCost.InputTokens)
		result.OutputTokens += int(resp.Cost.LlmCost.OutputTokens)
	}

	return resp, nil
}

// createSession creates a session and returns its ID.
func (r *Runner) createSession(ctx context.Context, name, agentID string) (string, error) {
	req := &loomv1.CreateSessionRequest{
		Name:    name,
		AgentId: agentID,
	}

	resp, err := r.client.CreateSession(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Id, nil
}

// deleteSession cleans up a session.
func (r *Runner) deleteSession(ctx context.Context, sessionID string) {
	_, _ = r.client.DeleteSession(ctx, &loomv1.DeleteSessionRequest{
		SessionId: sessionID,
	})
}

// runIngestWith processes an entry by feeding sessions through Weave, then querying.
// All turns happen in a single session so the conversation history accumulates,
// compression triggers, and the agent can search back through everything.
func (r *Runner) runIngestWith(ctx context.Context, entry Entry, sessions []SessionWithDate, result EntryResult, agentID string) EntryResult {
	sessionID, err := r.createSession(ctx, fmt.Sprintf("lme-%s", entry.QuestionID), agentID)
	if err != nil {
		result.Error = fmt.Sprintf("create session: %v", err)
		return result
	}

	// Phase 1: Ingest all sessions in the same session.
	for i, sess := range sessions {
		ingestMsg := fmt.Sprintf(
			"The following is conversation session %d that took place on %s. "+
				"Read it carefully and remember all the key details, facts, preferences, "+
				"and any specific information mentioned.\n\n%s",
			i+1, sess.Date, FormatSession(sess),
		)

		_, err = r.weave(ctx, sessionID, ingestMsg, agentID, &result)
		if err != nil {
			result.Error = fmt.Sprintf("ingest session %d: %v", i, err)
			return result
		}
	}

	// Phase 2: Ask the question in the same session.
	questionMsg := fmt.Sprintf(
		"Current date: %s\n\n"+
			"Based on everything you remember from our past conversation sessions, "+
			"answer the following question. Be specific and concise. "+
			"If you don't have enough information to answer, say \"I don't know.\"\n\n"+
			"Question: %s",
		entry.QuestionDate, entry.Question,
	)

	resp, err := r.weave(ctx, sessionID, questionMsg, agentID, &result)
	if err != nil {
		result.Error = fmt.Sprintf("ask question: %v", err)
		return result
	}
	result.Hypothesis = resp.Text

	return result
}

// runMultiSessionWith processes an entry by creating a separate agent session per
// haystack session. Each session is ingested, then ended so its messages are
// persisted to the DB and become FTS5-searchable. The question is asked in a
// fresh session — the agent must use graph_memory + conversation_memory to recall.
func (r *Runner) runMultiSessionWith(ctx context.Context, entry Entry, sessions []SessionWithDate, result EntryResult, agentID string) EntryResult {
	// Phase 1: Ingest each haystack session in its own agent session.
	for i, sess := range sessions {
		sessName := fmt.Sprintf("lme-%s-s%d", entry.QuestionID, i+1)
		sessionID, err := r.createSession(ctx, sessName, agentID)
		if err != nil {
			result.Error = fmt.Sprintf("create session %d: %v", i, err)
			return result
		}

		// Feed the session content through Weave.
		ingestMsg := fmt.Sprintf(
			"The following is a conversation that took place on %s. "+
				"Read it carefully and remember all the key details, facts, preferences, "+
				"and any specific information mentioned.\n\n%s",
			sess.Date, FormatSession(sess),
		)

		_, err = r.weave(ctx, sessionID, ingestMsg, agentID, &result)
		if err != nil {
			result.Error = fmt.Sprintf("ingest session %d: %v", i, err)
			return result
		}

		// End the session — messages are now persisted and searchable.
		// Don't delete — we need the data in the DB for cross-session search.
	}

	// Phase 2: Ask the question in a fresh session.
	questionSessionID, err := r.createSession(ctx, fmt.Sprintf("lme-%s-q", entry.QuestionID), agentID)
	if err != nil {
		result.Error = fmt.Sprintf("create question session: %v", err)
		return result
	}

	questionMsg := fmt.Sprintf(
		"Current date: %s\n\n"+
			"Based on everything you remember from our past conversation sessions, "+
			"answer the following question. Be specific and concise. "+
			"If you don't have enough information to answer, say \"I don't know.\"\n\n"+
			"Question: %s",
		entry.QuestionDate, entry.Question,
	)

	resp, err := r.weave(ctx, questionSessionID, questionMsg, agentID, &result)
	if err != nil {
		result.Error = fmt.Sprintf("ask question: %v", err)
		return result
	}
	result.Hypothesis = resp.Text

	return result
}

// runContextStuffingWith processes an entry by stuffing all sessions into one Weave call.
func (r *Runner) runContextStuffingWith(ctx context.Context, entry Entry, sessions []SessionWithDate, result EntryResult, agentID string) EntryResult {
	sessionID, err := r.createSession(ctx, fmt.Sprintf("lme-%s-cs", entry.QuestionID), agentID)
	if err != nil {
		result.Error = fmt.Sprintf("create session: %v", err)
		return result
	}
	defer r.deleteSession(ctx, sessionID)

	allSessions := FormatAllSessions(sessions)

	prompt := fmt.Sprintf(
		"I will give you several history chats between you and a user. "+
			"Please answer the question based on the relevant chat history.\n\n"+
			"History Chats:\n\n%s\n"+
			"Current Date: %s\n"+
			"Question: %s\n"+
			"Answer:",
		allSessions, entry.QuestionDate, entry.Question,
	)

	resp, err := r.weave(ctx, sessionID, prompt, agentID, &result)
	if err != nil {
		result.Error = fmt.Sprintf("ask question: %v", err)
		return result
	}
	result.Hypothesis = resp.Text

	return result
}
