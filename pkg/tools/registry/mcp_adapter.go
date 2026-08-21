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
package registry

import (
	"errors"
	"fmt"

	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// NewShuttleMCPManager adapts a *manager.Manager to the shuttle.MCPManager
// interface (needed because manager.Manager.GetClient returns a concrete
// *client.Client while the interface requires (interface{}, error)) and
// applies the sentinel-translation policy shared by every composition layer:
// "server was removed from configuration" (a stale tool index entry the
// executor should evict, issue #334) is reported as
// shuttle.ErrMCPServerNotFound, while "server is configured but not currently
// connected" (transient — keep the entry) passes through untranslated.
//
// This package is the one importable home that already depends on both
// pkg/mcp/manager and pkg/shuttle, so hosting the adapter here adds no import
// edges. mgr must be non-nil.
func NewShuttleMCPManager(mgr *manager.Manager) shuttle.MCPManager {
	return &shuttleMCPManagerAdapter{mgr: mgr}
}

// shuttleMCPManagerAdapter is the concrete adapter behind NewShuttleMCPManager.
type shuttleMCPManagerAdapter struct {
	mgr *manager.Manager
}

func (a *shuttleMCPManagerAdapter) GetClient(serverName string) (interface{}, error) {
	c, err := a.mgr.GetClient(serverName)
	if err != nil {
		// Distinguish "server was removed from configuration" (a stale tool
		// index entry the executor should evict, issue #334) from "server is
		// configured but not currently connected" (transient — keep it).
		if errors.Is(err, manager.ErrServerNotFound) {
			if _, cfgErr := a.mgr.GetServerConfig(serverName); cfgErr != nil {
				return nil, fmt.Errorf("%w: %s", shuttle.ErrMCPServerNotFound, serverName)
			}
		}
		return nil, err
	}
	return c, nil
}
