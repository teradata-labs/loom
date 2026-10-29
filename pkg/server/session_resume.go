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
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/metaagent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

// ResumeSession finishes a turn a crash interrupted. It runs on the agent the
// server itself built for the session, so the resumed turn has the same LLM
// provider, MCP tools and governance as the turn that crashed, and acts as
// the calling user.
//
// A resume executes tool calls and LLM requests, so it is a turn-executing
// entry point and is admitted like Weave: it takes the LLM slot-scheduling
// identity, parks at the door when max_active_conversations is reached, runs
// under a server span, and carries the agent's progress multiplexer.
func (s *MultiAgentServer) ResumeSession(ctx context.Context, req *loomv1.ResumeSessionRequest) (*loomv1.ResumeSessionResponse, error) {
	sessionID := req.GetSessionId()
	if sessionID == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	ag, agentID, err := s.sessionAgent(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	// The session exists (sessionAgent found it), so this is a resumed
	// conversation: its first LLM call classifies as in-flight.
	ctx = installTurnSlotInfo(ctx, true, sessionID, agentID)
	releaseDoor, doorErr := enterTurnDoor(ctx, s.logger)
	if doorErr != nil {
		return nil, doorErr
	}
	defer releaseDoor()

	s.mu.RLock()
	if pm, ok := s.progressMultiplexers[agentID]; ok {
		ctx = metaagent.WithProgress(ctx, pm)
	}
	tracer := s.tracer
	s.mu.RUnlock()

	var span *observability.Span
	if tracer != nil {
		ctx, span = tracer.StartSpan(ctx, "server.ResumeSession",
			observability.WithAttribute(observability.AttrSessionID, sessionID),
			observability.WithAttribute("agent.id", agentID),
		)
	}

	resp, err := s.runResumeSession(ctx, ag, sessionID)
	if span != nil {
		if err != nil {
			span.RecordError(err)
		} else {
			span.SetAttribute("resume.outcome", resp.GetOutcome().String())
		}
		tracer.EndSpan(span)
		s.RecordTraceSpan(span)
	}
	return resp, err
}

// runResumeSession runs the agent's resume and maps its outcome to the RPC
// response.
func (s *MultiAgentServer) runResumeSession(ctx context.Context, ag *agent.Agent, sessionID string) (*loomv1.ResumeSessionResponse, error) {
	resp, err := ag.ResumeSession(ctx, sessionID)
	var gap *agent.ExecutionGap
	var parked *agent.TurnParkedError
	switch {
	case err == nil:
		return &loomv1.ResumeSessionResponse{
			Outcome: loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_RESUMED,
			Content: resp.Content,
		}, nil
	case errors.As(err, &gap):
		return &loomv1.ResumeSessionResponse{
			Outcome: loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_EXECUTION_GAP,
			Gap:     executionGapProto(gap),
		}, nil
	case errors.As(err, &parked):
		return &loomv1.ResumeSessionResponse{
			Outcome: loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_PARKED,
		}, nil
	case errors.Is(err, agent.ErrNothingParked), errors.Is(err, agent.ErrNotParkedTail):
		return &loomv1.ResumeSessionResponse{
			Outcome: loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_NOTHING_TO_RESUME,
		}, nil
	}
	return nil, resumeStatus(err)
}

// ResolveExecutionGap records an operator's finding for the session's current
// execution gap, on the agent the server built for the session.
func (s *MultiAgentServer) ResolveExecutionGap(ctx context.Context, req *loomv1.ResolveExecutionGapRequest) (*loomv1.ResolveExecutionGapResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	var outcome agent.GapOutcome
	switch req.GetOutcome() {
	case loomv1.ExecutionGapOutcome_EXECUTION_GAP_OUTCOME_SUCCEEDED:
		if req.GetResultJson() == "" {
			return nil, status.Error(codes.InvalidArgument, "result_json is required when outcome is SUCCEEDED")
		}
		outcome = agent.GapSucceeded
	case loomv1.ExecutionGapOutcome_EXECUTION_GAP_OUTCOME_FAILED:
		outcome = agent.GapFailed
	default:
		return nil, status.Error(codes.InvalidArgument, "outcome must be SUCCEEDED or FAILED")
	}

	ag, _, err := s.sessionAgent(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	gap, err := ag.ResolveExecutionGap(ctx, req.GetSessionId(), outcome, []byte(req.GetResultJson()))
	if err != nil {
		if errors.Is(err, agent.ErrNoExecutionGap) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, resumeStatus(err)
	}
	return &loomv1.ResolveExecutionGapResponse{Gap: executionGapProto(gap)}, nil
}

// sessionAgent returns the agent that owns sessionID for the calling user,
// and that agent's id, with the same routing and isolation rules as Weave.
func (s *MultiAgentServer) sessionAgent(ctx context.Context, sessionID string) (*agent.Agent, string, error) {
	ag, agentID, exists, accessible := s.findSessionOwner(ctx, sessionID, postgres.UserIDFromContext(ctx))
	if !exists || !accessible {
		return nil, "", status.Error(codes.NotFound, "session not found")
	}
	if ag == nil {
		if agentID != "" {
			return nil, "", status.Errorf(codes.FailedPrecondition, "session belongs to agent %q, which is not registered", agentID)
		}
		return nil, "", status.Error(codes.FailedPrecondition, "session is not bound to an agent")
	}
	return ag, agentID, nil
}

// resumeStatus maps the errors ResumeSession and ResolveExecutionGap refuse
// with to gRPC status codes.
func resumeStatus(err error) error {
	var parked *agent.SessionParkedError
	switch {
	case isSessionLeaseError(err):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, agent.ErrEventLogDisabled),
		errors.Is(err, agent.ErrTurnNotRecorded),
		errors.Is(err, agent.ErrBatchNotSaved),
		errors.As(err, &parked):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	return status.Errorf(codes.Internal, "resume: %v", err)
}

func executionGapProto(gap *agent.ExecutionGap) *loomv1.ExecutionGap {
	return &loomv1.ExecutionGap{
		StepIndex:     int32(gap.StepIndex),     // #nosec G115 -- step counter, far below int32 range
		BatchPosition: int32(gap.BatchPosition), // #nosec G115 -- index within one LLM response's tool calls
		ToolName:      gap.ToolName,
		ToolUseId:     gap.ToolUseID,
	}
}
