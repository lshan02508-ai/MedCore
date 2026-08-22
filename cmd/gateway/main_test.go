package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShutdownServerDrainsIdleServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	err := shutdownServer(server.Config, time.Second, discardLogger())
	server.Close()
	if err != nil {
		t.Fatalf("shutdownServer() error = %v", err)
	}
}

func TestShutdownServerForcesCloseAfterDeadline(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestCanceled)
	}))
	t.Cleanup(server.Close)

	requestDone := make(chan error, 1)
	go func() {
		resp, err := server.Client().Get(server.URL)
		if resp != nil {
			_ = resp.Body.Close()
		}
		requestDone <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("request did not reach the server")
	}

	err := shutdownServer(server.Config, 20*time.Millisecond, discardLogger())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdownServer() error = %v, want context deadline exceeded", err)
	}

	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("forced close did not cancel the in-flight request")
	}

	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("client request did not finish after forced close")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
