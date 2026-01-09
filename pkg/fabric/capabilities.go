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

// NewCapabilities creates a new Capabilities instance with default values.
func NewCapabilities() *Capabilities {
	return &Capabilities{
		SupportsTransactions: false,
		SupportsConcurrency:  true,
		SupportsStreaming:    false,
		MaxConcurrentOps:     10,
		SupportedOperations:  []string{},
		Features:             make(map[string]bool),
		Limits:               make(map[string]int64),
	}
}

// WithTransactions sets transaction support.
func (c *Capabilities) WithTransactions(supported bool) *Capabilities {
	c.SupportsTransactions = supported
	return c
}

// WithConcurrency sets concurrency support and max operations.
func (c *Capabilities) WithConcurrency(supported bool, maxOps int) *Capabilities {
	c.SupportsConcurrency = supported
	c.MaxConcurrentOps = maxOps
	return c
}

// WithStreaming sets streaming support.
func (c *Capabilities) WithStreaming(supported bool) *Capabilities {
	c.SupportsStreaming = supported
	return c
}

// WithOperation adds a supported custom operation.
func (c *Capabilities) WithOperation(op string) *Capabilities {
	c.SupportedOperations = append(c.SupportedOperations, op)
	return c
}

// WithFeature sets a backend-specific feature flag.
func (c *Capabilities) WithFeature(name string, enabled bool) *Capabilities {
	c.Features[name] = enabled
	return c
}

// WithLimit sets a backend-specific limit.
func (c *Capabilities) WithLimit(name string, value int64) *Capabilities {
	c.Limits[name] = value
	return c
}

// SupportsOperation checks if a custom operation is supported.
func (c *Capabilities) SupportsOperation(op string) bool {
	for _, supported := range c.SupportedOperations {
		if supported == op {
			return true
		}
	}
	return false
}

// HasFeature checks if a feature is enabled.
func (c *Capabilities) HasFeature(name string) bool {
	enabled, ok := c.Features[name]
	return ok && enabled
}

// GetLimit retrieves a backend-specific limit.
func (c *Capabilities) GetLimit(name string) (int64, bool) {
	limit, ok := c.Limits[name]
	return limit, ok
}

// Common feature flags
const (
	FeatureFullTextSearch    = "full_text_search"
	FeatureJSONSupport       = "json_support"
	FeatureGeospatial        = "geospatial"
	FeatureWindowFunctions   = "window_functions"
	FeatureCTE               = "cte" // Common Table Expressions
	FeatureRecursiveCTE      = "recursive_cte"
	FeatureMaterializedViews = "materialized_views"
	FeaturePartitioning      = "partitioning"
	FeatureIndexing          = "indexing"
	FeatureCaching           = "caching"
)

// Common limits
const (
	LimitMaxQuerySize   = "max_query_size"
	LimitMaxResultSize  = "max_result_size"
	LimitMaxConcurrency = "max_concurrency"
	LimitQueryTimeout   = "query_timeout_ms"
	LimitRateLimit      = "rate_limit_per_second"
	LimitMaxConnections = "max_connections"
)
