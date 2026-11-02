package service

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Service contains business operations; API DTOs are generated from OpenAPI.
type Service struct {
	DB            *pgxpool.Pool
	OrderInterval time.Duration
	JWTSecret     []byte
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
}

// Actor comes from a verified access token and the current database user.
type Actor struct {
	ID   string
	Role string
}

type AppError struct {
	Status  int
	Code    string
	Message string
	Details map[string]interface{}
}

func (e *AppError) Error() string { return e.Code + ": " + e.Message }

func Fail(status int, code, message string, details map[string]interface{}) error {
	return &AppError{Status: status, Code: code, Message: message, Details: details}
}
