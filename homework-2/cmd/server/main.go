package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kolomigor/marketplace-homework2/internal/config"
	"github.com/kolomigor/marketplace-homework2/internal/httpapi"
	"github.com/kolomigor/marketplace-homework2/internal/service"
	"github.com/shopspring/decimal"
)

func main() {
	health := flag.Bool("healthcheck", false, "check the running HTTP server")
	flag.Parse()
	if *health {
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/health")
		if err != nil {
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	decimal.MarshalJSONWithoutQuotes = true
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = db.Ping(startup)
	cancel()
	if err != nil {
		return err
	}
	svc := &service.Service{DB: db, OrderInterval: cfg.OrderInterval, JWTSecret: cfg.JWTSecret, AccessTTL: cfg.AccessTTL, RefreshTTL: cfg.RefreshTTL}
	handler, err := httpapi.New(svc, logger)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.Address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	fail := make(chan error, 1)
	go func() { fail <- server.ListenAndServe() }()
	logger.Info("server started", "address", cfg.Address)
	select {
	case err := <-fail:
		if err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
	return nil
}
