# GoLLM Gateway 实现状态

> 最后更新：2026-08-22  
> 当前阶段：`v0.1-dev` OpenAI-Compatible 单后端流式代理内核

本文持续记录 GoLLM Gateway 已实现的功能、对应代码、当前限制和下一步开发计划。规划目标以仓库设计文档为准，本文只描述代码的实际状态。

## 1. 当前系统边界

当前版本是一个支持普通响应和 SSE 流式响应的 OpenAI-Compatible 单后端代理，还没有实现 Token-aware Admission、SLO-aware Scheduling 和多 Worker 路由。

```text
OpenAI Client
    ↓ HTTP :8080
GoLLM Gateway
    ├── 配置与进程生命周期
    ├── 健康和就绪检查
    ├── 基础 Prometheus 指标
    └── HTTP/SSE 透明代理
            ↓
OpenAI-Compatible Backend
    └── Mock Backend / vLLM / 其他兼容服务
```

## 2. 已实现功能

### 2.1 进程启动和关闭

代码：`cmd/gateway/main.go`

- 从 `-config` 参数读取配置文件路径；
- 初始化结构化日志和 HTTP Server；
- 监听 `SIGINT` 和 `SIGTERM`；
- 停止接收新连接后等待在途请求完成；
- 超过 `shutdown_timeout` 后强制关闭连接；
- 第二次关闭信号可直接终止进程。

### 2.2 配置加载

代码：

- `internal/adapters/config/config.go`
- `configs/gateway.json`
- `deploy/docker-compose/gateway.docker.json`

当前配置包含：

- Gateway 监听地址；
- 最大请求体大小；
- 请求总超时、响应头超时、首 Token 超时、流式空闲超时和优雅关闭超时；
- readiness 探测超时；
- 后端 Base URL 和 API Key；
- HTTP 空闲连接及连接池参数。

配置使用严格 JSON 解码，未知字段会导致启动失败。持续时间在启动时解析并校验为正数。

### 2.3 HTTP API

代码：`internal/app/app.go`

| Method | Path | 当前行为 |
|---|---|---|
| `POST` | `/v1/chat/completions` | 透明代理普通 JSON 和 SSE 响应 |
| `GET` | `/v1/models` | 透明代理后端模型列表 |
| `GET` | `/healthz` | Gateway 进程存活时返回 `200` |
| `GET` | `/readyz` | 后端 `/v1/models` 可访问时返回 `200`，否则返回 `503` |
| `GET` | `/metrics` | 输出 Prometheus 文本指标 |

### 2.4 OpenAI-Compatible 透明代理

代码：`internal/proxy/proxy.go`

- 原样转发 JSON 请求和后端扩展字段；
- 普通响应使用流式复制，避免完整缓冲响应；
- 识别 `text/event-stream` 并在每次读取后立即 `Flush`；
- 首个 SSE 数据到达前暂存响应头，超时可返回结构化 `504`；
- 流开始后检测数据块间空闲超时，超时时取消上游并终止流；
- 将客户端请求 Context 传播到上游请求；
- 支持请求总超时和请求体大小限制；
- 移除 Hop-by-hop Header；
- 添加 `X-Forwarded-Host`、`X-Forwarded-Proto` 和 `X-Forwarded-For`；
- 配置了后端 API Key 时替换客户端的 `Authorization`；
- 后端响应头、状态码和响应体默认透明传递；
- Gateway 自身错误使用 OpenAI 风格错误体。

当前 Gateway 错误映射：

| 条件 | HTTP 状态 | Code |
|---|---:|---|
| 后端不可访问 | `502` | `upstream_error` |
| 请求或后端超时 | `504` | `upstream_timeout` |
| 请求取消 | `499` | `request_canceled` |
| Gateway 内部请求构造失败 | `500` | `gateway_error` |

### 2.5 后端 readiness 探测

代码：`internal/adapters/backend/openai/health.go`

- 使用 `GET {backend_base_url}/v1/models` 探测兼容后端；
- 配置后端 API Key 时携带 Bearer Token；
- `2xx` 响应视为就绪；
- 连接错误、超时和非 `2xx` 状态视为未就绪；
- 限制探测响应体读取量，避免异常响应无限消耗资源。

readiness 只表示后端 API 可访问，不表示模型一定存在空闲推理容量。

### 2.6 基础指标

代码：`internal/telemetry/metrics.go`

当前指标：

```text
gollm_requests_total
gollm_requests_inflight
gollm_proxy_errors_total
gollm_stream_first_token_timeouts_total
gollm_stream_idle_timeouts_total
```

### 2.7 Mock Backend

代码：`cmd/mock-backend/main.go`

支持：

- `/v1/models`；
- 普通 Chat Completion；
- SSE Chat Completion；
- 可配置首 Token 延迟；
- 可配置 Token 间隔；
- 请求 Context 取消。

### 2.8 构建与部署

相关文件：

- `Makefile`；
- `Dockerfile`；
- `deploy/docker-compose/compose.yaml`；
- `deploy/docker-compose/mock.Dockerfile`。

可以使用 Go 分别启动 Gateway 和 Mock Backend，也可以通过 Docker Compose 启动完整本地环境。

## 3. 测试状态

本地存在以下测试：

- Gateway 正常排空和强制关闭；
- 配置默认值与非法配置；
- JSON 请求与响应透传；
- SSE 及时刷新；
- Context 取消传播；
- 后端 readiness 成功、失败和超时；
- `/readyz` HTTP 状态映射。

根据当前仓库设置，`*_test.go` 被 `.gitignore` 忽略，因此这些测试只存在于本地，不会上传到远程仓库。正常的 Go 开源项目通常会提交测试代码；如需 CI 和外部贡献者执行测试，应取消该忽略规则。

本地验证命令：

```bash
source /mnt/yp2/test/go-env.sh
export GOCACHE=/mnt/yp2/test/GoLLM/.go-cache

go test ./...
go test -race ./...
go vet ./...
```

## 4. 当前未实现能力

### 4.1 代理协议层

- 完整的 OpenAI Conformance Suite；
- `/v1/completions`、`/v1/embeddings` 和 `/v1/responses`。

### 4.2 身份与策略

- API Key/JWT 鉴权；
- Tenant、Project 和 User 身份；
- 扩展请求头的信任和裁决；
- 模型访问策略与租户配额。

### 4.3 成本、准入和调度

- OpenAI 请求规范化模型；
- Workload Class；
- Token/Cost Estimator；
- 有界队列；
- Token-aware Admission Control；
- 并发容量租约；
- Tenant Weighted Fair Queue 或 Deficit Round Robin；
- Deadline、Aging 和 Starvation Guard。

### 4.4 多后端与路由

- Worker Registry；
- 后台健康检查和状态快照；
- Worker 熔断、摘除和恢复；
- Round Robin、Least Load 和 Predictive Selector；
- vLLM 与 vLLM-Ascend 专有指标适配。

### 4.5 可观测性与生产能力

- Queue Time、TTFT、TPOT 和端到端延迟指标；
- OpenTelemetry Trace；
- 结构化调度 Reason Code；
- Helm、Grafana、SBOM 和 Release Artifact；
- Redis 分布式配额与 PostgreSQL 策略存储。

## 5. 下一步开发计划

### 已完成：SSE 流式超时

当前配置：

```json
{
  "backend": {
    "first_token_timeout": "30s",
    "stream_idle_timeout": "30s"
  }
}
```

当前行为：

1. 等待第一个 SSE 数据超过 `first_token_timeout` 时取消后端请求；
2. 流开始后，相邻数据间隔超过 `stream_idle_timeout` 时取消后端请求；
3. 每收到一个有效数据块就重置空闲计时器；
4. 客户端断开时继续立即取消后端请求；
5. 普通非流式响应行为保持不变；
6. 在响应尚未提交给客户端时尽量返回 OpenAI 风格 `504`；
7. 已经开始流式输出后发生超时时终止流，并记录明确日志和指标。

### 下一项：Worker 数据模型与内存 Registry

下一阶段将单后端抽象为 Worker Pool。单实例是只有一个 Worker 的 Pool，多实例由 Registry 保存多个 Worker 的地址、模型能力、健康状态和在途请求数。

第一步只建立通用数据模型和内存快照，不立即加入自动重试。随后依次增加多 Worker 配置、健康检查循环和基础 Selector。

### 后续顺序

```text
Worker 数据模型与内存 Registry
    ↓
多 Worker 配置与基础 Selector
    ↓
首次输出前的有限重试
    ↓
OpenAI 请求规范化与 Workload Class
    ↓
Token/Cost Estimator 与 Admission Control
    ↓
多租户公平调度
```

## 6. 本地运行

终端一启动 Mock Backend：

```bash
cd /mnt/yp2/test/GoLLM/gollm-gateway
source /mnt/yp2/test/go-env.sh
go run ./cmd/mock-backend
```

终端二启动 Gateway：

```bash
cd /mnt/yp2/test/GoLLM/gollm-gateway
source /mnt/yp2/test/go-env.sh
go run ./cmd/gateway -config configs/gateway.json
```

验证接口：

```bash
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz

curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock-model","messages":[{"role":"user","content":"hello"}]}'
```
