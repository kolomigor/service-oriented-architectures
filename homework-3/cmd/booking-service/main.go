package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	flightv1 "github.com/kolomigor/flight-booking/gen/flight/v1"
	"github.com/kolomigor/flight-booking/internal/booking"
)

func main() {
	if err := run(); err != nil {
		slog.Error("booking service stopped", "err", err)
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

	apiKey := os.Getenv("FLIGHT_API_KEY")
	if apiKey == "" {
		return errors.New("FLIGHT_API_KEY is required")
	}

	// NewClient does not connect yet: the connection is opened on the first call.
	conn, err := grpc.NewClient(os.Getenv("FLIGHT_SERVICE_ADDR"),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Order matters: retry is outermost, so each attempt gets its own timeout.
		grpc.WithChainUnaryInterceptor(
			booking.RetryInterceptor(3, 100*time.Millisecond),
			booking.TimeoutInterceptor(2*time.Second),
			booking.APIKeyInterceptor(apiKey),
		),
	)
	if err != nil {
		return fmt.Errorf("create flight service client: %w", err)
	}
	defer conn.Close()

	svc := booking.NewService(booking.NewStore(db), flightv1.NewFlightServiceClient(conn))

	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           booking.NewHandler(svc),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("booking service listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
