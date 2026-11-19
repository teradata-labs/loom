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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type contextBlockingReadCloser struct {
	ctx     context.Context
	started chan struct{}
}

func (r *contextBlockingReadCloser) Read(_ []byte) (int, error) {
	close(r.started)
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (r *contextBlockingReadCloser) Close() error {
	return nil
}

type zeroThenBlockingReadCloser struct {
	ctx   context.Context
	reads atomic.Int32
}

func (r *zeroThenBlockingReadCloser) Read(_ []byte) (int, error) {
	if r.reads.Add(1) == 1 {
		time.Sleep(150 * time.Millisecond)
		return 0, nil
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (r *zeroThenBlockingReadCloser) Close() error {
	return nil
}

func TestStreamTimeoutBody_CloseUnblocksRead(t *testing.T) {
	streamCtx, cancelStream := context.WithCancel(context.Background())
	body := &contextBlockingReadCloser{ctx: streamCtx, started: make(chan struct{})}
	reader := NewStreamTimeoutBody(body, time.Second, time.Second, time.Second, cancelStream)
	readDone := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 1))
		readDone <- err
	}()
	<-body.started

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- reader.Close()
	}()

	require.ErrorIs(t, <-readDone, context.Canceled)
	require.NoError(t, <-closeDone)
}

func TestStreamTimeoutBody_ZeroByteReadDoesNotResetFirstByteDeadline(t *testing.T) {
	streamCtx, cancelStream := context.WithCancel(context.Background())
	body := &zeroThenBlockingReadCloser{ctx: streamCtx}
	reader := NewStreamTimeoutBody(body, 200*time.Millisecond, 200*time.Millisecond, time.Second, cancelStream)
	defer func() { require.NoError(t, reader.Close()) }()

	startedAt := time.Now()
	n, err := reader.Read(make([]byte, 1))
	require.NoError(t, err)
	assert.Zero(t, n)

	_, err = reader.Read(make([]byte, 1))
	require.ErrorIs(t, err, ErrStreamTimeout)
	assert.Less(t, time.Since(startedAt), 300*time.Millisecond)
}

func TestDoStreamHTTP_HeaderStallIsAStreamTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never send headers
	}))
	defer srv.Close()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 100 * time.Millisecond
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	_, err = DoStreamHTTP(&http.Client{Transport: tr}, req, 100*time.Millisecond, time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStreamTimeout)
}

func TestDoStreamHTTP_CallerCancelIsNotAStreamTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	_, err = DoStreamHTTP(&http.Client{}, req, time.Second, time.Second)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrStreamTimeout, "the caller's deadline is not a stall")
}
