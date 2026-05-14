// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	listSessionsDefaultPageSize int32 = 50
	listSessionsMaxPageSize     int32 = 500
)

// normalizeListSessionsLimit applies server-side defaults and a hard cap. Values <= 0 use the default.
func validateListSessionsRequest(req *loomv1.ListSessionsRequest) error {
	if req != nil && req.Offset < 0 {
		return status.Error(codes.InvalidArgument, "offset must be >= 0")
	}
	return nil
}

// listSessionsNeedsArtifactDisk reads metadata.json per session when filters depend on merged fields.
func listSessionsNeedsArtifactDisk(req *loomv1.ListSessionsRequest) bool {
	if !artifacts.SessionMetadataEnabled() {
		return false
	}
	if req == nil {
		return false
	}
	return req.MetadataStatus != "" || req.ProjectId != ""
}

func normalizeListSessionsLimit(limit int32) int32 {
	switch {
	case limit <= 0:
		return listSessionsDefaultPageSize
	case limit > listSessionsMaxPageSize:
		return listSessionsMaxPageSize
	default:
		return limit
	}
}

func filterListSessions(sessions []*loomv1.Session, req *loomv1.ListSessionsRequest) []*loomv1.Session {
	if req == nil {
		return sessions
	}
	out := make([]*loomv1.Session, 0, len(sessions))
	for _, p := range sessions {
		if p == nil {
			continue
		}
		if req.State != "" && p.State != req.State {
			continue
		}
		if req.Backend != "" && p.Backend != req.Backend {
			continue
		}
		if req.AgentId != "" && p.AgentId != req.AgentId {
			continue
		}
		if req.ProjectId != "" {
			if p.Metadata == nil || p.Metadata["project_id"] != req.ProjectId {
				continue
			}
		}
		if req.MetadataStatus != "" && p.MetadataStatus != req.MetadataStatus {
			continue
		}
		out = append(out, p)
	}
	return out
}

func pageProtoSessions(sessions []*loomv1.Session, offset, limit int32) []*loomv1.Session {
	o := int(offset)
	if o < 0 {
		o = 0
	}
	if o >= len(sessions) {
		return nil
	}
	lim := int(normalizeListSessionsLimit(limit))
	end := o + lim
	if end > len(sessions) {
		end = len(sessions)
	}
	return sessions[o:end]
}
