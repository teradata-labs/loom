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
)

// CompactSession compacts a session's context on the agent the server built
// for it, with the same routing and isolation rules as Weave.
func (s *MultiAgentServer) CompactSession(ctx context.Context, req *loomv1.CompactSessionRequest) (*loomv1.CompactSessionResponse, error) {
	if err := validateCompactSessionRequest(req); err != nil {
		return nil, err
	}
	ag, _, err := s.sessionAgent(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	return compactSessionResponse(ag.CompactSession(ctx, req.GetSessionId(), compactOptions(req)))
}

// CompactSession compacts a session's context on the server's agent.
func (s *Server) CompactSession(ctx context.Context, req *loomv1.CompactSessionRequest) (*loomv1.CompactSessionResponse, error) {
	if err := validateCompactSessionRequest(req); err != nil {
		return nil, err
	}
	if _, ok := s.agent.GetSession(req.GetSessionId()); !ok {
		return nil, status.Error(codes.NotFound, "session not found")
	}
	return compactSessionResponse(s.agent.CompactSession(ctx, req.GetSessionId(), compactOptions(req)))
}

func validateCompactSessionRequest(req *loomv1.CompactSessionRequest) error {
	switch {
	case req.GetSessionId() == "":
		return status.Error(codes.InvalidArgument, "session_id is required")
	case req.GetKeepTurns() < 0:
		return status.Error(codes.InvalidArgument, "keep_turns must not be negative")
	case len(req.GetFocus()) > agent.MaxCompactFocusBytes:
		return status.Errorf(codes.InvalidArgument, "focus must be at most %d bytes", agent.MaxCompactFocusBytes)
	}
	return nil
}

func compactOptions(req *loomv1.CompactSessionRequest) agent.CompactOptions {
	return agent.CompactOptions{Focus: req.GetFocus(), KeepTurns: int(req.GetKeepTurns())}
}

// compactSessionResponse maps CompactSession's result to the RPC response.
func compactSessionResponse(res agent.CompactResult, err error) (*loomv1.CompactSessionResponse, error) {
	if err != nil {
		return nil, compactSessionStatus(err)
	}
	return &loomv1.CompactSessionResponse{
		Compacted:      res.Compacted,
		TurnsFolded:    res.TurnsFolded,
		TurnsKept:      int32(res.TurnsKept), // #nosec G115 -- at most the session's turn count
		TokensBefore:   int64(res.TokensBefore),
		TokensAfter:    int64(res.TokensAfter),
		SummaryVersion: int32(res.SummaryVersion), // #nosec G115 -- one version per fold
	}, nil
}

// compactSessionStatus maps the errors CompactSession refuses with to gRPC
// status codes. Nothing in the session changed in any of these cases.
func compactSessionStatus(err error) error {
	var parked *agent.SessionParkedError
	switch {
	case isSessionLeaseError(err), errors.Is(err, agent.ErrCompactionBusy):
		return status.Error(codes.Aborted, err.Error())
	case errors.As(err, &parked):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, agent.ErrSummaryUnavailable), errors.Is(err, agent.ErrCompactionNotSaved), errors.Is(err, agent.ErrSessionNotLoaded):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	return status.Errorf(codes.Internal, "compact: %v", err)
}
