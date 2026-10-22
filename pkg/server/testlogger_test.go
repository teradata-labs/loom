// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// testWriter forwards log output to t.Logf until the test finishes, then
// discards it.
//
// The server's background workers outlive the test body by design: a
// coordinator notification handler runs until its context is cancelled and logs
// once on the way out. Cancelling in t.Cleanup does not wait for that log, so it
// can land after tRunner has returned — and a zaptest logger writing to a
// finished *testing.T is a data race the detector reports against the test
// binary. Dropping late writes closes it without weakening the worker or hiding
// anything that happens while the test is running.
type testWriter struct {
	mu       sync.Mutex
	t        *testing.T
	finished bool
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished {
		return len(p), nil
	}
	w.t.Logf("%s", p)
	return len(p), nil
}

func (w *testWriter) Sync() error { return nil }

// newTestLogger is zaptest.NewLogger for tests that start background workers.
func newTestLogger(t *testing.T) *zap.Logger {
	t.Helper()
	w := &testWriter{t: t}
	// Cleanups run LIFO, so registering here — before the test wires up anything
	// it will cancel — means this one runs last, after every shutdown has been
	// signalled.
	t.Cleanup(func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.finished = true
	})
	return zap.New(zapcore.NewCore(
		zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()),
		w,
		zapcore.DebugLevel,
	))
}
