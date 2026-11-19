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
package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// streamIdleTimeoutError reports which wait expired. It unwraps to
// ErrStreamTimeout, so callers test with errors.Is.
type streamIdleTimeoutError struct {
	phase   string
	timeout time.Duration
}

func (e streamIdleTimeoutError) Error() string {
	return fmt.Sprintf("stream %s timeout after %s: %v", e.phase, e.timeout, ErrStreamTimeout)
}

func (e streamIdleTimeoutError) Unwrap() error {
	return ErrStreamTimeout
}

const (
	streamTimeoutPhaseNone int32 = iota
	streamTimeoutPhaseFirstByte
	streamTimeoutPhaseIdle
)

type streamReadDeadlineUpdate struct {
	timeout time.Duration
	phase   int32
	applied chan struct{}
}

type streamReadTimeoutReadCloser struct {
	body                  io.ReadCloser
	firstByteDeadline     time.Time
	firstByteTimeoutLimit time.Duration
	idleTimeout           time.Duration
	cancelStream          context.CancelFunc
	deadlineUpdates       chan streamReadDeadlineUpdate
	stop                  chan struct{}
	done                  chan struct{}
	stopOnce              sync.Once
	readMu                sync.Mutex
	firstByteRead         atomic.Bool
	timedOutPhase         atomic.Int32
}

// NewStreamTimeoutBody wraps a streaming response body so a read that waits
// longer than firstByteWait (before the first byte) or idleTimeout (between
// later reads) cancels the stream through cancelStream and returns an error
// wrapping ErrStreamTimeout. firstByteTimeoutLimit is the configured limit
// reported in the error. Zero durations disable that wait.
func NewStreamTimeoutBody(
	body io.ReadCloser,
	firstByteWait time.Duration,
	firstByteTimeoutLimit time.Duration,
	idleTimeout time.Duration,
	cancelStream context.CancelFunc,
) io.ReadCloser {
	var firstByteDeadline time.Time
	if firstByteWait > 0 {
		firstByteDeadline = time.Now().Add(firstByteWait)
	}
	reader := &streamReadTimeoutReadCloser{
		body:                  body,
		firstByteDeadline:     firstByteDeadline,
		firstByteTimeoutLimit: firstByteTimeoutLimit,
		idleTimeout:           idleTimeout,
		cancelStream:          cancelStream,
		deadlineUpdates:       make(chan streamReadDeadlineUpdate),
		stop:                  make(chan struct{}),
		done:                  make(chan struct{}),
	}
	go reader.watch()
	return reader
}

func (r *streamReadTimeoutReadCloser) watch() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var deadline <-chan time.Time
	phase := streamTimeoutPhaseNone
	defer close(r.done)
	defer timer.Stop()

	for {
		select {
		case <-deadline:
			r.timedOutPhase.Store(phase)
			r.cancelStream()
			return
		case update := <-r.deadlineUpdates:
			if deadline != nil && !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			deadline = nil
			phase = update.phase
			if update.timeout > 0 {
				timer.Reset(update.timeout)
				deadline = timer.C
			}
			close(update.applied)
		case <-r.stop:
			return
		}
	}
}

func (r *streamReadTimeoutReadCloser) setReadDeadline(timeout time.Duration, phase int32) bool {
	update := streamReadDeadlineUpdate{
		timeout: timeout,
		phase:   phase,
		applied: make(chan struct{}),
	}
	select {
	case r.deadlineUpdates <- update:
	case <-r.done:
		return false
	}
	select {
	case <-update.applied:
		return true
	case <-r.done:
		return false
	}
}

func (r *streamReadTimeoutReadCloser) Read(p []byte) (int, error) {
	r.readMu.Lock()
	defer r.readMu.Unlock()

	timeout := r.idleTimeout
	phase := streamTimeoutPhaseIdle
	if !r.firstByteRead.Load() && !r.firstByteDeadline.IsZero() {
		timeout = time.Until(r.firstByteDeadline)
		phase = streamTimeoutPhaseFirstByte
		if timeout <= 0 {
			r.cancelStream()
			return 0, streamIdleTimeoutError{phase: "first-byte", timeout: r.firstByteTimeoutLimit}
		}
	}
	if timeout > 0 {
		r.setReadDeadline(timeout, phase)
	}
	n, err := r.body.Read(p)
	if n > 0 {
		r.firstByteRead.Store(true)
	}
	if timeout > 0 && r.timedOutPhase.Load() == streamTimeoutPhaseNone {
		r.setReadDeadline(0, streamTimeoutPhaseNone)
	}
	if err != nil {
		switch r.timedOutPhase.Load() {
		case streamTimeoutPhaseFirstByte:
			return n, streamIdleTimeoutError{phase: "first-byte", timeout: r.firstByteTimeoutLimit}
		case streamTimeoutPhaseIdle:
			return n, streamIdleTimeoutError{phase: "idle", timeout: r.idleTimeout}
		}
	}
	return n, err
}

func (r *streamReadTimeoutReadCloser) Close() error {
	r.stopOnce.Do(func() {
		close(r.stop)
	})
	r.cancelStream()
	<-r.done
	r.readMu.Lock()
	defer r.readMu.Unlock()
	return r.body.Close()
}

// DoStreamHTTP sends req with httpClient and, when either timeout is
// positive, wraps the response body with NewStreamTimeoutBody. The first-byte
// budget is shared between waiting for headers and waiting for the first body
// byte. A header wait that already used the whole budget returns an error
// wrapping ErrStreamTimeout. httpClient should have no overall Timeout, so a
// long stream that keeps producing bytes is not cut off.
func DoStreamHTTP(httpClient *http.Client, req *http.Request, firstByteTimeout, idleTimeout time.Duration) (*http.Response, error) {
	callerCtx := req.Context()
	var cancelStream context.CancelFunc
	if firstByteTimeout > 0 || idleTimeout > 0 {
		streamCtx, cancel := context.WithCancel(callerCtx)
		cancelStream = cancel
		req = req.Clone(streamCtx)
	}
	startedAt := time.Now()
	// #nosec G704 -- the URL is the provider endpoint the calling client was configured with; this helper only adds timeouts
	resp, err := httpClient.Do(req)
	if err != nil && cancelStream != nil {
		cancelStream()
	}
	// No headers within the first-byte budget (the transport's
	// ResponseHeaderTimeout, or the budget simply spent) is the same stall as a
	// silent body, and is reported the same way so callers can retry it.
	if err != nil && firstByteTimeout > 0 && callerCtx.Err() == nil {
		var netErr net.Error
		if (errors.As(err, &netErr) && netErr.Timeout()) || time.Since(startedAt) >= firstByteTimeout {
			return nil, fmt.Errorf("%w: %w", streamIdleTimeoutError{phase: "first-byte", timeout: firstByteTimeout}, err)
		}
	}
	if err == nil && (firstByteTimeout > 0 || idleTimeout > 0) {
		firstByteWait := firstByteTimeout
		if firstByteWait > 0 {
			firstByteWait -= time.Since(startedAt)
			if firstByteWait <= 0 {
				cancelStream()
				_ = resp.Body.Close()
				return nil, streamIdleTimeoutError{phase: "first-byte", timeout: firstByteTimeout}
			}
		}
		resp.Body = NewStreamTimeoutBody(resp.Body, firstByteWait, firstByteTimeout, idleTimeout, cancelStream)
	}
	return resp, err
}
