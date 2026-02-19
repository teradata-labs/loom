// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// ManualProvider loads certificates from files specified in configuration.
type ManualProvider struct {
	config   *loomv1.ManualTLSConfig
	cert     *tls.Certificate
	x509Cert *x509.Certificate
}

// NewManualProvider creates a manual certificate provider.
func NewManualProvider(config *loomv1.ManualTLSConfig) (*ManualProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("manual TLS config is nil")
	}
	if config.CertFile == "" || config.KeyFile == "" {
		return nil, fmt.Errorf("cert_file and key_file are required for manual TLS")
	}

	// Load certificate and key
	cert, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load certificate: %w", err)
	}

	// Parse x509 certificate for metadata
	var x509Cert *x509.Certificate
	if len(cert.Certificate) > 0 {
		x509Cert, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse certificate: %w", err)
		}
	}

	// TODO: If CA file specified (for mTLS), implement client certificate verification
	// This would set up ClientAuth and ClientCAs in the TLS config

	return &ManualProvider{
		config:   config,
		cert:     &cert,
		x509Cert: x509Cert,
	}, nil
}

// GetCertificate returns the manually loaded certificate.
func (p *ManualProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if p.cert == nil {
		return nil, fmt.Errorf("no certificate loaded")
	}
	return p.cert, nil
}

// Start is a no-op for manual provider.
func (p *ManualProvider) Start(ctx context.Context) error {
	// Manual provider doesn't need background tasks
	return nil
}

// Stop is a no-op for manual provider.
func (p *ManualProvider) Stop(ctx context.Context) error {
	return nil
}

// Status returns the current certificate status.
func (p *ManualProvider) Status(ctx context.Context) (*loomv1.TLSStatus, error) {
	if p.x509Cert == nil {
		return &loomv1.TLSStatus{
			Enabled: false,
			Mode:    "manual",
		}, nil
	}

	daysUntilExpiry := int32(time.Until(p.x509Cert.NotAfter).Hours() / 24)

	return &loomv1.TLSStatus{
		Enabled: true,
		Mode:    "manual",
		Certificate: &loomv1.CertificateInfo{
			Domains:         p.x509Cert.DNSNames,
			Issuer:          p.x509Cert.Issuer.CommonName,
			ExpiresAt:       p.x509Cert.NotAfter.Unix(),
			DaysUntilExpiry: daysUntilExpiry,
			Valid:           time.Now().Before(p.x509Cert.NotAfter),
		},
	}, nil
}

// Renew returns an error because manual certificates must be renewed manually.
func (p *ManualProvider) Renew(ctx context.Context, force bool) error {
	return fmt.Errorf("manual certificates cannot be renewed automatically - replace certificate files and restart server")
}

// LoadCertificateFromFile loads a certificate from a PEM file.
func LoadCertificateFromFile(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to read certificate file: %w", err)
	}

	cert, err := x509.ParseCertificate(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return cert, nil
}
