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
package shuttle_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// csMock is a mockTool that also declares the ConcurrencySafe trait.
type csMock struct {
	mockTool
	safe bool
}

func (c *csMock) ConcurrencySafe() bool { return c.safe }

func TestPromptAwareTool_ForwardsConcurrencySafe(t *testing.T) {
	reg := &mockRegistry{}
	wrap := func(tool shuttle.Tool) shuttle.Tool { return shuttle.NewPromptAwareTool(tool, reg, "k") }

	assert.True(t, shuttle.IsConcurrencySafe(wrap(&csMock{safe: true})))
	assert.False(t, shuttle.IsConcurrencySafe(wrap(&csMock{safe: false})))
	assert.False(t, shuttle.IsConcurrencySafe(wrap(&mockTool{})), "undeclared wrapped tool stays unsafe")
	assert.NotPanics(t, func() {
		assert.False(t, shuttle.IsConcurrencySafe(wrap(nil)), "nil inner tool")
	})
}
