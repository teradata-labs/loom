// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visualization

import (
	"context"
	"fmt"
)

// StyleGuideClient fetches styling from Hawk StyleGuide service
type StyleGuideClient struct {
	endpoint string
	// In future: gRPC client to Hawk StyleGuide service
}

// NewStyleGuideClient creates a new StyleGuide client
func NewStyleGuideClient(endpoint string) *StyleGuideClient {
	return &StyleGuideClient{
		endpoint: endpoint,
	}
}

// FetchStyle retrieves the current style configuration from Hawk
// TODO: Implement gRPC client to Hawk StyleGuide service
func (sgc *StyleGuideClient) FetchStyle(ctx context.Context, theme string) (*StyleConfig, error) {
	// For now, return default Hawk styling
	// In future, this will make gRPC call to Hawk StyleGuide service
	// to fetch current design tokens, color palettes, fonts, etc.

	if sgc.endpoint == "" {
		// No endpoint configured, use defaults
		return DefaultStyleConfig(), nil
	}

	// TODO: Implement gRPC call
	// Example:
	// conn, err := grpc.Dial(sgc.endpoint, grpc.WithInsecure())
	// if err != nil {
	//     return nil, fmt.Errorf("failed to connect to StyleGuide service: %w", err)
	// }
	// defer conn.Close()
	//
	// client := styleguide.NewStyleGuideServiceClient(conn)
	// resp, err := client.GetStyle(ctx, &styleguide.GetStyleRequest{Theme: theme})
	// if err != nil {
	//     return nil, fmt.Errorf("failed to fetch style: %w", err)
	// }
	//
	// return convertProtoToStyleConfig(resp.Style), nil

	// For now, return default config
	return DefaultStyleConfig(), nil
}

// FetchStyleWithFallback attempts to fetch from Hawk, falls back to defaults on error
func (sgc *StyleGuideClient) FetchStyleWithFallback(ctx context.Context, theme string) *StyleConfig {
	style, err := sgc.FetchStyle(ctx, theme)
	if err != nil {
		// Log error and return defaults
		// In production, would use proper logger
		fmt.Printf("Warning: Failed to fetch style from Hawk, using defaults: %v\n", err)
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
