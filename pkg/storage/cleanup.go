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

package storage

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// Purger is a local interface for the PurgeDeleted method.
// This avoids importing pkg/agent (which would cause an import cycle).
// Any type implementing agent.SoftDeleteStorage satisfies this interface.
type Purger interface {
	PurgeDeleted(ctx context.Context, graceInterval string) error
}

// SoftDeleteCleaner manages the background goroutine that periodically purges
// soft-deleted records. Call Stop to cancel the goroutine and wait for it to exit.
type SoftDeleteCleaner struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Stop cancels the cleanup goroutine and blocks until it has exited.
func (c *SoftDeleteCleaner) Stop() {
	c.cancel()
	<-c.done
}

// StartSoftDeleteCleanup starts a background goroutine that periodically purges
// soft-deleted records older than the grace period. It calls PurgeDeleted on the
// provided Purger at the configured interval.
//
// Call Stop on the returned SoftDeleteCleaner during shutdown to cancel the
// goroutine and wait for it to exit cleanly.
func StartSoftDeleteCleanup(
	store Purger,
	gracePeriodSeconds int32,
	cleanupIntervalSeconds int32,
	logger *zap.Logger,
) *SoftDeleteCleaner {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	const defaultCleanupInterval = 24 * time.Hour
	interval := time.Duration(cleanupIntervalSeconds) * time.Second
	if interval <= 0 {
		logger.Warn("soft-delete cleanup interval is non-positive; using default",
			zap.Int32("configured_seconds", cleanupIntervalSeconds),
			zap.Duration("default", defaultCleanupInterval),
		)
		interval = defaultCleanupInterval
	}
	graceInterval := fmt.Sprintf("%d seconds", gracePeriodSeconds)

	logger.Info("Starting soft-delete cleanup goroutine",
		zap.Duration("interval", interval),
		zap.String("grace_period", graceInterval),
	)

	go func() {
		defer close(done)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				logger.Info("Soft-delete cleanup goroutine stopped")
				return
			case <-ticker.C:
				if err := store.PurgeDeleted(ctx, graceInterval); err != nil {
					logger.Error("Soft-delete cleanup failed", zap.Error(err))
				} else {
					logger.Debug("Soft-delete cleanup completed")
				}
			}
		}
	}()

	return &SoftDeleteCleaner{cancel: cancel, done: done}
}
