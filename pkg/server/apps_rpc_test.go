// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockAppProvider implements AppProvider for testing.
type mockAppProvider struct {
	infos []apps.AppInfo
	html  map[string][]byte
}

func (m *mockAppProvider) ListAppInfo() []apps.AppInfo {
	return m.infos
}

func (m *mockAppProvider) GetAppHTML(name string) ([]byte, *apps.AppInfo, error) {
	html, ok := m.html[name]
	if !ok {
		return nil, nil, fmt.Errorf("app not found: %s", name)
	}
	for i := range m.infos {
		if m.infos[i].Name == name {
			return html, &m.infos[i], nil
		}
	}
	return nil, nil, fmt.Errorf("app not found: %s", name)
}

// newTestAppsServer creates a MultiAgentServer with an optional mock app provider.
func newTestAppsServer(t *testing.T, provider AppProvider) *MultiAgentServer {
	t.Helper()
	srv := NewMultiAgentServer(nil, nil)
	srv.SetLogger(zaptest.NewLogger(t))
	if provider != nil {
		srv.SetAppProvider(provider)
	}
	return srv
}

// sampleAppProvider returns a mock provider with two sample apps.
func sampleAppProvider() *mockAppProvider {
	return &mockAppProvider{
		infos: []apps.AppInfo{
			{
				Name:          "data-chart",
				URI:           "ui://loom/data-chart",
				DisplayName:   "Data Chart",
				Description:   "Interactive data visualization",
				MimeType:      "text/html",
				PrefersBorder: true,
			},
			{
				Name:          "query-results",
				URI:           "ui://loom/query-results",
				DisplayName:   "Query Results",
				Description:   "Tabular query result viewer",
				MimeType:      "text/html",
				PrefersBorder: false,
			},
		},
		html: map[string][]byte{
			"data-chart":    []byte("<html><body>Data Chart App</body></html>"),
			"query-results": []byte("<html><body>Query Results App</body></html>"),
		},
	}
}

// --- ListUIApps Tests ---

func TestListUIApps(t *testing.T) {
	tests := []struct {
		name      string
		provider  AppProvider
		wantCount int32
		wantNames []string
	}{
		{
			name:      "list apps with two registered apps",
			provider:  sampleAppProvider(),
			wantCount: 2,
			wantNames: []string{"data-chart", "query-results"},
		},
		{
			name:      "list apps with nil provider returns empty response",
			provider:  nil,
			wantCount: 0,
			wantNames: nil,
		},
		{
			name: "list apps with empty provider returns empty response",
			provider: &mockAppProvider{
				infos: []apps.AppInfo{},
				html:  map[string][]byte{},
			},
			wantCount: 0,
			wantNames: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestAppsServer(t, tc.provider)
			resp, err := srv.ListUIApps(context.Background(), &loomv1.ListUIAppsRequest{})
			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, tc.wantCount, resp.TotalCount)

			if tc.wantNames != nil {
				require.Len(t, resp.Apps, len(tc.wantNames))
				for i, name := range tc.wantNames {
					assert.Equal(t, name, resp.Apps[i].Name)
				}
			} else {
				assert.Empty(t, resp.Apps)
			}
		})
	}
}

func TestListUIApps_FieldMapping(t *testing.T) {
	srv := newTestAppsServer(t, sampleAppProvider())
	resp, err := srv.ListUIApps(context.Background(), &loomv1.ListUIAppsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Apps, 2)

	// Verify all fields are correctly mapped for the first app
	app := resp.Apps[0]
	assert.Equal(t, "data-chart", app.Name)
	assert.Equal(t, "ui://loom/data-chart", app.Uri)
	assert.Equal(t, "Data Chart", app.DisplayName)
	assert.Equal(t, "Interactive data visualization", app.Description)
	assert.Equal(t, "text/html", app.MimeType)
	assert.True(t, app.PrefersBorder)

	// Verify second app has PrefersBorder=false
	assert.False(t, resp.Apps[1].PrefersBorder)
}

// --- GetUIApp Tests ---

func TestGetUIApp(t *testing.T) {
	tests := []struct {
		name     string
		provider AppProvider
		appName  string
		wantErr  bool
		wantCode codes.Code
		wantName string
		wantHTML string
	}{
		{
			name:     "get existing app by name",
			provider: sampleAppProvider(),
			appName:  "data-chart",
			wantName: "data-chart",
			wantHTML: "<html><body>Data Chart App</body></html>",
		},
		{
			name:     "get second app by name",
			provider: sampleAppProvider(),
			appName:  "query-results",
			wantName: "query-results",
			wantHTML: "<html><body>Query Results App</body></html>",
		},
		{
			name:     "get nonexistent app returns NotFound",
			provider: sampleAppProvider(),
			appName:  "nonexistent",
			wantErr:  true,
			wantCode: codes.NotFound,
		},
		{
			name:     "get app with empty name returns InvalidArgument",
			provider: sampleAppProvider(),
			appName:  "",
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "get app with nil provider returns NotFound",
			provider: nil,
			appName:  "data-chart",
			wantErr:  true,
			wantCode: codes.NotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestAppsServer(t, tc.provider)
			resp, err := srv.GetUIApp(context.Background(), &loomv1.GetUIAppRequest{
				Name: tc.appName,
			})

			if tc.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tc.wantCode, st.Code())
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			require.NotNil(t, resp.App)
			assert.Equal(t, tc.wantName, resp.App.Name)
			assert.Equal(t, []byte(tc.wantHTML), resp.Content)
		})
	}
}

func TestGetUIApp_FullFieldMapping(t *testing.T) {
	srv := newTestAppsServer(t, sampleAppProvider())
	resp, err := srv.GetUIApp(context.Background(), &loomv1.GetUIAppRequest{
		Name: "data-chart",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.App)

	assert.Equal(t, "data-chart", resp.App.Name)
	assert.Equal(t, "ui://loom/data-chart", resp.App.Uri)
	assert.Equal(t, "Data Chart", resp.App.DisplayName)
	assert.Equal(t, "Interactive data visualization", resp.App.Description)
	assert.Equal(t, "text/html", resp.App.MimeType)
	assert.True(t, resp.App.PrefersBorder)
	assert.Equal(t, []byte("<html><body>Data Chart App</body></html>"), resp.Content)
}

// --- SetAppProvider Tests ---

func TestSetAppProvider(t *testing.T) {
	srv := NewMultiAgentServer(nil, nil)
	srv.SetLogger(zaptest.NewLogger(t))

	// Initially nil - ListUIApps should return empty
	resp, err := srv.ListUIApps(context.Background(), &loomv1.ListUIAppsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Apps)

	// Set provider - ListUIApps should return apps
	srv.SetAppProvider(sampleAppProvider())
	resp, err = srv.ListUIApps(context.Background(), &loomv1.ListUIAppsRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.Apps, 2)

	// Replace with nil - ListUIApps should return empty again
	srv.SetAppProvider(nil)
	resp, err = srv.ListUIApps(context.Background(), &loomv1.ListUIAppsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Apps)
}

// --- Concurrency Tests ---

func TestAppsRPC_ConcurrentReadAccess(t *testing.T) {
	// Test concurrent reads with a stable provider (no writes).
	// All calls should succeed without races.
	srv := newTestAppsServer(t, sampleAppProvider())

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Concurrent ListUIApps calls
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			resp, err := srv.ListUIApps(context.Background(), &loomv1.ListUIAppsRequest{})
			assert.NoError(t, err)
			assert.NotNil(t, resp)
			assert.Equal(t, int32(2), resp.TotalCount)
		}()
	}

	// Concurrent GetUIApp calls
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			resp, err := srv.GetUIApp(context.Background(), &loomv1.GetUIAppRequest{
				Name: "data-chart",
			})
			assert.NoError(t, err)
			assert.NotNil(t, resp)
		}()
	}

	wg.Wait()
}

func TestAppsRPC_ConcurrentReadWriteAccess(t *testing.T) {
	// Test concurrent reads interleaved with writes to exercise locking.
	// The race detector is the primary assertion here -- no data races allowed.
	// Individual calls may succeed or fail depending on whether the provider
	// is nil at the moment of the call, so we do not assert on results.
	srv := newTestAppsServer(t, sampleAppProvider())

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = srv.ListUIApps(context.Background(), &loomv1.ListUIAppsRequest{})
		}()
	}

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = srv.GetUIApp(context.Background(), &loomv1.GetUIAppRequest{
				Name: "data-chart",
			})
		}()
	}

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				srv.SetAppProvider(sampleAppProvider())
			} else {
				srv.SetAppProvider(nil)
			}
		}(i)
	}

	wg.Wait()
}
