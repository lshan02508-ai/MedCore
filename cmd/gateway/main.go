package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lshan02508-ai/GOLLM/internal/adapters/config"
	"github.com/lshan02508-ai/GOLLM/internal/app"
)

func main() {
	configPath := flag.String("config", "configs/gateway.json", "path to the gateway JSON configuration")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	handler, err := app.NewHandler(cfg, logger)
	if err != nil {
		logger.Error("gateway initialization failed", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: cfg.Server.Listen, Handler: handler, ReadHeaderTimeout: cfg.ReadHeaderTimeout}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()

		// Restore the default signal behavior so a second signal terminates the
		// process immediately if graceful draining gets stuck.
		stop()
		logger.Info("shutdown signal received")
		if err := shutdownServer(server, cfg.ShutdownTimeout, logger); err != nil {
			logger.Warn("gateway required forced shutdown", "error", err)
		}
	}()
	logger.Info("gateway listening", "address", cfg.Server.Listen, "backend", cfg.Backend.BaseURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("gateway stopped unexpectedly", "error", err)
		os.Exit(1)
	}
}

// shutdownServer first stops accepting new connections and gives in-flight
// requests time to finish. If the drain deadline expires, Close is used as a
// last resort so process shutdown remains bounded.
func shutdownServer(server *http.Server, timeout time.Duration, logger *slog.Logger) error {
	logger.Info("draining in-flight requests", "timeout", timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Warn("graceful drain failed; forcing connections closed", "error", err)
		closeErr := server.Close()
		if closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			return errors.Join(fmt.Errorf("graceful shutdown: %w", err), fmt.Errorf("force close: %w", closeErr))
		}
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	logger.Info("in-flight requests drained")
	return nil
}
