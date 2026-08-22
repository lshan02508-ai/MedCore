package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lshan02508-ai/GOLLM/internal/telemetry"
)

type Proxy struct {
	backend        *url.URL
	apiKey         string
	client         *http.Client
	maxBodyBytes   int64
	requestTimeout time.Duration
	logger         *slog.Logger
	metrics        *telemetry.Metrics
}

type Options struct {
	BackendURL     string
	APIKey         string
	Client         *http.Client
	MaxBodyBytes   int64
	RequestTimeout time.Duration
	Logger         *slog.Logger
	Metrics        *telemetry.Metrics
}

func New(opts Options) (*Proxy, error) {
	u, err := url.Parse(opts.BackendURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid backend URL %q", opts.BackendURL)
	}
	if opts.Client == nil {
		opts.Client = http.DefaultClient
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Metrics == nil {
		opts.Metrics = &telemetry.Metrics{}
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 32 << 20
	}
	return &Proxy{backend: u, apiKey: opts.APIKey, client: opts.Client, maxBodyBytes: opts.MaxBodyBytes, requestTimeout: opts.RequestTimeout, logger: opts.Logger, metrics: opts.Metrics}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	failed := true
	done := p.metrics.Begin()
	defer func() { done(failed) }()

	ctx := r.Context()
	var cancel context.CancelFunc
	if p.requestTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, p.requestTimeout)
		defer cancel()
	}

	target := *p.backend
	target.Path = joinPath(p.backend.Path, r.URL.Path)
	target.RawQuery = r.URL.RawQuery
	body := http.MaxBytesReader(w, r.Body, p.maxBodyBytes)
	defer body.Close()
	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), body)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "gateway_error", "failed to create upstream request")
		return
	}
	copyHeaders(req.Header, r.Header)
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	appendForwardedHeaders(req.Header, r)

	resp, err := p.client.Do(req)
	if err != nil {
		status, code, message := upstreamError(err)
		p.logger.Warn("upstream request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeOpenAIError(w, status, code, message)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.Header().Set("X-GoLLM-Upstream", p.backend.Host)
	w.WriteHeader(resp.StatusCode)

	if isSSE(resp.Header) {
		if err := stream(w, resp.Body); err != nil {
			p.logger.Debug("stream ended", "path", r.URL.Path, "error", err)
			return
		}
	} else if _, err := io.Copy(w, resp.Body); err != nil {
		p.logger.Debug("response copy failed", "path", r.URL.Path, "error", err)
		return
	}
	failed = resp.StatusCode >= 500
}

func stream(w http.ResponseWriter, src io.Reader) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("response writer does not support streaming")
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			flusher.Flush()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func isSSE(h http.Header) bool {
	return strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "text/event-stream")
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		if hopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func appendForwardedHeaders(h http.Header, r *http.Request) {
	if host := r.Host; host != "" {
		h.Set("X-Forwarded-Host", host)
	}
	h.Set("X-Forwarded-Proto", forwardedProto(r))
	if ip := clientIP(r.RemoteAddr); ip != "" {
		if prior := h.Get("X-Forwarded-For"); prior != "" {
			ip = prior + ", " + ip
		}
		h.Set("X-Forwarded-For", ip)
	}
}

func forwardedProto(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func clientIP(remote string) string {
	if i := strings.LastIndex(remote, ":"); i >= 0 {
		return strings.Trim(remote[:i], "[]")
	}
	return remote
}

func hopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func joinPath(base, request string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(request, "/")
}

func upstreamError(err error) (int, string, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "upstream_timeout", "the inference backend timed out"
	}
	if errors.Is(err, context.Canceled) {
		return 499, "request_canceled", "the request was canceled"
	}
	return http.StatusBadGateway, "upstream_error", "the inference backend is unavailable"
}

func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": code, "code": code}})
}
