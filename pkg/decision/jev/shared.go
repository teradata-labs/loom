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

package jev

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// sharedClients is the process-wide client registry behind Shared.
var sharedClients = struct {
	mu      sync.Mutex
	clients map[string]*Client
}{clients: make(map[string]*Client)}

// Shared returns the process-wide Client for cfg, building it on first use.
// Two configurations that would send the same request to the same place
// under the same budget map to one Client, and therefore one rate limiter:
// a fleet of agents with the same decider settings draws on one tier
// budget instead of multiplying it by the agent count. Configurations that
// differ in endpoint, credentials, model, timeout, attempts, or rate get
// their own Client. Errors are New's.
func Shared(cfg Config) (*Client, error) {
	key := sharedKey(cfg)
	sharedClients.mu.Lock()
	defer sharedClients.mu.Unlock()
	if c, ok := sharedClients.clients[key]; ok {
		return c, nil
	}
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	sharedClients.clients[key] = c
	return c, nil
}

// ResetShared drops every shared client. Tests use it between cases; a
// server never needs to.
func ResetShared() {
	sharedClients.mu.Lock()
	defer sharedClients.mu.Unlock()
	sharedClients.clients = make(map[string]*Client)
}

// sharedKey identifies a Config for Shared. The API key is hashed into it
// rather than kept, so the map holds no credential in the clear. The HTTP
// client is deliberately excluded: an injected transport does not change
// what is sent or how much.
func sharedKey(cfg Config) string {
	extras := make([]string, 0, len(cfg.ExtraHeaders))
	for k, v := range cfg.ExtraHeaders {
		extras = append(extras, k+"="+v)
	}
	sort.Strings(extras)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%g\x00%g",
		cfg.BaseURL, cfg.Path, cfg.Model, cfg.AuthHeader, cfg.AuthScheme, cfg.APIKey,
		strings.Join(extras, "\x01"), cfg.Timeout, cfg.MaxAttempts, cfg.RequestsPerMinute,
		cfg.PricePerMillionInputTokens)
	return hex.EncodeToString(h.Sum(nil))
}
