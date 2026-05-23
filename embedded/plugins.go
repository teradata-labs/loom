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

package embedded

import "embed"

// PluginsFS holds the embedded plugin YAML files shipped with the binary.
// Plugins in this directory are loaded at startup before any user-defined plugins.
// The directory is intentionally empty in the base distribution; operators add
// plugins by placing YAML files here or in the runtime plugins/ directory.
//
//go:embed plugins/.gitkeep
var PluginsFS embed.FS
