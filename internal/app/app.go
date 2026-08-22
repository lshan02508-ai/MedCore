package app

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	backendopenai "github.com/lshan02508-ai/GOLLM/internal/adapters/backend/openai"
	"github.com/lshan02508-ai/GOLLM/internal/adapters/config"
	"github.com/lshan02508-ai/GOLLM/internal/proxy"
	"github.com/lshan02508-ai/GOLLM/internal/telemetry"
)

func NewHandler(cfg config.Runtime, logger *slog.Logger) (http.Handler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: cfg.ResponseHeaderTimeout}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          cfg.Backend.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.Backend.MaxIdleConnsPerHost,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
	}
	metrics := &telemetry.Metrics{}
	client := &http.Client{Transport: transport}
	p, err := proxy.New(proxy.Options{BackendURL: cfg.Backend.BaseURL, APIKey: cfg.Backend.APIKey, Client: client, MaxBodyBytes: cfg.Server.MaxRequestBodyBytes, RequestTimeout: cfg.RequestTimeout, FirstTokenTimeout: cfg.FirstTokenTimeout, StreamIdleTimeout: cfg.StreamIdleTimeout, Logger: logger, Metrics: metrics})
	if err != nil {
		return nil, err
	}
	checker, err := backendopenai.NewHealthChecker(cfg.Backend.BaseURL, cfg.Backend.APIKey, client)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /readyz", readinessHandler(checker, cfg.ReadinessTimeout, logger))
	mux.Handle("GET /metrics", metrics)
	mux.Handle("POST /v1/chat/completions", p)
	mux.Handle("GET /v1/models", p)
	return mux, nil
}

type readinessChecker interface {
	Ready(context.Context) error
}

func readinessHandler(checker readinessChecker, timeout time.Duration, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := checker.Ready(ctx); err != nil {
			logger.Debug("backend readiness probe failed", "error", err)
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
}
