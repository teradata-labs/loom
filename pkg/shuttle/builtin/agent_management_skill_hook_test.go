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

package builtin

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/session"
)

// TestAgentManagementTool_SkillWriteHook locks the contract that
// cmd/looms/cmd_serve.go relies on: after a successful skill YAML
// write via create_skill, the registered SkillWriteHook fires once
// with the skill name and the absolute file path.
//
// The cmd_serve hook calls Registry.ReloadAllSkillRouters; if this
// test breaks, weaver-authored skills would silently fail to surface
// on the next chat turn until a server restart.
func TestAgentManagementTool_SkillWriteHook(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	type hookCall struct {
		skillName string
		filePath  string
	}
	var calls []hookCall
	var callCount atomic.Int32
	hook := func(skillName, filePath string) {
		callCount.Add(1)
		calls = append(calls, hookCall{skillName, filePath})
	}

	tool := NewAgentManagementTool(WithSkillWriteHook(hook))
	ctx := session.WithAgentID(context.Background(), "weaver")

	params := map[string]interface{}{
		"action": "create_skill",
		"config": map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":        "test-skill-with-hook",
				"description": "Skill exercising the post-write hook.",
				"domain":      "general",
				"version":     "1.0.0",
			},
			"trigger": map[string]interface{}{
				"slash_commands": []string{"/test-skill-with-hook"},
				"keywords":       []string{"test", "hook"},
				"mode":           "AUTO",
				"min_confidence": 0.6,
			},
			"prompt": map[string]interface{}{
				"instructions": "Test skill body.",
			},
		},
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	require.True(t, result.Success, "expected create_skill to succeed; error: %+v", result.Error)

	require.Equal(t, int32(1), callCount.Load(),
		"hook must fire exactly once for a successful create")
	require.Len(t, calls, 1)
	assert.Equal(t, "test-skill-with-hook", calls[0].skillName)
	assert.Equal(t, filepath.Join(tmpDir, "skills", "test-skill-with-hook.yaml"), calls[0].filePath)
}

// TestAgentManagementTool_SkillWriteHookNotCalledOnFailure asserts
// the hook does NOT fire when the write fails. Otherwise downstream
// router reloads would happen for files that don't exist on disk.
func TestAgentManagementTool_SkillWriteHookNotCalledOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	var callCount atomic.Int32
	hook := func(string, string) { callCount.Add(1) }

	tool := NewAgentManagementTool(WithSkillWriteHook(hook))
	ctx := session.WithAgentID(context.Background(), "weaver")

	// Missing required name -> conversion error before write.
	params := map[string]interface{}{
		"action": "create_skill",
		"config": map[string]interface{}{
			"metadata": map[string]interface{}{
				// no name
				"description": "Should fail",
			},
		},
	}
	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Equal(t, int32(0), callCount.Load(),
		"hook must not fire when create_skill fails before write")
}

// TestAgentManagementTool_NoHookByDefault confirms the legacy
// zero-options constructor still works exactly as before this option
// existed.
func TestAgentManagementTool_NoHookByDefault(t *testing.T) {
	tool := NewAgentManagementTool()
	assert.Nil(t, tool.skillWriteHook,
		"default constructor must leave skillWriteHook nil")
}
