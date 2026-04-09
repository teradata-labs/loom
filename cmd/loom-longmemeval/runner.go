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
)

// RunMode determines how conversation history is processed.
type RunMode string

const (
	// ModeIngest feeds sessions through the agent via Weave so the agent
	// builds up memory (graph memory, conversation history, etc.), then
	// asks the question in a separate session.
	ModeIngest RunMode = "ingest"

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
}

// EntryResult holds the result of evaluating a single entry.
type EntryResult struct {
	QuestionID   string        `json:"question_id"`
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
	config RunConfig
	logger *zap.Logger
	client loomv1.LoomServiceClient
	conn   *grpc.ClientConn
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

	return &Runner{
		config: cfg,
		logger: logger,
		client: client,
		conn:   conn,
	}, nil
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

	for i, entry := range entries {
		select {
		case <-ctx.Done():
			break
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

	switch r.config.Mode {
	case ModeIngest:
		result = r.runIngest(ctx, entry, sessions, result)
	case ModeContextStuffing:
		result = r.runContextStuffing(ctx, entry, sessions, result)
	default:
		result.Error = fmt.Sprintf("unknown mode: %s", r.config.Mode)
	}

	result.Duration = time.Since(start)
	return result
}

// weave sends a Weave RPC and accumulates token usage into result.
func (r *Runner) weave(ctx context.Context, sessionID, query string, result *EntryResult) (*loomv1.WeaveResponse, error) {
	req := &loomv1.WeaveRequest{
		Query:          query,
		SessionId:      sessionID,
		TimeoutSeconds: 120,
		AgentId:        r.config.AgentID,
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
func (r *Runner) createSession(ctx context.Context, name string) (string, error) {
	req := &loomv1.CreateSessionRequest{
		Name:    name,
		AgentId: r.config.AgentID,
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

// runIngest processes an entry by feeding sessions through Weave, then querying.
// All turns happen in a single session so the conversation history accumulates,
// compression triggers, and the agent can search back through everything.
func (r *Runner) runIngest(ctx context.Context, entry Entry, sessions []SessionWithDate, result EntryResult) EntryResult {
	sessionID, err := r.createSession(ctx, fmt.Sprintf("lme-%s", entry.QuestionID))
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

		_, err = r.weave(ctx, sessionID, ingestMsg, &result)
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

	resp, err := r.weave(ctx, sessionID, questionMsg, &result)
	if err != nil {
		result.Error = fmt.Sprintf("ask question: %v", err)
		return result
	}
	result.Hypothesis = resp.Text

	return result
}

// runContextStuffing processes an entry by stuffing all sessions into one Weave call.
func (r *Runner) runContextStuffing(ctx context.Context, entry Entry, sessions []SessionWithDate, result EntryResult) EntryResult {
	sessionID, err := r.createSession(ctx, fmt.Sprintf("lme-%s-cs", entry.QuestionID))
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

	resp, err := r.weave(ctx, sessionID, prompt, &result)
	if err != nil {
		result.Error = fmt.Sprintf("ask question: %v", err)
		return result
	}
	result.Hypothesis = resp.Text

	return result
}
