package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestProxy(t *testing.T, backend http.Handler) (*httptest.Server, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(backend)
	p, err := New(Options{BackendURL: upstream.URL, Client: upstream.Client(), MaxBodyBytes: 1 << 20, RequestTimeout: 5 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		upstream.Close()
		t.Fatal(err)
	}
	gateway := httptest.NewServer(p)
	t.Cleanup(gateway.Close)
	t.Cleanup(upstream.Close)
	return gateway, upstream
}

func TestNonStreamingPassThrough(t *testing.T) {
	gateway, _ := newTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Test") != "preserved" {
			t.Error("request header was not preserved")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["custom_field"] != "preserved" {
			t.Error("unknown request field was not preserved")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend-Test", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test"}`))
	}))
	req, _ := http.NewRequest(http.MethodPost, gateway.URL+"/v1/chat/completions", strings.NewReader(`{"model":"test","custom_field":"preserved"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test", "preserved")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Backend-Test") != "yes" {
		t.Error("response header was not preserved")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"id":"chatcmpl-test"}` {
		t.Fatalf("body = %q", body)
	}
}

func TestSSEIsFlushed(t *testing.T) {
	releaseSecond := make(chan struct{})
	gateway, _ := newTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		<-releaseSecond
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	resp, err := http.Post(gateway.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "data: first\n" {
		t.Fatalf("first line = %q", line)
	}
	close(releaseSecond)
}

func TestClientCancellationPropagates(t *testing.T) {
	upstreamStarted := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(upstreamStarted)
		<-r.Context().Done()
		close(upstreamCanceled)
		return nil, r.Context().Err()
	})}

	p, err := New(Options{
		BackendURL:     "http://backend.test",
		Client:         client,
		MaxBodyBytes:   1 << 20,
		RequestTimeout: 5 * time.Second,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)).WithContext(ctx)
	result := make(chan struct{})
	go func() {
		p.ServeHTTP(httptest.NewRecorder(), req)
		close(result)
	}()

	select {
	case <-upstreamStarted:
	case <-time.After(time.Second):
		t.Fatal("request did not reach upstream")
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach upstream")
	}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("proxy request did not finish")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
