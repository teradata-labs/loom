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
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Provider implements shuttle.BuiltinToolProvider to provide builtin tools
// without creating import cycles.
type Provider struct{}

// NewProvider creates a new builtin tool provider.
func NewProvider() *Provider {
	return &Provider{}
}

// GetTool returns a builtin tool by name, or nil if not found.
func (p *Provider) GetTool(name string) shuttle.Tool {
	return ByName(name)
}
