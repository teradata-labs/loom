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
package util

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/teradata-labs/loom/internal/uiutil"
)

// ExecShell parses a shell command string and executes it with exec.Command.
// Uses shell.Fields for proper handling of shell syntax like quotes and
// arguments while preserving TTY handling for terminal editors.
func ExecShell(ctx context.Context, cmdStr string, callback tea.ExecCallback) tea.Cmd {
	return uiutil.ExecShell(ctx, cmdStr, callback)
}
