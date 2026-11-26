// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingEphemeralHandler captures the spawn request the tool builds.
type recordingEphemeralHandler struct {
	spawned *SpawnSubAgentRequest
}

func (h *recordingEphemeralHandler) SpawnSubAgent(_ context.Context, req *SpawnSubAgentRequest) (*SpawnSubAgentResponse, error) {
	h.spawned = req
	return &SpawnSubAgentResponse{SubAgentID: "child-1", SessionID: req.ParentSessionID, Status: "completed", Output: "ok"}, nil
}

func (h *recordingEphemeralHandler) DespawnSubAgent(_ context.Context, req *DespawnSubAgentRequest) (*DespawnSubAgentResponse, error) {
	return &DespawnSubAgentResponse{SubAgentID: req.SubAgentID, SessionID: req.ParentSessionID, Status: "despawned"}, nil
}

func (h *recordingEphemeralHandler) ListAvailableAgents(_ context.Context, _ *ListAvailableAgentsRequest) (*ListAvailableAgentsResponse, error) {
	return &ListAvailableAgentsResponse{}, nil
}

func TestManageEphemeralAgents_SpawnTimeoutSeconds(t *testing.T) {
	tests := []struct {
		name        string
		raw         any // value for params["timeout_seconds"]; nil = absent
		wantSeconds int
		wantErrCode string
	}{
		{name: "absent means not specified", raw: nil, wantSeconds: 0},
		{name: "JSON number", raw: float64(600), wantSeconds: 600},
		{name: "integer", raw: 45, wantSeconds: 45},
		{name: "numeric string", raw: "120", wantSeconds: 120},
		{name: "blank string means not specified", raw: "  ", wantSeconds: 0},
		{name: "zero is rejected", raw: float64(0), wantErrCode: "INVALID_TIMEOUT"},
		{name: "negative is rejected", raw: float64(-5), wantErrCode: "INVALID_TIMEOUT"},
		{name: "fraction is rejected", raw: 1.5, wantErrCode: "INVALID_TIMEOUT"},
		{name: "non-numeric string is rejected", raw: "soon", wantErrCode: "INVALID_TIMEOUT"},
		{name: "boolean is rejected", raw: true, wantErrCode: "INVALID_TIMEOUT"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := &recordingEphemeralHandler{}
			tool := NewManageEphemeralAgentsTool(handler, "sess-1", "parent-1")
			params := map[string]any{"command": "spawn", "preset": "quick_chat", "initial_message": "go"}
			if tc.raw != nil {
				params["timeout_seconds"] = tc.raw
			}

			res, err := tool.Execute(context.Background(), params)
			require.NoError(t, err)
			require.NotNil(t, res)

			if tc.wantErrCode != "" {
				assert.False(t, res.Success)
				require.NotNil(t, res.Error)
				assert.Equal(t, tc.wantErrCode, res.Error.Code)
				assert.Nil(t, handler.spawned, "an invalid timeout must never reach the handler")
				return
			}

			assert.True(t, res.Success)
			require.NotNil(t, handler.spawned)
			assert.Equal(t, tc.wantSeconds, handler.spawned.TimeoutSeconds)
		})
	}
}

func TestManageEphemeralAgents_SchemaAdvertisesTimeout(t *testing.T) {
	tool := NewManageEphemeralAgentsTool(&recordingEphemeralHandler{}, "sess-1", "parent-1")
	schema := tool.InputSchema()
	require.NotNil(t, schema)
	prop, ok := schema.Properties["timeout_seconds"]
	require.True(t, ok, "timeout_seconds must be advertised so the model can size a sub-agent's budget")
	assert.Equal(t, "number", prop.Type)
	assert.NotContains(t, schema.Required, "timeout_seconds", "the parameter is optional")
}
