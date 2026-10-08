package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/kolomigor/marketplace-homework2/internal/api"
)

func TestAccessTokenRejectsRefreshToken(t *testing.T) {
	s := &Service{JWTSecret: []byte("test-secret-at-least-thirty-two-bytes")}
	raw, _, _, err := s.signToken(Actor{ID: uuid.NewString(), Role: "USER"}, "refresh", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Authenticate(context.Background(), raw)
	assertAuthError(t, err, "TOKEN_INVALID")
}

func TestAccessTokenRejectsExpiredAndTampered(t *testing.T) {
	s := &Service{JWTSecret: []byte("test-secret-at-least-thirty-two-bytes")}
	actor := Actor{ID: uuid.NewString(), Role: "USER"}
	expired, _, _, err := s.signToken(actor, "access", time.Now().Add(-time.Hour), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Authenticate(context.Background(), expired)
	assertAuthError(t, err, "TOKEN_EXPIRED")
	attacker := &Service{JWTSecret: []byte("another-secret-at-least-thirty-two-bytes")}
	tampered, _, _, err := attacker.signToken(actor, "access", time.Now().Add(-time.Hour), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Authenticate(context.Background(), tampered)
	assertAuthError(t, err, "TOKEN_INVALID")
}

func TestTokenValidationRejectsIncompleteClaimsAndUnexpectedAlgorithm(t *testing.T) {
	s := &Service{JWTSecret: []byte("test-secret-at-least-thirty-two-bytes")}
	now := time.Now()
	base := tokenClaims{Role: "USER", TokenType: "access", RegisteredClaims: jwt.RegisteredClaims{
		Issuer: tokenIssuer, Subject: uuid.NewString(), ID: uuid.NewString(),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now),
	}}
	cases := []struct {
		name   string
		mutate func(*tokenClaims)
		method jwt.SigningMethod
	}{
		{"missing expiration", func(c *tokenClaims) { c.ExpiresAt = nil }, jwt.SigningMethodHS256},
		{"wrong issuer", func(c *tokenClaims) { c.Issuer = "another-service" }, jwt.SigningMethodHS256},
		{"invalid subject", func(c *tokenClaims) { c.Subject = "not-a-uuid" }, jwt.SigningMethodHS256},
		{"invalid token ID", func(c *tokenClaims) { c.ID = "not-a-uuid" }, jwt.SigningMethodHS256},
		{"invalid role", func(c *tokenClaims) { c.Role = "SUPERADMIN" }, jwt.SigningMethodHS256},
		{"wrong token type", func(c *tokenClaims) { c.TokenType = "refresh" }, jwt.SigningMethodHS256},
		{"unexpected algorithm", func(c *tokenClaims) {}, jwt.SigningMethodHS384},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := base
			tc.mutate(&claims)
			raw, err := jwt.NewWithClaims(tc.method, claims).SignedString(s.JWTSecret)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.parseToken(raw, "access"); err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
	valid, err := jwt.NewWithClaims(jwt.SigningMethodHS256, base).SignedString(s.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.parseToken(valid, "access")
	if err != nil || claims.Subject != base.Subject || claims.Role != base.Role {
		t.Fatalf("valid token rejected or claims changed: %v", err)
	}
}

func TestRefreshRejectsAccessTokenBeforeDatabase(t *testing.T) {
	s := &Service{JWTSecret: []byte("test-secret-at-least-thirty-two-bytes")}
	raw, _, _, err := s.signToken(Actor{ID: uuid.NewString(), Role: "USER"}, "access", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Refresh(context.Background(), api.RefreshRequest{RefreshToken: raw})
	assertAuthError(t, err, "REFRESH_TOKEN_INVALID")
}

func TestPasswordLimitCountsBytes(t *testing.T) {
	if err := validatePassword(strings.Repeat("я", 36)); err != nil {
		t.Fatalf("72-byte password rejected: %v", err)
	}
	err := validatePassword(strings.Repeat("я", 37))
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Status != http.StatusBadRequest || appErr.Code != "VALIDATION_ERROR" {
		t.Fatalf("overlong Unicode password should fail validation: %v", err)
	}
}

func assertAuthError(t *testing.T, err error, code string) {
	t.Helper()
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Status != http.StatusUnauthorized || appErr.Code != code {
		t.Fatalf("expected 401 %s, got %v", code, err)
	}
}
