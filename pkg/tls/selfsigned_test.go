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
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestSelfSignedProvider_Generation(t *testing.T) {
	config := &loomv1.SelfSignedConfig{
		Hostnames:    []string{"localhost", "test.local"},
		IpAddresses:  []string{"127.0.0.1", "::1"},
		ValidityDays: 365,
		Organization: "Test Org",
	}

	provider, err := NewSelfSignedProvider(config)
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Check certificate was generated
	assert.NotNil(t, provider.cert)
	assert.NotNil(t, provider.x509Cert)
	assert.Equal(t, "Test Org", provider.x509Cert.Subject.Organization[0])
}

func TestSelfSignedProvider_GetCertificate(t *testing.T) {
	config := &loomv1.SelfSignedConfig{
		Hostnames:    []string{"localhost"},
		IpAddresses:  []string{"127.0.0.1"},
		ValidityDays: 30,
		Organization: "Test",
	}

	provider, err := NewSelfSignedProvider(config)
	require.NoError(t, err)

	// GetCertificate should return the same cert regardless of ClientHello
	cert1, err := provider.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, cert1)

	cert2, err := provider.GetCertificate(&tls.ClientHelloInfo{
		ServerName: "localhost",
	})
	require.NoError(t, err)
	require.NotNil(t, cert2)

	// Both calls should return the same cert
	assert.Equal(t, cert1, cert2)
}

func TestSelfSignedProvider_Status(t *testing.T) {
	config := &loomv1.SelfSignedConfig{
		Hostnames:    []string{"localhost", "test.local"},
		IpAddresses:  []string{"127.0.0.1"},
		ValidityDays: 100,
		Organization: "Status Test",
	}

	provider, err := NewSelfSignedProvider(config)
	require.NoError(t, err)

	ctx := context.Background()
	status, err := provider.Status(ctx)
	require.NoError(t, err)
	require.NotNil(t, status)

	// Verify status fields
	assert.True(t, status.Enabled)
	assert.Equal(t, "self-signed", status.Mode)
	assert.NotNil(t, status.Certificate)
	assert.Equal(t, "Self-Signed", status.Certificate.Issuer)
	assert.Contains(t, status.Certificate.Domains, "localhost")
	assert.Contains(t, status.Certificate.Domains, "test.local")
	assert.True(t, status.Certificate.Valid)
	assert.Greater(t, status.Certificate.DaysUntilExpiry, int32(95)) // Should be ~100 days
}

func TestSelfSignedProvider_Lifecycle(t *testing.T) {
	config := &loomv1.SelfSignedConfig{
		Hostnames:    []string{"localhost"},
		IpAddresses:  []string{"127.0.0.1"},
		ValidityDays: 365,
		Organization: "Lifecycle Test",
	}

	provider, err := NewSelfSignedProvider(config)
	require.NoError(t, err)

	ctx := context.Background()

	// Start should be no-op for self-signed
	err = provider.Start(ctx)
	assert.NoError(t, err)

	// Stop should be no-op for self-signed
	err = provider.Stop(ctx)
	assert.NoError(t, err)
}

func TestSelfSignedProvider_Renew(t *testing.T) {
	config := &loomv1.SelfSignedConfig{
		Hostnames:    []string{"localhost"},
		IpAddresses:  []string{"127.0.0.1"},
		ValidityDays: 30,
		Organization: "Renew Test",
	}

	provider, err := NewSelfSignedProvider(config)
	require.NoError(t, err)

	ctx := context.Background()

	// Get original cert
	origCert, err := provider.GetCertificate(nil)
	require.NoError(t, err)
	origSerial := provider.x509Cert.SerialNumber

	// Renew certificate
	err = provider.Renew(ctx, true)
	require.NoError(t, err)

	// Get new cert
	newCert, err := provider.GetCertificate(nil)
	require.NoError(t, err)

	// Should be a different certificate (different serial number)
	assert.NotEqual(t, origSerial, provider.x509Cert.SerialNumber)
	assert.NotEqual(t, origCert, newCert)
}

func TestSelfSignedProvider_DefaultHostnames(t *testing.T) {
	// Test with empty hostnames
	config := &loomv1.SelfSignedConfig{
		Hostnames:    []string{},
		IpAddresses:  []string{},
		ValidityDays: 365,
		Organization: "Default Test",
	}

	provider, err := NewSelfSignedProvider(config)
	require.NoError(t, err)

	// Should default to localhost
	ctx := context.Background()
	status, err := provider.Status(ctx)
	require.NoError(t, err)
	assert.Contains(t, status.Certificate.Domains, "localhost")
}

func TestSelfSignedProvider_InvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *loomv1.SelfSignedConfig
	}{
		{
			name:   "nil config",
			config: nil,
		},
		{
			name: "zero validity",
			config: &loomv1.SelfSignedConfig{
				Hostnames:    []string{"localhost"},
				ValidityDays: 0,
			},
		},
		{
			name: "negative validity",
			config: &loomv1.SelfSignedConfig{
				Hostnames:    []string{"localhost"},
				ValidityDays: -10,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewSelfSignedProvider(tt.config)
			assert.Error(t, err)
			assert.Nil(t, provider)
		})
	}
}
