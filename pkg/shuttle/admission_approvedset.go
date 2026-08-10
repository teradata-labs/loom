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
package shuttle

import (
	"context"
	"sync"

	"github.com/teradata-labs/loom/pkg/session"
)

// approvedSet is a dedicated membership store for admission approvals, keyed on
// (session, state_key). It is deliberately NOT the shared-memory blob store:
// an approved set is authorization state, so it must not be LRU-evictable, must
// not expire on a TTL while its session is alive, and a later render must UNION
// into the set, never replace it — three properties a cache line cannot give.
// The instance lives on one agent (each agent constructs its own), so its
// lifetime is the agent's; entries are small (canonicalized statements) and are
// dropped explicitly per session via ForgetSession when a host ends a session.
type approvedSet struct {
	mu   sync.Mutex
	sets map[approvedSetKey]map[CallIdentity]struct{}
}

// approvedSetKey partitions the store: the session is part of the KEY, not just
// an owner stamp, so two sessions of one governed agent can never read — or
// destroy — each other's approvals.
type approvedSetKey struct {
	sessionID string
	stateKey  string
}

// NewApprovedSet builds an ApprovedSetAccessor. The composition layer binds it
// to the executor so a gated-allowlist reads it as AdmissionRequest.State.
func NewApprovedSet() ApprovedSetAccessor {
	return &approvedSet{sets: make(map[approvedSetKey]map[CallIdentity]struct{})}
}

// Record unions ids into the approved set for (current session, stateKey).
// Empty identities are never recorded — an empty key would act as a wildcard
// membership. A record with no usable ids is a no-op.
func (a *approvedSet) Record(ctx context.Context, stateKey string, ids []CallIdentity) error {
	key := approvedSetKey{sessionID: session.SessionIDFromContext(ctx), stateKey: stateKey}
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.sets[key]
	if set == nil {
		set = make(map[CallIdentity]struct{}, len(ids))
		a.sets[key] = set
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		set[id] = struct{}{}
	}
	return nil
}

// Contains reports whether id is in the approved set for (current session,
// stateKey). An empty id is never a member; a missing set means no membership
// (both deny at the gated hook, fail-closed).
func (a *approvedSet) Contains(ctx context.Context, stateKey string, id CallIdentity) (bool, error) {
	if id == "" {
		return false, nil
	}
	key := approvedSetKey{sessionID: session.SessionIDFromContext(ctx), stateKey: stateKey}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.sets[key][id]
	return ok, nil
}

// ForgetSession drops every approved set the given session recorded, freeing
// the store when a host retires a session.
func (a *approvedSet) ForgetSession(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key := range a.sets {
		if key.sessionID == sessionID {
			delete(a.sets, key)
		}
	}
}
