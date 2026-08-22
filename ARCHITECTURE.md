# Architecture

## v0.1 request lifecycle

```text
OpenAI client
    ↓
Go net/http server
    ↓
Route allowlist and request-body limit
    ↓
Request context + total deadline
    ↓
Reusable backend HTTP transport
    ↓
OpenAI-compatible inference backend
    ↓
JSON copy or incremental SSE copy + Flush
```

The proxy kernel is deliberately small. It validates gateway configuration but treats inference request and response bodies as opaque bytes. This prevents the gateway from dropping protocol extensions and keeps the core independent from a particular vLLM release.

## Cancellation ownership

The client request context is the root context for the upstream request. The following events cancel upstream work:

- client disconnect;
- caller cancellation;
- gateway total request timeout;
- graceful shutdown after its drain deadline.

Response bodies are always closed by the proxy. The incoming body is wrapped with `http.MaxBytesReader` and closed after the upstream exchange.

## Timeout layers

- `read_header_timeout`: protects the gateway HTTP server while reading headers;
- `response_header_timeout`: limits the time waiting for backend response headers;
- `request_timeout`: limits the complete request, including a stream;
- `shutdown_timeout`: bounds graceful draining;
- `idle_conn_timeout`: controls pooled idle backend connections, not request execution.

Exact time-to-first-SSE-token and between-token idle deadlines require a stream-aware timeout layer and are planned for the next proxy iteration.

## Health and readiness

`/healthz` reports process liveness and does not depend on an inference backend. `/readyz` performs a bounded, authenticated `GET /v1/models` probe against the configured OpenAI-compatible backend. A failed, timed-out, or non-2xx probe returns `503`, allowing an orchestrator to stop routing inference traffic through an unavailable gateway instance.

## Next architecture increment

The next increment inserts normalized request metadata, a bounded queue, capacity leases, and admission decisions before the backend transport. The proxy and backend interfaces remain responsible for HTTP semantics and cancellation.
