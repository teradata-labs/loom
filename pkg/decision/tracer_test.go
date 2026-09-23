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

package decision_test

import (
	"sync"

	"github.com/teradata-labs/loom/pkg/observability"
)

// recordedMetric is one RecordMetric call.
type recordedMetric struct {
	Name   string
	Value  float64
	Labels map[string]string
}

// recordingTracer keeps spans (via MockTracer) and, unlike MockTracer, also
// keeps metrics so tests can assert on them.
type recordingTracer struct {
	*observability.MockTracer
	mu      sync.Mutex
	metrics []recordedMetric
}

func newRecordingTracer() *recordingTracer {
	return &recordingTracer{MockTracer: observability.NewMockTracer()}
}

func (r *recordingTracer) RecordMetric(name string, value float64, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make(map[string]string, len(labels))
	for k, v := range labels {
		cp[k] = v
	}
	r.metrics = append(r.metrics, recordedMetric{Name: name, Value: value, Labels: cp})
}

func (r *recordingTracer) Metrics(name string) []recordedMetric {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recordedMetric
	for _, m := range r.metrics {
		if m.Name == name {
			out = append(out, m)
		}
	}
	return out
}
