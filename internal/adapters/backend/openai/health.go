package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxProbeResponseBytes = 64 << 10

// HealthChecker verifies that an OpenAI-compatible backend can serve its
// model-list endpoint. This proves API reachability, not available model capacity.
type HealthChecker struct {
	modelsURL string
	apiKey    string
	client    *http.Client
}

func NewHealthChecker(baseURL, apiKey string, client *http.Client) (*HealthChecker, error) {
	target, err := url.Parse(baseURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("invalid backend URL %q", baseURL)
	}
	target.Path = strings.TrimRight(target.Path, "/") + "/v1/models"
	target.RawQuery = ""
	target.Fragment = ""
	if client == nil {
		client = http.DefaultClient
	}
	return &HealthChecker{modelsURL: target.String(), apiKey: apiKey, client: client}, nil
}

func (c *HealthChecker) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.modelsURL, nil)
	if err != nil {
		return fmt.Errorf("create readiness request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("readiness request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeResponseBytes))

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("readiness request returned HTTP %d", resp.StatusCode)
	}
	return nil
}
