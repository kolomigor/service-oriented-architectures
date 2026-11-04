package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	flightv1 "github.com/kolomigor/flight-booking/gen/flight/v1"
	"github.com/kolomigor/flight-booking/internal/flight"
)

func main() {
	if err := run(); err != nil {
		slog.Error("flight service stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	// An empty key would let in requests with an empty key, so it is required.
	apiKey := os.Getenv("FLIGHT_API_KEY")
	if apiKey == "" {
		return errors.New("FLIGHT_API_KEY is required")
	}

	cacheTTL := 5 * time.Minute
	if s := os.Getenv("CACHE_TTL"); s != "" {
		if cacheTTL, err = time.ParseDuration(s); err != nil || cacheTTL <= 0 {
			return fmt.Errorf("CACHE_TTL must be a positive duration, got %q", s)
		}
	}
	// With REDIS_MASTER_NAME set, REDIS_ADDRS are Sentinel addresses and the
	// client asks them which node is the master; otherwise it is one Redis node.
	// Short timeouts and no retries: if Redis is down, requests fall back
	// to the database quickly instead of waiting for seconds.
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:         strings.Split(os.Getenv("REDIS_ADDRS"), ","),
		MasterName:    os.Getenv("REDIS_MASTER_NAME"),
		DialTimeout:   300 * time.Millisecond,
		ReadTimeout:   300 * time.Millisecond,
		WriteTimeout:  300 * time.Millisecond,
		MaxRetries:    -1,
		DialerRetries: 1,
	})
	defer rdb.Close()

	addr := os.Getenv("GRPC_ADDR")
	if addr == "" {
		addr = ":50051"
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	srv := grpc.NewServer(
		grpc.UnaryInterceptor(flight.UnaryAuthInterceptor(apiKey)),
		grpc.StreamInterceptor(flight.StreamAuthInterceptor(apiKey)),
	)
	store := flight.NewCachedStore(flight.NewStore(db), rdb, cacheTTL)
	flightv1.RegisterFlightServiceServer(srv, flight.NewServer(store))
	// Lets tools like grpcurl discover the methods without the .proto file.
	reflection.Register(srv)

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	slog.Info("flight service listening", "addr", addr)
	return srv.Serve(lis)
}
