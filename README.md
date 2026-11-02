# MedCore

> 面向普通用户的健康科普后端：把多会话管理、按需检索和流式 AI 回答连接成一条完整链路。

MedCore 当前聚焦**健康科普**。用户可以连续提问；服务结合对话历史，由模型决定是否查询本地医学知识库或联网搜索，再生成中文回答。它用于辅助理解健康信息，**不能替代医生诊断、处方或急救服务**。

> [!NOTE]
> 本仓库仅包含 **FastAPI 后端**。Vue 前端、独立 RAG 检索服务、Milvus 和模型服务需要分别部署，不包含在此仓库中。设计背景与后续规划见[健康科普架构文档](docs/health-education-architecture.md)。

## 当前能力

| 模块 | 已实现 |
| --- | --- |
| 用户与会话 | 注册、登录、JWT 鉴权；创建、列出、删除会话；消息持久化 |
| 健康科普 | LangGraph 根据问题和近期历史选择检索工具，再生成回答 |
| 知识来源 | 按需调用独立 RAG 服务或 Tavily 联网搜索；展示回答引用的检索结果 |
| 流式交互 | SSE 返回规划、检索和生成状态及回答增量；完成后保存整轮对话 |

当前是**单轮工具选择**：一次规划最多调用两个检索工具，之后生成回答；尚未实现根据检索结果反复补检的 Agent 循环。引用筛选也不等同于医学事实核查。

## 系统如何工作

```text
前端 / API 客户端
        │  POST /api/v1/chats/{id}/turns/stream
        ▼
FastAPI ── JWT 鉴权、读取会话历史
        ▼
LangGraph 健康科普工作流
        ├── 按需调用独立 RAG 服务
        ├── 按需调用 Tavily Web Search
        └── DeepSeek 生成回答
        ▼
SSE 流式返回 ── 完成后保存消息与引用到 MySQL
```

后端通过 OpenAI 兼容 SDK 调用 DeepSeek。本地知识库由**独立服务**负责检索，因此后端进程不需要加载 Milvus 或向量模型。

## 快速启动

需要 Python 3.11+、可用的 MySQL、DeepSeek API Key，以及 [uv](https://docs.astral.sh/uv/) 包管理器。只有当工作流选择相应工具时，才需要可访问的 RAG 服务或 Tavily Key。

```bash
# 在本仓库根目录
uv sync --frozen
cp .env.example .env
```

编辑 `.env`，至少填写 `MYSQL_USER`、`MYSQL_PASSWORD`、`JWT_SECRET_KEY` 和 `DEEPSEEK_API_KEY`。可用以下命令生成 JWT 密钥：

```bash
python -c "import secrets; print(secrets.token_urlsafe(32))"
```

需要本地检索时，设置 `RAG_SERVICE_URL`（默认 `http://127.0.0.1:8091`）并**另行启动 RAG 服务**；需要联网搜索时，在 `.env` 中添加 `TAVILY_API_KEY=...`。不要将真实密钥提交到 Git。

```bash
PYTHONPATH=src .venv/bin/alembic upgrade head
PYTHONPATH=src .venv/bin/uvicorn medcore.main:app --host 127.0.0.1 --port 8089 --reload
```

启动后访问 [API 文档](http://127.0.0.1:8089/docs)，或请求 `http://127.0.0.1:8089/health` 检查 API 进程。默认只监听本机；如需外部访问，建议通过前端代理或反向代理发布。

## 主要接口

注册和登录分别使用 `POST /auth/register`、`POST /auth/login`。聊天接口需携带 `Authorization: Bearer <access_token>`。

| 方法与路径 | 用途 |
| --- | --- |
| `POST /api/v1/chats` | 创建会话 |
| `GET /api/v1/chats` | 列出当前用户的会话 |
| `GET /api/v1/chats/{id}/messages` | 分页读取消息 |
| `POST /api/v1/chats/{id}/turns` | 非流式生成并保存一轮对话 |
| `POST /api/v1/chats/{id}/turns/stream` | SSE 流式生成并保存一轮对话 |
| `DELETE /api/v1/chats/{id}` | 删除会话及消息 |

流式接口接收 JSON 请求体 `{"content":"什么是病毒？"}`。SSE 事件包括 `status`（`planning`、`retrieving`、`generating`）、`delta`（文本片段）、`done`（已保存的完整消息）和 `error`。生成失败不会保存不完整的这一轮对话。

## 代码结构

```text
src/medcore/
├── api/                     HTTP 路由与鉴权依赖
├── services/                会话业务流程
├── repositories/            数据库读写
├── database/                SQLAlchemy 模型与连接
├── workflows/education/     健康科普状态、提示词、工具与 LangGraph 图
├── llm/                     DeepSeek 调用封装
├── rag/                     独立 RAG 服务的 HTTP 客户端
└── capabilities/web_search/ 联网搜索
migrations/                  Alembic 迁移
tests/                       后端自动化测试
```

## 测试与现阶段边界

```bash
PYTHONPATH=src .venv/bin/python -m unittest discover -s tests -q
```

现有测试覆盖鉴权、会话隔离、工具选择接口与流式保存等行为；外部模型和检索调用使用模拟对象。单元测试通过**不代表**真实 RAG、联网搜索和医学回答质量已经验证。后续应重点评测多轮指代、检索相关性、引用真实性、用药咨询和紧急症状处理。
