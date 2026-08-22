package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	Server  ServerConfig  `json:"server"`
	Backend BackendConfig `json:"backend"`
}

type ServerConfig struct {
	Listen              string `json:"listen"`
	MaxRequestBodyBytes int64  `json:"max_request_body_bytes"`
	ReadHeaderTimeout   string `json:"read_header_timeout"`
	RequestTimeout      string `json:"request_timeout"`
	ShutdownTimeout     string `json:"shutdown_timeout"`
}

type BackendConfig struct {
	BaseURL               string `json:"base_url"`
	APIKey                string `json:"api_key"`
	ReadinessTimeout      string `json:"readiness_timeout"`
	FirstTokenTimeout     string `json:"first_token_timeout"`
	StreamIdleTimeout     string `json:"stream_idle_timeout"`
	ResponseHeaderTimeout string `json:"response_header_timeout"`
	IdleConnTimeout       string `json:"idle_conn_timeout"`
	MaxIdleConns          int    `json:"max_idle_conns"`
	MaxIdleConnsPerHost   int    `json:"max_idle_conns_per_host"`
}

type Runtime struct {
	Config
	ReadHeaderTimeout     time.Duration
	RequestTimeout        time.Duration
	ShutdownTimeout       time.Duration
	ReadinessTimeout      time.Duration
	FirstTokenTimeout     time.Duration
	StreamIdleTimeout     time.Duration
	ResponseHeaderTimeout time.Duration
	IdleConnTimeout       time.Duration
}

func Load(path string) (Runtime, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Runtime{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Runtime{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg.Validate()
}

func (c Config) Validate() (Runtime, error) {
	if c.Server.Listen == "" {
		c.Server.Listen = ":8080"
	}
	if c.Server.MaxRequestBodyBytes == 0 {
		c.Server.MaxRequestBodyBytes = 32 << 20
	}
	if c.Server.MaxRequestBodyBytes < 1 {
		return Runtime{}, errors.New("server.max_request_body_bytes must be positive")
	}
	if strings.TrimSpace(c.Backend.BaseURL) == "" {
		return Runtime{}, errors.New("backend.base_url is required")
	}
	c.Server.ReadHeaderTimeout = defaultString(c.Server.ReadHeaderTimeout, "5s")
	c.Server.RequestTimeout = defaultString(c.Server.RequestTimeout, "10m")
	c.Server.ShutdownTimeout = defaultString(c.Server.ShutdownTimeout, "30s")
	c.Backend.ReadinessTimeout = defaultString(c.Backend.ReadinessTimeout, "2s")
	c.Backend.FirstTokenTimeout = defaultString(c.Backend.FirstTokenTimeout, "30s")
	c.Backend.StreamIdleTimeout = defaultString(c.Backend.StreamIdleTimeout, "30s")
	c.Backend.ResponseHeaderTimeout = defaultString(c.Backend.ResponseHeaderTimeout, "30s")
	c.Backend.IdleConnTimeout = defaultString(c.Backend.IdleConnTimeout, "90s")
	if c.Backend.MaxIdleConns == 0 {
		c.Backend.MaxIdleConns = 100
	}
	if c.Backend.MaxIdleConnsPerHost == 0 {
		c.Backend.MaxIdleConnsPerHost = 100
	}

	readHeader, err := positiveDuration("server.read_header_timeout", c.Server.ReadHeaderTimeout)
	if err != nil {
		return Runtime{}, err
	}
	request, err := positiveDuration("server.request_timeout", c.Server.RequestTimeout)
	if err != nil {
		return Runtime{}, err
	}
	shutdown, err := positiveDuration("server.shutdown_timeout", c.Server.ShutdownTimeout)
	if err != nil {
		return Runtime{}, err
	}
	readiness, err := positiveDuration("backend.readiness_timeout", c.Backend.ReadinessTimeout)
	if err != nil {
		return Runtime{}, err
	}
	firstToken, err := positiveDuration("backend.first_token_timeout", c.Backend.FirstTokenTimeout)
	if err != nil {
		return Runtime{}, err
	}
	streamIdle, err := positiveDuration("backend.stream_idle_timeout", c.Backend.StreamIdleTimeout)
	if err != nil {
		return Runtime{}, err
	}
	responseHeader, err := positiveDuration("backend.response_header_timeout", c.Backend.ResponseHeaderTimeout)
	if err != nil {
		return Runtime{}, err
	}
	idle, err := positiveDuration("backend.idle_conn_timeout", c.Backend.IdleConnTimeout)
	if err != nil {
		return Runtime{}, err
	}

	c.Backend.BaseURL = strings.TrimRight(c.Backend.BaseURL, "/")
	return Runtime{Config: c, ReadHeaderTimeout: readHeader, RequestTimeout: request, ShutdownTimeout: shutdown, ReadinessTimeout: readiness, FirstTokenTimeout: firstToken, StreamIdleTimeout: streamIdle, ResponseHeaderTimeout: responseHeader, IdleConnTimeout: idle}, nil
}

func positiveDuration(name, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration: %q", name, value)
	}
	return d, nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
