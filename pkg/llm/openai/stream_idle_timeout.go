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
package openai

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type streamIdleTimeoutError struct {
	phase   string
	timeout time.Duration
}

func (e streamIdleTimeoutError) Error() string {
	return fmt.Sprintf("stream %s timeout after %s: %v", e.phase, e.timeout, context.DeadlineExceeded)
}

func (e streamIdleTimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}

func (e streamIdleTimeoutError) Timeout() bool {
	return true
}

const (
	streamTimeoutPhaseNone int32 = iota
	streamTimeoutPhaseFirstByte
	streamTimeoutPhaseIdle
)

type streamReadTimeoutReadCloser struct {
	body             io.ReadCloser
	firstByteTimeout time.Duration
	idleTimeout      time.Duration
	activity         chan time.Duration
	stop             chan struct{}
	done             chan struct{}
	stopOnce         sync.Once
	timedOutPhase    atomic.Int32
}

func newStreamReadTimeoutReadCloser(body io.ReadCloser, firstByteTimeout, idleTimeout time.Duration) io.ReadCloser {
	reader := &streamReadTimeoutReadCloser{
		body:             body,
		firstByteTimeout: firstByteTimeout,
		idleTimeout:      idleTimeout,
		activity:         make(chan time.Duration, 1),
		stop:             make(chan struct{}),
		done:             make(chan struct{}),
	}
	go reader.watch()
	return reader
}

func (r *streamReadTimeoutReadCloser) watch() {
	timeout := r.firstByteTimeout
	phase := streamTimeoutPhaseFirstByte
	if timeout <= 0 {
		timeout = r.idleTimeout
		phase = streamTimeoutPhaseIdle
	}
	if timeout <= 0 {
		close(r.done)
		return
	}

	timer := time.NewTimer(timeout)
	defer close(r.done)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			r.timedOutPhase.Store(phase)
			_ = r.body.Close()
			return
		case timeout = <-r.activity:
			if timeout <= 0 {
				return
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			phase = streamTimeoutPhaseIdle
			timer.Reset(timeout)
		case <-r.stop:
			return
		}
	}
}

func (r *streamReadTimeoutReadCloser) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if n > 0 {
		select {
		case r.activity <- r.idleTimeout:
		default:
		}
	}
	if err != nil {
		switch r.timedOutPhase.Load() {
		case streamTimeoutPhaseFirstByte:
			return n, streamIdleTimeoutError{phase: "first-byte", timeout: r.firstByteTimeout}
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
	<-r.done
	return r.body.Close()
}
