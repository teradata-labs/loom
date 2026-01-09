// Copyright 2026 Teradata
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

package prompts

import (
	"context"
	"fmt"
)

// PromptioRegistry stub type (only used for type checking)
type PromptioRegistry struct {
	promptsDir string
}

// NewPromptioRegistry creates a stub registry that returns errors.
// To enable Promptio support, build with: go build -tags promptio
func NewPromptioRegistry(promptsDir string) *PromptioRegistry {
	return &PromptioRegistry{promptsDir: promptsDir}
}

// Get returns an error when built without promptio build tag.
func (r *PromptioRegistry) Get(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
	return "", fmt.Errorf("promptio support not compiled in (rebuild with -tags promptio)")
}

// GetWithVariant returns an error when built without promptio build tag.
func (r *PromptioRegistry) GetWithVariant(ctx context.Context, key string, variant string, vars map[string]interface{}) (string, error) {
	return "", fmt.Errorf("promptio support not compiled in (rebuild with -tags promptio)")
}

// GetMetadata returns an error when built without promptio build tag.
func (r *PromptioRegistry) GetMetadata(ctx context.Context, key string) (*PromptMetadata, error) {
	return nil, fmt.Errorf("promptio support not compiled in (rebuild with -tags promptio)")
}

// List returns an error when built without promptio build tag.
func (r *PromptioRegistry) List(ctx context.Context, filters map[string]string) ([]string, error) {
	return nil, fmt.Errorf("promptio support not compiled in (rebuild with -tags promptio)")
}

// Reload returns an error when built without promptio build tag.
func (r *PromptioRegistry) Reload(ctx context.Context) error {
	return fmt.Errorf("promptio support not compiled in (rebuild with -tags promptio)")
}

// Watch returns an error when built without promptio build tag.
func (r *PromptioRegistry) Watch(ctx context.Context) (<-chan PromptUpdate, error) {
	return nil, fmt.Errorf("promptio support not compiled in (rebuild with -tags promptio)")
}

// Ensure PromptioRegistry implements PromptRegistry interface.
var _ PromptRegistry = (*PromptioRegistry)(nil)
