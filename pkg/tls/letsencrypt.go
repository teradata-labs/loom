// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package tls

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
)

// Default ACME directory URLs.
// Can be overridden via environment variables:
//   - LOOM_ACME_PRODUCTION_URL
//   - LOOM_ACME_STAGING_URL
const (
	DefaultLetsEncryptProduction = "https://acme-v02.api.letsencrypt.org/directory"
	DefaultLetsEncryptStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// LetsEncryptProduction returns the production ACME directory URL.
func LetsEncryptProduction() string {
	if url := os.Getenv("LOOM_ACME_PRODUCTION_URL"); url != "" {
		return url
	}
	return DefaultLetsEncryptProduction
}

// LetsEncryptStaging returns the staging ACME directory URL.
func LetsEncryptStaging() string {
	if url := os.Getenv("LOOM_ACME_STAGING_URL"); url != "" {
		return url
	}
	return DefaultLetsEncryptStaging
}

// LetsEncryptProvider manages certificates from Let's Encrypt.
type LetsEncryptProvider struct {
	config        *loomv1.LetsEncryptConfig
	client        *lego.Client
	cert          *tls.Certificate
	x509Cert      *x509.Certificate
	certResource  *certificate.Resource
	renewalTicker *time.Ticker
	stopChan      chan struct{}
	mu            sync.RWMutex
	logger        *zap.Logger
}

// ACMEUser implements the required registration.User interface.
type ACMEUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *ACMEUser) GetEmail() string {
	return u.Email
}

func (u *ACMEUser) GetRegistration() *registration.Resource {
	return u.Registration
}

func (u *ACMEUser) GetPrivateKey() crypto.PrivateKey {
	return u.key
}

// NewLetsEncryptProvider creates a Let's Encrypt certificate provider.
func NewLetsEncryptProvider(config *loomv1.LetsEncryptConfig) (*LetsEncryptProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("letsencrypt config is nil")
	}

	// Validate required fields
	if len(config.Domains) == 0 {
		return nil, fmt.Errorf("at least one domain is required for Let's Encrypt")
	}
	if config.Email == "" {
		return nil, fmt.Errorf("email is required for Let's Encrypt")
	}
	if !config.AcceptTos {
		return nil, fmt.Errorf("must accept Let's Encrypt Terms of Service (set accept_tos: true)")
	}

	// Set defaults
	if config.AcmeDirectoryUrl == "" {
		config.AcmeDirectoryUrl = LetsEncryptProduction()
	}
	if config.HttpChallengePort == 0 {
		config.HttpChallengePort = 80
	}
	if config.CacheDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		config.CacheDir = filepath.Join(homeDir, ".loom", "certs")
	}
	if config.RenewBeforeDays == 0 {
		config.RenewBeforeDays = 30
	}
	if !config.AutoRenew {
		config.AutoRenew = true // Default to true
	}

	// Initialize logger
	logger, _ := zap.NewProduction()

	provider := &LetsEncryptProvider{
		config:   config,
		stopChan: make(chan struct{}),
		logger:   logger,
	}

	// Create cache directory
	if err := os.MkdirAll(config.CacheDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Try to load existing certificate
	if err := provider.loadCachedCertificate(); err == nil {
		logger.Info("loaded cached certificate", zap.Strings("domains", config.Domains))
	} else {
		logger.Info("no cached certificate found, will obtain new certificate", zap.Error(err))
	}

	return provider, nil
}

// Start initializes the ACME client and starts background renewal.
func (p *LetsEncryptProvider) Start(ctx context.Context) error {
	// If we don't have a certificate yet, obtain one
	if p.cert == nil {
		if err := p.obtainCertificate(); err != nil {
			return fmt.Errorf("failed to obtain initial certificate: %w", err)
		}
	}

	// Start renewal ticker if auto-renewal is enabled
	if p.config.AutoRenew {
		// Check for renewal once per day
		p.renewalTicker = time.NewTicker(24 * time.Hour)
		go p.renewalLoop()
	}

	return nil
}

// Stop gracefully shuts down the provider.
func (p *LetsEncryptProvider) Stop(ctx context.Context) error {
	close(p.stopChan)
	if p.renewalTicker != nil {
		p.renewalTicker.Stop()
	}
	return nil
}

// GetCertificate returns the current certificate.
func (p *LetsEncryptProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.cert == nil {
		return nil, fmt.Errorf("no certificate available")
	}
	return p.cert, nil
}

// Status returns the current certificate status.
func (p *LetsEncryptProvider) Status(ctx context.Context) (*loomv1.TLSStatus, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.x509Cert == nil {
		return &loomv1.TLSStatus{
			Enabled: false,
			Mode:    "letsencrypt",
		}, nil
	}

	daysUntilExpiry := int32(time.Until(p.x509Cert.NotAfter).Hours() / 24)
	needsRenewal := daysUntilExpiry <= p.config.RenewBeforeDays

	status := &loomv1.TLSStatus{
		Enabled: true,
		Mode:    "letsencrypt",
		Certificate: &loomv1.CertificateInfo{
			Domains:         p.x509Cert.DNSNames,
			Issuer:          p.x509Cert.Issuer.CommonName,
			ExpiresAt:       p.x509Cert.NotAfter.Unix(),
			DaysUntilExpiry: daysUntilExpiry,
			Valid:           time.Now().Before(p.x509Cert.NotAfter),
		},
		Renewal: &loomv1.RenewalStatus{
			Enabled: p.config.AutoRenew,
		},
	}

	if needsRenewal {
		nextRenewal := time.Now().Add(24 * time.Hour) // Next check in 24 hours
		status.Renewal.NextRenewalAt = nextRenewal.Unix()
	}

	return status, nil
}

// Renew manually triggers certificate renewal.
func (p *LetsEncryptProvider) Renew(ctx context.Context, force bool) error {
	p.mu.RLock()
	daysUntilExpiry := int32(time.Until(p.x509Cert.NotAfter).Hours() / 24)
	p.mu.RUnlock()

	if !force && daysUntilExpiry > p.config.RenewBeforeDays {
		return fmt.Errorf("certificate not due for renewal (expires in %d days, renew threshold is %d days)",
			daysUntilExpiry, p.config.RenewBeforeDays)
	}

	return p.renewCertificate()
}

// obtainCertificate obtains a new certificate from Let's Encrypt.
func (p *LetsEncryptProvider) obtainCertificate() error {
	// Initialize ACME client
	if err := p.initACMEClient(); err != nil {
		return fmt.Errorf("failed to initialize ACME client: %w", err)
	}

	// Request certificate
	request := certificate.ObtainRequest{
		Domains: p.config.Domains,
		Bundle:  true,
	}

	p.logger.Info("obtaining certificate from Let's Encrypt",
		zap.Strings("domains", p.config.Domains),
		zap.String("directory", p.config.AcmeDirectoryUrl))

	certResource, err := p.client.Certificate.Obtain(request)
	if err != nil {
		return fmt.Errorf("failed to obtain certificate: %w", err)
	}

	// Load the certificate
	if err := p.loadCertificateResource(certResource); err != nil {
		return fmt.Errorf("failed to load certificate: %w", err)
	}

	// Cache the certificate
	if err := p.cacheCertificate(certResource); err != nil {
		p.logger.Warn("failed to cache certificate", zap.Error(err))
	}

	p.logger.Info("successfully obtained certificate", zap.Strings("domains", p.config.Domains))
	return nil
}

// renewCertificate renews the existing certificate.
func (p *LetsEncryptProvider) renewCertificate() error {
	p.mu.RLock()
	certResource := p.certResource
	p.mu.RUnlock()

	if certResource == nil {
		return fmt.Errorf("no certificate to renew")
	}

	// Initialize ACME client if not already done
	if p.client == nil {
		if err := p.initACMEClient(); err != nil {
			return fmt.Errorf("failed to initialize ACME client: %w", err)
		}
	}

	p.logger.Info("renewing certificate", zap.Strings("domains", p.config.Domains))

	// Renew certificate using RenewWithOptions (Renew is deprecated)
	newCertResource, err := p.client.Certificate.RenewWithOptions(*certResource, &certificate.RenewOptions{
		Bundle:         true,
		MustStaple:     false,
		PreferredChain: "",
	})
	if err != nil {
		return fmt.Errorf("failed to renew certificate: %w", err)
	}

	// Load the new certificate
	if err := p.loadCertificateResource(newCertResource); err != nil {
		return fmt.Errorf("failed to load renewed certificate: %w", err)
	}

	// Cache the new certificate
	if err := p.cacheCertificate(newCertResource); err != nil {
		p.logger.Warn("failed to cache renewed certificate", zap.Error(err))
	}

	p.logger.Info("successfully renewed certificate", zap.Strings("domains", p.config.Domains))
	return nil
}

// initACMEClient initializes the ACME client.
func (p *LetsEncryptProvider) initACMEClient() error {
	// Load or create user account
	user, err := p.loadOrCreateUser()
	if err != nil {
		return fmt.Errorf("failed to load/create ACME user: %w", err)
	}

	// Create ACME client config
	config := lego.NewConfig(user)
	config.CADirURL = p.config.AcmeDirectoryUrl
	config.Certificate.KeyType = certcrypto.RSA2048

	// Create client
	client, err := lego.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create ACME client: %w", err)
	}

	// Set up HTTP-01 challenge
	provider := http01.NewProviderServer("", fmt.Sprintf("%d", p.config.HttpChallengePort))
	if err := client.Challenge.SetHTTP01Provider(provider); err != nil {
		return fmt.Errorf("failed to set HTTP-01 provider: %w", err)
	}

	p.client = client
	return nil
}

// loadOrCreateUser loads an existing ACME user or creates a new one.
func (p *LetsEncryptProvider) loadOrCreateUser() (*ACMEUser, error) {
	userPath := filepath.Join(p.config.CacheDir, "user.json")

	// Try to load existing user
	if data, err := os.ReadFile(userPath); err == nil {
		var savedUser struct {
			Email        string
			Registration *registration.Resource
			PrivateKey   string
		}
		if err := json.Unmarshal(data, &savedUser); err == nil {
			// Parse private key
			block, _ := pem.Decode([]byte(savedUser.PrivateKey))
			if block != nil {
				key, err := x509.ParseECPrivateKey(block.Bytes)
				if err == nil {
					return &ACMEUser{
						Email:        savedUser.Email,
						Registration: savedUser.Registration,
						key:          key,
					}, nil
				}
			}
		}
	}

	// Create new user
	privateKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	user := &ACMEUser{
		Email: p.config.Email,
		key:   privateKey,
	}

	// Register with ACME server
	config := lego.NewConfig(user)
	config.CADirURL = p.config.AcmeDirectoryUrl
	client, err := lego.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create client for registration: %w", err)
	}

	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, fmt.Errorf("failed to register: %w", err)
	}
	user.Registration = reg

	// Save user
	keyDER, _ := x509.MarshalECPrivateKey(privateKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	savedUser := struct {
		Email        string
		Registration *registration.Resource
		PrivateKey   string
	}{
		Email:        user.Email,
		Registration: user.Registration,
		PrivateKey:   string(keyPEM),
	}

	data, _ := json.MarshalIndent(savedUser, "", "  ")
	if err := os.WriteFile(userPath, data, 0600); err != nil {
		p.logger.Warn("failed to save user", zap.Error(err))
	}

	return user, nil
}

// loadCertificateResource loads a certificate resource into memory.
func (p *LetsEncryptProvider) loadCertificateResource(certResource *certificate.Resource) error {
	// Parse TLS certificate
	tlsCert, err := tls.X509KeyPair(certResource.Certificate, certResource.PrivateKey)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Parse x509 certificate
	var x509Cert *x509.Certificate
	if len(tlsCert.Certificate) > 0 {
		x509Cert, err = x509.ParseCertificate(tlsCert.Certificate[0])
		if err != nil {
			return fmt.Errorf("failed to parse x509 certificate: %w", err)
		}
	}

	p.mu.Lock()
	p.cert = &tlsCert
	p.x509Cert = x509Cert
	p.certResource = certResource
	p.mu.Unlock()

	return nil
}

// cacheCertificate saves a certificate to disk.
func (p *LetsEncryptProvider) cacheCertificate(certResource *certificate.Resource) error {
	certPath := filepath.Join(p.config.CacheDir, "certificate.pem")
	keyPath := filepath.Join(p.config.CacheDir, "key.pem")

	if err := os.WriteFile(certPath, certResource.Certificate, 0600); err != nil {
		return fmt.Errorf("failed to write certificate: %w", err)
	}

	if err := os.WriteFile(keyPath, certResource.PrivateKey, 0600); err != nil {
		return fmt.Errorf("failed to write key: %w", err)
	}

	// Also save the full resource
	resourcePath := filepath.Join(p.config.CacheDir, "resource.json")
	data, _ := json.MarshalIndent(certResource, "", "  ")
	_ = os.WriteFile(resourcePath, data, 0600)

	return nil
}

// loadCachedCertificate loads a certificate from disk cache.
func (p *LetsEncryptProvider) loadCachedCertificate() error {
	resourcePath := filepath.Join(p.config.CacheDir, "resource.json")

	data, err := os.ReadFile(resourcePath)
	if err != nil {
		return fmt.Errorf("failed to read cached certificate: %w", err)
	}

	var certResource certificate.Resource
	if err := json.Unmarshal(data, &certResource); err != nil {
		return fmt.Errorf("failed to parse cached certificate: %w", err)
	}

	return p.loadCertificateResource(&certResource)
}

// renewalLoop runs the automatic renewal background task.
func (p *LetsEncryptProvider) renewalLoop() {
	for {
		select {
		case <-p.renewalTicker.C:
			p.mu.RLock()
			daysUntilExpiry := int32(time.Until(p.x509Cert.NotAfter).Hours() / 24)
			p.mu.RUnlock()

			if daysUntilExpiry <= p.config.RenewBeforeDays {
				p.logger.Info("certificate due for renewal",
					zap.Int32("days_until_expiry", daysUntilExpiry),
					zap.Int32("threshold", p.config.RenewBeforeDays))

				if err := p.renewCertificate(); err != nil {
					p.logger.Error("automatic renewal failed", zap.Error(err))
				}
			}

		case <-p.stopChan:
			return
		}
	}
}
