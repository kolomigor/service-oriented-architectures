package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/kolomigor/marketplace-homework2/internal/api"
)

const tokenIssuer = "market-homework2"

type tokenClaims struct {
	Role      string `json:"role"`
	TokenType string `json:"token_type"`
	jwt.RegisteredClaims
}

func (s *Service) Register(ctx context.Context, input api.RegisterRequest) (api.UserResponse, error) {
	if err := validatePassword(input.Password); err != nil {
		return api.UserResponse{}, err
	}
	role := "USER"
	if input.Role != nil {
		role = string(*input.Role)
	}
	if role != "USER" && role != "SELLER" {
		return api.UserResponse{}, Fail(http.StatusBadRequest, "VALIDATION_ERROR", "Registration supports USER and SELLER roles", map[string]interface{}{"fields": []map[string]string{{"field": "role", "message": "Allowed values: USER, SELLER"}}})
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return api.UserResponse{}, fmt.Errorf("hash password: %w", err)
	}
	user := api.UserResponse{Id: uuid.NewString(), Email: normalizeEmail(input.Email), Role: api.UserRole(role)}
	err = s.DB.QueryRow(ctx, `INSERT INTO users (id, email, password_hash, role)
		VALUES ($1, $2, $3, $4) RETURNING created_at`, user.Id, user.Email, string(passwordHash), role).Scan(&user.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return api.UserResponse{}, Fail(http.StatusConflict, "USER_ALREADY_EXISTS", "A user with this email already exists", nil)
		}
		return api.UserResponse{}, fmt.Errorf("register user: %w", err)
	}
	return user, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validatePassword(password string) error {
	if len(password) > 72 {
		return Fail(http.StatusBadRequest, "VALIDATION_ERROR", "Password is too long", map[string]interface{}{"fields": []map[string]string{{"field": "password", "message": "Password must not exceed 72 bytes"}}})
	}
	return nil
}

func (s *Service) Login(ctx context.Context, input api.LoginRequest) (api.TokenPair, error) {
	if err := validatePassword(input.Password); err != nil {
		return api.TokenPair{}, err
	}
	var actor Actor
	var passwordHash string
	err := s.DB.QueryRow(ctx, `SELECT id::text, role, password_hash FROM users WHERE email = $1`, normalizeEmail(input.Email)).Scan(&actor.ID, &actor.Role, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.TokenPair{}, invalidCredentials()
	}
	if err != nil {
		return api.TokenPair{}, fmt.Errorf("find login user: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil {
		return api.TokenPair{}, invalidCredentials()
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return api.TokenPair{}, fmt.Errorf("begin login: %w", err)
	}
	defer tx.Rollback(ctx)
	pair, err := s.issueTokenPair(ctx, tx, actor)
	if err != nil {
		return api.TokenPair{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return api.TokenPair{}, fmt.Errorf("commit login: %w", err)
	}
	return pair, nil
}

func invalidCredentials() error {
	return Fail(http.StatusUnauthorized, "AUTH_INVALID_CREDENTIALS", "Invalid email or password", nil)
}

// Refresh tokens are single-use: row locking makes concurrent refreshes serialize,
// and revocation and issuing the replacement share the same transaction.
func (s *Service) Refresh(ctx context.Context, input api.RefreshRequest) (api.TokenPair, error) {
	claims, err := s.parseToken(input.RefreshToken, "refresh")
	if err != nil {
		return api.TokenPair{}, invalidRefreshToken()
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return api.TokenPair{}, fmt.Errorf("begin token refresh: %w", err)
	}
	defer tx.Rollback(ctx)
	var storedUserID string
	var expiresAt time.Time
	var revokedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text, expires_at, revoked_at FROM refresh_tokens
		WHERE id = $1 AND token_hash = $2 FOR UPDATE`, claims.ID, hashToken(input.RefreshToken)).Scan(&storedUserID, &expiresAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.TokenPair{}, invalidRefreshToken()
	}
	if err != nil {
		return api.TokenPair{}, fmt.Errorf("find refresh token: %w", err)
	}
	if storedUserID != claims.Subject || revokedAt != nil || !expiresAt.After(time.Now()) {
		return api.TokenPair{}, invalidRefreshToken()
	}
	actor := Actor{ID: storedUserID}
	err = tx.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, actor.ID).Scan(&actor.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.TokenPair{}, invalidRefreshToken()
	}
	if err != nil {
		return api.TokenPair{}, fmt.Errorf("find refresh user: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1`, claims.ID); err != nil {
		return api.TokenPair{}, fmt.Errorf("revoke refresh token: %w", err)
	}
	pair, err := s.issueTokenPair(ctx, tx, actor)
	if err != nil {
		return api.TokenPair{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return api.TokenPair{}, fmt.Errorf("commit token refresh: %w", err)
	}
	return pair, nil
}

func invalidRefreshToken() error {
	return Fail(http.StatusUnauthorized, "REFRESH_TOKEN_INVALID", "Refresh token is invalid, expired or already used", nil)
}

func (s *Service) Authenticate(ctx context.Context, token string) (Actor, error) {
	claims, err := s.parseToken(token, "access")
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) && !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			return Actor{}, Fail(http.StatusUnauthorized, "TOKEN_EXPIRED", "Access token has expired", nil)
		}
		return Actor{}, Fail(http.StatusUnauthorized, "TOKEN_INVALID", "Access token is invalid", nil)
	}
	actor := Actor{ID: claims.Subject}
	err = s.DB.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, actor.ID).Scan(&actor.Role)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !validRole(actor.Role)) {
		return Actor{}, Fail(http.StatusUnauthorized, "TOKEN_INVALID", "Access token user is no longer valid", nil)
	}
	if err != nil {
		return Actor{}, fmt.Errorf("find access token user: %w", err)
	}
	return actor, nil
}

func (s *Service) parseToken(raw, expectedType string) (*tokenClaims, error) {
	claims := new(tokenClaims)
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (interface{}, error) {
		return s.JWTSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(tokenIssuer), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	if !token.Valid || claims.TokenType != expectedType || !validRole(claims.Role) {
		return nil, errors.New("invalid token claims")
	}
	if _, err := uuid.Parse(claims.Subject); err != nil {
		return nil, errors.New("invalid token subject")
	}
	if _, err := uuid.Parse(claims.ID); err != nil {
		return nil, errors.New("invalid token ID")
	}
	return claims, nil
}

func validRole(role string) bool {
	return role == "USER" || role == "SELLER" || role == "ADMIN"
}

func (s *Service) issueTokenPair(ctx context.Context, tx pgx.Tx, actor Actor) (api.TokenPair, error) {
	now := time.Now().UTC()
	accessToken, _, _, err := s.signToken(actor, "access", now, s.AccessTTL)
	if err != nil {
		return api.TokenPair{}, fmt.Errorf("sign access token: %w", err)
	}
	refreshToken, refreshID, refreshExpiration, err := s.signToken(actor, "refresh", now, s.RefreshTTL)
	if err != nil {
		return api.TokenPair{}, fmt.Errorf("sign refresh token: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)`, refreshID, actor.ID, hashToken(refreshToken), refreshExpiration); err != nil {
		return api.TokenPair{}, fmt.Errorf("store refresh token: %w", err)
	}
	return api.TokenPair{AccessToken: accessToken, RefreshToken: refreshToken, TokenType: "Bearer", ExpiresIn: int(s.AccessTTL.Seconds())}, nil
}

func (s *Service) signToken(actor Actor, tokenType string, now time.Time, ttl time.Duration) (string, string, time.Time, error) {
	id := uuid.NewString()
	expiresAt := now.Add(ttl).Truncate(time.Second)
	claims := tokenClaims{
		Role: actor.Role, TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: tokenIssuer, Subject: actor.ID, ID: id,
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.JWTSecret)
	return raw, id, expiresAt, err
}

func hashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
