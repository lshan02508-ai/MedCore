import json

from langgraph.config import get_stream_writer
from langgraph.graph import END, START, StateGraph

from medcore.llm.client import choose_tools, stream_answer
from medcore.rag.pipeline import format_evidence
from medcore.workflows.education.prompts import (
    build_education_messages,
    build_tool_selection_messages,
)
from medcore.workflows.education.state import EducationState
from medcore.workflows.education.tools import EDUCATION_TOOLS


def decide_node(state: EducationState) -> dict:
    writer = get_stream_writer()
    writer({"name": "status", "payload": {"phase": "planning"}})
    calls = choose_tools(
        build_tool_selection_messages(state["question"], state["history"]),
        EDUCATION_TOOLS,
    )
    if len(calls) > 2:
        raise RuntimeError("单轮最多调用两个检索工具")
    return {"tool_calls": calls}


def _normalize_source(tool_name: str, item: dict, number: int) -> dict | None:
    if tool_name == "search_local_rag":
        excerpt = str(item.get("excerpt") or "").strip()
        if not excerpt:
            return None
        return {**item, "number": number, "excerpt": excerpt, "source_type": "rag"}
    if tool_name == "search_web_knowledge":
        excerpt = str(item.get("content") or "").strip()
        url = str(item.get("url") or "").strip()
        if not excerpt or not url:
            return None
        title = str(item.get("title") or url).strip()
        return {
            "number": number,
            "source": title,
            "question": title,
            "excerpt": excerpt[:1200],
            "url": url,
            "source_type": "web",
        }
    raise RuntimeError(f"未知检索工具：{tool_name}")


def execute_tools_node(state: EducationState) -> dict:
    writer = get_stream_writer()
    writer({"name": "status", "payload": {"phase": "retrieving"}})
    tools_by_name = {tool.name: tool for tool in EDUCATION_TOOLS}
    sources = []
    for call in state["tool_calls"]:
        name = call["name"]
        tool = tools_by_name.get(name)
        if tool is None:
            raise RuntimeError(f"模型选择了未知工具：{name}")
        try:
            arguments = json.loads(call["arguments"])
        except (TypeError, ValueError) as exc:
            raise RuntimeError("模型返回了无效的工具参数") from exc
        if not isinstance(arguments, dict):
            raise RuntimeError("工具参数必须是对象")
        query = arguments.get("query")
        if not isinstance(query, str) or not query.strip():
            raise RuntimeError("模型没有生成有效的检索问题")
        results = tool.invoke({"query": query.strip()})
        if not isinstance(results, list):
            raise RuntimeError("检索工具返回格式错误")
        for item in results:
            if not isinstance(item, dict):
                continue
            source = _normalize_source(name, item, len(sources) + 1)
            if source is not None:
                sources.append(source)
    return {"sources": sources}


def answer_node(state: EducationState) -> dict:
    writer = get_stream_writer()
    writer({"name": "status", "payload": {"phase": "generating"}})

    context = format_evidence(state.get("sources", []))
    parts = []
    messages = build_education_messages(state["question"], state["history"], context)
    for text in stream_answer(messages):
        parts.append(text)
        writer({"name": "delta", "payload": {"text": text}})

    answer = "".join(parts).strip()
    if not answer:
        raise RuntimeError("模型返回了空回复")
    return {"answer": answer}


def build_education_graph():
    builder = StateGraph(EducationState)
    builder.add_node("decide", decide_node)
    builder.add_node("tools", execute_tools_node)
    builder.add_node("answer", answer_node)
    builder.add_edge(START, "decide")
    builder.add_conditional_edges(
        "decide", lambda state: "tools" if state["tool_calls"] else "answer"
    )
    builder.add_edge("tools", "answer")
    builder.add_edge("answer", END)
    return builder.compile()
