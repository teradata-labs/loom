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

package observability

import "fmt"

// NewHawkTracer returns an error when built without hawk build tag.
// To enable Hawk HTTP export support, build with: go build -tags hawk
// Note: Embedded tracing (NewEmbeddedTracer) is always available without build tags.
func NewHawkTracer(config HawkConfig) (Tracer, error) {
	return nil, fmt.Errorf("hawk HTTP export not compiled in (rebuild with -tags hawk)")
}

// NewHawkJudgeExporter returns an error when built without hawk build tag.
// To enable Hawk judge export support, build with: go build -tags hawk
func NewHawkJudgeExporter(config *HawkJudgeExporterConfig) (*HawkJudgeExporter, error) {
	return nil, fmt.Errorf("hawk judge exporter not compiled in (rebuild with -tags hawk)")
}
