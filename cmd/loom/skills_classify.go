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

package main

import (
	"fmt"
	"os"

	"github.com/teradata-labs/loom/pkg/llm/factory"
	"github.com/teradata-labs/loom/pkg/types"
)

// classifyEnabled is true when the user opted into LLM classification via
// the --classify flag. The cobra command in skills_import.go binds it.
var classifyEnabled bool

// buildClassifyLLM constructs an LLM provider from environment variables.
// Returns (nil, nil) when classification was not requested — callers fall
// back to the legacy unclassified/<domain> path. Returns nil with a
// non-nil error when classification IS requested but the environment
// isn't configured.
//
// Provider creds are read from each provider's standard env vars
// (ANTHROPIC_API_KEY, AWS chain, OPENAI_API_KEY, etc.) by the factory's
// per-provider create* methods, not by this function — keeps the
// importer's surface minimal.
func buildClassifyLLM() (types.LLMProvider, error) {
	if !classifyEnabled {
		return nil, nil
	}
	provider := os.Getenv("LOOM_CLASSIFY_PROVIDER")
	if provider == "" {
		provider = os.Getenv("LOOM_DEFAULT_PROVIDER")
	}
	if provider == "" {
		return nil, fmt.Errorf("--classify requires LOOM_CLASSIFY_PROVIDER or LOOM_DEFAULT_PROVIDER " +
			"(supported: anthropic, bedrock, ollama, openai, azure-openai, mistral, gemini, huggingface)")
	}
	model := os.Getenv("LOOM_CLASSIFY_MODEL")

	cfg := factory.FactoryConfig{
		DefaultProvider: provider,
		DefaultModel:    model,
		Temperature:     0.0, // classification is a deterministic task
	}
	f := factory.NewProviderFactory(cfg)

	raw, err := f.CreateProvider(provider, model)
	if err != nil {
		return nil, fmt.Errorf("create classify provider %q: %w", provider, err)
	}
	llm, ok := raw.(types.LLMProvider)
	if !ok {
		return nil, fmt.Errorf("classify provider %q does not implement types.LLMProvider", provider)
	}
	return llm, nil
}

// classifyProviderInfo returns a human-readable description of the
// configured classify provider for the import banner.
func classifyProviderInfo() string {
	provider := os.Getenv("LOOM_CLASSIFY_PROVIDER")
	if provider == "" {
		provider = os.Getenv("LOOM_DEFAULT_PROVIDER")
	}
	model := os.Getenv("LOOM_CLASSIFY_MODEL")
	if model == "" {
		model = "(provider default)"
	}
	return fmt.Sprintf("provider=%s model=%s", provider, model)
}
