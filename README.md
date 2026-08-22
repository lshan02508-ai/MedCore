# GoLLM Gateway

GoLLM Gateway is an OpenAI-compatible inference gateway with token-cost admission, SLO-aware scheduling, and inference-aware multi-worker routing.

The current V1 implementation includes:

- `POST /v1/chat/completions` pass-through;
- `GET /v1/models` support;
- non-streaming and SSE streaming responses;
- request cancellation propagation;
- bounded request bodies and layered timeouts;
- OpenAI-shaped gateway errors;
- health, readiness, and Prometheus scheduler metrics;
- single-worker compatibility and model-aware multi-worker routing;
- token-cost admission at model-pool and Worker levels;
- a bounded queue with `429`, `Retry-After`, and maximum queue wait;
- Workload Class, Deadline, Aging, limited backfill, and starvation protection;
- Least-Estimated-Work routing with success-only EWMA service rates;
- active dispatch that keeps backend queues shallow;
- idempotent cost/concurrency leases held through JSON or SSE completion;
- background Worker health monitoring with failure and recovery thresholds;
- OpenAI request normalization and configurable workload classification;
- task profiling and conservative per-request token budgets;
- a controllable mock inference backend;
- only one runtime dependency, the maintained YAML parser.

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

The gateway accepts strict JSON and YAML configuration. Unknown fields, duplicate YAML keys, multiple documents, and unsupported file extensions fail startup instead of being silently ignored. See [`configs/gateway.json`](configs/gateway.json) and [`configs/gateway.yaml`](configs/gateway.yaml).

```bash
go run ./cmd/gateway -config configs/gateway.json
go run ./cmd/gateway -config configs/gateway.yaml
```

The backend API key, when configured, replaces the incoming `Authorization` header. When it is empty, the client's authorization header is passed through.

Single-worker deployments can continue using `backend.base_url`. Multi-worker deployments use model pools:

```json
{
  "backend": {
    "readiness_timeout": "2s",
    "first_token_timeout": "30s",
    "stream_idle_timeout": "30s"
  },
  "worker_health": {
    "interval": "5s",
    "timeout": "2s",
    "unhealthy_threshold": 3,
    "healthy_threshold": 2
  },
  "workload_classification": {
    "allow_client_hint": false,
    "realtime_max_input_bytes": 4096,
    "realtime_max_output_tokens": 256,
    "long_context_min_input_bytes": 32768,
    "batch_min_output_tokens": 2048
  },
  "token_budget": {
    "allow_client_task_hint": false,
    "bytes_per_token": 3,
    "message_overhead_tokens": 4,
    "default_context_window_tokens": 32768,
    "default_output_tokens": 1024,
    "task_output_tokens": {
      "qa": 256,
      "extraction": 512,
      "summarization": 1024,
      "code_generation": 2048,
      "reasoning": 4096,
      "tool_use": 1024
    },
    "model_context_window_tokens": {"qwen": 32768}
  },
  "scheduler": {
    "queue_capacity": 500,
    "max_queued_cost": 500000,
    "max_queue_wait": "5s",
    "retry_after": "1s",
    "aging_interval": "30s",
    "max_bypass": 8,
    "prefill_weight": 1,
    "decode_weight": 3,
    "ewma_alpha": 0.2,
    "min_service_rate": 100,
    "max_service_rate": 1000000,
    "class_slo": {
      "realtime": "800ms",
      "interactive": "3s",
      "long_context": "10s",
      "batch": ""
    }
  },
  "model_pools": {
    "qwen": {
      "model": "qwen",
      "max_inflight_cost": 100000,
      "workers": [
        {"id": "qwen-1", "endpoint": "http://vllm-1:8000", "max_concurrency": 20, "max_inflight_cost": 50000, "initial_service_rate": 3000},
        {"id": "qwen-2", "endpoint": "http://vllm-2:8000", "max_concurrency": 20, "max_inflight_cost": 50000, "initial_service_rate": 3000}
      ]
    }
  }
}
```

The gateway first computes `RequestCost = estimated prompt tokens × prefill_weight + reserved output tokens × decode_weight`. Requests that cannot run immediately wait in the bounded Gateway queue. Dispatch order uses Workload Class, absolute Deadline, Aging, and deterministic tie-breaking; limited short-job backfill avoids capacity holes without allowing large jobs to starve.

For every dispatch, the gateway filters Workers by model, health, concurrency, and cost capacity. It selects the minimum `(worker inflight cost + request cost) / EWMA service rate`. Successful completions update the rate; cancellation, timeout, and upstream `5xx` samples do not.

Every Worker is probed concurrently in the background. Three consecutive failures mark a Worker unhealthy by default; two consecutive successful probes restore it. `/readyz` reads the resulting Registry snapshot and returns `503` when no healthy Worker remains.

For Chat Completions, the gateway derives immutable request metadata without rebuilding the JSON body. It assigns one of `realtime`, `interactive`, `long_context`, or `batch`; the default rules use streaming mode, bounded request bytes, and the declared output-token limit. `X-GoLLM-Workload-Class` is ignored by default because authentication is not implemented; trusted deployments may explicitly enable `allow_client_hint`. Successful responses expose the authoritative class, cost, queue wait, scheduler reason, selected Worker, and expected load in `X-GoLLM-*` headers.

The token estimator uses normalized message/tool bytes, chat-message overhead, and an output reservation from the explicit OpenAI limit or task/workload defaults. It is intentionally conservative and is now consumed by admission and routing. It does not rewrite an omitted `max_tokens`, replace a model-native tokenizer, or represent billing usage.

## Development

```bash
make fmt
make test
make race
make build
```

## Status

This repository is under active development and has not reached a stable release. Public API and configuration compatibility are not yet guaranteed.

## License

Apache License 2.0.
