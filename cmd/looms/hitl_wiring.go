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
package main

import (
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// hitlNotifier returns the notifier EVERY HITL hold origin in serve is wired
// with — the ask resolver and both contact_human registration paths (cold start
// and hot reload). One function so the origins cannot drift, and so the wiring
// is reachable from a test: a nil notifier here is silent in exactly the way a
// held turn cannot afford, costing the pending card AND the heartbeats that
// keep the caller's stream alive for the length of the hold.
//
// The bridge it returns also implements shuttle.Heartbeater, which the waiters
// discover by interface assertion; hitlNotifierBeats pins that so the
// capability cannot be lost by swapping in a plain Notifier.
func hitlNotifier() shuttle.Notifier { return agent.NewProgressNotifier() }
