package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL, Address                 string
	JWTSecret                            []byte
	AccessTTL, RefreshTTL, OrderInterval time.Duration
}

func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), Address: env("HTTP_ADDR", ":8080"), JWTSecret: []byte(os.Getenv("JWT_SECRET"))}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if len(c.JWTSecret) < 32 {
		return c, fmt.Errorf("JWT_SECRET must contain at least 32 bytes")
	}
	access, err := strconv.Atoi(env("ACCESS_TOKEN_MINUTES", "20"))
	if err != nil || access < 15 || access > 30 {
		return c, fmt.Errorf("ACCESS_TOKEN_MINUTES must be 15..30")
	}
	refresh, err := strconv.Atoi(env("REFRESH_TOKEN_DAYS", "14"))
	if err != nil || refresh < 7 || refresh > 30 {
		return c, fmt.Errorf("REFRESH_TOKEN_DAYS must be 7..30")
	}
	rate, err := strconv.ParseFloat(env("ORDER_RATE_LIMIT_MINUTES", "1"), 64)
	if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 1440 {
		return c, fmt.Errorf("ORDER_RATE_LIMIT_MINUTES must be 0..1440")
	}
	c.AccessTTL, c.RefreshTTL = time.Duration(access)*time.Minute, time.Duration(refresh)*24*time.Hour
	c.OrderInterval = time.Duration(rate * float64(time.Minute))
	return c, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
