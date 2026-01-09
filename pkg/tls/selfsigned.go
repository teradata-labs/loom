// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package tls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// SelfSignedProvider generates and serves self-signed certificates for development.
type SelfSignedProvider struct {
	config   *loomv1.SelfSignedConfig
	cert     *tls.Certificate
	x509Cert *x509.Certificate
}

// NewSelfSignedProvider creates a self-signed certificate provider.
func NewSelfSignedProvider(config *loomv1.SelfSignedConfig) (*SelfSignedProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("self-signed config is nil")
	}

	// Validate and apply defaults
	if config.ValidityDays <= 0 {
		return nil, fmt.Errorf("validity_days must be positive, got %d", config.ValidityDays)
	}

	// Default to localhost if no hostnames specified
	if len(config.Hostnames) == 0 && len(config.IpAddresses) == 0 {
		config.Hostnames = []string{"localhost"}
	}

	// Generate certificate
	cert, x509Cert, err := generateSelfSignedCertificate(config)
	if err != nil {
		return nil, fmt.Errorf("failed to generate self-signed certificate: %w", err)
	}

	return &SelfSignedProvider{
		config:   config,
		cert:     cert,
		x509Cert: x509Cert,
	}, nil
}

// GetCertificate returns the self-signed certificate.
func (p *SelfSignedProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if p.cert == nil {
		return nil, fmt.Errorf("no certificate generated")
	}
	return p.cert, nil
}

// Start is a no-op for self-signed provider.
func (p *SelfSignedProvider) Start(ctx context.Context) error {
	// Self-signed provider doesn't need background tasks
	return nil
}

// Stop is a no-op for self-signed provider.
func (p *SelfSignedProvider) Stop(ctx context.Context) error {
	return nil
}

// Status returns the current certificate status.
func (p *SelfSignedProvider) Status(ctx context.Context) (*loomv1.TLSStatus, error) {
	if p.x509Cert == nil {
		return &loomv1.TLSStatus{
			Enabled: false,
			Mode:    "self-signed",
		}, nil
	}

	daysUntilExpiry := int32(time.Until(p.x509Cert.NotAfter).Hours() / 24)

	return &loomv1.TLSStatus{
		Enabled: true,
		Mode:    "self-signed",
		Certificate: &loomv1.CertificateInfo{
			Domains:         p.x509Cert.DNSNames,
			Issuer:          "Self-Signed",
			ExpiresAt:       p.x509Cert.NotAfter.Unix(),
			DaysUntilExpiry: daysUntilExpiry,
			Valid:           time.Now().Before(p.x509Cert.NotAfter),
		},
	}, nil
}

// Renew regenerates the self-signed certificate.
func (p *SelfSignedProvider) Renew(ctx context.Context, force bool) error {
	cert, x509Cert, err := generateSelfSignedCertificate(p.config)
	if err != nil {
		return fmt.Errorf("failed to regenerate certificate: %w", err)
	}

	p.cert = cert
	p.x509Cert = x509Cert
	return nil
}

// generateSelfSignedCertificate creates a new self-signed certificate.
func generateSelfSignedCertificate(config *loomv1.SelfSignedConfig) (*tls.Certificate, *x509.Certificate, error) {
	// Generate private key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// Set up certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(time.Duration(config.ValidityDays) * 24 * time.Hour)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{config.Organization},
			CommonName:   "localhost",
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Add DNS names
	template.DNSNames = append(template.DNSNames, config.Hostnames...)

	// Add IP addresses
	for _, ipStr := range config.IpAddresses {
		if ip := net.ParseIP(ipStr); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		}
	}

	// Create certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	// Parse the certificate
	x509Cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Encode to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Create tls.Certificate
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create X509 key pair: %w", err)
	}

	return &tlsCert, x509Cert, nil
}
