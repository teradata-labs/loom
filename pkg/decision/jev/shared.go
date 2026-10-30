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
	"sort"
	"strings"
	"sync"
	"time"
)

// sharedClients is the process-wide client registry behind Shared.
var sharedClients = struct {
	mu      sync.Mutex
	clients map[sharedClientKey]*Client
}{clients: make(map[sharedClientKey]*Client)}

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
	sharedClients.clients = make(map[sharedClientKey]*Client)
}

// sharedClientKey identifies a Config for Shared: every field that changes
// what is sent, where, or under which budget. The HTTP client is
// deliberately excluded: an injected transport does not change what is sent
// or how much. The key is compared in memory only and lives no longer than
// the Client it indexes, which holds the same API key itself, so it is kept
// as-is rather than hashed.
type sharedClientKey struct {
	baseURL, path, model     string
	authHeader, authScheme   string
	apiKey                   string
	extraHeaders             string
	timeout                  time.Duration
	maxAttempts              int
	requestsPerMinute        float64
	pricePerMillionInputToks float64
	maxQuestionsPerRequest   int
}

func sharedKey(cfg Config) sharedClientKey {
	extras := make([]string, 0, len(cfg.ExtraHeaders))
	for k, v := range cfg.ExtraHeaders {
		extras = append(extras, k+"\x00"+v)
	}
	sort.Strings(extras)
	return sharedClientKey{
		baseURL:                  cfg.BaseURL,
		path:                     cfg.Path,
		model:                    cfg.Model,
		authHeader:               cfg.AuthHeader,
		authScheme:               cfg.AuthScheme,
		apiKey:                   cfg.APIKey,
		extraHeaders:             strings.Join(extras, "\x01"),
		timeout:                  cfg.Timeout,
		maxAttempts:              cfg.MaxAttempts,
		requestsPerMinute:        cfg.RequestsPerMinute,
		pricePerMillionInputToks: cfg.PricePerMillionInputTokens,
		maxQuestionsPerRequest:   cfg.MaxQuestionsPerRequest,
	}
}
