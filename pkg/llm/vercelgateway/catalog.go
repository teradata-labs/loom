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

package vercelgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

const (
	// DefaultCatalogRefresh is how often the catalog is re-fetched. The
	// gateway publishes no polling guidance or cache headers for /v1/models.
	DefaultCatalogRefresh = time.Hour
	// catalogBodyLimit bounds the /v1/models response (hundreds of models at
	// ~1 KB each).
	catalogBodyLimit = 16 << 20
	catalogTimeout   = 30 * time.Second
	languageType     = "language"
	tagReasoning     = "reasoning"
	perMillion       = 1_000_000
)

// modelsResponse is the GET /v1/models body.
type modelsResponse struct {
	Data []modelEntry `json:"data"`
}

// modelEntry is one /v1/models entry (REST API docs, 2026-11). Prices are
// decimal strings in USD per token.
type modelEntry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	ContextWindow int64    `json:"context_window"`
	MaxTokens     int64    `json:"max_tokens"`
	Tags          []string `json:"tags"`
	Pricing       *struct {
		Input           string `json:"input"`
		Output          string `json:"output"`
		InputCacheRead  string `json:"input_cache_read"`
		InputCacheWrite string `json:"input_cache_write"`
	} `json:"pricing"`
}

// DecodeModels parses a /v1/models body into catalog rows under
// CatalogProvider. Only language models are kept. A price that is missing,
// negative, non-finite or unparseable is treated as unpublished (0), never
// guessed. Entries with an invalid id are skipped.
func DecodeModels(body []byte) ([]*loomv1.ModelInfo, error) {
	var resp modelsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("vercel-ai-gateway: decode /v1/models: %w", err)
	}
	out := make([]*loomv1.ModelInfo, 0, len(resp.Data))
	for _, e := range resp.Data {
		if e.Type != languageType || ValidateModelID(e.ID) != nil {
			continue
		}
		info := &loomv1.ModelInfo{
			Id:              e.ID,
			Name:            e.Name,
			Provider:        CatalogProvider,
			Capabilities:    append([]string(nil), e.Tags...),
			ContextWindow:   clampInt32(e.ContextWindow),
			MaxOutputTokens: clampInt32(e.MaxTokens),
			Available:       true,
			ShowInDropdown:  true,
		}
		if info.Name == "" {
			info.Name = e.ID
		}
		for _, t := range e.Tags {
			if t == tagReasoning {
				info.IsReasoning = true
			}
		}
		if p := e.Pricing; p != nil {
			info.CostPer_1MInputUsd = perTokenToPerMillion(p.Input)
			info.CostPer_1MOutputUsd = perTokenToPerMillion(p.Output)
			info.CostPer_1MCacheReadUsd = perTokenToPerMillion(p.InputCacheRead)
			info.CostPer_1MCacheWriteUsd = perTokenToPerMillion(p.InputCacheWrite)
		}
		out = append(out, info)
	}
	return out, nil
}

func perTokenToPerMillion(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	v := f * perMillion
	if math.IsInf(v, 0) {
		return 0
	}
	return v
}

func clampInt32(v int64) int32 {
	switch {
	case v <= 0:
		return 0
	case v > math.MaxInt32:
		return math.MaxInt32
	}
	return int32(v)
}

// CatalogSource is a catalog.Source holding the last successfully fetched
// gateway catalog. Lookups never touch the network; Refresh replaces the
// snapshot, and a failed Refresh keeps the previous one.
type CatalogSource struct {
	endpoint string
	client   *http.Client

	snap      atomic.Pointer[map[string]*loomv1.ModelInfo]
	refreshMu sync.Mutex // one Refresh at a time
}

// NewCatalogSource builds an empty source for the gateway at endpoint
// (normalised as for the client; empty = DefaultEndpoint). httpClient nil =
// a client with a 30s timeout. /v1/models needs no credential.
func NewCatalogSource(endpoint string, httpClient *http.Client) (*CatalogSource, error) {
	base, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: catalogTimeout}
	}
	s := &CatalogSource{endpoint: base, client: httpClient}
	empty := map[string]*loomv1.ModelInfo{}
	s.snap.Store(&empty)
	return s, nil
}

// Refresh fetches GET /v1/models and replaces the snapshot. It returns the
// number of language models now held.
func (s *CatalogSource) Refresh(ctx context.Context) (int, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint+ModelsPath, nil)
	if err != nil {
		return 0, fmt.Errorf("vercel-ai-gateway: create /v1/models request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("vercel-ai-gateway: fetch /v1/models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, catalogBodyLimit+1))
	if err != nil {
		return 0, fmt.Errorf("vercel-ai-gateway: read /v1/models: %w", err)
	}
	if len(body) > catalogBodyLimit {
		return 0, fmt.Errorf("vercel-ai-gateway: /v1/models response exceeds %d bytes", catalogBodyLimit)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("vercel-ai-gateway: /v1/models: status %d", resp.StatusCode)
	}
	models, err := DecodeModels(body)
	if err != nil {
		return 0, err
	}
	if len(models) == 0 {
		// An empty catalog is far more likely a broken response than a gateway
		// that serves nothing; keep the previous snapshot.
		return 0, fmt.Errorf("vercel-ai-gateway: /v1/models listed no language models")
	}
	byID := make(map[string]*loomv1.ModelInfo, len(models))
	for _, m := range models {
		byID[m.Id] = m
	}
	s.snap.Store(&byID)
	return len(byID), nil
}

// Lookup implements catalog.Source for the CatalogProvider namespace.
func (s *CatalogSource) Lookup(_ context.Context, provider, modelID string) *loomv1.ModelInfo {
	if !IsProviderName(provider) {
		return nil
	}
	return (*s.snap.Load())[modelID]
}

// List implements catalog.Source.
func (s *CatalogSource) List(_ context.Context) map[string][]*loomv1.ModelInfo {
	snap := *s.snap.Load()
	if len(snap) == 0 {
		return nil
	}
	models := make([]*loomv1.ModelInfo, 0, len(snap))
	for _, m := range snap {
		models = append(models, m)
	}
	return map[string][]*loomv1.ModelInfo{CatalogProvider: models}
}

// Len returns the number of models in the current snapshot.
func (s *CatalogSource) Len() int { return len(*s.snap.Load()) }

// RegisterCatalog fetches the gateway catalog once, registers it with
// catalog.RegisterNamed(CatalogProvider, …) ahead of the base catalog, and
// refreshes it every interval (<= 0 = DefaultCatalogRefresh) until ctx ends.
//
// This is opt-in network I/O: call it at server startup only when the
// provider is configured; importing the package never dials out. A failed
// first fetch is returned for the caller to log, but the source is still
// registered and refreshed, so the catalog fills in once the gateway is
// reachable. Until then Claude ids are priced by the Claude family table and
// other ids are reported unpriced.
func RegisterCatalog(ctx context.Context, endpoint string, interval time.Duration, logger *zap.Logger) (*CatalogSource, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	src, err := NewCatalogSource(endpoint, nil)
	if err != nil {
		return nil, err
	}
	catalog.RegisterNamed(CatalogProvider, src)
	n, firstErr := src.Refresh(ctx)
	if firstErr == nil {
		logger.Info("vercel-ai-gateway: catalog loaded", zap.Int("models", n), zap.String("endpoint", src.endpoint))
	}
	if interval <= 0 {
		interval = DefaultCatalogRefresh
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := src.Refresh(ctx); err != nil {
					logger.Warn("vercel-ai-gateway: catalog refresh failed; keeping the previous catalog", zap.Error(err))
				} else {
					logger.Debug("vercel-ai-gateway: catalog refreshed", zap.Int("models", n))
				}
			}
		}
	}()
	return src, firstErr
}

var _ catalog.Source = (*CatalogSource)(nil)
