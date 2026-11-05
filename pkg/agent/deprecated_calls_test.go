// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// A call kept only so old callers compile says, once, that it has no effect:
// the caller learns the behavior is gone instead of losing it silently.
func TestADeprecatedCallSaysOnceThatItDoesNothing(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	deprecatedWarned.Delete("SegmentedMemory.DropTurnPayloads")

	sm := NewSegmentedMemory("ROM", 200000, 20000)
	sm.DropTurnPayloads()
	sm.DropTurnPayloads()

	got := logs.FilterField(zap.String("call", "SegmentedMemory.DropTurnPayloads")).Len()
	assert.Equal(t, 1, got, "a deprecated no-op should warn exactly once")
}

// A file that still sets a key the loader no longer reads is told so.
func TestARemovedConfigKeyIsReportedOnLoad(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()

	path := filepath.Join(t.TempDir(), "a.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent:\n  name: a\n  behavior:\n    context_relief_warning: true\n"), 0o600))
	_, err := LoadAgentConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 1, logs.FilterMessageSnippet("context_relief_warning").Len(), "the removed key loaded silently")
}
