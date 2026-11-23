package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IdaemonI/stockroom-go/internal/httpapi"
	"github.com/IdaemonI/stockroom-go/internal/inventory"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, *addr); err != nil {
		slog.Error("stockroom stopped", "error", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, addr string) error {
	keys, err := httpapi.NewKeys(os.Getenv("STOCKROOM_READ_KEY"), os.Getenv("STOCKROOM_WRITE_KEY"))
	if err != nil {
		return err
	}
	var store httpapi.Store = inventory.NewStore()
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer db.Close()
		startup, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = inventory.Migrate(startup, db)
		cancel()
		if err != nil {
			return fmt.Errorf("migrate database: %w", err)
		}
		store = inventory.NewPostgres(db)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           httpapi.New(store, keys),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
	}
	slog.Info("listening", "address", listener.Addr().String())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(drain); err != nil {
			_ = server.Close()
			return err
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
