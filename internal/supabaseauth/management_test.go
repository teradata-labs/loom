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

package supabaseauth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freeLoopbackRedirect returns an unused loopback redirect URI.
func freeLoopbackRedirect(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return "http://" + addr + "/callback"
}

func TestManagementLogin_AndListProjects(t *testing.T) {
	redirectURI := freeLoopbackRedirect(t)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth/token":
			user, pass, ok := r.BasicAuth()
			assert.True(t, ok, "token endpoint must use HTTP Basic auth")
			assert.Equal(t, "cid", user)
			assert.Equal(t, "csecret", pass)
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "authorization_code", r.Form.Get("grant_type"))
			assert.Equal(t, "the-code", r.Form.Get("code"))
			assert.NotEmpty(t, r.Form.Get("code_verifier"))
			assert.Equal(t, redirectURI, r.Form.Get("redirect_uri"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "mgmt-token"})
		case "/v1/projects":
			assert.Equal(t, "Bearer mgmt-token", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]Project{{ID: "ref1", Name: "Proj", Region: "us-east-1"}})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer api.Close()

	openBrowser := func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		assert.Equal(t, "S256", u.Query().Get("code_challenge_method"))
		assert.Equal(t, "cid", u.Query().Get("client_id"))
		redirect := u.Query().Get("redirect_uri")
		state := u.Query().Get("state")
		go func() {
			resp, err := http.Get(redirect + "?code=the-code&state=" + url.QueryEscape(state))
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	token, err := ManagementLogin(ctx, ManagementConfig{
		ClientID:     "cid",
		ClientSecret: "csecret",
		RedirectURI:  redirectURI,
		APIBase:      api.URL,
		OpenBrowser:  openBrowser,
	})
	require.NoError(t, err)
	assert.Equal(t, "mgmt-token", token)

	projects, err := ListProjects(ctx, nil, api.URL, token)
	require.NoError(t, err)
	require.Len(t, projects, 1)
	assert.Equal(t, "ref1", projects[0].ID)
	assert.Equal(t, "us-east-1", projects[0].Region)
}

func TestManagementLogin_RequiresClientCreds(t *testing.T) {
	_, err := ManagementLogin(context.Background(), ManagementConfig{
		RedirectURI: "http://127.0.0.1:1/callback",
		OpenBrowser: func(string) error { return nil },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client_id and client_secret")
}

func TestListProjects_ErrorStatus(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer api.Close()
	_, err := ListProjects(context.Background(), nil, api.URL, "bad-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

// TestManagementLogin_HTTPSLoopback exercises the TLS callback path: the
// Supabase Management API rejects plain-HTTP redirect URIs ("redirect_uri
// must use HTTPS"), so the loopback callback must serve an ephemeral
// self-signed certificate. The browser stub accepts the untrusted cert the
// way a user clicking through the warning does.
func TestManagementLogin_HTTPSLoopback(t *testing.T) {
	redirectURI := "https" + strings.TrimPrefix(freeLoopbackRedirect(t), "http")

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/oauth/token" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		require.NoError(t, r.ParseForm())
		assert.Equal(t, redirectURI, r.Form.Get("redirect_uri"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "mgmt-token"})
	}))
	defer api.Close()

	insecureClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- simulates the user accepting the self-signed loopback cert
	}}
	openBrowser := func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		redirect := u.Query().Get("redirect_uri")
		assert.True(t, strings.HasPrefix(redirect, "https://"), "redirect_uri must be https")
		state := u.Query().Get("state")
		go func() {
			resp, err := insecureClient.Get(redirect + "?code=the-code&state=" + url.QueryEscape(state))
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	token, err := ManagementLogin(ctx, ManagementConfig{
		ClientID:     "cid",
		ClientSecret: "csecret",
		RedirectURI:  redirectURI,
		APIBase:      api.URL,
		OpenBrowser:  openBrowser,
	})
	require.NoError(t, err)
	assert.Equal(t, "mgmt-token", token)
}
