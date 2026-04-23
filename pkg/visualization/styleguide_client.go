// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visualization

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
)

// ErrStyleGuideRemoteNotImplemented is returned when a non-empty StyleGuide
// endpoint is configured but the remote gRPC client is not implemented yet.
var ErrStyleGuideRemoteNotImplemented = errors.New("styleguide: remote fetch not implemented")

// StyleGuideClient fetches styling from Hawk StyleGuide service.
type StyleGuideClient struct {
	endpoint string
	logger   *zap.Logger
	// In future: gRPC client to Hawk StyleGuide service
}

// NewStyleGuideClient creates a new StyleGuide client. It uses the global
// [zap.Logger] for any fallback warnings; call [StyleGuideClient.WithLogger]
// to inject a specific logger.
func NewStyleGuideClient(endpoint string) *StyleGuideClient {
	return &StyleGuideClient{
		endpoint: endpoint,
		logger:   zap.L(),
	}
}

// WithLogger sets the structured logger used by [StyleGuideClient.FetchStyleWithFallback].
// A nil logger is replaced by [zap.NewNop] so the client never panics on write.
// The receiver is returned for chaining.
func (sgc *StyleGuideClient) WithLogger(logger *zap.Logger) *StyleGuideClient {
	if logger == nil {
		logger = zap.NewNop()
	}
	sgc.logger = logger
	return sgc
}

// FetchStyle retrieves the current style configuration from Hawk.
// It respects ctx cancellation: if ctx is done, it returns ctx.Err().
// An empty endpoint returns default styling without calling the network.
// A non-empty endpoint returns [ErrStyleGuideRemoteNotImplemented] until the
// gRPC client exists. The theme argument is reserved for the future RPC; it is
// not used until that client is implemented.
func (sgc *StyleGuideClient) FetchStyle(ctx context.Context, theme string) (*StyleConfig, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if sgc.endpoint == "" {
		return DefaultStyleConfig(), nil
	}

	_ = theme // Used by future gRPC GetStyle(theme).

	// TODO: Implement gRPC call, for example (local dev only — use TLS creds in production):
	// conn, err := grpc.NewClient(sgc.endpoint,
	//     grpc.WithTransportCredentials(insecure.NewCredentials()))
	// if err != nil {
	//     return nil, fmt.Errorf("styleguide dial: %w", err)
	// }
	// defer conn.Close()
	//
	// client := styleguide.NewStyleGuideServiceClient(conn)
	// resp, err := client.GetStyle(ctx, &styleguide.GetStyleRequest{Theme: theme})
	// ...

	return nil, fmt.Errorf("%w: endpoint is set but remote client is not implemented", ErrStyleGuideRemoteNotImplemented)
}

// FetchStyleWithFallback calls [StyleGuideClient.FetchStyle] and returns [DefaultStyleConfig]
// on any error, including context cancellation, [ErrStyleGuideRemoteNotImplemented] when the
// endpoint is non-empty but the remote client is not implemented yet, and future RPC failures.
// It logs the error with the configured [zap.Logger] (see [StyleGuideClient.WithLogger]).
// Callers that need to detect misconfiguration, respect cancellation, or surface errors to
// users should use FetchStyle instead.
func (sgc *StyleGuideClient) FetchStyleWithFallback(ctx context.Context, theme string) *StyleConfig {
	style, err := sgc.FetchStyle(ctx, theme)
	if err != nil {
		logger := sgc.logger
		if logger == nil {
			logger = zap.L()
		}
		logger.Warn("visualization: style fetch failed, using defaults",
			zap.String("endpoint", sgc.endpoint),
			zap.String("theme", theme),
			zap.Error(err),
		)
		return DefaultStyleConfig()
	}
	return style
}

// ValidateStyle validates a StyleConfig has all required fields
func ValidateStyle(style *StyleConfig) error {
	if style == nil {
		return fmt.Errorf("style config is nil")
	}
	if style.ColorPrimary == "" {
		return fmt.Errorf("color_primary is required")
	}
	if style.FontFamily == "" {
		return fmt.Errorf("font_family is required")
	}
	if style.AnimationDuration <= 0 {
		return fmt.Errorf("animation_duration must be positive")
	}
	return nil
}

// MergeStyles merges a custom style with defaults (custom overrides defaults)
func MergeStyles(custom, defaults *StyleConfig) *StyleConfig {
	if custom == nil {
		return defaults
	}
	if defaults == nil {
		defaults = DefaultStyleConfig()
	}

	merged := *defaults // Copy defaults

	// Override with custom values (if non-zero)
	if custom.ColorPrimary != "" {
		merged.ColorPrimary = custom.ColorPrimary
	}
	if custom.ColorBackground != "" {
		merged.ColorBackground = custom.ColorBackground
	}
	if custom.ColorText != "" {
		merged.ColorText = custom.ColorText
	}
	if custom.ColorTextMuted != "" {
		merged.ColorTextMuted = custom.ColorTextMuted
	}
	if custom.ColorBorder != "" {
		merged.ColorBorder = custom.ColorBorder
	}
	if custom.ColorGlass != "" {
		merged.ColorGlass = custom.ColorGlass
	}
	if len(custom.ColorPalette) > 0 {
		merged.ColorPalette = custom.ColorPalette
	}
	if custom.FontFamily != "" {
		merged.FontFamily = custom.FontFamily
	}
	if custom.FontSizeTitle > 0 {
		merged.FontSizeTitle = custom.FontSizeTitle
	}
	if custom.FontSizeLabel > 0 {
		merged.FontSizeLabel = custom.FontSizeLabel
	}
	if custom.FontSizeTooltip > 0 {
		merged.FontSizeTooltip = custom.FontSizeTooltip
	}
	if custom.AnimationDuration > 0 {
		merged.AnimationDuration = custom.AnimationDuration
	}
	if custom.AnimationEasing != "" {
		merged.AnimationEasing = custom.AnimationEasing
	}
	if custom.ShadowBlur > 0 {
		merged.ShadowBlur = custom.ShadowBlur
	}
	if custom.GlowIntensity > 0 {
		merged.GlowIntensity = custom.GlowIntensity
	}

	return &merged
}

// GetThemeVariant returns a style config for a specific theme variant
func GetThemeVariant(variant string) *StyleConfig {
	style := DefaultStyleConfig()

	switch variant {
	case "dark":
		// Already default (dark theme)
		return style

	case "light":
		// Light theme variant
		style.ColorBackground = "#ffffff"
		style.ColorText = "#1a1a1a"
		style.ColorTextMuted = "#6b7280"
		style.ColorBorder = "#e5e7eb"
		style.ColorGlass = "rgba(255, 255, 255, 0.8)"
		return style

	case "teradata":
		// Teradata branding emphasis
		style.ColorPrimary = "#f37021" // Teradata Orange
		style.ColorPalette = []string{
			"#f37021", // Teradata Orange
			"#00233d", // Teradata Navy
			"#fbbf24", // Gold
			"#10b981", // Green
		}
		return style

	case "minimal":
		// Minimal monochrome theme
		style.ColorPrimary = "#6b7280"
		style.ColorPalette = []string{
			"#1f2937",
			"#374151",
			"#4b5563",
			"#6b7280",
			"#9ca3af",
		}
		style.AnimationDuration = 800
		return style

	default:
		return style
	}
}
