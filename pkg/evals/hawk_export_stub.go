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
//go:build !hawk

package evals

import (
	"context"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// HawkExportConfig stub (only used for type checking)
type HawkExportConfig struct {
	Endpoint   string
	APIKey     string
	Timeout    interface{}
	HTTPClient interface{}
}

// ExportToHawk returns an error when built without hawk build tag.
// To enable Hawk export support, build with: go build -tags hawk
func ExportToHawk(ctx context.Context, result *loomv1.EvalResult, config *HawkExportConfig) error {
	return fmt.Errorf("hawk export not compiled in (rebuild with -tags hawk)")
}
