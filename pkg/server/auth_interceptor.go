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
	"fmt"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/teradata-labs/loom/pkg/backends/supabase"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

// authMetadataKey is the gRPC metadata key carrying the bearer token.
const authMetadataKey = "authorization"

// AuthConfig configures Supabase-JWT authentication. It is the server-package
// mirror of the viper config, decoupled so this package has no config dependency.
type AuthConfig struct {
	// Required rejects unauthenticated requests when true. When false ("optional"
	// mode), a missing token passes through (the legacy x-user-id path applies);
	// a present-but-invalid token is always rejected.
	Required bool
	// HS256Secret is the Supabase project JWT secret for legacy HS256 tokens.
	HS256Secret []byte
	// JWKSURL is the Supabase JWKS endpoint for asymmetric (RS256/ES256) tokens.
	JWKSURL string
	// Audience and Issuer are validated against the token's aud/iss claims.
	Audience string
	Issuer   string
	// Leeway tolerates clock skew on exp/nbf/iat (default 60s).
	Leeway time.Duration
	Logger *zap.Logger
}

// Authenticator validates Supabase-issued JWTs and injects the authenticated
// user identity into request context. It supports both legacy HS256 (shared
// secret) and asymmetric (JWKS) Supabase projects simultaneously.
type Authenticator struct {
	parser   *jwt.Parser
	keyfunc  jwt.Keyfunc
	required bool
	logger   *zap.Logger
}

// NewAuthenticator builds an Authenticator, fetching/caching the JWKS in the
// background when a JWKS URL is configured. At least one verification source
// (HS256 secret or JWKS URL) is required.
func NewAuthenticator(ctx context.Context, cfg AuthConfig) (*Authenticator, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	leeway := cfg.Leeway
	if leeway == 0 {
		leeway = 60 * time.Second
	}

	var jwksKeyfunc jwt.Keyfunc
	if cfg.JWKSURL != "" {
		k, err := keyfunc.NewDefaultCtx(ctx, []string{cfg.JWKSURL})
		if err != nil {
			return nil, fmt.Errorf("initialize JWKS from %s: %w", cfg.JWKSURL, err)
		}
		jwksKeyfunc = k.Keyfunc
	}
	if len(cfg.HS256Secret) == 0 && jwksKeyfunc == nil {
		return nil, fmt.Errorf("auth requires an HS256 secret or a JWKS URL")
	}

	// Dispatch by algorithm: HMAC -> shared secret; asymmetric -> JWKS.
	keyFn := func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); ok {
			if len(cfg.HS256Secret) == 0 {
				return nil, fmt.Errorf("HS256 token received but no shared secret configured")
			}
			return cfg.HS256Secret, nil
		}
		if jwksKeyfunc == nil {
			return nil, fmt.Errorf("asymmetric token received but no JWKS configured")
		}
		return jwksKeyfunc(t)
	}

	parserOpts := []jwt.ParserOption{
		// Algorithm allowlist blocks alg:none and RS/HS confusion attacks.
		jwt.WithValidMethods([]string{"HS256", "RS256", "ES256"}),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(leeway),
	}
	if cfg.Audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(cfg.Audience))
	}
	if cfg.Issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(cfg.Issuer))
	}

	return &Authenticator{
		parser:   jwt.NewParser(parserOpts...),
		keyfunc:  keyFn,
		required: cfg.Required,
		logger:   logger,
	}, nil
}

// ValidateToken verifies a raw JWT string and returns its claims. The signature
// is checked before any claim is trusted; aud/iss/exp and the algorithm
// allowlist are enforced by the parser.
func (a *Authenticator) ValidateToken(raw string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	if _, err := a.parser.ParseWithClaims(raw, claims, a.keyfunc); err != nil {
		return nil, err
	}
	return claims, nil
}

// authenticate reads the bearer from gRPC metadata, validates it, and returns a
// context carrying the authenticated user id (postgres.ContextWithUserID, the
// same key the legacy interceptor uses) and the raw claims (supabase.WithJWT,
// for RLS). When the token is missing and auth is optional, ctx is returned
// unchanged so the legacy x-user-id path applies.
func (a *Authenticator) authenticate(ctx context.Context) (context.Context, error) {
	raw := bearerFromContext(ctx)
	if raw == "" {
		if a.required {
			return nil, status.Error(codes.Unauthenticated, "authorization bearer token required")
		}
		return ctx, nil
	}

	claims, err := a.ValidateToken(raw)
	if err != nil {
		// Never log the token; log only the reason.
		a.logger.Debug("jwt validation failed", zap.Error(err))
		return nil, status.Error(codes.Unauthenticated, "invalid authentication token")
	}

	sub, err := claims.GetSubject()
	if err != nil || sub == "" {
		return nil, status.Error(codes.Unauthenticated, "token missing subject (sub) claim")
	}
	if err := validateUserID(sub); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid subject claim: %v", err)
	}

	ctx = postgres.ContextWithUserID(ctx, sub)
	ctx = supabase.WithJWT(ctx, map[string]interface{}(claims))
	return ctx, nil
}

// UnaryServerInterceptor authenticates unary RPCs.
func (a *Authenticator) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := a.authenticate(ctx)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamServerInterceptor authenticates streaming RPCs.
func (a *Authenticator) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := a.authenticate(ss.Context())
		if err != nil {
			return err
		}
		return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: ctx})
	}
}

// bearerFromContext extracts the token from an "authorization: Bearer <jwt>"
// gRPC metadata header (case-insensitive scheme).
func bearerFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(authMetadataKey)
	if len(vals) == 0 {
		return ""
	}
	return parseBearer(vals[0])
}

// parseBearer strips a case-insensitive "Bearer " prefix and surrounding space.
func parseBearer(header string) string {
	header = strings.TrimSpace(header)
	if len(header) >= 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}
