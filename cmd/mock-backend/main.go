package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

type chatRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func main() {
	listen := flag.String("listen", ":8000", "listen address")
	firstTokenDelay := flag.Duration("first-token-delay", 50*time.Millisecond, "delay before the first token")
	tokenDelay := flag.Duration("token-delay", 30*time.Millisecond, "delay between streamed tokens")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{{"id": "mock-model", "object": "model"}}})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":{"message":"invalid JSON","type":"invalid_request_error"}}`, http.StatusBadRequest)
			return
		}
		if !wait(r, *firstTokenDelay) {
			return
		}
		if req.Stream {
			serveStream(w, r, req.Model, *tokenDelay)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion", "model": req.Model,
			"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "hello from mock"}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 8, "completion_tokens": 3, "total_tokens": 11},
		})
	})

	log.Printf("mock backend listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, mux))
}

func serveStream(w http.ResponseWriter, r *http.Request, model string, delay time.Duration) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for i, token := range []string{"hello", " ", "from", " ", "mock"} {
		chunk := map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": token}, "finish_reason": nil}}}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
		if i < 4 && !wait(r, delay) {
			return
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func wait(r *http.Request, delay time.Duration) bool {
	select {
	case <-time.After(delay):
		return true
	case <-r.Context().Done():
		return false
	}
}
