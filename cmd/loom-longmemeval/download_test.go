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

//go:build fts5

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadFile(t *testing.T) {
	const body = `[{"question_id":"q1"}]`

	tests := []struct {
		name    string
		handler http.HandlerFunc
		// stale is written to <dest>.tmp before the download, as a killed
		// earlier attempt (no deferred cleanup) would leave it.
		stale   string
		wantErr string
	}{
		{
			name: "complete body is renamed into place",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			},
		},
		{
			name:  "a stale .tmp from a killed attempt is overwritten",
			stale: "garbage from a previous attempt that is much longer than the body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			},
		},
		{
			name: "HTTP error leaves nothing behind",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "nope", http.StatusBadGateway)
			},
			wantErr: "HTTP 502",
		},
		{
			name: "connection dropped mid-body leaves nothing behind",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				// Promise more bytes than are sent, then drop the connection:
				// the client sees an unexpected EOF part-way through the copy.
				w.Header().Set("Content-Length", strconv.Itoa(len(body)*100))
				_, _ = w.Write([]byte(body))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				hj, ok := w.(http.Hijacker)
				if !ok {
					return
				}
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			},
			wantErr: "write file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			dest := filepath.Join(t.TempDir(), "longmemeval_oracle.json")
			if tt.stale != "" {
				require.NoError(t, os.WriteFile(dest+".tmp", []byte(tt.stale), 0o600))
			}

			err := downloadFile(srv.URL+"/longmemeval_oracle.json", dest)

			_, tmpErr := os.Stat(dest + ".tmp")
			assert.True(t, os.IsNotExist(tmpErr), "the .tmp must never outlive downloadFile")

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				_, statErr := os.Stat(dest)
				assert.True(t, os.IsNotExist(statErr),
					"a failed download must not leave a file the download command would skip next time")
				return
			}

			require.NoError(t, err)
			got, err := os.ReadFile(dest)
			require.NoError(t, err)
			assert.Equal(t, body, string(got))
		})
	}
}
