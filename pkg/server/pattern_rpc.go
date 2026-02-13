// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/patterns"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// LoadPatterns loads pattern definitions from a directory for one or all agents.
// If agent_id is specified, loads patterns for that agent only.
// If source is provided without agent_id, loads for all agents.
// If force_reload is true, clears the pattern cache before loading.
func (s *MultiAgentServer) LoadPatterns(ctx context.Context, req *loomv1.LoadPatternsRequest) (*loomv1.LoadPatternsResponse, error) {
	// Validate request: source is required when no agent_id is specified,
	// because we need to know where to load patterns from.
	if req.Source == "" && req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "source is required when agent_id is not specified")
	}

	var totalLoaded int32
	var allNames []string
	var allErrors []string

	if req.AgentId != "" {
		// Load patterns for a specific agent
		loaded, names, errs, err := s.loadPatternsForAgent(req.AgentId, req.Source, req.Domains, req.ForceReload)
		if err != nil {
			return nil, err
		}
		totalLoaded = loaded
		allNames = names
		allErrors = errs
	} else {
		// Load patterns for all agents using the provided source
		s.mu.RLock()
		agentIDs := make([]string, 0, len(s.agents))
		for id := range s.agents {
			agentIDs = append(agentIDs, id)
		}
		s.mu.RUnlock()

		for _, agentID := range agentIDs {
			loaded, names, errs, err := s.loadPatternsForAgent(agentID, req.Source, req.Domains, req.ForceReload)
			if err != nil {
				// Collect as non-fatal error for multi-agent load
				allErrors = append(allErrors, fmt.Sprintf("agent %s: %v", agentID, err))
				continue
			}
			totalLoaded += loaded
			allNames = append(allNames, names...)
			allErrors = append(allErrors, errs...)
		}
	}

	// Broadcast pattern load event
	if s.patternBroadcaster != nil && totalLoaded > 0 {
		agentID := req.AgentId
		if agentID == "" {
			agentID = "all"
		}
		s.patternBroadcaster.BroadcastPatternCreated(agentID, fmt.Sprintf("bulk_load:%d", totalLoaded), "", req.Source)
	}

	if s.logger != nil {
		s.logger.Info("LoadPatterns completed",
			zap.Int32("patterns_loaded", totalLoaded),
			zap.Int("pattern_names", len(allNames)),
			zap.Int("errors", len(allErrors)),
			zap.String("source", req.Source),
			zap.String("agent_id", req.AgentId),
		)
	}

	return &loomv1.LoadPatternsResponse{
		PatternsLoaded: totalLoaded,
		PatternNames:   allNames,
		Errors:         allErrors,
	}, nil
}

// loadPatternsForAgent loads patterns for a single agent, returning counts and names.
func (s *MultiAgentServer) loadPatternsForAgent(agentID, source string, domains []string, forceReload bool) (int32, []string, []string, error) {
	ag, resolvedID, err := s.getAgent(agentID)
	if err != nil {
		return 0, nil, nil, status.Errorf(codes.NotFound, "agent not found: %v", err)
	}

	// Determine the patterns source directory.
	// If source is provided in the request, use that; otherwise fall back to agent config.
	patternsDir := source
	if patternsDir == "" {
		patternsDir = ag.GetConfig().PatternsDir
	}
	if patternsDir == "" {
		return 0, nil, nil, status.Error(codes.FailedPrecondition, "no pattern source specified and agent has no patterns_dir configured")
	}

	// Validate directory exists before proceeding
	if _, statErr := os.Stat(patternsDir); statErr != nil {
		return 0, nil, nil, status.Errorf(codes.InvalidArgument, "pattern source directory not accessible: %v", statErr)
	}

	// Get or create a pattern library for loading
	orchestrator := ag.GetOrchestrator()
	if orchestrator == nil {
		return 0, nil, nil, status.Error(codes.FailedPrecondition, "agent has no pattern orchestrator")
	}

	library := orchestrator.GetLibrary()
	if library == nil {
		return 0, nil, nil, status.Error(codes.FailedPrecondition, "agent has no pattern library")
	}

	// If source was provided, update the library's patterns directory
	if source != "" {
		library.SetPatternsDir(patternsDir)
	}

	// Force reload: clear cache first
	if forceReload {
		library.ClearCache()
	}

	// List all available patterns from the library
	summaries := library.ListAll()

	// Apply domain filters if specified
	if len(domains) > 0 {
		summaries = filterSummariesByDomains(summaries, domains)
	}

	var names []string
	var loadErrors []string
	for _, summary := range summaries {
		names = append(names, summary.Name)
	}

	// Set up or update hot-reloader for this agent.
	// We create the reloader under the lock, then start it outside the lock
	// to avoid holding the mutex during filesystem operations.
	// The hot-reloader uses zap.NewNop() because it runs asynchronously and may
	// outlive the caller's context (preventing races with test loggers).
	var newReloader *patterns.HotReloader
	s.mu.Lock()
	if _, exists := s.hotReloaders[resolvedID]; !exists {
		hr, hrErr := patterns.NewHotReloader(library, patterns.HotReloadConfig{
			Enabled:    true,
			DebounceMs: 500,
			Logger:     zap.NewNop(),
		})
		if hrErr != nil {
			loadErrors = append(loadErrors, fmt.Sprintf("failed to create hot-reloader: %v", hrErr))
		} else {
			s.hotReloaders[resolvedID] = hr
			newReloader = hr
		}
	}
	s.mu.Unlock()

	// Start the hot-reloader outside the lock
	if newReloader != nil {
		if startErr := newReloader.Start(context.Background()); startErr != nil {
			loadErrors = append(loadErrors, fmt.Sprintf("failed to start hot-reloader: %v", startErr))
		}
	}

	return safeIntToInt32(len(names)), names, loadErrors, nil
}

// filterSummariesByDomains filters pattern summaries to only include those matching the given domains.
func filterSummariesByDomains(summaries []patterns.PatternSummary, domains []string) []patterns.PatternSummary {
	domainSet := make(map[string]bool, len(domains))
	for _, d := range domains {
		domainSet[strings.ToLower(d)] = true
	}

	filtered := make([]patterns.PatternSummary, 0, len(summaries))
	for _, s := range summaries {
		// Match on BackendType since the internal Pattern uses BackendType as domain equivalent
		if domainSet[strings.ToLower(s.BackendType)] || domainSet[strings.ToLower(s.Category)] {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// ListPatterns retrieves available patterns with optional filtering by domain, category, or search query.
// Iterates through all agents' pattern libraries and aggregates results.
func (s *MultiAgentServer) ListPatterns(ctx context.Context, req *loomv1.ListPatternsRequest) (*loomv1.ListPatternsResponse, error) {
	s.mu.RLock()
	agentsCopy := make(map[string]*patterns.Library)
	for id, ag := range s.agents {
		if orch := ag.GetOrchestrator(); orch != nil {
			if lib := orch.GetLibrary(); lib != nil {
				agentsCopy[id] = lib
			}
		}
	}
	s.mu.RUnlock()

	// Deduplicate patterns by name (same pattern might exist across agents)
	seen := make(map[string]bool)
	var protoPatterns []*loomv1.Pattern

	for _, lib := range agentsCopy {
		var summaries []patterns.PatternSummary

		// Apply filtering strategy based on request fields
		switch {
		case req.Search != "":
			summaries = lib.Search(req.Search)
		case req.Category != "":
			summaries = lib.FilterByCategory(req.Category)
		default:
			summaries = lib.ListAll()
		}

		// Apply domain filter on top of category/search results
		if req.Domain != "" {
			summaries = filterSummariesByDomain(summaries, req.Domain)
		}

		for _, summary := range summaries {
			if seen[summary.Name] {
				continue
			}
			seen[summary.Name] = true

			// Load full pattern for complete conversion
			fullPattern, err := lib.Load(summary.Name)
			if err != nil {
				// Fall back to summary-only conversion
				protoPatterns = append(protoPatterns, summaryToProto(&summary))
				continue
			}
			protoPatterns = append(protoPatterns, internalPatternToProto(fullPattern))
		}
	}

	if s.logger != nil {
		s.logger.Info("ListPatterns completed",
			zap.Int("total_count", len(protoPatterns)),
			zap.String("domain", req.Domain),
			zap.String("category", req.Category),
			zap.String("search", req.Search),
		)
	}

	return &loomv1.ListPatternsResponse{
		Patterns:   protoPatterns,
		TotalCount: safeIntToInt32(len(protoPatterns)),
	}, nil
}

// filterSummariesByDomain filters summaries by a single domain string.
func filterSummariesByDomain(summaries []patterns.PatternSummary, domain string) []patterns.PatternSummary {
	domainLower := strings.ToLower(domain)
	filtered := make([]patterns.PatternSummary, 0, len(summaries))
	for _, s := range summaries {
		if strings.ToLower(s.BackendType) == domainLower || strings.ToLower(s.Category) == domainLower {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// GetPattern retrieves a specific pattern by exact name match.
// Searches across all agents' pattern libraries.
func (s *MultiAgentServer) GetPattern(ctx context.Context, req *loomv1.GetPatternRequest) (*loomv1.Pattern, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "pattern name is required")
	}

	s.mu.RLock()
	agentLibraries := make([]*patterns.Library, 0, len(s.agents))
	for _, ag := range s.agents {
		if orch := ag.GetOrchestrator(); orch != nil {
			if lib := orch.GetLibrary(); lib != nil {
				agentLibraries = append(agentLibraries, lib)
			}
		}
	}
	s.mu.RUnlock()

	// Search across all agent libraries for the pattern by name
	for _, lib := range agentLibraries {
		pattern, err := lib.Load(req.Name)
		if err == nil {
			if s.logger != nil {
				s.logger.Info("GetPattern found",
					zap.String("name", req.Name),
					zap.String("category", pattern.Category),
				)
			}
			return internalPatternToProto(pattern), nil
		}
	}

	return nil, status.Errorf(codes.NotFound, "pattern not found: %s", req.Name)
}

// internalPatternToProto converts an internal patterns.Pattern to a proto loomv1.Pattern.
func internalPatternToProto(p *patterns.Pattern) *loomv1.Pattern {
	proto := &loomv1.Pattern{
		Name:        p.Name,
		Domain:      p.BackendType,
		Category:    p.Category,
		Description: p.Description,
	}

	// Convert parameters
	for _, param := range p.Parameters {
		proto.Parameters = append(proto.Parameters, &loomv1.PatternParameter{
			Name:         param.Name,
			Type:         param.Type,
			Required:     param.Required,
			DefaultValue: param.DefaultValue,
			Description:  param.Description,
		})
	}

	// Convert examples
	for _, ex := range p.Examples {
		proto.Examples = append(proto.Examples, &loomv1.PatternExample{
			Input:       ex.Name,
			Output:      ex.ExpectedResult,
			Description: ex.Description,
		})
	}

	// Build backend hints from relevant fields
	hints := make(map[string]string)
	if p.BackendFunction != "" {
		hints["backend_function"] = p.BackendFunction
	}
	if p.Difficulty != "" {
		hints["difficulty"] = p.Difficulty
	}
	if p.BestPractices != "" {
		hints["best_practices"] = p.BestPractices
	}
	if len(hints) > 0 {
		proto.BackendHints = hints
	}

	// Build tags from use cases and related patterns
	var tags []string
	tags = append(tags, p.UseCases...)
	tags = append(tags, p.RelatedPatterns...)
	if len(tags) > 0 {
		proto.Tags = tags
	}

	return proto
}

// summaryToProto converts a patterns.PatternSummary to a proto loomv1.Pattern.
// Used as a fallback when full pattern load fails.
func summaryToProto(s *patterns.PatternSummary) *loomv1.Pattern {
	proto := &loomv1.Pattern{
		Name:        s.Name,
		Domain:      s.BackendType,
		Category:    s.Category,
		Description: s.Description,
	}

	// Build backend hints from summary fields
	hints := make(map[string]string)
	if s.BackendFunction != "" {
		hints["backend_function"] = s.BackendFunction
	}
	if s.Difficulty != "" {
		hints["difficulty"] = s.Difficulty
	}
	if len(hints) > 0 {
		proto.BackendHints = hints
	}

	// Use cases become tags
	if len(s.UseCases) > 0 {
		proto.Tags = s.UseCases
	}

	return proto
}

// safeIntToInt32 converts an int to int32, clamping to math.MaxInt32 to
// prevent integer overflow. This satisfies gosec G115.
func safeIntToInt32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n) // #nosec G115 -- bounds checked above
}
