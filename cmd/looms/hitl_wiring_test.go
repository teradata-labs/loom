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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// serve must hand its HITL origins a real notifier. A nil one is silent in
// exactly the way a held turn cannot afford: no pending card reaches the
// caller, and no heartbeat keeps the stream alive for the length of the hold.
func TestHITLNotifier_IsWiredAndNotNil(t *testing.T) {
	n := hitlNotifier()
	require.NotNil(t, n, "serve must wire a real notifier into every HITL hold origin")
}

// The waiters reach the heartbeat only through an interface assertion, so a
// notifier that stops advertising the capability would silently return every
// hold to being byte-silent — with nothing failing to say so.
func TestHITLNotifier_AdvertisesHeartbeater(t *testing.T) {
	_, ok := hitlNotifier().(shuttle.Heartbeater)
	require.True(t, ok, "serve's HITL notifier must implement shuttle.Heartbeater")
}

// Guards the wiring itself: every HITL hold origin in serve must be handed
// hitlNotifier(), never a nil literal. This is a source check because the
// origins are built deep inside serve's startup path, and the failure it
// catches is invisible at runtime — a nil notifier does not error, it just
// never emits.
func TestServe_AllHITLOriginsUseTheSharedNotifier(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".", "cmd_serve.go"))
	require.NoError(t, err)
	text := string(src)

	askResolver := regexp.MustCompile(`(?s)shuttle\.NewHITLAskResolver\((.*?)\n\t*\)`)
	asks := askResolver.FindAllStringSubmatch(text, -1)
	require.NotEmpty(t, asks, "expected serve to build an ask resolver")
	for _, m := range asks {
		require.Contains(t, m[1], "hitlNotifier()",
			"the ask resolver must be wired with hitlNotifier(); a nil notifier drops the card AND the heartbeats")
	}

	contactHuman := regexp.MustCompile(`(?s)shuttle\.ContactHumanConfig\{(.*?)\}`)
	contacts := contactHuman.FindAllStringSubmatch(text, -1)
	require.NotEmpty(t, contacts, "expected serve to register contact_human")
	for _, m := range contacts {
		require.Contains(t, m[1], "Notifier: hitlNotifier()",
			"every contact_human registration (cold start AND hot reload) must be wired with hitlNotifier()")
	}
	require.GreaterOrEqual(t, len(contacts), 2,
		"both the cold-start and hot-reload registration paths must be covered")
}

// The notifier bridge lives in pkg/agent because pkg/shuttle cannot import it;
// this pins that serve reaches it through the one intended entry point rather
// than re-deriving a second bridge that could drift.
func TestHITLNotifier_ComesFromTheAgentBridge(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".", "hitl_wiring.go"))
	require.NoError(t, err)
	require.True(t, strings.Contains(string(src), "agent.NewProgressNotifier()"),
		"hitlNotifier must return the pkg/agent progress bridge")
}
