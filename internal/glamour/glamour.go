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
// Package glamour provides markdown rendering (stub replacement for charm.land/glamour/v2).
package glamour

import (
	"io"
)

// TermRenderer renders markdown to terminal output.
type TermRenderer struct {
	wordWrap int
	style    string
}

// NewTermRenderer creates a new terminal renderer.
func NewTermRenderer(opts ...TermRendererOption) (*TermRenderer, error) {
	r := &TermRenderer{
		wordWrap: 80,
		style:    "dark",
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// TermRendererOption configures a TermRenderer.
type TermRendererOption func(*TermRenderer)

// WithWordWrap sets word wrap width.
func WithWordWrap(w int) TermRendererOption {
	return func(r *TermRenderer) {
		r.wordWrap = w
	}
}

// WithStyles sets the style.
func WithStyles(s interface{}) TermRendererOption {
	return func(r *TermRenderer) {
		// No-op for stub
	}
}

// WithEmoji enables/disables emoji.
func WithEmoji() TermRendererOption {
	return func(r *TermRenderer) {
		// No-op for stub
	}
}

// Render renders markdown to string.
func (r *TermRenderer) Render(in string) (string, error) {
	// Basic rendering - just return the input for now
	// In a real implementation, this would render markdown
	return in, nil
}

// RenderBytes renders markdown bytes.
func (r *TermRenderer) RenderBytes(in []byte) ([]byte, error) {
	s, err := r.Render(string(in))
	return []byte(s), err
}

// Render is a convenience function.
func Render(in string, style string) (string, error) {
	r, err := NewTermRenderer()
	if err != nil {
		return "", err
	}
	return r.Render(in)
}

// RenderFrom renders from a reader.
func RenderFrom(r io.Reader, style string) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return Render(string(b), style)
}

// DarkStyleConfig is a dark style configuration.
var DarkStyleConfig = struct{}{}

// LightStyleConfig is a light style configuration.
var LightStyleConfig = struct{}{}
