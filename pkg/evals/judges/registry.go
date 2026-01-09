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
//go:build hawk

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package judges

import (
	"fmt"
	"sync"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Registry manages judge instances.
// Provides thread-safe registration and lookup of judges.
type Registry struct {
	mu     sync.RWMutex
	judges map[string]Judge
}

// NewRegistry creates a new judge registry.
func NewRegistry() *Registry {
	return &Registry{
		judges: make(map[string]Judge),
	}
}

// Register adds a judge to the registry.
func (r *Registry) Register(judge Judge) error {
	if judge == nil {
		return fmt.Errorf("judge cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	id := judge.ID()
	if id == "" {
		return fmt.Errorf("judge ID cannot be empty")
	}

	// Check for duplicate
	if _, exists := r.judges[id]; exists {
		return fmt.Errorf("judge %s already registered", id)
	}

	r.judges[id] = judge
	return nil
}

// Unregister removes a judge from the registry.
func (r *Registry) Unregister(judgeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.judges[judgeID]; !exists {
		return fmt.Errorf("judge %s not found", judgeID)
	}

	delete(r.judges, judgeID)
	return nil
}

// Get retrieves a judge by ID.
func (r *Registry) Get(judgeID string) (Judge, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	judge, exists := r.judges[judgeID]
	if !exists {
		return nil, fmt.Errorf("judge %s not found", judgeID)
	}

	return judge, nil
}

// GetJudges retrieves multiple judges by IDs.
// Returns an error if any judge is not found.
func (r *Registry) GetJudges(judgeIDs []string) ([]Judge, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	judges := make([]Judge, 0, len(judgeIDs))
	for _, id := range judgeIDs {
		judge, exists := r.judges[id]
		if !exists {
			return nil, fmt.Errorf("judge %s not found", id)
		}
		judges = append(judges, judge)
	}

	return judges, nil
}

// GetAll returns all registered judges.
func (r *Registry) GetAll() []Judge {
	r.mu.RLock()
	defer r.mu.RUnlock()

	judges := make([]Judge, 0, len(r.judges))
	for _, judge := range r.judges {
		judges = append(judges, judge)
	}

	return judges
}

// List returns information about all registered judges.
func (r *Registry) List() []*JudgeInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]*JudgeInfo, 0, len(r.judges))
	for _, judge := range r.judges {
		infos = append(infos, &JudgeInfo{
			ID:          judge.ID(),
			Name:        judge.Name(),
			Criteria:    judge.Criteria(),
			Weight:      judge.Weight(),
			Criticality: judge.Criticality(),
			Dimensions:  judge.Dimensions(),
		})
	}

	return infos
}

// FilterByCriticality returns judges matching the specified criticality level.
func (r *Registry) FilterByCriticality(criticality loomv1.JudgeCriticality) []Judge {
	r.mu.RLock()
	defer r.mu.RUnlock()

	judges := make([]Judge, 0)
	for _, judge := range r.judges {
		if judge.Criticality() == criticality {
			judges = append(judges, judge)
		}
	}

	return judges
}

// FilterByDimension returns judges that evaluate the specified dimension.
func (r *Registry) FilterByDimension(dimension loomv1.JudgeDimension) []Judge {
	r.mu.RLock()
	defer r.mu.RUnlock()

	judges := make([]Judge, 0)
	for _, judge := range r.judges {
		for _, dim := range judge.Dimensions() {
			if dim == dimension {
				judges = append(judges, judge)
				break
			}
		}
	}

	return judges
}

// Count returns the number of registered judges.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.judges)
}

// JudgeInfo contains metadata about a registered judge.
type JudgeInfo struct {
	ID          string
	Name        string
	Criteria    []string
	Weight      float64
	Criticality loomv1.JudgeCriticality
	Dimensions  []loomv1.JudgeDimension
}
