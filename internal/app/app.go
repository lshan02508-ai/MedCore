package app

import (
	"log/slog"
	"net"
	"net/http"

	"github.com/lshan02508-ai/GOLLM/internal/adapters/config"
	"github.com/lshan02508-ai/GOLLM/internal/proxy"
	"github.com/lshan02508-ai/GOLLM/internal/telemetry"
)

func NewHandler(cfg config.Runtime, logger *slog.Logger) (http.Handler, error) {
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
	p, err := proxy.New(proxy.Options{BackendURL: cfg.Backend.BaseURL, APIKey: cfg.Backend.APIKey, Client: &http.Client{Transport: transport}, MaxBodyBytes: cfg.Server.MaxRequestBodyBytes, RequestTimeout: cfg.RequestTimeout, Logger: logger, Metrics: metrics})
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.Handle("GET /metrics", metrics)
	mux.Handle("POST /v1/chat/completions", p)
	mux.Handle("GET /v1/models", p)
	return mux, nil
}
