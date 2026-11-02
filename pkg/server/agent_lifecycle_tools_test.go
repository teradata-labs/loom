// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
)

func TestAgentLifecycle_DirectCreateHonorsDisabledTools(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "disabled"}[disabled], func(t *testing.T) {
			srv := newTestMultiAgentServer(t)
			require.Nil(t, srv.registry)
			info, err := srv.CreateAgentFromConfig(context.Background(), &loomv1.CreateAgentRequest{
				Config: &loomv1.AgentConfig{Name: "tool-policy", Tools: &loomv1.ToolsConfig{Disabled: disabled}},
			})
			require.NoError(t, err)
			ag, _, err := srv.getAgent(info.Id)
			require.NoError(t, err)
			ag.RegisterTool(builtin.ByName("http_request"))
			if disabled {
				assert.Empty(t, ag.ListTools(), "server AddAgent and later registration must preserve disabling")
			} else {
				assert.Contains(t, ag.ListTools(), "http_request")
			}
		})
	}
}

func TestAgentLifecycle_DirectReloadHonorsDisabledTools(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	require.Nil(t, srv.registry)
	ctx := context.Background()
	info, err := srv.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{Config: &loomv1.AgentConfig{Name: "reload-tools"}})
	require.NoError(t, err)
	previous, _, err := srv.getAgent(info.Id)
	require.NoError(t, err)
	require.NotEmpty(t, previous.ListTools())

	for _, disabled := range []bool{true, false} {
		info, err = srv.ReloadAgent(ctx, &loomv1.ReloadAgentRequest{
			AgentId: info.Id,
			Config:  &loomv1.AgentConfig{Name: "reload-tools", Tools: &loomv1.ToolsConfig{Disabled: disabled}},
		})
		require.NoError(t, err)
		replacement, _, err := srv.getAgent(info.Id)
		require.NoError(t, err)
		assert.NotSame(t, previous, replacement)
		assert.Equal(t, previous.GetID(), replacement.GetID())
		replacement.RegisterTool(builtin.ByName("http_request"))
		if disabled {
			assert.Empty(t, replacement.ListTools(), "server UpdateAgent must not restore tools")
		} else {
			assert.Contains(t, replacement.ListTools(), "http_request", "reload may re-enable tools")
		}
		previous = replacement
	}
}
