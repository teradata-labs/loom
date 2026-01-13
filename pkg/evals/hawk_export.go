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

package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// HawkExportConfig configures Hawk export for eval results
type HawkExportConfig struct {
	// Endpoint is the Hawk API endpoint for eval results
	// Default: $HAWK_ENDPOINT or http://localhost:8080
	Endpoint string

	// APIKey for authentication
	// Default: $HAWK_API_KEY
	APIKey string

	// Timeout for HTTP requests
	// Default: 10s
	Timeout time.Duration

	// HTTPClient for custom transport
	// If nil, uses http.DefaultClient with configured timeout
	HTTPClient *http.Client
}

// ExportToHawk exports an eval result to Hawk for tracking and analysis
func ExportToHawk(ctx context.Context, result *loomv1.EvalResult, config *HawkExportConfig) error {
	if config == nil {
		config = &HawkExportConfig{}
	}

	// Apply defaults
	if config.Endpoint == "" {
		config.Endpoint = os.Getenv("HAWK_ENDPOINT")
		if config.Endpoint == "" {
			config.Endpoint = "http://localhost:8080"
		}
	}

	if config.APIKey == "" {
		config.APIKey = os.Getenv("HAWK_API_KEY")
	}

	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}

	// Create HTTP client
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: config.Timeout,
		}
	}

	// Construct Hawk eval endpoint
	hawkURL := fmt.Sprintf("%s/v1/evals", config.Endpoint)

	// Convert EvalResult to JSON
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal eval result: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", hawkURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create hawk request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	if config.APIKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.APIKey))
	}
	req.Header.Set("User-Agent", "loom-eval/1.0")

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to export to hawk: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("hawk export failed with status %d: %s", resp.StatusCode, resp.Status)
	}

	return nil
}

// ExportBatch exports multiple eval results to Hawk in a single request
func ExportBatch(ctx context.Context, results []*loomv1.EvalResult, config *HawkExportConfig) error {
	if len(results) == 0 {
		return nil
	}

	if config == nil {
		config = &HawkExportConfig{}
	}

	// Apply defaults (same as ExportToHawk)
	if config.Endpoint == "" {
		config.Endpoint = os.Getenv("HAWK_ENDPOINT")
		if config.Endpoint == "" {
			config.Endpoint = "http://localhost:8080"
		}
	}

	if config.APIKey == "" {
		config.APIKey = os.Getenv("HAWK_API_KEY")
	}

	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}

	client := config.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: config.Timeout,
		}
	}

	// Construct batch endpoint
	hawkURL := fmt.Sprintf("%s/v1/evals/batch", config.Endpoint)

	// Create batch payload
	batchPayload := map[string]interface{}{
		"results": results,
	}

	payload, err := json.Marshal(batchPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal batch payload: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", hawkURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create hawk batch request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	if config.APIKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.APIKey))
	}
	req.Header.Set("User-Agent", "loom-eval/1.0")

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to export batch to hawk: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("hawk batch export failed with status %d: %s", resp.StatusCode, resp.Status)
	}

	return nil
}
