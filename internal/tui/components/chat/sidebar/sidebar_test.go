// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package sidebar

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/internal/session"
)

// TestCurrentModelBlock_NoHardcodedDefault is a regression test for #170:
// before any model source is known, the sidebar used to render the hardcoded
// "Claude Sonnet 4" default as the active model — wrong whenever the selected
// agent was configured with a different model. It must instead show a neutral
// placeholder, and the real model once one is available.
func TestCurrentModelBlock_NoHardcodedDefault(t *testing.T) {
	t.Run("placeholder when no model source is known", func(t *testing.T) {
		s := &sidebarCmp{}
		out := s.currentModelBlock()
		assert.NotContains(t, out, "Claude Sonnet 4",
			"must not display the hardcoded default model as active")
		assert.Contains(t, out, "detecting model",
			"should show a neutral placeholder until the model resolves")
	})

	t.Run("uses the session model from the LLM cost report", func(t *testing.T) {
		s := &sidebarCmp{session: session.Session{Model: "claude-opus-4-5", Provider: "anthropic"}}
		out := s.currentModelBlock()
		assert.Contains(t, out, "anthropic/claude-opus-4-5")
		assert.NotContains(t, out, "detecting model")
	})

	t.Run("uses the selected agent's configured model", func(t *testing.T) {
		s := &sidebarCmp{
			currentAgent: "weaver",
			agents:       []AgentInfo{{ID: "weaver", ModelInfo: "anthropic/claude-opus-4-5"}},
		}
		out := s.currentModelBlock()
		assert.Contains(t, out, "anthropic/claude-opus-4-5")
		assert.NotContains(t, out, "detecting model")
	})
}
