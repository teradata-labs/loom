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

// Session-scoped artifact lookups.
//
// Loom keeps one artifact catalog per server, shared by every surface that
// talks to it: a file written through the workspace tool in one client is
// listable from all of them. The general artifact methods live in client.go;
// these two exist because names are only unique per session, and until the
// wire carried session_id a remote surface could not ask "the files this
// session produced" at all.

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// ListSessionArtifacts returns the artifacts belonging to one session.
//
// This is the "files this session produced" listing. limit of 0 asks for the
// server default.
func (c *Client) ListSessionArtifacts(ctx context.Context, sessionID string, limit, offset int32) ([]*loomv1.Artifact, error) {
	req := &loomv1.ListArtifactsRequest{
		SessionId: sessionID,
		Limit:     limit,
		Offset:    offset,
	}

	resp, err := c.client.ListArtifacts(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Artifacts, nil
}

// GetArtifactByName resolves an artifact by name within a session.
//
// Names are only unique per session, so the session is required here: a bare
// name lookup from outside the session's own call context is ambiguous.
func (c *Client) GetArtifactByName(ctx context.Context, name, sessionID string) (*loomv1.Artifact, error) {
	req := &loomv1.GetArtifactRequest{
		Name:      name,
		SessionId: sessionID,
	}

	resp, err := c.client.GetArtifact(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Artifact, nil
}
