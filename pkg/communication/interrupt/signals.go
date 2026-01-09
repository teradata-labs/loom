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
// Package interrupt provides a dedicated communication channel for agent interrupts.
//
// This is the 4th channel in Loom's quad-modal communication system:
// 1. MESSAGE QUEUE - ordered, persistent task delivery
// 2. SHARED MEMORY - KV store for agent state
// 3. BROADCAST BUS - pub/sub for events
// 4. INTERRUPT CHANNEL - targeted, guaranteed signal delivery (THIS PACKAGE)
//
// Interrupts are semantically different from broadcasts:
// - Targeted (not anonymous pub/sub)
// - Guaranteed delivery for CRITICAL priority
// - Type-safe enums (not string topics)
// - Fast path (<1ms) + Slow path (persistent SQLite queue)
//
// Signal Priority Ranges:
// - 0-9:   CRITICAL (guaranteed delivery, persistent queue, <1s)
// - 10-19: HIGH (best-effort, <5s)
// - 20-29: NORMAL (best-effort, <30s)
// - 30-39: LOW (background)
// - 40-49: LEARNING (autonomous learning triggers)
// - 1000+: CUSTOM (user-defined signals)
package interrupt

import "fmt"

// InterruptSignal represents a predefined interrupt type.
// Signals are type-safe enums that prevent typos and enable compile-time validation.
type InterruptSignal int

const (
	// CRITICAL priority (0-9): <1s delivery, guaranteed, persistent queue fallback
	// These interrupts CANNOT be dropped and will retry until delivered.

	// SignalEmergencyStop immediately halts all agent operations.
	// Used for: Critical system failures, data corruption, security breaches
	SignalEmergencyStop InterruptSignal = 0

	// SignalSystemShutdown initiates graceful system-wide shutdown sequence.
	// Used for: Maintenance windows, cluster rebalancing, emergency drains
	SignalSystemShutdown InterruptSignal = 1

	// SignalThresholdCritical alerts on critical threshold breach (SLA violations, resource exhaustion).
	// Used for: Database connection pool exhausted, memory >95%, disk full
	SignalThresholdCritical InterruptSignal = 2

	// SignalDatabaseDown signals complete database unavailability.
	// Used for: Connection failures, cluster down, network partition
	SignalDatabaseDown InterruptSignal = 3

	// SignalSecurityBreach signals confirmed security incident.
	// Used for: Unauthorized access, injection attempts, credential leaks
	SignalSecurityBreach InterruptSignal = 4

	// HIGH priority (10-19): <5s delivery, best-effort (large buffers)
	// These interrupts are important but can tolerate brief delays.

	// SignalThresholdHigh alerts on high threshold breach (early warning).
	// Used for: Memory >80%, CPU sustained >70%, query latency P99 >5s
	SignalThresholdHigh InterruptSignal = 10

	// SignalAlertSecurity signals suspicious activity requiring investigation.
	// Used for: Failed login attempts, unusual access patterns, rate limit hits
	SignalAlertSecurity InterruptSignal = 11

	// SignalAlertError signals non-critical errors requiring attention.
	// Used for: Query failures, timeout errors, retry exhaustion (non-critical)
	SignalAlertError InterruptSignal = 12

	// SignalResourceExhausted signals resource pressure (not yet critical).
	// Used for: Connection pool pressure, cache eviction storms, queue backlog
	SignalResourceExhausted InterruptSignal = 13

	// NORMAL priority (20-29): <30s delivery, best-effort
	// These interrupts handle routine operations and lifecycle events.

	// SignalWakeup awakens a DORMANT agent to ACTIVE state.
	// Used for: Scheduled workflows, cron triggers, on-demand activation
	SignalWakeup InterruptSignal = 20

	// SignalGracefulShutdown initiates graceful agent shutdown (not system-wide).
	// Used for: Agent TTL expiry, idle timeout, resource cleanup
	SignalGracefulShutdown InterruptSignal = 21

	// SignalHealthCheck requests health status from agent.
	// Used for: Kubernetes liveness probes, orchestrator health checks
	SignalHealthCheck InterruptSignal = 22

	// SignalConfigReload triggers hot-reload of agent configuration.
	// Used for: Pattern updates, prompt changes, settings refresh
	SignalConfigReload InterruptSignal = 23

	// LOW priority (30-39): background, best-effort
	// These interrupts handle non-urgent background tasks.

	// SignalMetricsCollection triggers metrics aggregation and reporting.
	// Used for: Periodic Hawk exports, cost tracking, performance profiling
	SignalMetricsCollection InterruptSignal = 30

	// SignalLogRotation triggers log file rotation and archival.
	// Used for: Periodic log cleanup, compression, upload to storage
	SignalLogRotation InterruptSignal = 31

	// LEARNING signals (40-49): Autonomous learning triggers
	// These interrupts enable agents to learn and improve autonomously.

	// SignalLearningAnalyze triggers pattern analysis on recent executions.
	// Used for: Post-execution pattern mining, performance profiling
	SignalLearningAnalyze InterruptSignal = 40

	// SignalLearningOptimize triggers DSPy optimization on current prompts.
	// Used for: Scheduled prompt tuning, A/B test winner selection
	SignalLearningOptimize InterruptSignal = 41

	// SignalLearningABTest starts A/B test for new pattern vs baseline.
	// Used for: Validating learned patterns, comparing prompt variations
	SignalLearningABTest InterruptSignal = 42

	// SignalLearningProposal notifies that a new pattern proposal is ready for review.
	// Used for: Async learning cycles, human-in-the-loop approval
	SignalLearningProposal InterruptSignal = 43

	// SignalLearningValidate triggers validation of learned patterns against held-out data.
	// Used for: Post-optimization validation, regression testing
	SignalLearningValidate InterruptSignal = 44

	// SignalLearningExport triggers export of learned knowledge to Promptio.
	// Used for: Persisting approved patterns, cross-agent knowledge sharing
	SignalLearningExport InterruptSignal = 45

	// SignalLearningSync synchronizes learned patterns with other agents.
	// Used for: Multi-agent learning, cluster-wide pattern distribution
	SignalLearningSync InterruptSignal = 46

	// Custom signals (1000+)
	// Users can define custom signals starting from this base.

	// SignalCustomBase is the starting point for user-defined custom signals.
	// Example: const SignalMyCustomAlert = interrupt.SignalCustomBase + 1
	SignalCustomBase InterruptSignal = 1000
)

// Priority defines interrupt delivery priority level.
type Priority int

const (
	// PriorityCritical: <1s delivery, guaranteed via persistent queue, never dropped
	PriorityCritical Priority = 0

	// PriorityHigh: <5s delivery, best-effort via large buffers (10k)
	PriorityHigh Priority = 1

	// PriorityNormal: <30s delivery, best-effort via medium buffers (1k)
	PriorityNormal Priority = 2

	// PriorityLow: background, best-effort via small buffers (100)
	PriorityLow Priority = 3
)

// String returns human-readable signal name.
func (s InterruptSignal) String() string {
	switch s {
	// CRITICAL (0-9)
	case SignalEmergencyStop:
		return "EMERGENCY_STOP"
	case SignalSystemShutdown:
		return "SYSTEM_SHUTDOWN"
	case SignalThresholdCritical:
		return "THRESHOLD_CRITICAL"
	case SignalDatabaseDown:
		return "DATABASE_DOWN"
	case SignalSecurityBreach:
		return "SECURITY_BREACH"

	// HIGH (10-19)
	case SignalThresholdHigh:
		return "THRESHOLD_HIGH"
	case SignalAlertSecurity:
		return "ALERT_SECURITY"
	case SignalAlertError:
		return "ALERT_ERROR"
	case SignalResourceExhausted:
		return "RESOURCE_EXHAUSTED"

	// NORMAL (20-29)
	case SignalWakeup:
		return "WAKEUP"
	case SignalGracefulShutdown:
		return "GRACEFUL_SHUTDOWN"
	case SignalHealthCheck:
		return "HEALTH_CHECK"
	case SignalConfigReload:
		return "CONFIG_RELOAD"

	// LOW (30-39)
	case SignalMetricsCollection:
		return "METRICS_COLLECTION"
	case SignalLogRotation:
		return "LOG_ROTATION"

	// LEARNING (40-49)
	case SignalLearningAnalyze:
		return "LEARNING_ANALYZE"
	case SignalLearningOptimize:
		return "LEARNING_OPTIMIZE"
	case SignalLearningABTest:
		return "LEARNING_ABTEST"
	case SignalLearningProposal:
		return "LEARNING_PROPOSAL"
	case SignalLearningValidate:
		return "LEARNING_VALIDATE"
	case SignalLearningExport:
		return "LEARNING_EXPORT"
	case SignalLearningSync:
		return "LEARNING_SYNC"

	default:
		if s >= SignalCustomBase {
			return fmt.Sprintf("CUSTOM_%d", s-SignalCustomBase)
		}
		return fmt.Sprintf("UNKNOWN_%d", s)
	}
}

// Priority returns the delivery priority for this signal based on its range.
func (s InterruptSignal) Priority() Priority {
	switch {
	case s >= 0 && s <= 9:
		return PriorityCritical
	case s >= 10 && s <= 19:
		return PriorityHigh
	case s >= 20 && s <= 29:
		return PriorityNormal
	case s >= 30 && s <= 39:
		return PriorityLow
	case s >= 40 && s <= 49:
		return PriorityNormal // Learning signals use NORMAL priority
	default:
		return PriorityNormal // Custom signals default to NORMAL
	}
}

// BufferSize returns the recommended channel buffer size for this priority.
func (p Priority) BufferSize() int {
	switch p {
	case PriorityCritical:
		return 10000 // Large buffer to minimize persistent queue usage
	case PriorityHigh:
		return 10000 // Large buffer for important signals
	case PriorityNormal:
		return 1000 // Medium buffer for routine operations
	case PriorityLow:
		return 100 // Small buffer for background tasks
	default:
		return 1000 // Default to medium
	}
}

// String returns human-readable priority name.
func (p Priority) String() string {
	switch p {
	case PriorityCritical:
		return "CRITICAL"
	case PriorityHigh:
		return "HIGH"
	case PriorityNormal:
		return "NORMAL"
	case PriorityLow:
		return "LOW"
	default:
		return fmt.Sprintf("UNKNOWN_%d", p)
	}
}

// IsCritical returns true if this signal requires guaranteed delivery.
func (s InterruptSignal) IsCritical() bool {
	return s.Priority() == PriorityCritical
}
