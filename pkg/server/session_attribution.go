// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
)

func sessionMetadataFromContext(s *agent.Session) map[string]string {
	if s == nil || s.Context == nil {
		return nil
	}
	keys := []string{"project_id", "conversation_id", "backend"}
	out := make(map[string]string)
	for _, k := range keys {
		if v, ok := artifacts.StringFromContext(s.Context, k); ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeArtifactMetadataIntoProto(p *loomv1.Session, sessionID string) {
	if p == nil || sessionID == "" {
		return
	}
	meta, err := artifacts.ReadSessionArtifactMetadata(sessionID)
	if err != nil {
		return
	}
	if meta.AgentID != "" {
		p.AgentId = meta.AgentID
	}
	if meta.AgentName != "" {
		p.AgentName = meta.AgentName
	}
	if meta.StartedAt != "" {
		p.StartedAt = meta.StartedAt
	}
	if meta.EndedAt != "" {
		p.EndedAt = meta.EndedAt
	}
	if meta.Status != "" {
		p.MetadataStatus = meta.Status
	}
	if meta.Artifacts != nil {
		p.ArtifactCount = int32(meta.Artifacts.Created)
	}
	filtered := artifacts.FilterPublicArtifactContext(meta.Context)
	if len(filtered) > 0 {
		if p.Metadata == nil {
			p.Metadata = make(map[string]string)
		}
		for k, v := range filtered {
			p.Metadata[k] = v
		}
	}
}
