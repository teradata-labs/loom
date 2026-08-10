// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDefaultKeyMap_ReadlineKeysFreed is a regression test for #171: the global
// keymap must not bind the editor's readline editing keys (ctrl+a/e/k/u/w), so
// they fall through to the chat input's textarea (LineStart/LineEnd/kill/word-
// delete). The agents/workflows panels move to alt+a/alt+w, and the command
// palette keeps ctrl+p (ctrl+k is freed for kill-to-end-of-line).
func TestDefaultKeyMap_ReadlineKeysFreed(t *testing.T) {
	km := DefaultKeyMap()

	assert.Equal(t, []string{"alt+a"}, km.AgentsDialog.Keys(), "agents dialog should move to alt+a")
	assert.Equal(t, []string{"alt+w"}, km.WorkflowsDialog.Keys(), "workflows dialog should move to alt+w")
	assert.Equal(t, []string{"ctrl+p"}, km.Commands.Keys(), "commands should keep ctrl+p only (ctrl+k freed)")

	readline := map[string]bool{"ctrl+a": true, "ctrl+e": true, "ctrl+k": true, "ctrl+u": true, "ctrl+w": true}
	for name, keys := range map[string][]string{
		"AgentsDialog":    km.AgentsDialog.Keys(),
		"WorkflowsDialog": km.WorkflowsDialog.Keys(),
		"Commands":        km.Commands.Keys(),
		"Sessions":        km.Sessions.Keys(),
	} {
		for _, k := range keys {
			assert.Falsef(t, readline[k], "%s must not bind editor readline key %q", name, k)
		}
	}
}
