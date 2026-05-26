// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package factory

import (
	"net/http"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
)

func TestNewProviderFactory_DoesNotForceDefaultTimeout(t *testing.T) {
	f := NewProviderFactory(FactoryConfig{})
	assert.Equal(t, 0, f.config.Timeout)
}

func TestCreateOllamaProvider_ExplicitTimeoutIsHonored(t *testing.T) {
	f := NewProviderFactory(FactoryConfig{Timeout: 1, TimeoutExplicit: true})
	raw, err := f.createOllamaProvider("llama3.1")
	require.NoError(t, err)
	client, ok := raw.(*ollama.Client)
	require.True(t, ok)

	transport := requireOllamaTransport(t, client)
	assert.Equal(t, 1*time.Second, transport.ResponseHeaderTimeout)
}

func TestCreateOllamaProvider_NonExplicitTimeoutUsesDefault(t *testing.T) {
	f := NewProviderFactory(FactoryConfig{Timeout: 1, TimeoutExplicit: false})
	raw, err := f.createOllamaProvider("llama3.1")
	require.NoError(t, err)
	client, ok := raw.(*ollama.Client)
	require.True(t, ok)

	transport := requireOllamaTransport(t, client)
	assert.Equal(t, 300*time.Second, transport.ResponseHeaderTimeout)
}

func requireOllamaTransport(t *testing.T, client *ollama.Client) *http.Transport {
	rv := reflect.ValueOf(client).Elem()
	httpClientField := rv.FieldByName("httpClient")
	require.True(t, httpClientField.IsValid(), "expected ollama.Client to have httpClient field")

	httpClientPtr := (**http.Client)(unsafe.Pointer(httpClientField.UnsafeAddr()))
	httpClient := *httpClientPtr
	require.NotNil(t, httpClient.Transport)

	transport, ok := httpClient.Transport.(*http.Transport)
	require.True(t, ok, "expected http.Transport on ollama client")
	return transport
}
