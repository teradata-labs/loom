// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package scheduler implements the LLM slot scheduler: per provider-scope
// admission of LLM calls where waiting is a state, not an error. A caller
// that cannot be granted a slot parks until completion churn, capacity
// calibration, or aging wakes it — it is never killed by a scheduler-owned
// timeout. Design: docs/architecture/llm-slot-scheduler.md.
package scheduler

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Config configures one scope's scheduler. Zero values derive defaults; see
// the proto LLMSchedulerConfig for field semantics.
type Config struct {
	// TokensPerMinute is the enforced budget. 0 = start from
	// DefaultTokensPerMinute and let calibration take over.
	TokensPerMinute int64
	// UtilizationTarget in (0,1]; 0 = 0.8.
	UtilizationTarget float64
	// StarvationAge promotes a waiter one class after this long. 0 = 60s.
	StarvationAge time.Duration
	// InteractiveHeadroom in [0,1): fraction of the budget batch admission
	// cannot touch, kept free for the interactive band. 0 = default 0.15;
	// negative = no headroom (batch may reserve the full budget).
	InteractiveHeadroom float64
	// Logger for scheduler events. nil = no-op.
	Logger *zap.Logger
}

const (
	// DefaultTokensPerMinute is deliberately conservative; provider
	// telemetry calibrates upward immediately on the first response.
	DefaultTokensPerMinute     int64 = 40000
	defaultUtilization               = 0.8
	defaultStarvationAge             = 60 * time.Second
	defaultInteractiveHeadroom       = 0.15
	// aimdIncreaseStep is the additive per-clean-interval budget increase
	// for signal-free providers, as a fraction of the current ceiling.
	aimdIncreaseStep = 0.05
	// aimdDecreaseFactor halves the ceiling on an observed throttle.
	aimdDecreaseFactor = 0.5
	// throttleDecreaseCooldown bounds AIMD decreases to one per congestion
	// EVENT, not one per report: client retry loops report every 429 attempt
	// of a single logical call, and concurrent callers multiply the reports.
	// Without the hold-off, one throttled minute could halve the ceiling a
	// dozen times and wedge the scope below its smallest admissible
	// reservation.
	throttleDecreaseCooldown = 15 * time.Second
)

// Request describes one LLM call asking for a slot.
type Request struct {
	// ConversationID identifies the requesting conversation (for
	// observability and future per-conversation bookkeeping).
	ConversationID string
	// AgentName is the requesting agent (observability).
	AgentName string
	// Class is the request's priority class. Unspecified = NEW.
	Class loomv1.SlotPriorityClass
	// Origin bands the request: INTERACTIVE (a human is waiting on this
	// single turn) outranks BATCH entirely. Unspecified = BATCH.
	Origin loomv1.SlotOrigin
	// ReservationTokens is the quota this call will be charged at admission
	// by reservation-accounting providers: prompt estimate + max_tokens.
	// <= 0 reserves a nominal 1 so accounting never divides by zero.
	ReservationTokens int64
}

// Grant is a granted slot. Exactly one Release or Cancel call must follow;
// Release with the actual token usage trues up the reservation.
type Grant struct {
	s           *Scheduler
	reservation int64
	released    sync.Once
}

// Release returns the slot after the call ran. actualTokens is the
// provider-metered usage of the call (0 if unknown — the reservation is then
// charged as used, the conservative reading for a call that did run without
// a true-up).
func (g *Grant) Release(actualTokens int64) {
	g.released.Do(func() { g.s.release(g.reservation, actualTokens, true) })
}

// Cancel returns the slot for a call that never ran (e.g. its context
// expired between grant and dispatch). The reservation is returned WITHOUT
// charging the window: a call that never reached the provider consumed no
// quota, and charging it would let a mass-cancel wave exhaust the window
// with phantom usage.
func (g *Grant) Cancel() {
	g.released.Do(func() { g.s.release(g.reservation, 0, false) })
}

// waiter is one parked request.
type waiter struct {
	req      Request
	origin   loomv1.SlotOrigin
	class    loomv1.SlotPriorityClass
	since    time.Time
	promoted int32
	ready    chan *Grant // closed-with-value on grant; buffered 1
	// cancelled is set (under the scheduler mutex) when the waiter's context
	// expired and it must be skipped by dispatch.
	cancelled bool
}

// Scheduler is one provider-scope slot scheduler. Safe for concurrent use.
type Scheduler struct {
	scope  string
	logger *zap.Logger

	mu sync.Mutex
	// budget accounting (guarded by mu)
	effectiveTPM  int64 // calibrated ceiling
	calibrated    bool  // true once provider telemetry has spoken
	utilization   float64
	windowStart   time.Time
	windowUsed    int64 // actual tokens charged in the current minute window
	reservedOut   int64 // reservations of grants currently outstanding
	nextWake      time.Time
	starvationAge time.Duration
	headroom      float64
	// minSeenReservation is the smallest reservation any request has carried:
	// the anchor for the AIMD decrease floor and the "remaining exhausted"
	// header check. 0 until the first Acquire.
	minSeenReservation int64
	// decreaseHoldOffUntil suppresses further AIMD decreases: one
	// multiplicative decrease per congestion event (see ObserveThrottle).
	decreaseHoldOffUntil time.Time
	// closed is set by Close; Acquire then grants immediately instead of
	// parking on a scheduler nothing will ever wake again.
	closed bool
	// queues indexed by band then priority class; FIFO within a class.
	queues map[loomv1.SlotOrigin]map[loomv1.SlotPriorityClass][]*waiter

	grantsTotal     int64
	promotionsTotal int64

	stopCh   chan struct{}
	stopOnce sync.Once
}

// New creates a scheduler for one provider scope.
func New(scope string, cfg Config) *Scheduler {
	if cfg.TokensPerMinute <= 0 {
		cfg.TokensPerMinute = DefaultTokensPerMinute
	}
	if cfg.UtilizationTarget <= 0 || cfg.UtilizationTarget > 1 {
		cfg.UtilizationTarget = defaultUtilization
	}
	if cfg.StarvationAge <= 0 {
		cfg.StarvationAge = defaultStarvationAge
	}
	switch {
	case cfg.InteractiveHeadroom < 0:
		cfg.InteractiveHeadroom = 0 // explicitly disabled
	case cfg.InteractiveHeadroom == 0 || cfg.InteractiveHeadroom >= 1:
		cfg.InteractiveHeadroom = defaultInteractiveHeadroom
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	s := &Scheduler{
		scope:         scope,
		logger:        cfg.Logger,
		effectiveTPM:  cfg.TokensPerMinute,
		utilization:   cfg.UtilizationTarget,
		windowStart:   time.Now(),
		starvationAge: cfg.StarvationAge,
		headroom:      cfg.InteractiveHeadroom,
		queues: map[loomv1.SlotOrigin]map[loomv1.SlotPriorityClass][]*waiter{
			loomv1.SlotOrigin_SLOT_ORIGIN_BATCH:       {},
			loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE: {},
		},
		stopCh: make(chan struct{}),
	}
	go s.tick()
	return s
}

// Close stops the scheduler's background ticker. Outstanding grants remain
// valid; parked waiters are woken with a grant so no caller hangs on a
// closed scheduler (the process is shutting down — pacing no longer
// matters). Close-time grants go through grantLocked so the accounting
// (reservedOut, grantsTotal) stays consistent when they are later released.
// Acquire after Close grants immediately for the same reason: parking on a
// scheduler nothing will ever wake again would hang the caller forever.
func (s *Scheduler) Close() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closed = true
		for _, band := range s.queues {
			for class, q := range band {
				for _, w := range q {
					if !w.cancelled {
						w.ready <- s.grantLocked(w.req.ReservationTokens)
					}
				}
				band[class] = nil
			}
		}
	})
}

// Acquire blocks until a slot is granted or ctx is done. It never fails for
// lack of capacity: the only error it returns is ctx.Err(). This is the
// scheduler's core contract — waiting is a state, not an error.
func (s *Scheduler) Acquire(ctx context.Context, req Request) (*Grant, error) {
	if req.ReservationTokens <= 0 {
		req.ReservationTokens = 1
	}
	class := req.Class
	if class == loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_UNSPECIFIED {
		class = loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_NEW
	}
	origin := req.Origin
	if origin == loomv1.SlotOrigin_SLOT_ORIGIN_UNSPECIFIED {
		origin = loomv1.SlotOrigin_SLOT_ORIGIN_BATCH
	}

	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if s.minSeenReservation == 0 || req.ReservationTokens < s.minSeenReservation {
		s.minSeenReservation = req.ReservationTokens
	}
	if s.closed {
		// Shutdown semantics (see Close): grant immediately, never park.
		g := s.grantLocked(req.ReservationTokens)
		s.mu.Unlock()
		return g, nil
	}
	if s.canGrantLocked(origin, req.ReservationTokens) && s.queuesEmptyAtOrAboveLocked(origin, class) {
		g := s.grantLocked(req.ReservationTokens)
		s.mu.Unlock()
		return g, nil
	}
	w := &waiter{
		req:    req,
		origin: origin,
		class:  class,
		since:  time.Now(),
		ready:  make(chan *Grant, 1),
	}
	s.queues[origin][class] = append(s.queues[origin][class], w)
	s.mu.Unlock()

	select {
	case g := <-w.ready:
		return g, nil
	case <-ctx.Done():
		s.mu.Lock()
		w.cancelled = true
		s.mu.Unlock()
		// A grant may have raced the cancellation; if so, return it to the
		// pool rather than leaking the reservation. The call never ran, so
		// the reservation is returned without charging the window (Cancel,
		// not Release: charging phantom usage here would let a mass-cancel
		// wave exhaust the window).
		select {
		case g := <-w.ready:
			g.Cancel()
		default:
		}
		return nil, ctx.Err()
	}
}

// release returns a grant's reservation and charges actual usage, then
// dispatches waiters — completion is the churn that wakes the queue. ran
// distinguishes Release (the call ran: unknown usage is conservatively
// charged as the full reservation) from Cancel (the call never ran: nothing
// is charged).
func (s *Scheduler) release(reservation, actualTokens int64, ran bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reservedOut -= reservation
	if s.reservedOut < 0 {
		s.reservedOut = 0
	}
	if actualTokens <= 0 {
		if ran {
			actualTokens = reservation
		} else {
			actualTokens = 0
		}
	}
	s.windowUsed += actualTokens
	s.dispatchLocked()
}

// UpdateFromHeaders calibrates the scope from provider response telemetry
// (x-ratelimit-limit-tokens et al.). limitTokens <= 0 is ignored.
func (s *Scheduler) UpdateFromHeaders(limitTokens, remainingTokens int64, reset time.Duration) {
	if limitTokens <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.calibrated || limitTokens != s.effectiveTPM {
		s.logger.Info("LLM scheduler calibrated from provider telemetry",
			zap.String("scope", s.scope),
			zap.Int64("tokens_per_minute", limitTokens),
			zap.Int64("remaining", remainingTokens))
	}
	s.effectiveTPM = limitTokens
	s.calibrated = true
	// Trust the provider's remaining figure over our own window arithmetic:
	// other consumers may share the deployment.
	if remainingTokens >= 0 {
		used := limitTokens - remainingTokens
		if used > s.windowUsed {
			s.windowUsed = used
		}
		// The reset header is INFORMATIONAL while quota remains: it states
		// when the provider's window renews, not that admission must stop.
		// Arming nextWake from it on every healthy response froze the scope
		// for the remainder of the provider's window after each success. It
		// gates admission only when remaining is actually exhausted — too
		// small to admit even the smallest reservation this scope serves.
		// (An actual throttle's Retry-After gates via ObserveThrottle.)
		if reset > 0 && remainingTokens < s.smallestReservationLocked() {
			now := time.Now()
			if wake := now.Add(reset); wake.After(s.nextWake) {
				s.nextWake = wake
			}
			// Align our window with the provider's: at reset the provider's
			// window renews, so ours renews with it — otherwise the stale
			// windowUsed keeps refusing grants after the provider recovered.
			s.windowStart = now.Add(reset - time.Minute)
		}
	}
	s.dispatchLocked()
}

// ObserveThrottle applies the AIMD decrease for signal-free providers (and
// schedules the wake from retryAfter when the provider supplied one).
//
// Decreases use congestion-EVENT semantics, not per-report semantics: client
// retry loops report every 429 attempt of one logical call and concurrent
// callers multiply the reports, so at most one multiplicative decrease is
// applied per hold-off window (the provider's stated wait, floored at
// throttleDecreaseCooldown). The ceiling never drops below the floor that
// keeps the scope's smallest reservation admissible: a ceiling that admits
// nothing observes no successes and can never repair itself.
func (s *Scheduler) ObserveThrottle(retryAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if retryAfter > 0 {
		// The provider's latest stated wait is real whichever congestion
		// event it belongs to; keep the furthest wake.
		if wake := now.Add(retryAfter); wake.After(s.nextWake) {
			s.nextWake = wake
		}
	}
	if now.Before(s.decreaseHoldOffUntil) {
		return // same congestion event: one decrease already applied
	}
	hold := retryAfter
	if hold < throttleDecreaseCooldown {
		hold = throttleDecreaseCooldown
	}
	s.decreaseHoldOffUntil = now.Add(hold)
	reduced := int64(float64(s.effectiveTPM) * aimdDecreaseFactor)
	if floor := s.floorTPMLocked(); reduced < floor {
		reduced = floor
	}
	if reduced >= s.effectiveTPM {
		return // already at or below the floor: nothing left to decrease
	}
	s.effectiveTPM = reduced
	s.logger.Warn("LLM scheduler ceiling reduced after throttle",
		zap.String("scope", s.scope),
		zap.Int64("effective_tokens_per_minute", s.effectiveTPM),
		zap.Duration("retry_after", retryAfter))
}

// ObserveSuccess applies the AIMD additive increase. This is the
// provider-agnostic fallback: a client whose provider states no ratelimit
// telemetry at all calls this on every clean response, and the ceiling
// grows additively until a throttle halves it — TCP congestion control
// without ECN. Header calibration, when it ever arrives, outranks AIMD.
func (s *Scheduler) ObserveSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calibrated {
		return // header telemetry outranks AIMD
	}
	step := int64(float64(s.effectiveTPM) * aimdIncreaseStep)
	if step < 1 {
		step = 1
	}
	s.effectiveTPM += step
	s.dispatchLocked()
}

// State reports the scope's observable state.
func (s *Scheduler) State() *loomv1.SlotState {
	s.mu.Lock()
	defer s.mu.Unlock()
	parked := 0
	for _, band := range s.queues {
		for _, q := range band {
			for _, w := range q {
				if !w.cancelled {
					parked++
				}
			}
		}
	}
	st := &loomv1.SlotState{
		Scope:                     s.scope,
		ParkedRequests:            int32(parked), // #nosec G115 -- queue depth is operator-bounded
		EffectiveTokensPerMinute:  s.effectiveTPM,
		ReservedTokensOutstanding: s.reservedOut,
		GrantsTotal:               s.grantsTotal,
		PromotionsTotal:           s.promotionsTotal,
	}
	if !s.nextWake.IsZero() && time.Now().Before(s.nextWake) {
		st.NextWake = timestamppb.New(s.nextWake)
	}
	return st
}

// Waiters lists the scope's parked requests, highest class first.
func (s *Scheduler) Waiters() []*loomv1.SlotWaiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*loomv1.SlotWaiter
	for _, band := range bandOrder {
		for _, class := range dispatchOrder {
			for _, w := range s.queues[band][class] {
				if w.cancelled {
					continue
				}
				out = append(out, &loomv1.SlotWaiter{
					ConversationId: w.req.ConversationID,
					AgentName:      w.req.AgentName,
					Class:          w.class,
					Origin:         w.origin,
					Promotions:     w.promoted,
					WaitingSince:   timestamppb.New(w.since),
				})
			}
		}
	}
	return out
}

// --- internals (all *Locked functions require s.mu held) ---

var dispatchOrder = []loomv1.SlotPriorityClass{
	loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_RESOURCE_HOLDER,
	loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_IN_FLIGHT,
	loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_NEW,
}

func (s *Scheduler) budgetLocked() int64 {
	return int64(float64(s.effectiveTPM) * s.utilization)
}

// smallestReservationLocked is the smallest reservation any request has
// carried on this scope (1 until the first Acquire): the yardstick for
// "remaining is effectively exhausted" in header calibration.
func (s *Scheduler) smallestReservationLocked() int64 {
	if s.minSeenReservation > 0 {
		return s.minSeenReservation
	}
	return 1
}

// floorTPMLocked is the lowest ceiling an AIMD decrease may set: the
// smallest reservation this scope has actually served must remain admissible
// through the batch band's headroom-capped budget with an empty window.
// Below that the scope could admit nothing, observe no successes, and never
// repair — the permanent-wedge state.
func (s *Scheduler) floorTPMLocked() int64 {
	if s.minSeenReservation <= 0 {
		return 1
	}
	denom := s.utilization * (1 - s.headroom)
	if denom <= 0 {
		denom = defaultUtilization * (1 - defaultInteractiveHeadroom)
	}
	return int64(float64(s.minSeenReservation)/denom) + 1
}

func (s *Scheduler) rolloverLocked() {
	if time.Since(s.windowStart) >= time.Minute {
		s.windowStart = time.Now()
		s.windowUsed = 0
	}
}

// canGrantLocked applies the band's budget: interactive may use the full
// budget; batch is capped below the interactive headroom so an arriving
// human never waits behind a fully batch-reserved window.
func (s *Scheduler) canGrantLocked(origin loomv1.SlotOrigin, reservation int64) bool {
	s.rolloverLocked()
	if !s.nextWake.IsZero() && time.Now().Before(s.nextWake) {
		return false // provider told us to wait (Retry-After / exhausted reset)
	}
	// Work-conserving anti-wedge valve: a scope with zero outstanding work
	// and an untouched window always admits its head request, whatever the
	// budget arithmetic says. An idle scope that refuses everything can
	// never observe the success it needs to recover a collapsed ceiling —
	// this makes the wedge structurally impossible.
	if s.reservedOut == 0 && s.windowUsed == 0 {
		return true
	}
	budget := s.budgetLocked()
	if origin != loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE {
		budget = int64(float64(budget) * (1 - s.headroom))
	}
	return s.windowUsed+s.reservedOut+reservation <= budget
}

func (s *Scheduler) grantLocked(reservation int64) *Grant {
	s.reservedOut += reservation
	s.grantsTotal++
	return &Grant{s: s, reservation: reservation}
}

// queuesEmptyAtOrAboveLocked reports whether no waiter that outranks the
// given (origin, class) is parked — a newly arriving request must not
// overtake them. Any interactive waiter outranks every batch arrival.
func (s *Scheduler) queuesEmptyAtOrAboveLocked(origin loomv1.SlotOrigin, class loomv1.SlotPriorityClass) bool {
	for _, band := range bandOrder {
		if bandRank(band) < bandRank(origin) {
			continue
		}
		for _, c := range dispatchOrder {
			if band == origin && c < class {
				continue
			}
			for _, w := range s.queues[band][c] {
				if !w.cancelled {
					return false
				}
			}
		}
	}
	return true
}

var bandOrder = []loomv1.SlotOrigin{
	loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE,
	loomv1.SlotOrigin_SLOT_ORIGIN_BATCH,
}

func bandRank(o loomv1.SlotOrigin) int {
	if o == loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE {
		return 1
	}
	return 0
}

// dispatchLocked grants as many parked waiters as the budget allows:
// interactive band first (full budget), then batch (headroom-capped budget);
// highest class first within a band, FIFO within a class. Each waiter is
// woken with its own grant — one completion wakes bounded work, no herd.
//
// Backfill stops entirely below a STARVED blocked head (a waiter already at
// the top class of its band that has aged past its next promotion point):
// aging can no longer help it, and letting smaller lower-class or lower-band
// work keep consuming every freed token would starve a large reservation
// forever. Halting backfill lets completion churn drain the window until the
// head fits — admission liveness, the proto's starvation bound. Below a
// non-starved head, backfill continues (throughput).
func (s *Scheduler) dispatchLocked() {
	for _, band := range bandOrder {
		for _, class := range dispatchOrder {
			q := s.queues[band][class]
			var remaining []*waiter
			var blockedOnStarvedHead bool
			for i, w := range q {
				if w.cancelled {
					continue
				}
				if !s.canGrantLocked(band, w.req.ReservationTokens) {
					remaining = append(remaining, q[i:]...)
					blockedOnStarvedHead = s.starvedAtTopLocked(w)
					break
				}
				w.ready <- s.grantLocked(w.req.ReservationTokens)
			}
			// Filter any cancelled stragglers retained by the break above.
			filtered := remaining[:0]
			for _, w := range remaining {
				if !w.cancelled {
					filtered = append(filtered, w)
				}
			}
			s.queues[band][class] = filtered
			if blockedOnStarvedHead {
				return
			}
		}
	}
}

// starvedAtTopLocked reports whether w has waited past its next aging point
// while already at the top class of its band — the point where promotion can
// no longer help it and admission liveness (backfill halt) must take over.
func (s *Scheduler) starvedAtTopLocked(w *waiter) bool {
	return w.class == loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_RESOURCE_HOLDER &&
		time.Since(w.since) >= s.starvationAge*time.Duration(w.promoted+1)
}

// tick drives window rollover, Retry-After expiry, and starvation aging.
func (s *Scheduler) tick() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.mu.Lock()
			s.ageLocked()
			s.dispatchLocked()
			s.mu.Unlock()
		case <-s.stopCh:
			return
		}
	}
}

// ageLocked promotes waiters older than starvationAge one class within
// their band (liveness: top class of the band in at most 2 * starvationAge).
// Aging NEVER crosses bands: saturated batch must not flood the interactive
// lane by seniority. Note the asymmetry this leaves by design: batch has no
// reserved budget share, so a saturated interactive band CAN starve batch
// entirely (a human is waiting; throughput work waits — see SlotOrigin in
// the proto). Within-band aging is therefore a class-liveness guarantee
// only; batch regains admission when the interactive band goes quiet.
func (s *Scheduler) ageLocked() {
	for _, band := range bandOrder {
		promote := func(from, to loomv1.SlotPriorityClass) {
			var keep []*waiter
			for _, w := range s.queues[band][from] {
				if w.cancelled {
					continue
				}
				if time.Since(w.since) >= s.starvationAge*time.Duration(w.promoted+1) {
					w.promoted++
					w.class = to
					s.promotionsTotal++
					s.queues[band][to] = append(s.queues[band][to], w)
					continue
				}
				keep = append(keep, w)
			}
			s.queues[band][from] = keep
		}
		promote(loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_IN_FLIGHT,
			loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_RESOURCE_HOLDER)
		promote(loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_NEW,
			loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_IN_FLIGHT)
	}
}

// SetConfig replaces the runtime-tunable knobs. Zero values leave the
// current setting unchanged (a negative interactiveHeadroom disables the
// headroom, mirroring Config). An explicit tokens-per-minute re-pins the
// ceiling and clears calibration so operator intent wins until the next
// provider telemetry arrives.
func (s *Scheduler) SetConfig(tokensPerMinute int64, utilization float64, starvationAge time.Duration, interactiveHeadroom float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tokensPerMinute > 0 {
		s.effectiveTPM = tokensPerMinute
		s.calibrated = false
	}
	if utilization > 0 && utilization <= 1 {
		s.utilization = utilization
	}
	if starvationAge > 0 {
		s.starvationAge = starvationAge
	}
	switch {
	case interactiveHeadroom < 0:
		s.headroom = 0 // explicitly disabled
	case interactiveHeadroom > 0 && interactiveHeadroom < 1:
		s.headroom = interactiveHeadroom
	}
	s.dispatchLocked()
}
