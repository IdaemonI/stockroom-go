package main

import (
	"context"
	"errors"
	"flag"
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
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           httpapi.New(inventory.NewStore()),
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
