// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"regexp"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// appNameRegex validates app names: lowercase alphanumeric, hyphens, 1-63 chars.
var appNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// reservedAppNames are names that cannot be used for apps because they
// collide with HTTP gateway routes (e.g. GET /v1/apps/component-types).
var reservedAppNames = map[string]bool{
	"component-types": true,
}

// AppProvider provides UI app information to the gRPC server.
// Implemented by UIResourceRegistry to avoid tight coupling between
// the server package and the apps package internals.
type AppProvider interface {
	ListAppInfo() []apps.AppInfo
	GetAppHTML(name string) ([]byte, *apps.AppInfo, error)
	CreateApp(name, displayName, description string, html []byte, overwrite bool) (*apps.AppInfo, bool, error)
	UpdateApp(name, displayName, description string, html []byte) (*apps.AppInfo, error)
	DeleteApp(name string) error
}

// AppCompiler compiles UIAppSpec to HTML. Separated from AppProvider to keep
// the registry focused on storage and the compiler focused on validation/rendering.
type AppCompiler interface {
	Compile(spec *loomv1.UIAppSpec) ([]byte, error)
	Validate(spec *loomv1.UIAppSpec) error
	ListComponentTypes() []*loomv1.ComponentType
}

// SetAppProvider sets the app provider for ListUIApps/GetUIApp RPCs.
func (s *MultiAgentServer) SetAppProvider(p AppProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appProvider = p
}

// SetAppCompiler sets the compiler for CreateUIApp/UpdateUIApp RPCs.
func (s *MultiAgentServer) SetAppCompiler(c AppCompiler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appCompiler = c
}

// ListUIApps lists all available UI apps registered with the app provider.
func (s *MultiAgentServer) ListUIApps(ctx context.Context, req *loomv1.ListUIAppsRequest) (*loomv1.ListUIAppsResponse, error) {
	s.mu.RLock()
	provider := s.appProvider
	s.mu.RUnlock()

	if provider == nil {
		return &loomv1.ListUIAppsResponse{}, nil
	}

	infos := provider.ListAppInfo()
	protoApps := make([]*loomv1.UIApp, 0, len(infos))
	for _, info := range infos {
		protoApps = append(protoApps, appInfoToProto(&info))
	}

	if s.logger != nil {
		s.logger.Info("ListUIApps completed", zap.Int("total_count", len(protoApps)))
	}

	return &loomv1.ListUIAppsResponse{
		Apps:       protoApps,
		TotalCount: safeIntToInt32(len(protoApps)),
	}, nil
}

// GetUIApp retrieves a specific UI app by short name, returning its metadata and HTML content.
func (s *MultiAgentServer) GetUIApp(ctx context.Context, req *loomv1.GetUIAppRequest) (*loomv1.GetUIAppResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "app name is required")
	}

	s.mu.RLock()
	provider := s.appProvider
	s.mu.RUnlock()

	if provider == nil {
		return nil, status.Error(codes.NotFound, "no app provider configured")
	}

	content, info, err := provider.GetAppHTML(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "app not found: %s", req.Name)
	}

	if s.logger != nil {
		s.logger.Info("GetUIApp completed", zap.String("name", req.Name))
	}

	return &loomv1.GetUIAppResponse{
		App:     appInfoToProto(info),
		Content: content,
	}, nil
}

// CreateUIApp creates a dynamic UI app from a declarative spec.
func (s *MultiAgentServer) CreateUIApp(ctx context.Context, req *loomv1.CreateUIAppRequest) (*loomv1.CreateUIAppResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "app name is required")
	}
	if !appNameRegex.MatchString(req.Name) {
		return nil, status.Errorf(codes.InvalidArgument,
			"invalid app name %q: must match ^[a-z0-9][a-z0-9-]{0,62}$", req.Name)
	}
	if reservedAppNames[req.Name] {
		return nil, status.Errorf(codes.InvalidArgument,
			"app name %q is reserved (collides with HTTP route)", req.Name)
	}
	if req.Spec == nil {
		return nil, status.Error(codes.InvalidArgument, "spec is required")
	}

	s.mu.RLock()
	provider := s.appProvider
	compiler := s.appCompiler
	s.mu.RUnlock()

	if provider == nil {
		return nil, status.Error(codes.FailedPrecondition, "no app provider configured")
	}
	if compiler == nil {
		return nil, status.Error(codes.FailedPrecondition, "no app compiler configured")
	}

	// Compile spec to HTML
	html, err := compiler.Compile(req.Spec)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "compile spec: %v", err)
	}

	// Use spec title as display name if not provided
	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Spec.Title
	}

	// Create app in registry
	info, overwritten, err := provider.CreateApp(req.Name, displayName, req.Description, html, req.Overwrite)
	if err != nil {
		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "already exists"):
			return nil, status.Errorf(codes.AlreadyExists, "%v", err)
		case strings.Contains(errMsg, "cannot overwrite embedded"):
			return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
		case strings.Contains(errMsg, "limit reached"):
			return nil, status.Errorf(codes.ResourceExhausted, "%v", err)
		default:
			return nil, status.Errorf(codes.Internal, "%v", err)
		}
	}

	if s.logger != nil {
		s.logger.Info("CreateUIApp completed",
			zap.String("name", req.Name),
			zap.Bool("overwritten", overwritten),
			zap.Int("html_bytes", len(html)),
		)
	}

	return &loomv1.CreateUIAppResponse{
		App:         appInfoToProto(info),
		Content:     html,
		Overwritten: overwritten,
	}, nil
}

// UpdateUIApp updates an existing dynamic app's spec.
func (s *MultiAgentServer) UpdateUIApp(ctx context.Context, req *loomv1.UpdateUIAppRequest) (*loomv1.UpdateUIAppResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "app name is required")
	}
	if req.Spec == nil {
		return nil, status.Error(codes.InvalidArgument, "spec is required")
	}

	s.mu.RLock()
	provider := s.appProvider
	compiler := s.appCompiler
	s.mu.RUnlock()

	if provider == nil {
		return nil, status.Error(codes.FailedPrecondition, "no app provider configured")
	}
	if compiler == nil {
		return nil, status.Error(codes.FailedPrecondition, "no app compiler configured")
	}

	// Compile spec to HTML
	html, err := compiler.Compile(req.Spec)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "compile spec: %v", err)
	}

	// Update in registry
	info, err := provider.UpdateApp(req.Name, req.DisplayName, req.Description, html)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}

	if s.logger != nil {
		s.logger.Info("UpdateUIApp completed",
			zap.String("name", req.Name),
			zap.Int("html_bytes", len(html)),
		)
	}

	return &loomv1.UpdateUIAppResponse{
		App:     appInfoToProto(info),
		Content: html,
	}, nil
}

// DeleteUIApp deletes a dynamic UI app.
func (s *MultiAgentServer) DeleteUIApp(ctx context.Context, req *loomv1.DeleteUIAppRequest) (*loomv1.DeleteUIAppResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "app name is required")
	}

	s.mu.RLock()
	provider := s.appProvider
	s.mu.RUnlock()

	if provider == nil {
		return nil, status.Error(codes.FailedPrecondition, "no app provider configured")
	}

	if err := provider.DeleteApp(req.Name); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}

	if s.logger != nil {
		s.logger.Info("DeleteUIApp completed", zap.String("name", req.Name))
	}

	return &loomv1.DeleteUIAppResponse{
		Deleted: true,
	}, nil
}

// ListComponentTypes returns the catalog of available component types for building dynamic apps.
func (s *MultiAgentServer) ListComponentTypes(ctx context.Context, req *loomv1.ListComponentTypesRequest) (*loomv1.ListComponentTypesResponse, error) {
	s.mu.RLock()
	compiler := s.appCompiler
	s.mu.RUnlock()

	if compiler == nil {
		return &loomv1.ListComponentTypesResponse{}, nil
	}

	return &loomv1.ListComponentTypesResponse{
		Types: compiler.ListComponentTypes(),
	}, nil
}

// appInfoToProto converts an apps.AppInfo to a proto UIApp message.
func appInfoToProto(info *apps.AppInfo) *loomv1.UIApp {
	return &loomv1.UIApp{
		Name:          info.Name,
		Uri:           info.URI,
		DisplayName:   info.DisplayName,
		Description:   info.Description,
		MimeType:      info.MimeType,
		PrefersBorder: info.PrefersBorder,
		Dynamic:       info.Dynamic,
	}
}
