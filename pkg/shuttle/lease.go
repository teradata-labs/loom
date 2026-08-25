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

// Lease events are the backend-agnostic contract through which a tool result
// declares that its conversation acquired or released a scarce,
// session-scoped backend resource — a database session handle, an API
// session under a concurrency cap. loom itself never knows what the resource
// IS: the backend names it (Kind) and identifies it (ID), and loom reacts
// generically — a conversation with an outstanding lease is scheduled in the
// LLM slot scheduler's RESOURCE_HOLDER class (priority inheritance: it is
// blocking other agents, so it must finish and release first), and the last
// release drops it back to the ordinary classes.
//
// Transport is a well-known Result.Metadata key (MetadataLeaseEvents), so any
// backend adapter — or a YAML-declared tool that shapes its own metadata —
// can emit lease events without importing these types. Entries are stored in
// the JSON-shaped form (a []interface{} of map[string]interface{} with
// "action", "kind", "id" string fields) so a metadata map that crosses a
// JSON marshal/unmarshal boundary parses identically on the other side.

// MetadataLeaseEvents is the well-known Result.Metadata key carrying the
// result's lease events. The value is a []interface{} whose entries are
// map[string]interface{} with "action", "kind", and "id" string fields
// (typed LeaseEvent entries are also accepted on read).
const MetadataLeaseEvents = "loom.lease_events"

// LeaseAction says which side of a lease's lifetime an event marks.
type LeaseAction string

const (
	// LeaseAcquired tells loom "this conversation now holds a scarce backend
	// resource": the conversation's remaining LLM calls — this turn and every
	// following turn on the session, until the lease is released — ride the
	// RESOURCE_HOLDER scheduling class.
	LeaseAcquired LeaseAction = "acquired"

	// LeaseReleased tells loom the lease ended. When it was the session's
	// last outstanding lease, the conversation's LLM calls fall back to the
	// ordinary call-count classes.
	LeaseReleased LeaseAction = "released"
)

// LeaseEvent is one acquire or release of a backend-defined lease. Kind and
// ID are opaque, backend-defined strings (e.g. "db-session" / "sess-42");
// loom never interprets them beyond identity — a release matches an acquire
// only when both Kind and ID are equal.
type LeaseEvent struct {
	Action LeaseAction
	Kind   string
	ID     string
}

// valid reports whether the event carries enough to track: a known action
// and a non-empty ID. Kind may be empty (identity is still the exact
// (Kind, ID) pair).
func (ev LeaseEvent) valid() bool {
	return (ev.Action == LeaseAcquired || ev.Action == LeaseReleased) && ev.ID != ""
}

// AppendLeaseEvent records a lease event on a tool result, initializing the
// metadata map and the events slice as needed. The event is stored in the
// JSON-shaped form so the result parses identically after crossing a
// marshal/unmarshal boundary. A nil result is a no-op. Existing entries are
// preserved untouched, valid or not — tool results are data.
func AppendLeaseEvent(res *Result, ev LeaseEvent) {
	if res == nil {
		return
	}
	if res.Metadata == nil {
		res.Metadata = make(map[string]interface{})
	}
	entries, _ := res.Metadata[MetadataLeaseEvents].([]interface{})
	res.Metadata[MetadataLeaseEvents] = append(entries, map[string]interface{}{
		"action": string(ev.Action),
		"kind":   ev.Kind,
		"id":     ev.ID,
	})
}

// LeaseEventsFrom extracts the lease events a tool result declares, in emit
// order. Parsing is tolerant by contract: tool results are data, so a
// malformed entry — wrong container type, wrong entry type, unknown action,
// missing ID — is skipped, never an error. A result without the key (or a
// nil result) yields nil.
func LeaseEventsFrom(res *Result) []LeaseEvent {
	if res == nil || res.Metadata == nil {
		return nil
	}
	entries, ok := res.Metadata[MetadataLeaseEvents].([]interface{})
	if !ok {
		return nil
	}
	var events []LeaseEvent
	for _, entry := range entries {
		if ev, ok := leaseEventFromEntry(entry); ok {
			events = append(events, ev)
		}
	}
	return events
}

// leaseEventFromEntry parses one metadata entry: the JSON-shaped map form
// (the stored form, and what any JSON round-trip produces) or a typed
// LeaseEvent a Go emitter placed directly.
func leaseEventFromEntry(entry interface{}) (LeaseEvent, bool) {
	switch v := entry.(type) {
	case LeaseEvent:
		return v, v.valid()
	case map[string]interface{}:
		action, _ := v["action"].(string)
		kind, _ := v["kind"].(string)
		id, _ := v["id"].(string)
		ev := LeaseEvent{Action: LeaseAction(action), Kind: kind, ID: id}
		return ev, ev.valid()
	default:
		return LeaseEvent{}, false
	}
}
