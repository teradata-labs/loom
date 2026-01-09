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
package tls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// createTestCertificate creates a test certificate and key for testing
func createTestCertificate(t *testing.T, dir string) (certPath, keyPath string) {
	// Generate private key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	// Create certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Test Org"},
			CommonName:   "localhost",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", "test.local"},
	}

	// Create certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)

	// Write certificate file
	certPath = filepath.Join(dir, "test.crt")
	certFile, err := os.Create(certPath)
	require.NoError(t, err)
	defer certFile.Close()
	err = pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	require.NoError(t, err)

	// Write key file
	keyPath = filepath.Join(dir, "test.key")
	keyFile, err := os.Create(keyPath)
	require.NoError(t, err)
	defer keyFile.Close()
	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	require.NoError(t, err)
	err = pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	require.NoError(t, err)

	return certPath, keyPath
}

func TestManualProvider_LoadCertificate(t *testing.T) {
	// Create temporary directory for test certificates
	tempDir := t.TempDir()
	certPath, keyPath := createTestCertificate(t, tempDir)

	config := &loomv1.ManualTLSConfig{
		CertFile: certPath,
		KeyFile:  keyPath,
	}

	provider, err := NewManualProvider(config)
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Verify certificate was loaded
	assert.NotNil(t, provider.cert)
	assert.NotNil(t, provider.x509Cert)
}

func TestManualProvider_GetCertificate(t *testing.T) {
	tempDir := t.TempDir()
	certPath, keyPath := createTestCertificate(t, tempDir)

	config := &loomv1.ManualTLSConfig{
		CertFile: certPath,
		KeyFile:  keyPath,
	}

	provider, err := NewManualProvider(config)
	require.NoError(t, err)

	// GetCertificate should return the loaded cert
	cert, err := provider.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, cert)
	assert.Equal(t, provider.cert, cert)
}

func TestManualProvider_Status(t *testing.T) {
	tempDir := t.TempDir()
	certPath, keyPath := createTestCertificate(t, tempDir)

	config := &loomv1.ManualTLSConfig{
		CertFile: certPath,
		KeyFile:  keyPath,
	}

	provider, err := NewManualProvider(config)
	require.NoError(t, err)

	ctx := context.Background()
	status, err := provider.Status(ctx)
	require.NoError(t, err)
	require.NotNil(t, status)

	// Verify status fields
	assert.True(t, status.Enabled)
	assert.Equal(t, "manual", status.Mode)
	assert.NotNil(t, status.Certificate)
	assert.Contains(t, status.Certificate.Domains, "localhost")
	assert.Contains(t, status.Certificate.Domains, "test.local")
	assert.True(t, status.Certificate.Valid)
}

func TestManualProvider_Lifecycle(t *testing.T) {
	tempDir := t.TempDir()
	certPath, keyPath := createTestCertificate(t, tempDir)

	config := &loomv1.ManualTLSConfig{
		CertFile: certPath,
		KeyFile:  keyPath,
	}

	provider, err := NewManualProvider(config)
	require.NoError(t, err)

	ctx := context.Background()

	// Start should be no-op for manual
	err = provider.Start(ctx)
	assert.NoError(t, err)

	// Stop should be no-op for manual
	err = provider.Stop(ctx)
	assert.NoError(t, err)
}

func TestManualProvider_RenewNotSupported(t *testing.T) {
	tempDir := t.TempDir()
	certPath, keyPath := createTestCertificate(t, tempDir)

	config := &loomv1.ManualTLSConfig{
		CertFile: certPath,
		KeyFile:  keyPath,
	}

	provider, err := NewManualProvider(config)
	require.NoError(t, err)

	ctx := context.Background()

	// Renew should return error for manual provider
	err = provider.Renew(ctx, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be renewed automatically")
}

func TestManualProvider_InvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *loomv1.ManualTLSConfig
	}{
		{
			name:   "nil config",
			config: nil,
		},
		{
			name: "empty cert file",
			config: &loomv1.ManualTLSConfig{
				CertFile: "",
				KeyFile:  "/path/to/key.pem",
			},
		},
		{
			name: "empty key file",
			config: &loomv1.ManualTLSConfig{
				CertFile: "/path/to/cert.pem",
				KeyFile:  "",
			},
		},
		{
			name: "nonexistent cert file",
			config: &loomv1.ManualTLSConfig{
				CertFile: "/nonexistent/cert.pem",
				KeyFile:  "/nonexistent/key.pem",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewManualProvider(tt.config)
			assert.Error(t, err)
			assert.Nil(t, provider)
		})
	}
}

func TestManualProvider_WithCA(t *testing.T) {
	tempDir := t.TempDir()
	certPath, keyPath := createTestCertificate(t, tempDir)

	// Create CA certificate (just reuse the test cert for simplicity)
	caPath := filepath.Join(tempDir, "ca.crt")
	err := os.WriteFile(caPath, []byte{}, 0644) // Empty CA file (just for path validation)
	require.NoError(t, err)

	config := &loomv1.ManualTLSConfig{
		CertFile: certPath,
		KeyFile:  keyPath,
		CaFile:   caPath,
	}

	provider, err := NewManualProvider(config)
	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.Equal(t, caPath, provider.config.CaFile)
}
