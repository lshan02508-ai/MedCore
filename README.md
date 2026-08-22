# GoLLM Gateway

GoLLM Gateway is an OpenAI-compatible inference proxy that provides the foundation for token-aware admission control, SLO-aware scheduling, and predictive worker routing.

The current `v0.1-dev` milestone implements the generic proxy kernel:

- `POST /v1/chat/completions` pass-through;
- `GET /v1/models` pass-through;
- non-streaming and SSE streaming responses;
- request cancellation propagation;
- bounded request bodies and layered timeouts;
- OpenAI-shaped gateway errors;
- health, readiness, and basic Prometheus metrics;
- a controllable mock inference backend;
- no third-party Go dependencies.

Admission control and scheduling are intentionally not part of this first milestone.

## Quickstart

Requirements: Go 1.22 or Docker Compose.

Run locally in two terminals:

```bash
make mock
make run
```

Or start both services with Docker Compose:

```bash
docker compose -f deploy/docker-compose/compose.yaml up --build
```

Send a non-streaming request:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock-model","messages":[{"role":"user","content":"hello"}]}'
```

Send a streaming request:

```bash
curl -N http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock-model","stream":true,"messages":[{"role":"user","content":"hello"}]}'
```

Use the Python OpenAI SDK:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="local-dev")
response = client.chat.completions.create(
    model="mock-model",
    messages=[{"role": "user", "content": "hello"}],
)
print(response.choices[0].message.content)
```

## Configuration

The initial implementation uses strict JSON configuration so it can remain dependency-free. Unknown fields fail startup instead of being silently ignored. See [`configs/gateway.json`](configs/gateway.json).

The backend API key, when configured, replaces the incoming `Authorization` header. When it is empty, the client's authorization header is passed through.

## Development

```bash
make fmt
make test
make race
make build
```

See [`API.md`](API.md) for current behavior and [`ARCHITECTURE.md`](ARCHITECTURE.md) for the request lifecycle.

## Status

This repository is under active development and has not reached a stable release. Public API and configuration compatibility are not yet guaranteed.

## License

Apache License 2.0.
