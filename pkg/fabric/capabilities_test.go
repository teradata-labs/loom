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
package fabric

import (
	"testing"
)

func TestNewCapabilities(t *testing.T) {
	caps := NewCapabilities()

	if caps == nil {
		t.Fatal("Expected non-nil capabilities")
	}

	if caps.SupportsTransactions {
		t.Error("Expected SupportsTransactions to be false by default")
	}

	if !caps.SupportsConcurrency {
		t.Error("Expected SupportsConcurrency to be true by default")
	}

	if caps.SupportsStreaming {
		t.Error("Expected SupportsStreaming to be false by default")
	}

	if caps.MaxConcurrentOps != 10 {
		t.Errorf("Expected MaxConcurrentOps to be 10, got %d", caps.MaxConcurrentOps)
	}

	if caps.Features == nil {
		t.Error("Expected Features map to be initialized")
	}

	if caps.Limits == nil {
		t.Error("Expected Limits map to be initialized")
	}
}

func TestCapabilities_WithTransactions(t *testing.T) {
	caps := NewCapabilities().WithTransactions(true)

	if !caps.SupportsTransactions {
		t.Error("Expected SupportsTransactions to be true")
	}

	caps.WithTransactions(false)
	if caps.SupportsTransactions {
		t.Error("Expected SupportsTransactions to be false")
	}
}

func TestCapabilities_WithConcurrency(t *testing.T) {
	caps := NewCapabilities().WithConcurrency(true, 50)

	if !caps.SupportsConcurrency {
		t.Error("Expected SupportsConcurrency to be true")
	}

	if caps.MaxConcurrentOps != 50 {
		t.Errorf("Expected MaxConcurrentOps to be 50, got %d", caps.MaxConcurrentOps)
	}
}

func TestCapabilities_WithStreaming(t *testing.T) {
	caps := NewCapabilities().WithStreaming(true)

	if !caps.SupportsStreaming {
		t.Error("Expected SupportsStreaming to be true")
	}
}

func TestCapabilities_WithOperation(t *testing.T) {
	caps := NewCapabilities().
		WithOperation("bulk_insert").
		WithOperation("batch_update")

	if len(caps.SupportedOperations) != 2 {
		t.Errorf("Expected 2 operations, got %d", len(caps.SupportedOperations))
	}

	if !caps.SupportsOperation("bulk_insert") {
		t.Error("Expected bulk_insert to be supported")
	}

	if !caps.SupportsOperation("batch_update") {
		t.Error("Expected batch_update to be supported")
	}

	if caps.SupportsOperation("nonexistent") {
		t.Error("Expected nonexistent to not be supported")
	}
}

func TestCapabilities_WithFeature(t *testing.T) {
	caps := NewCapabilities().
		WithFeature(FeatureFullTextSearch, true).
		WithFeature(FeatureJSONSupport, true).
		WithFeature(FeatureGeospatial, false)

	if !caps.HasFeature(FeatureFullTextSearch) {
		t.Error("Expected full text search to be enabled")
	}

	if !caps.HasFeature(FeatureJSONSupport) {
		t.Error("Expected JSON support to be enabled")
	}

	if caps.HasFeature(FeatureGeospatial) {
		t.Error("Expected geospatial to be disabled")
	}

	if caps.HasFeature("nonexistent") {
		t.Error("Expected nonexistent feature to return false")
	}
}

func TestCapabilities_WithLimit(t *testing.T) {
	caps := NewCapabilities().
		WithLimit(LimitMaxQuerySize, 1024*1024).    // 1MB
		WithLimit(LimitMaxResultSize, 10*1024*1024) // 10MB

	limit, ok := caps.GetLimit(LimitMaxQuerySize)
	if !ok {
		t.Error("Expected max_query_size limit to exist")
	}
	if limit != 1024*1024 {
		t.Errorf("Expected limit to be 1MB, got %d", limit)
	}

	limit, ok = caps.GetLimit(LimitMaxResultSize)
	if !ok {
		t.Error("Expected max_result_size limit to exist")
	}
	if limit != 10*1024*1024 {
		t.Errorf("Expected limit to be 10MB, got %d", limit)
	}

	_, ok = caps.GetLimit("nonexistent")
	if ok {
		t.Error("Expected nonexistent limit to not exist")
	}
}

func TestCapabilities_Chaining(t *testing.T) {
	caps := NewCapabilities().
		WithTransactions(true).
		WithConcurrency(true, 100).
		WithStreaming(true).
		WithOperation("custom_op").
		WithFeature(FeatureCTE, true).
		WithLimit(LimitQueryTimeout, 30000)

	if !caps.SupportsTransactions {
		t.Error("Expected transactions to be supported")
	}

	if caps.MaxConcurrentOps != 100 {
		t.Error("Expected max concurrent ops to be 100")
	}

	if !caps.SupportsStreaming {
		t.Error("Expected streaming to be supported")
	}

	if !caps.SupportsOperation("custom_op") {
		t.Error("Expected custom_op to be supported")
	}

	if !caps.HasFeature(FeatureCTE) {
		t.Error("Expected CTE feature to be enabled")
	}

	timeout, ok := caps.GetLimit(LimitQueryTimeout)
	if !ok || timeout != 30000 {
		t.Error("Expected query timeout to be 30000ms")
	}
}

func TestCapabilities_SQLBackendExample(t *testing.T) {
	caps := NewCapabilities().
		WithTransactions(true).
		WithConcurrency(true, 25).
		WithFeature(FeatureWindowFunctions, true).
		WithFeature(FeatureCTE, true).
		WithFeature(FeatureRecursiveCTE, true).
		WithLimit(LimitMaxQuerySize, 10*1024*1024).
		WithLimit(LimitQueryTimeout, 300000) // 5 minutes

	// Verify SQL backend capabilities
	if !caps.SupportsTransactions {
		t.Error("SQL backends should support transactions")
	}

	if !caps.HasFeature(FeatureWindowFunctions) {
		t.Error("Modern SQL backends should support window functions")
	}

	if !caps.HasFeature(FeatureCTE) {
		t.Error("Modern SQL backends should support CTEs")
	}

	querySize, _ := caps.GetLimit(LimitMaxQuerySize)
	if querySize != 10*1024*1024 {
		t.Error("Expected 10MB max query size")
	}
}

func TestCapabilities_APIBackendExample(t *testing.T) {
	caps := NewCapabilities().
		WithConcurrency(true, 10).
		WithOperation("batch_request").
		WithOperation("webhook").
		WithLimit(LimitRateLimit, 1000). // 1000 req/sec
		WithLimit(LimitMaxConnections, 50)

	// Verify API backend capabilities
	if !caps.SupportsOperation("batch_request") {
		t.Error("API backends should support batch requests")
	}

	if !caps.SupportsOperation("webhook") {
		t.Error("API backends should support webhooks")
	}

	rateLimit, _ := caps.GetLimit(LimitRateLimit)
	if rateLimit != 1000 {
		t.Error("Expected 1000 requests per second")
	}
}

func TestCapabilities_FeatureConstants(t *testing.T) {
	// Test that all feature constants are defined
	features := []string{
		FeatureFullTextSearch,
		FeatureJSONSupport,
		FeatureGeospatial,
		FeatureWindowFunctions,
		FeatureCTE,
		FeatureRecursiveCTE,
		FeatureMaterializedViews,
		FeaturePartitioning,
		FeatureIndexing,
		FeatureCaching,
	}

	for _, feature := range features {
		if feature == "" {
			t.Error("Feature constant should not be empty")
		}
	}
}

func TestCapabilities_LimitConstants(t *testing.T) {
	// Test that all limit constants are defined
	limits := []string{
		LimitMaxQuerySize,
		LimitMaxResultSize,
		LimitMaxConcurrency,
		LimitQueryTimeout,
		LimitRateLimit,
		LimitMaxConnections,
	}

	for _, limit := range limits {
		if limit == "" {
			t.Error("Limit constant should not be empty")
		}
	}
}
