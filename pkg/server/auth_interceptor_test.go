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

package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

const (
	testIssuer   = "https://test.supabase.co/auth/v1"
	testAudience = "authenticated"
)

func testHS256Authenticator(t *testing.T, secret []byte) *Authenticator {
	t.Helper()
	a, err := NewAuthenticator(context.Background(), AuthConfig{
		Required:    true,
		HS256Secret: secret,
		Audience:    testAudience,
		Issuer:      testIssuer,
	})
	require.NoError(t, err)
	return a
}

func mintHS256(t *testing.T, secret []byte, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	require.NoError(t, err)
	return signed
}

func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"sub": "user-123",
		"aud": testAudience,
		"iss": testIssuer,
		"exp": time.Now().Add(time.Hour).Unix(),
	}
}

func TestValidateToken_HS256(t *testing.T) {
	secret := []byte("super-secret-key")
	otherSecret := []byte("a-different-secret")
	a := testHS256Authenticator(t, secret)

	tests := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"valid", mintHS256(t, secret, validClaims()), false},
		{"bad signature", mintHS256(t, otherSecret, validClaims()), true},
		{"expired beyond leeway", mintHS256(t, secret, jwt.MapClaims{
			"sub": "u", "aud": testAudience, "iss": testIssuer,
			"exp": time.Now().Add(-2 * time.Minute).Unix(),
		}), true},
		{"within leeway", mintHS256(t, secret, jwt.MapClaims{
			"sub": "u", "aud": testAudience, "iss": testIssuer,
			"exp": time.Now().Add(-30 * time.Second).Unix(), // <60s leeway
		}), false},
		{"wrong audience", mintHS256(t, secret, jwt.MapClaims{
			"sub": "u", "aud": "anon", "iss": testIssuer,
			"exp": time.Now().Add(time.Hour).Unix(),
		}), true},
		{"wrong issuer", mintHS256(t, secret, jwt.MapClaims{
			"sub": "u", "aud": testAudience, "iss": "https://evil.example/auth/v1",
			"exp": time.Now().Add(time.Hour).Unix(),
		}), true},
		{"missing exp", mintHS256(t, secret, jwt.MapClaims{
			"sub": "u", "aud": testAudience, "iss": testIssuer,
		}), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims, err := a.ValidateToken(tt.token)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, claims["sub"])
			}
		})
	}
}

func TestValidateToken_RejectsAlgNone(t *testing.T) {
	a := testHS256Authenticator(t, []byte("secret"))
	// A token with alg:none must be rejected by the algorithm allowlist.
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims())
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	_, err = a.ValidateToken(signed)
	assert.Error(t, err, "alg:none must be rejected")
}

func TestValidateToken_RS256_ViaJWKS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	const kid = "test-rsa-key"
	jwksJSON := buildRSAJWKS(t, &key.PublicKey, kid)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksJSON)
	}))
	defer ts.Close()

	a, err := NewAuthenticator(context.Background(), AuthConfig{
		Required: true,
		JWKSURL:  ts.URL,
		Audience: testAudience,
		Issuer:   testIssuer,
	})
	require.NoError(t, err)

	// Valid RS256 token signed with the matching kid.
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, validClaims())
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	require.NoError(t, err)

	claims, err := a.ValidateToken(signed)
	require.NoError(t, err)
	assert.Equal(t, "user-123", claims["sub"])

	// A token signed by a different RSA key must fail (no matching JWKS entry).
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	badTok := jwt.NewWithClaims(jwt.SigningMethodRS256, validClaims())
	badTok.Header["kid"] = kid
	badSigned, err := badTok.SignedString(otherKey)
	require.NoError(t, err)
	_, err = a.ValidateToken(badSigned)
	assert.Error(t, err, "token signed by a non-JWKS key must be rejected")
}

// buildRSAJWKS hand-builds a minimal JWK Set JSON for an RSA public key.
func buildRSAJWKS(t *testing.T, pub *rsa.PublicKey, kid string) []byte {
	t.Helper()
	jwk := map[string]any{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
	out, err := json.Marshal(map[string]any{"keys": []any{jwk}})
	require.NoError(t, err)
	return out
}

func TestNewAuthenticator_RequiresVerificationSource(t *testing.T) {
	_, err := NewAuthenticator(context.Background(), AuthConfig{Required: true})
	assert.Error(t, err, "must require an HS256 secret or JWKS URL")
}

func TestAuthenticator_UnaryInterceptor(t *testing.T) {
	secret := []byte("secret")
	a := testHS256Authenticator(t, secret)
	token := mintHS256(t, secret, validClaims())

	var capturedUserID string
	handler := func(ctx context.Context, _ any) (any, error) {
		capturedUserID = postgres.UserIDFromContext(ctx)
		return "ok", nil
	}
	interceptor := a.UnaryServerInterceptor()
	info := &grpc.UnaryServerInfo{}

	t.Run("valid token sets user id from sub", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer "+token))
		_, err := interceptor(ctx, nil, info, handler)
		require.NoError(t, err)
		assert.Equal(t, "user-123", capturedUserID)
	})

	t.Run("missing token is Unauthenticated when required", func(t *testing.T) {
		_, err := interceptor(context.Background(), nil, info, handler)
		require.Error(t, err)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("invalid token is Unauthenticated", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer not-a-jwt"))
		_, err := interceptor(ctx, nil, info, handler)
		require.Error(t, err)
		assert.Equal(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestAuthenticator_OptionalModePassesThroughWhenMissing(t *testing.T) {
	secret := []byte("secret")
	a, err := NewAuthenticator(context.Background(), AuthConfig{
		Required:    false, // optional mode
		HS256Secret: secret,
		Audience:    testAudience,
		Issuer:      testIssuer,
	})
	require.NoError(t, err)

	called := false
	handler := func(ctx context.Context, _ any) (any, error) {
		called = true
		// No token => no auth-derived identity; legacy interceptor would apply a default.
		assert.Empty(t, postgres.UserIDFromContext(ctx))
		return "ok", nil
	}
	_, err = a.UnaryServerInterceptor()(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)
	require.NoError(t, err)
	assert.True(t, called, "optional mode must invoke the handler when no token is present")

	// But a present-but-invalid token is still rejected, even in optional mode.
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer garbage"))
	_, err = a.UnaryServerInterceptor()(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestParseBearer(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":   "abc",
		"bearer abc":   "abc",
		"BEARER  abc ": "abc",
		"abc":          "",
		"":             "",
		"Basic abc":    "",
	}
	for in, want := range cases {
		assert.Equal(t, want, parseBearer(in), "parseBearer(%q)", in)
	}
}
