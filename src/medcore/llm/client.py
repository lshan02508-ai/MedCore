"""Model transport through the OpenAI-compatible DeepSeek API."""

import os
from collections.abc import Iterator

from langchain_core.tools import BaseTool
from langchain_core.utils.function_calling import convert_to_openai_tool

def _api_key() -> str:
    api_key = os.getenv("DEEPSEEK_API_KEY", "").strip()
    if not api_key or api_key == "your_deepseek_api_key":
        raise RuntimeError("尚未配置 DEEPSEEK_API_KEY")
    return api_key


def _client():
    from openai import OpenAI

    return OpenAI(
        api_key=_api_key(),
        base_url=os.getenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
        timeout=60.0,
        max_retries=1,
    )


def generate_answer(messages: list[dict[str, str]]) -> str:
    with _client() as client:
        response = client.chat.completions.create(
            model=os.getenv("DEEPSEEK_MODEL", "deepseek-flash"),
            messages=messages, # type: ignore
            stream=False,
        ) # type: ignore
    answer = response.choices[0].message.content
    if not answer or not answer.strip():
        raise RuntimeError("模型返回了空回复")
    return answer.strip()


def stream_answer(messages: list[dict[str, str]]) -> Iterator[str]:
    with _client() as client:
        stream = client.chat.completions.create(
            model=os.getenv("DEEPSEEK_MODEL", "deepseek-flash"),
            messages=messages, # type: ignore
            stream=True,
        ) # type: ignore
        for chunk in stream:
            if chunk.choices:
                text = chunk.choices[0].delta.content
                if text:
                    yield text


def choose_tools(
    messages: list[dict[str, str]], tools: list[BaseTool]
) -> list[dict[str, str]]:
    """Ask the model which search tools to use; execution stays in the workflow."""
    with _client() as client:
        response = client.chat.completions.create(
            model=os.getenv("DEEPSEEK_TOOL_MODEL", os.getenv("DEEPSEEK_MODEL", "deepseek-flash")),
            messages=messages,  # type: ignore[arg-type]
            tools=[convert_to_openai_tool(tool) for tool in tools],  # type: ignore[arg-type]
            tool_choice="auto",
            stream=False,
        )
    calls = response.choices[0].message.tool_calls or []
    return [
        {
            "name": call.function.name,
            "arguments": call.function.arguments,
        }
        for call in calls
    ]
