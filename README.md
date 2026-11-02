# MedCore API

健康科普第一阶段的背景、实施规划与目标架构见 [健康科普架构文档](docs/health-education-architecture.md)。

将 `.env.example` 复制为 `.env`，填写 MySQL 连接信息，并把 `JWT_SECRET_KEY` 改成随机生成的长密钥。例如可用 `python -c "import secrets; print(secrets.token_urlsafe(32))"` 生成。请勿将 `.env` 提交到版本库。要启用 AI 问答，还需填写 `DEEPSEEK_API_KEY`；MedCore 通过 `RAG_SERVICE_URL` 调用独立检索服务。

启动服务后，请求 `POST /auth/login`，JSON 请求体为 `{"email":"user@example.com","password":"your_password"}`。成功时返回 `access_token` 和 `token_type: "bearer"`；token 有效期为 30 分钟，`sub` 是用户 ID。邮箱或密码错误以及账号停用时返回 401。

在 `medcore` 目录准备后端依赖，再执行 `PYTHONPATH=src .venv/bin/alembic upgrade head` 创建或更新表结构。迁移使用 MySQL InnoDB 外键，删除会话会级联删除其消息。完成后可用 `PYTHONPATH=src .venv/bin/uvicorn medcore.main:app --host 127.0.0.1 --port 8089 --reload` 启动 API；也可运行 `.venv/bin/medcore`，默认监听 `127.0.0.1:8089`。后端只需供同机 Vite 代理访问，不必对外开放。MedCore 仍需 OpenAI SDK 来调用 DeepSeek，但不需要安装 PyTorch 或 Milvus。独立检索服务的启动方式见仓库根目录的 `rag_service/README.md`。

聊天接口均需 `Authorization: Bearer <access_token>`：

| 接口 | 功能 |
| --- | --- |
| `POST /api/v1/chats` | 创建会话，标题可选 |
| `GET /api/v1/chats?limit=50&offset=0` | 按最近活跃时间列出当前用户的会话 |
| `GET /api/v1/chats/{id}/messages?limit=50&before_id=...` | 查询消息，返回 `items` 和 `next_before_id` |
| `POST /api/v1/chats/{id}/messages` | 保存一条用户消息，返回保存后的消息 |
| `POST /api/v1/chats/{id}/turns` | 请求独立 RAG 服务、调用 DeepSeek、原子保存用户消息和 AI 回复，返回 `user_message`、`assistant_message` |
| `POST /api/v1/chats/{id}/turns/stream` | 流式回答，返回 `text/event-stream`；先后发送 `status`、`delta`、`done` 事件，失败时发送 `error` |
| `DELETE /api/v1/chats/{id}` | 删除会话及其消息 |

RAG 服务使用现有 `medical_qa_v2` 集合的 `question_embedding` 向量，以及 `answer_chunk` 等字段。AI 回复的 `sources` 字段保存了本次检索到的片段，前端会展示来源。流式接口使用 POST 和 Bearer token；`status` 的 `phase` 为 `retrieving` 或 `generating`，`delta` 包含 `text`，`done` 返回保存后的 `user_message`、`assistant_message`。检索或模型调用失败时不会保存半截对话，前端保留输入供重试。上下文压缩尚未接入。


cd /mnt/yp2/MedCore
/mnt/yp2/RAG/.venv/bin/python -m rag_service

cd /mnt/yp2/MedCore/medcore
PYTHONPATH=src .venv/bin/uvicorn medcore.main:app --host 127.0.0.1 --port 8089 --reload
