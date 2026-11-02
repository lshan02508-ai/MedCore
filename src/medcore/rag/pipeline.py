"""HTTP client for the standalone RAG retrieval service."""

import json
import os
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


def retrieve(question: str) -> list[dict]:
    url = os.getenv("RAG_SERVICE_URL", "http://127.0.0.1:8091").rstrip("/")
    body = json.dumps({
        "question": question,
        "top_k": max(1, min(int(os.getenv("RAG_TOP_K", "5")), 10)),
    }).encode("utf-8")
    headers = {"Content-Type": "application/json"}
    token = os.getenv("RAG_SERVICE_TOKEN", "").strip()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = Request(f"{url}/retrieve", data=body, headers=headers, method="POST")
    try:
        with urlopen(request, timeout=90) as response:
            payload = json.load(response)
    except (HTTPError, URLError, TimeoutError, ValueError) as exc:
        raise RuntimeError("RAG 检索服务不可用") from exc
    sources = payload.get("sources") if isinstance(payload, dict) else None
    if not isinstance(sources, list):
        raise RuntimeError("RAG 检索服务返回格式错误")
    return sources


def format_evidence(sources: list[dict]) -> str:
    chunks = []
    for source in sources:
        url = f"；网址：{source['url']}" if source.get("url") else ""
        chunks.append(
            f"[{source['number']}] 来源：{source.get('source') or '未标注'}；"
            f"相关问题：{source.get('question', '')}{url}\n{source['excerpt']}"
        )
    return "\n\n".join(chunks)
