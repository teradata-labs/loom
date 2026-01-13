// Copyright 2026 Teradata Corporation
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

//go:build promptio

package llm

import (
	"embed"
	"fmt"

	"github.com/teradata-labs/promptio/pkg/loader"
	"github.com/teradata-labs/promptio/pkg/prompt"
	"github.com/teradata-labs/promptio/pkg/render"
)

// initPromptManager initializes promptio (only when built with -tags promptio)
func (j *Judge) initPromptManager(promptsFS embed.FS) error {
	if promptsFS != (embed.FS{}) {
		embedLoader := loader.NewEmbed(promptsFS, "prompts")
		j.promptMgr = prompt.New(
			prompt.WithLoader(embedLoader),
			prompt.WithRenderer(render.NewTemplateEngine()),
		)
	}
	return nil
}

// tryPromptioRender attempts to render using promptio (only when built with -tags promptio)
func (j *Judge) tryPromptioRender(templateName string, data map[string]interface{}) (string, error) {
	if j.promptMgr == nil {
		return "", fmt.Errorf("promptio manager not initialized")
	}

	// Type assert to *prompt.Manager
	mgr, ok := j.promptMgr.(*prompt.Manager)
	if !ok {
		return "", fmt.Errorf("invalid promptio manager type")
	}

	return mgr.Render(templateName, data)
}
