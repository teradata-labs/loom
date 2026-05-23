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
package factory

import (
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// buildModelCatalog returns the static catalog of all known models across providers.
// The canonical data now lives in pkg/llm/catalog so that consumers outside the
// factory (e.g. the agent package resolving token budgets) can read entries
// without pulling in all provider client packages. Edit the catalog package to
// add, remove, or update models.
func buildModelCatalog() map[string][]*loomv1.ModelInfo {
	return catalog.BuildCatalog()
}
