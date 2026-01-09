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

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithVariant(t *testing.T) {
	ctx := context.Background()
	variant := "control"

	ctx = WithVariant(ctx, variant)

	retrieved := GetVariant(ctx)
	assert.Equal(t, variant, retrieved, "Variant should be retrieved from context")
}

func TestGetVariant_Empty(t *testing.T) {
	ctx := context.Background()

	retrieved := GetVariant(ctx)
	assert.Equal(t, "", retrieved, "Should return empty string when no variant in context")
}

func TestWithPatternName(t *testing.T) {
	ctx := context.Background()
	patternName := "sql.joins.optimize"

	ctx = WithPatternName(ctx, patternName)

	retrieved := GetPatternName(ctx)
	assert.Equal(t, patternName, retrieved, "Pattern name should be retrieved from context")
}

func TestGetPatternName_Empty(t *testing.T) {
	ctx := context.Background()

	retrieved := GetPatternName(ctx)
	assert.Equal(t, "", retrieved, "Should return empty string when no pattern name in context")
}

func TestWithPatternDomain(t *testing.T) {
	ctx := context.Background()
	domain := "sql"

	ctx = WithPatternDomain(ctx, domain)

	retrieved := GetPatternDomain(ctx)
	assert.Equal(t, domain, retrieved, "Domain should be retrieved from context")
}

func TestGetPatternDomain_Empty(t *testing.T) {
	ctx := context.Background()

	retrieved := GetPatternDomain(ctx)
	assert.Equal(t, "", retrieved, "Should return empty string when no domain in context")
}

func TestWithPatternMetadata(t *testing.T) {
	ctx := context.Background()
	patternName := "sql.joins.optimize"
	variant := "treatment"
	domain := "sql"

	ctx = WithPatternMetadata(ctx, patternName, variant, domain)

	// Verify all values are set
	assert.Equal(t, patternName, GetPatternName(ctx), "Pattern name should be set")
	assert.Equal(t, variant, GetVariant(ctx), "Variant should be set")
	assert.Equal(t, domain, GetPatternDomain(ctx), "Domain should be set")
}

func TestGetPatternMetadata(t *testing.T) {
	ctx := context.Background()
	patternName := "sql.joins.optimize"
	variant := "treatment"
	domain := "sql"

	ctx = WithPatternMetadata(ctx, patternName, variant, domain)

	metadata := GetPatternMetadata(ctx)
	assert.Equal(t, patternName, metadata.Name, "Pattern name should be in metadata")
	assert.Equal(t, variant, metadata.Variant, "Variant should be in metadata")
	assert.Equal(t, domain, metadata.Domain, "Domain should be in metadata")
}

func TestGetPatternMetadata_Empty(t *testing.T) {
	ctx := context.Background()

	metadata := GetPatternMetadata(ctx)
	assert.Equal(t, "", metadata.Name, "Should return empty name")
	assert.Equal(t, "", metadata.Variant, "Should return empty variant")
	assert.Equal(t, "", metadata.Domain, "Should return empty domain")
}

func TestGetPatternMetadata_Partial(t *testing.T) {
	ctx := context.Background()
	ctx = WithPatternName(ctx, "sql.joins.optimize")
	ctx = WithVariant(ctx, "control")
	// Domain intentionally not set

	metadata := GetPatternMetadata(ctx)
	assert.Equal(t, "sql.joins.optimize", metadata.Name, "Should return pattern name")
	assert.Equal(t, "control", metadata.Variant, "Should return variant")
	assert.Equal(t, "", metadata.Domain, "Should return empty domain")
}

func TestContextOverwrite(t *testing.T) {
	ctx := context.Background()

	// Set initial variant
	ctx = WithVariant(ctx, "control")
	assert.Equal(t, "control", GetVariant(ctx), "Initial variant should be 'control'")

	// Overwrite with new variant
	ctx = WithVariant(ctx, "treatment")
	assert.Equal(t, "treatment", GetVariant(ctx), "Variant should be overwritten to 'treatment'")
}

func TestContextIsolation(t *testing.T) {
	// Create two independent contexts
	ctx1 := context.Background()
	ctx2 := context.Background()

	ctx1 = WithVariant(ctx1, "control")
	ctx2 = WithVariant(ctx2, "treatment")

	// Verify contexts are isolated
	assert.Equal(t, "control", GetVariant(ctx1), "ctx1 should have 'control'")
	assert.Equal(t, "treatment", GetVariant(ctx2), "ctx2 should have 'treatment'")
}

func TestContextChaining(t *testing.T) {
	ctx := context.Background()

	// Chain multiple context additions
	ctx = WithPatternName(ctx, "pattern1")
	ctx = WithVariant(ctx, "variant1")
	ctx = WithPatternDomain(ctx, "domain1")

	// Verify all are preserved
	assert.Equal(t, "pattern1", GetPatternName(ctx))
	assert.Equal(t, "variant1", GetVariant(ctx))
	assert.Equal(t, "domain1", GetPatternDomain(ctx))

	// Add more to the chain
	ctx = WithPatternName(ctx, "pattern2")

	// Verify new value and old values are preserved
	assert.Equal(t, "pattern2", GetPatternName(ctx), "Pattern name should be updated")
	assert.Equal(t, "variant1", GetVariant(ctx), "Variant should still be preserved")
	assert.Equal(t, "domain1", GetPatternDomain(ctx), "Domain should still be preserved")
}

func TestEmptyStringValues(t *testing.T) {
	ctx := context.Background()

	// Explicitly set empty strings
	ctx = WithPatternMetadata(ctx, "", "", "")

	metadata := GetPatternMetadata(ctx)
	assert.Equal(t, "", metadata.Name, "Empty name should be retrievable")
	assert.Equal(t, "", metadata.Variant, "Empty variant should be retrievable")
	assert.Equal(t, "", metadata.Domain, "Empty domain should be retrievable")
}

func TestWithPatternMetadata_Integration(t *testing.T) {
	// Simulate a realistic usage pattern
	ctx := context.Background()

	// Step 1: Set pattern metadata when selecting a pattern
	ctx = WithPatternMetadata(ctx, "sql.joins.optimize", "control", "sql")

	// Step 2: Retrieve metadata during execution
	metadata := GetPatternMetadata(ctx)
	assert.Equal(t, "sql.joins.optimize", metadata.Name)
	assert.Equal(t, "control", metadata.Variant)
	assert.Equal(t, "sql", metadata.Domain)

	// Step 3: Update variant during execution (e.g., after A/B test selection)
	ctx = WithVariant(ctx, "treatment")

	// Step 4: Verify update doesn't affect other values
	metadata = GetPatternMetadata(ctx)
	assert.Equal(t, "sql.joins.optimize", metadata.Name, "Name should be unchanged")
	assert.Equal(t, "treatment", metadata.Variant, "Variant should be updated")
	assert.Equal(t, "sql", metadata.Domain, "Domain should be unchanged")
}
