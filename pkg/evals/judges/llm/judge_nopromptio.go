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

//go:build !promptio

package llm

import (
	"embed"
	"fmt"
)

// initPromptManager is a no-op when promptio is not included
func (j *Judge) initPromptManager(promptsFS embed.FS) error {
	// No-op: promptio not available in this build
	return nil
}

// tryPromptioRender always returns error when promptio is not included
func (j *Judge) tryPromptioRender(templateName string, data map[string]interface{}) (string, error) {
	return "", fmt.Errorf("promptio not available (build with -tags promptio to enable YAML template support)")
}
