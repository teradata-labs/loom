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

package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/types"
)

// Go strips Authorization on a cross-host redirect but copies x-api-key; the
// client's redirect policy drops it so the key never reaches another host.
func TestRedirect_CrossHostDropsAPIKey(t *testing.T) {
	var gotKey atomic.Value
	gotKey.Store("unset")
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey.Store(r.Header.Get("x-api-key"))
		_, _ = io.WriteString(w, gatewayMessagesOK)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// localhost vs 127.0.0.1: a different host for the client.
		http.Redirect(w, r, "http://localhost:"+other.URL[len("http://127.0.0.1:"):]+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	c := NewClient(Config{APIKey: "sk-ant-test-not-a-real-key", Model: "claude-haiku-4-5", Endpoint: origin.URL})
	_, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "", gotKey.Load(), "x-api-key must not follow a cross-host redirect")
}

func TestRedirect_SameHostKeepsAPIKey(t *testing.T) {
	var gotKey atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		gotKey.Store(r.Header.Get("x-api-key"))
		_, _ = io.WriteString(w, gatewayMessagesOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewClient(Config{APIKey: "sk-ant-test-not-a-real-key", Model: "claude-haiku-4-5", Endpoint: srv.URL + "/old"})
	_, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-test-not-a-real-key", gotKey.Load())
}
