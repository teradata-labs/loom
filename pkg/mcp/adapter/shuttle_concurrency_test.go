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
package adapter

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// adapterWithAnnotations builds an adapter for a TRUSTED server; the trust
// gate itself is covered by TestMCPAdapter_UntrustedByDefault.
func adapterWithAnnotations(a *protocol.ToolAnnotations) shuttle.Tool {
	ad := NewMCPToolAdapter(nil, protocol.Tool{
		Name:        "t",
		InputSchema: map[string]interface{}{"type": "object"},
		Annotations: a,
	}, "srv")
	ad.SetTrustAnnotations(true)
	return ad
}

func TestMCPAdapter_ReadOnlyHintTrue(t *testing.T) {
	yes := true
	assert.True(t, shuttle.IsConcurrencySafe(adapterWithAnnotations(&protocol.ToolAnnotations{ReadOnlyHint: &yes})))
}

func TestMCPAdapter_NilHintFalse(t *testing.T) {
	assert.False(t, shuttle.IsConcurrencySafe(adapterWithAnnotations(nil)), "no annotations")
	assert.False(t, shuttle.IsConcurrencySafe(adapterWithAnnotations(&protocol.ToolAnnotations{})), "annotations without readOnlyHint")
}

func TestMCPAdapter_ReadOnlyAndDestructiveIsFalse(t *testing.T) {
	yes, no := true, false
	assert.False(t, shuttle.IsConcurrencySafe(adapterWithAnnotations(&protocol.ToolAnnotations{ReadOnlyHint: &yes, DestructiveHint: &yes})))
	assert.True(t, shuttle.IsConcurrencySafe(adapterWithAnnotations(&protocol.ToolAnnotations{ReadOnlyHint: &yes, DestructiveHint: &no})))
}

func TestMCPAdapter_TypedNilIsFalse(t *testing.T) {
	var a *MCPToolAdapter
	assert.False(t, shuttle.IsConcurrencySafe(a))
}

func TestMCPAdapter_ReadOnlyFalseIsFalse(t *testing.T) {
	no := false
	assert.False(t, shuttle.IsConcurrencySafe(adapterWithAnnotations(&protocol.ToolAnnotations{ReadOnlyHint: &no})))
}

// The same read-only annotated tool is concurrency-safe only once its server
// is trusted; an untrusted server's annotations are never believed.
func TestMCPAdapter_UntrustedByDefault(t *testing.T) {
	yes := true
	tool := protocol.Tool{
		Name:        "t",
		InputSchema: map[string]interface{}{"type": "object"},
		Annotations: &protocol.ToolAnnotations{ReadOnlyHint: &yes},
	}
	a := NewMCPToolAdapter(nil, tool, "srv")
	assert.False(t, shuttle.IsConcurrencySafe(a), "trust unset")
	a.SetTrustAnnotations(true)
	assert.True(t, shuttle.IsConcurrencySafe(a), "trust_annotations true")
	a.SetTrustAnnotations(false)
	assert.False(t, shuttle.IsConcurrencySafe(a), "trust withdrawn")
}
