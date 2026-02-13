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

func (m *mockAppProvider) CreateApp(name, displayName, description string, html []byte, overwrite bool) (*apps.AppInfo, bool, error) {
	for _, info := range m.infos {
		if info.Name == name && !overwrite {
			return nil, false, fmt.Errorf("app already exists: %s", name)
		}
	}
	info := apps.AppInfo{
		Name:        name,
		URI:         "ui://loom/" + name,
		DisplayName: displayName,
		Description: description,
		MimeType:    "text/html;profile=mcp-app",
		Dynamic:     true,
	}
	m.infos = append(m.infos, info)
	m.html[name] = html
	return &info, false, nil
}

func (m *mockAppProvider) UpdateApp(name, displayName, description string, html []byte) (*apps.AppInfo, error) {
	for i, info := range m.infos {
		if info.Name == name {
			if displayName != "" {
				m.infos[i].DisplayName = displayName
			}
			if description != "" {
				m.infos[i].Description = description
			}
			m.html[name] = html
			return &m.infos[i], nil
		}
	}
	return nil, fmt.Errorf("app not found: %s", name)
}

func (m *mockAppProvider) DeleteApp(name string) error {
	for i, info := range m.infos {
		if info.Name == name {
			m.infos = append(m.infos[:i], m.infos[i+1:]...)
			delete(m.html, name)
			return nil
		}
	}
	return fmt.Errorf("app not found: %s", name)
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

// --- mockAppCompiler for testing Create/Update/ListComponentTypes RPCs ---

type mockAppCompiler struct {
	compileFunc func(spec *loomv1.UIAppSpec) ([]byte, error)
	validateErr error
	types       []*loomv1.ComponentType
}

func (m *mockAppCompiler) Compile(spec *loomv1.UIAppSpec) ([]byte, error) {
	if m.compileFunc != nil {
		return m.compileFunc(spec)
	}
	// Default: return a simple HTML document with the title
	title := spec.Title
	if title == "" {
		title = "Loom App"
	}
	return []byte("<html><title>" + title + "</title></html>"), nil
}

func (m *mockAppCompiler) Validate(spec *loomv1.UIAppSpec) error {
	return m.validateErr
}

func (m *mockAppCompiler) ListComponentTypes() []*loomv1.ComponentType {
	return m.types
}

// newTestAppsServerWithCompiler creates a MultiAgentServer with both provider and compiler.
func newTestAppsServerWithCompiler(t *testing.T, provider AppProvider, compiler AppCompiler) *MultiAgentServer {
	t.Helper()
	srv := NewMultiAgentServer(nil, nil)
	srv.SetLogger(zaptest.NewLogger(t))
	if provider != nil {
		srv.SetAppProvider(provider)
	}
	if compiler != nil {
		srv.SetAppCompiler(compiler)
	}
	return srv
}

// --- CreateUIApp Tests ---

func TestCreateUIApp_HappyPath(t *testing.T) {
	provider := &mockAppProvider{
		infos: []apps.AppInfo{},
		html:  map[string][]byte{},
	}
	compiler := &mockAppCompiler{}

	srv := newTestAppsServerWithCompiler(t, provider, compiler)

	resp, err := srv.CreateUIApp(context.Background(), &loomv1.CreateUIAppRequest{
		Name:        "revenue-dashboard",
		DisplayName: "Revenue Dashboard",
		Description: "Shows revenue metrics",
		Spec: &loomv1.UIAppSpec{
			Version: "1.0",
			Title:   "Revenue Dashboard",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.App)
	assert.Equal(t, "revenue-dashboard", resp.App.Name)
	assert.Equal(t, "Revenue Dashboard", resp.App.DisplayName)
	assert.Equal(t, "Shows revenue metrics", resp.App.Description)
	assert.True(t, resp.App.Dynamic)
	assert.NotEmpty(t, resp.Content)
	assert.False(t, resp.Overwritten)
}

func TestCreateUIApp_InvalidName(t *testing.T) {
	tests := []struct {
		name    string
		appName string
	}{
		{"empty name", ""},
		{"uppercase letters", "MyApp"},
		{"spaces", "my app"},
		{"starts with hyphen", "-my-app"},
		{"special characters", "my_app!"},
		{"too long (64 chars)", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
			compiler := &mockAppCompiler{}
			srv := newTestAppsServerWithCompiler(t, provider, compiler)

			_, err := srv.CreateUIApp(context.Background(), &loomv1.CreateUIAppRequest{
				Name: tc.appName,
				Spec: &loomv1.UIAppSpec{Version: "1.0", Title: "Test"},
			})
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.InvalidArgument, st.Code())
		})
	}
}

func TestCreateUIApp_MissingSpec(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	compiler := &mockAppCompiler{}
	srv := newTestAppsServerWithCompiler(t, provider, compiler)

	_, err := srv.CreateUIApp(context.Background(), &loomv1.CreateUIAppRequest{
		Name: "my-app",
		Spec: nil,
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "spec is required")
}

func TestCreateUIApp_NoProvider(t *testing.T) {
	compiler := &mockAppCompiler{}
	srv := newTestAppsServerWithCompiler(t, nil, compiler)

	_, err := srv.CreateUIApp(context.Background(), &loomv1.CreateUIAppRequest{
		Name: "my-app",
		Spec: &loomv1.UIAppSpec{Version: "1.0", Title: "Test"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Contains(t, st.Message(), "no app provider")
}

func TestCreateUIApp_NoCompiler(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	srv := newTestAppsServerWithCompiler(t, provider, nil)

	_, err := srv.CreateUIApp(context.Background(), &loomv1.CreateUIAppRequest{
		Name: "my-app",
		Spec: &loomv1.UIAppSpec{Version: "1.0", Title: "Test"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Contains(t, st.Message(), "no app compiler")
}

func TestCreateUIApp_CompileError(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	compiler := &mockAppCompiler{
		compileFunc: func(spec *loomv1.UIAppSpec) ([]byte, error) {
			return nil, fmt.Errorf("invalid spec: bad version")
		},
	}
	srv := newTestAppsServerWithCompiler(t, provider, compiler)

	_, err := srv.CreateUIApp(context.Background(), &loomv1.CreateUIAppRequest{
		Name: "my-app",
		Spec: &loomv1.UIAppSpec{Version: "2.0", Title: "Test"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "compile spec")
}

func TestCreateUIApp_UsesSpecTitleAsDisplayName(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	compiler := &mockAppCompiler{}
	srv := newTestAppsServerWithCompiler(t, provider, compiler)

	// When DisplayName is empty, it should fall back to spec.Title
	resp, err := srv.CreateUIApp(context.Background(), &loomv1.CreateUIAppRequest{
		Name:        "my-app",
		DisplayName: "", // empty
		Spec:        &loomv1.UIAppSpec{Version: "1.0", Title: "Spec Title"},
	})
	require.NoError(t, err)
	assert.Equal(t, "Spec Title", resp.App.DisplayName)
}

// --- UpdateUIApp Tests ---

func TestUpdateUIApp_HappyPath(t *testing.T) {
	provider := &mockAppProvider{
		infos: []apps.AppInfo{
			{Name: "my-app", URI: "ui://loom/my-app", DisplayName: "My App", Dynamic: true},
		},
		html: map[string][]byte{
			"my-app": []byte("<html>v1</html>"),
		},
	}
	compiler := &mockAppCompiler{}
	srv := newTestAppsServerWithCompiler(t, provider, compiler)

	resp, err := srv.UpdateUIApp(context.Background(), &loomv1.UpdateUIAppRequest{
		Name:        "my-app",
		DisplayName: "My App v2",
		Description: "Updated description",
		Spec:        &loomv1.UIAppSpec{Version: "1.0", Title: "My App v2"},
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.App)
	assert.Equal(t, "my-app", resp.App.Name)
	assert.Equal(t, "My App v2", resp.App.DisplayName)
	assert.NotEmpty(t, resp.Content)
}

func TestUpdateUIApp_NotFound(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	compiler := &mockAppCompiler{}
	srv := newTestAppsServerWithCompiler(t, provider, compiler)

	_, err := srv.UpdateUIApp(context.Background(), &loomv1.UpdateUIAppRequest{
		Name: "nonexistent",
		Spec: &loomv1.UIAppSpec{Version: "1.0", Title: "Test"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestUpdateUIApp_EmptyName(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	compiler := &mockAppCompiler{}
	srv := newTestAppsServerWithCompiler(t, provider, compiler)

	_, err := srv.UpdateUIApp(context.Background(), &loomv1.UpdateUIAppRequest{
		Name: "",
		Spec: &loomv1.UIAppSpec{Version: "1.0", Title: "Test"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestUpdateUIApp_MissingSpec(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	compiler := &mockAppCompiler{}
	srv := newTestAppsServerWithCompiler(t, provider, compiler)

	_, err := srv.UpdateUIApp(context.Background(), &loomv1.UpdateUIAppRequest{
		Name: "my-app",
		Spec: nil,
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "spec is required")
}

func TestUpdateUIApp_NoProvider(t *testing.T) {
	compiler := &mockAppCompiler{}
	srv := newTestAppsServerWithCompiler(t, nil, compiler)

	_, err := srv.UpdateUIApp(context.Background(), &loomv1.UpdateUIAppRequest{
		Name: "my-app",
		Spec: &loomv1.UIAppSpec{Version: "1.0", Title: "Test"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

func TestUpdateUIApp_NoCompiler(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	srv := newTestAppsServerWithCompiler(t, provider, nil)

	_, err := srv.UpdateUIApp(context.Background(), &loomv1.UpdateUIAppRequest{
		Name: "my-app",
		Spec: &loomv1.UIAppSpec{Version: "1.0", Title: "Test"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

// --- DeleteUIApp Tests ---

func TestDeleteUIApp_HappyPath(t *testing.T) {
	provider := &mockAppProvider{
		infos: []apps.AppInfo{
			{Name: "my-app", URI: "ui://loom/my-app", Dynamic: true},
		},
		html: map[string][]byte{
			"my-app": []byte("<html>app</html>"),
		},
	}
	srv := newTestAppsServerWithCompiler(t, provider, nil)

	resp, err := srv.DeleteUIApp(context.Background(), &loomv1.DeleteUIAppRequest{
		Name: "my-app",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Deleted)

	// Verify it was deleted from the mock provider
	assert.Empty(t, provider.infos)
}

func TestDeleteUIApp_NotFound(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	srv := newTestAppsServerWithCompiler(t, provider, nil)

	_, err := srv.DeleteUIApp(context.Background(), &loomv1.DeleteUIAppRequest{
		Name: "nonexistent",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestDeleteUIApp_EmptyName(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	srv := newTestAppsServerWithCompiler(t, provider, nil)

	_, err := srv.DeleteUIApp(context.Background(), &loomv1.DeleteUIAppRequest{
		Name: "",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestDeleteUIApp_NoProvider(t *testing.T) {
	srv := newTestAppsServerWithCompiler(t, nil, nil)

	_, err := srv.DeleteUIApp(context.Background(), &loomv1.DeleteUIAppRequest{
		Name: "my-app",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

// --- ListComponentTypes Tests ---

func TestListComponentTypes_HappyPath(t *testing.T) {
	compiler := &mockAppCompiler{
		types: []*loomv1.ComponentType{
			{Type: "stat-cards", Description: "KPI cards", Category: "display"},
			{Type: "chart", Description: "Charts", Category: "display"},
			{Type: "section", Description: "Layout section", Category: "layout"},
		},
	}
	srv := newTestAppsServerWithCompiler(t, nil, compiler)

	resp, err := srv.ListComponentTypes(context.Background(), &loomv1.ListComponentTypesRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Types, 3)

	assert.Equal(t, "stat-cards", resp.Types[0].Type)
	assert.Equal(t, "chart", resp.Types[1].Type)
	assert.Equal(t, "section", resp.Types[2].Type)
}

func TestListComponentTypes_NoCompiler(t *testing.T) {
	srv := newTestAppsServerWithCompiler(t, nil, nil)

	resp, err := srv.ListComponentTypes(context.Background(), &loomv1.ListComponentTypesRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Types)
}

// --- Concurrent CRUD Tests ---

// threadSafeAppProvider wraps mockAppProvider with a mutex for concurrent test use.
type threadSafeAppProvider struct {
	mu       sync.Mutex
	provider *mockAppProvider
}

func (t *threadSafeAppProvider) ListAppInfo() []apps.AppInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.provider.ListAppInfo()
}

func (t *threadSafeAppProvider) GetAppHTML(name string) ([]byte, *apps.AppInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.provider.GetAppHTML(name)
}

func (t *threadSafeAppProvider) CreateApp(name, displayName, description string, html []byte, overwrite bool) (*apps.AppInfo, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.provider.CreateApp(name, displayName, description, html, overwrite)
}

func (t *threadSafeAppProvider) UpdateApp(name, displayName, description string, html []byte) (*apps.AppInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.provider.UpdateApp(name, displayName, description, html)
}

func (t *threadSafeAppProvider) DeleteApp(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.provider.DeleteApp(name)
}

func TestAppsRPC_ConcurrentCRUDAccess(t *testing.T) {
	provider := &threadSafeAppProvider{
		provider: &mockAppProvider{
			infos: []apps.AppInfo{
				{Name: "existing-app", URI: "ui://loom/existing-app", Dynamic: true},
			},
			html: map[string][]byte{
				"existing-app": []byte("<html>existing</html>"),
			},
		},
	}
	compiler := &mockAppCompiler{}
	srv := newTestAppsServerWithCompiler(t, provider, compiler)

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines * 4)

	// Concurrent creates
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			_, _ = srv.CreateUIApp(context.Background(), &loomv1.CreateUIAppRequest{
				Name: fmt.Sprintf("app-%d", idx),
				Spec: &loomv1.UIAppSpec{Version: "1.0", Title: "App"},
			})
		}(i)
	}

	// Concurrent updates
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = srv.UpdateUIApp(context.Background(), &loomv1.UpdateUIAppRequest{
				Name: "existing-app",
				Spec: &loomv1.UIAppSpec{Version: "1.0", Title: "Updated"},
			})
		}()
	}

	// Concurrent lists
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = srv.ListUIApps(context.Background(), &loomv1.ListUIAppsRequest{})
		}()
	}

	// Concurrent ListComponentTypes
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = srv.ListComponentTypes(context.Background(), &loomv1.ListComponentTypesRequest{})
		}()
	}

	wg.Wait()
	// Race detector is the primary assertion
}
