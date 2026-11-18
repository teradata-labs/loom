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

package catalog

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

type oneModel struct{ provider, id, name string }

func (o oneModel) Lookup(_ context.Context, provider, id string) *loomv1.ModelInfo {
	if provider == o.provider && id == o.id {
		return &loomv1.ModelInfo{Id: id, Provider: provider, Name: o.name}
	}
	return nil
}

func (o oneModel) List(context.Context) map[string][]*loomv1.ModelInfo {
	return map[string][]*loomv1.ModelInfo{o.provider: {{Id: o.id, Provider: o.provider, Name: o.name}}}
}

func TestRegisterNamed_StacksReplacesRemoves(t *testing.T) {
	t.Cleanup(func() { Register(nil) })
	Register(nil)

	RegisterNamed("ollama", oneModel{"ollama", "discovered-only:latest", "v1"})
	RegisterNamed("vercel-ai-gateway", oneModel{"vercel-ai-gateway", "openai/gpt-5", "gw"})

	// Both discovered sources resolve, and the static catalog still does.
	assert.Equal(t, "v1", Lookup("ollama", "discovered-only:latest").GetName())
	assert.Equal(t, "gw", Lookup("vercel-ai-gateway", "openai/gpt-5").GetName())
	assert.NotNil(t, Lookup("anthropic", "claude-sonnet-4-5-20250929"))

	// A refresh replaces in place; the chain does not grow.
	RegisterNamed("ollama", oneModel{"ollama", "discovered-only:latest", "v2"})
	assert.Equal(t, "v2", Lookup("ollama", "discovered-only:latest").GetName())
	assert.Len(t, DefaultSource().(MultiSource), 3)

	// Removal leaves the other named source.
	RegisterNamed("ollama", nil)
	assert.Nil(t, Lookup("ollama", "discovered-only:latest"))
	assert.Equal(t, "gw", Lookup("vercel-ai-gateway", "openai/gpt-5").GetName())
	RegisterNamed("never-registered", nil) // no-op

	// Register drops named sources and sets a new base.
	Register(oneModel{"x", "y", "base"})
	assert.Nil(t, Lookup("vercel-ai-gateway", "openai/gpt-5"))
	assert.Equal(t, "base", Lookup("x", "y").GetName())
}

func TestRegisterNamed_ConcurrentWithLookup(t *testing.T) {
	t.Cleanup(func() { Register(nil) })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				RegisterNamed("a", oneModel{"p", "m", "a"})
				RegisterNamed("b", oneModel{"p", "n", "b"})
				RegisterNamed("a", nil)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = Lookup("p", "m")
				_ = Lookup("anthropic", "claude-sonnet-4-5-20250929")
			}
		}()
	}
	wg.Wait()
}
