// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Registry holds one Scheduler per provider quota scope. The zero value is
// not usable; construct with NewRegistry.
type Registry struct {
	mu     sync.Mutex
	m      map[string]*Scheduler
	logger *zap.Logger
}

// NewRegistry creates an empty scheduler registry.
func NewRegistry(logger *zap.Logger) *Registry {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Registry{m: map[string]*Scheduler{}, logger: logger}
}

// For returns the scope's scheduler, creating it with cfg on first use.
// Unlike the rate limiters, schedulers are keyed by scope alone: one quota
// boundary has exactly one arbiter, whatever configs individual agents carry.
func (r *Registry) For(scope string, cfg Config) *Scheduler {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.m[scope]; ok {
		return s
	}
	if cfg.Logger == nil {
		cfg.Logger = r.logger
	}
	s := New(scope, cfg)
	r.m[scope] = s
	return s
}

// SetLogger replaces the logger used for schedulers created after this
// call (the default registry is built before looms has a logger).
func (r *Registry) SetLogger(l *zap.Logger) {
	if l == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logger = l
}

// Get returns the scope's scheduler if it exists.
func (r *Registry) Get(scope string) (*Scheduler, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[scope]
	return s, ok
}

// Scopes returns all registered scopes, sorted.
func (r *Registry) Scopes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.m))
	for k := range r.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Close stops every scheduler.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.m {
		s.Close()
	}
}

// Service implements loomv1.LLMSchedulerServiceServer over a Registry.
type Service struct {
	loomv1.UnimplementedLLMSchedulerServiceServer
	reg *Registry
}

// NewService wraps a registry in the gRPC observability/admin surface.
func NewService(reg *Registry) *Service {
	return &Service{reg: reg}
}

// GetSlotState returns the live state of one scope, or all scopes. Every
// returned state also carries the process-wide door gate's live counters
// (active_conversations / door_queue_depth): the door is one gate in front
// of every scope, so all scopes report the same values.
func (s *Service) GetSlotState(_ context.Context, req *loomv1.GetSlotStateRequest) (*loomv1.GetSlotStateResponse, error) {
	resp := &loomv1.GetSlotStateResponse{}
	if scope := req.GetScope(); scope != "" {
		if sched, ok := s.reg.Get(scope); ok {
			resp.States = append(resp.States, sched.State())
		}
	} else {
		for _, scope := range s.reg.Scopes() {
			if sched, ok := s.reg.Get(scope); ok {
				resp.States = append(resp.States, sched.State())
			}
		}
	}
	doorActive, doorQueued := Door().DoorState()
	for _, st := range resp.States {
		st.ActiveConversations = int32(doorActive) // #nosec G115 -- bounded by the operator-set ceiling
		st.DoorQueueDepth = int32(doorQueued)      // #nosec G115 -- bounded by the operator-set queue cap / live request count
	}
	return resp, nil
}

// ListWaiters returns the parked slot requests of a scope.
func (s *Service) ListWaiters(_ context.Context, req *loomv1.ListWaitersRequest) (*loomv1.ListWaitersResponse, error) {
	resp := &loomv1.ListWaitersResponse{}
	if sched, ok := s.reg.Get(req.GetScope()); ok {
		resp.Waiters = sched.Waiters()
	}
	return resp, nil
}

// SetSchedulerConfig replaces a scope's runtime-tunable configuration.
func (s *Service) SetSchedulerConfig(_ context.Context, req *loomv1.SetSchedulerConfigRequest) (*loomv1.SetSchedulerConfigResponse, error) {
	cfg := req.GetConfig()
	sched := s.reg.For(req.GetScope(), Config{
		TokensPerMinute:     cfg.GetTokensPerMinute(),
		UtilizationTarget:   float64(cfg.GetUtilizationTarget()),
		StarvationAge:       time.Duration(cfg.GetStarvationAgeS()) * time.Second,
		InteractiveHeadroom: float64(cfg.GetInteractiveHeadroom()),
	})
	sched.SetConfig(cfg.GetTokensPerMinute(), float64(cfg.GetUtilizationTarget()),
		time.Duration(cfg.GetStarvationAgeS())*time.Second,
		float64(cfg.GetInteractiveHeadroom()))
	return &loomv1.SetSchedulerConfigResponse{State: sched.State()}, nil
}
