// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package tls

import (
	"context"
	"crypto/tls"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Manager handles TLS certificate management for the server.
// It supports multiple certificate sources: Let's Encrypt, manual files, and self-signed.
type Manager struct {
	config   *loomv1.TLSConfig
	provider Provider
}

// Provider is the interface for TLS certificate providers.
type Provider interface {
	// GetCertificate returns a certificate for the given client hello.
	// This is called on every TLS handshake.
	GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)

	// Start initializes the provider and starts background tasks (e.g., renewal).
	Start(ctx context.Context) error

	// Stop gracefully shuts down the provider.
	Stop(ctx context.Context) error

	// Status returns the current status of certificates managed by this provider.
	Status(ctx context.Context) (*loomv1.TLSStatus, error)

	// Renew manually triggers certificate renewal.
	Renew(ctx context.Context, force bool) error
}

// NewManager creates a new TLS manager from configuration.
func NewManager(config *loomv1.TLSConfig) (*Manager, error) {
	if config == nil || !config.Enabled {
		return nil, fmt.Errorf("TLS not enabled")
	}

	var provider Provider
	var err error

	switch config.Mode {
	case "letsencrypt":
		if config.Letsencrypt == nil {
			return nil, fmt.Errorf("letsencrypt config required for mode=letsencrypt")
		}
		provider, err = NewLetsEncryptProvider(config.Letsencrypt)
	case "manual":
		if config.Manual == nil {
			return nil, fmt.Errorf("manual config required for mode=manual")
		}
		provider, err = NewManualProvider(config.Manual)
	case "self-signed":
		if config.SelfSigned == nil {
			config.SelfSigned = &loomv1.SelfSignedConfig{
				Hostnames:    []string{"localhost"},
				IpAddresses:  []string{"127.0.0.1"},
				ValidityDays: 365,
				Organization: "Loom Development",
			}
		}
		provider, err = NewSelfSignedProvider(config.SelfSigned)
	default:
		return nil, fmt.Errorf("unknown TLS mode: %s (must be letsencrypt, manual, or self-signed)", config.Mode)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create TLS provider: %w", err)
	}

	return &Manager{
		config:   config,
		provider: provider,
	}, nil
}

// Start initializes the TLS manager and starts background tasks.
func (m *Manager) Start(ctx context.Context) error {
	if m.provider == nil {
		return fmt.Errorf("no TLS provider configured")
	}
	return m.provider.Start(ctx)
}

// Stop gracefully shuts down the TLS manager.
func (m *Manager) Stop(ctx context.Context) error {
	if m.provider == nil {
		return nil
	}
	return m.provider.Stop(ctx)
}

// TLSConfig returns a *tls.Config for use with gRPC/HTTP servers.
func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: m.provider.GetCertificate,
		MinVersion:     tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		},
	}
}

// Status returns the current TLS status.
func (m *Manager) Status(ctx context.Context) (*loomv1.TLSStatus, error) {
	if m.provider == nil {
		return &loomv1.TLSStatus{
			Enabled: false,
			Mode:    "none",
		}, nil
	}
	return m.provider.Status(ctx)
}

// Renew manually triggers certificate renewal.
func (m *Manager) Renew(ctx context.Context, force bool) error {
	if m.provider == nil {
		return fmt.Errorf("no TLS provider configured")
	}
	return m.provider.Renew(ctx, force)
}
