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
package patterns

import "context"

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const (
	// patternVariantKey stores the variant of the pattern being used.
	patternVariantKey contextKey = "pattern_variant"

	// patternNameKey stores the name of the pattern being executed.
	patternNameKey contextKey = "pattern_name"

	// patternDomainKey stores the domain of the pattern being executed.
	patternDomainKey contextKey = "pattern_domain"
)

// WithVariant adds a pattern variant to the context.
// This should be set when a pattern variant is selected for execution.
//
// Example:
//
//	ctx = patterns.WithVariant(ctx, "control")
//	pattern, _ := library.LoadWithVariant(ctx, "sql.joins.optimize", "control")
func WithVariant(ctx context.Context, variant string) context.Context {
	return context.WithValue(ctx, patternVariantKey, variant)
}

// GetVariant retrieves the pattern variant from context.
// Returns empty string if no variant is set.
func GetVariant(ctx context.Context) string {
	if variant, ok := ctx.Value(patternVariantKey).(string); ok {
		return variant
	}
	return ""
}

// WithPatternName adds a pattern name to the context.
// This should be set when a pattern is about to be executed.
//
// Example:
//
//	ctx = patterns.WithPatternName(ctx, "sql.joins.optimize")
func WithPatternName(ctx context.Context, patternName string) context.Context {
	return context.WithValue(ctx, patternNameKey, patternName)
}

// GetPatternName retrieves the pattern name from context.
// Returns empty string if no pattern name is set.
func GetPatternName(ctx context.Context) string {
	if name, ok := ctx.Value(patternNameKey).(string); ok {
		return name
	}
	return ""
}

// WithPatternDomain adds a pattern domain to the context.
// This should be set when a pattern is about to be executed.
//
// Example:
//
//	ctx = patterns.WithPatternDomain(ctx, "sql")
func WithPatternDomain(ctx context.Context, domain string) context.Context {
	return context.WithValue(ctx, patternDomainKey, domain)
}

// GetPatternDomain retrieves the pattern domain from context.
// Returns empty string if no domain is set.
func GetPatternDomain(ctx context.Context) string {
	if domain, ok := ctx.Value(patternDomainKey).(string); ok {
		return domain
	}
	return ""
}

// WithPatternMetadata is a convenience function that sets all pattern metadata
// in the context at once (name, variant, domain).
//
// Example:
//
//	ctx = patterns.WithPatternMetadata(ctx, "sql.joins.optimize", "control", "sql")
func WithPatternMetadata(ctx context.Context, patternName, variant, domain string) context.Context {
	ctx = WithPatternName(ctx, patternName)
	ctx = WithVariant(ctx, variant)
	ctx = WithPatternDomain(ctx, domain)
	return ctx
}

// PatternMetadata is a convenience struct for extracting all pattern metadata from context.
type PatternMetadata struct {
	Name    string
	Variant string
	Domain  string
}

// GetPatternMetadata extracts all pattern metadata from context.
// Returns a PatternMetadata struct with all available information.
//
// Example:
//
//	metadata := patterns.GetPatternMetadata(ctx)
//	if metadata.Name != "" {
//	    // Pattern metadata available
//	}
func GetPatternMetadata(ctx context.Context) PatternMetadata {
	return PatternMetadata{
		Name:    GetPatternName(ctx),
		Variant: GetVariant(ctx),
		Domain:  GetPatternDomain(ctx),
	}
}
