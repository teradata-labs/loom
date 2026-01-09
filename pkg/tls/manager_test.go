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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestNewManager_SelfSigned(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "self-signed",
		SelfSigned: &loomv1.SelfSignedConfig{
			Hostnames:    []string{"localhost"},
			IpAddresses:  []string{"127.0.0.1"},
			ValidityDays: 365,
			Organization: "Test Org",
		},
	}

	manager, err := NewManager(config)
	require.NoError(t, err)
	require.NotNil(t, manager)

	// Verify manager has a provider
	assert.NotNil(t, manager.provider)
	assert.NotNil(t, manager.config)
}

func TestNewManager_Manual(t *testing.T) {
	// Create test certificates
	tempDir := t.TempDir()
	certPath, keyPath := createTestCertificate(t, tempDir)

	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "manual",
		Manual: &loomv1.ManualTLSConfig{
			CertFile: certPath,
			KeyFile:  keyPath,
		},
	}

	manager, err := NewManager(config)
	require.NoError(t, err)
	require.NotNil(t, manager)
	assert.NotNil(t, manager.provider)
}

func TestNewManager_LetsEncrypt(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "letsencrypt",
		Letsencrypt: &loomv1.LetsEncryptConfig{
			Domains:           []string{"example.com"},
			Email:             "test@example.com",
			AcmeDirectoryUrl:  "https://acme-staging-v02.api.letsencrypt.org/directory",
			HttpChallengePort: 80,
			CacheDir:          t.TempDir(),
			AutoRenew:         true,
			RenewBeforeDays:   30,
			AcceptTos:         true,
		},
	}

	manager, err := NewManager(config)
	require.NoError(t, err)
	require.NotNil(t, manager)
	assert.NotNil(t, manager.provider)
}

func TestNewManager_DisabledTLS(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: false,
	}

	manager, err := NewManager(config)
	assert.Error(t, err)
	assert.Nil(t, manager)
	assert.Contains(t, err.Error(), "not enabled")
}

func TestNewManager_InvalidMode(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "invalid-mode",
	}

	manager, err := NewManager(config)
	assert.Error(t, err)
	assert.Nil(t, manager)
	assert.Contains(t, err.Error(), "unknown TLS mode")
}

func TestNewManager_NilConfig(t *testing.T) {
	manager, err := NewManager(nil)
	assert.Error(t, err)
	assert.Nil(t, manager)
}

func TestManager_GetCertificate(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "self-signed",
		SelfSigned: &loomv1.SelfSignedConfig{
			Hostnames:    []string{"localhost"},
			ValidityDays: 365,
		},
	}

	manager, err := NewManager(config)
	require.NoError(t, err)

	// Get TLS config and call GetCertificate
	tlsConfig := manager.TLSConfig()
	require.NotNil(t, tlsConfig.GetCertificate)

	cert, err := tlsConfig.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, cert)
}

func TestManager_Start(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "self-signed",
		SelfSigned: &loomv1.SelfSignedConfig{
			Hostnames:    []string{"localhost"},
			ValidityDays: 365,
		},
	}

	manager, err := NewManager(config)
	require.NoError(t, err)

	ctx := context.Background()
	err = manager.Start(ctx)
	assert.NoError(t, err)

	// Start should be idempotent
	err = manager.Start(ctx)
	assert.NoError(t, err)
}

func TestManager_Stop(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "self-signed",
		SelfSigned: &loomv1.SelfSignedConfig{
			Hostnames:    []string{"localhost"},
			ValidityDays: 365,
		},
	}

	manager, err := NewManager(config)
	require.NoError(t, err)

	ctx := context.Background()
	err = manager.Start(ctx)
	require.NoError(t, err)

	err = manager.Stop(ctx)
	assert.NoError(t, err)

	// Stop should be idempotent
	err = manager.Stop(ctx)
	assert.NoError(t, err)
}

func TestManager_Status(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "self-signed",
		SelfSigned: &loomv1.SelfSignedConfig{
			Hostnames:    []string{"localhost", "test.local"},
			IpAddresses:  []string{"127.0.0.1"},
			ValidityDays: 100,
			Organization: "Status Test",
		},
	}

	manager, err := NewManager(config)
	require.NoError(t, err)

	ctx := context.Background()
	status, err := manager.Status(ctx)
	require.NoError(t, err)
	require.NotNil(t, status)

	// Verify status matches config
	assert.True(t, status.Enabled)
	assert.Equal(t, "self-signed", status.Mode)
	assert.NotNil(t, status.Certificate)
	assert.Contains(t, status.Certificate.Domains, "localhost")
	assert.True(t, status.Certificate.Valid)
}

func TestManager_Renew(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "self-signed",
		SelfSigned: &loomv1.SelfSignedConfig{
			Hostnames:    []string{"localhost"},
			ValidityDays: 365,
		},
	}

	manager, err := NewManager(config)
	require.NoError(t, err)

	ctx := context.Background()
	tlsConfig := manager.TLSConfig()

	// Get original certificate
	origCert, err := tlsConfig.GetCertificate(nil)
	require.NoError(t, err)

	// Renew certificate
	err = manager.Renew(ctx, true)
	require.NoError(t, err)

	// Get new certificate
	newCert, err := tlsConfig.GetCertificate(nil)
	require.NoError(t, err)

	// Certificates should be different after renewal
	assert.NotEqual(t, origCert, newCert)
}

func TestManager_TLSConfig(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "self-signed",
		SelfSigned: &loomv1.SelfSignedConfig{
			Hostnames:    []string{"localhost"},
			ValidityDays: 365,
		},
	}

	manager, err := NewManager(config)
	require.NoError(t, err)

	tlsConfig := manager.TLSConfig()
	require.NotNil(t, tlsConfig)

	// Verify TLS config properties
	assert.NotNil(t, tlsConfig.GetCertificate)
	assert.Equal(t, uint16(0x0303), tlsConfig.MinVersion) // TLS 1.2
}

func TestManager_Lifecycle(t *testing.T) {
	config := &loomv1.TLSConfig{
		Enabled: true,
		Mode:    "self-signed",
		SelfSigned: &loomv1.SelfSignedConfig{
			Hostnames:    []string{"localhost"},
			ValidityDays: 365,
		},
	}

	manager, err := NewManager(config)
	require.NoError(t, err)

	ctx := context.Background()
	tlsConfig := manager.TLSConfig()

	// Full lifecycle: Start -> Get Cert -> Renew -> Stop
	err = manager.Start(ctx)
	require.NoError(t, err)

	cert, err := tlsConfig.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, cert)

	status, err := manager.Status(ctx)
	require.NoError(t, err)
	require.True(t, status.Enabled)

	err = manager.Renew(ctx, true)
	require.NoError(t, err)

	err = manager.Stop(ctx)
	require.NoError(t, err)
}
