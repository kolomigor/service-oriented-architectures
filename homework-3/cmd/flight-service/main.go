package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
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
	flightv1.RegisterFlightServiceServer(srv, flight.NewServer(flight.NewStore(db)))
	// Lets tools like grpcurl discover the methods without the .proto file.
	reflection.Register(srv)

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	slog.Info("flight service listening", "addr", addr)
	return srv.Serve(lis)
}
