from langchain_core.tools import tool

from medcore.rag.pipeline import retrieve


@tool
def search_local_rag(query: str) -> list[dict]:
    """检索本地医学知识库。query 应是结合对话历史、消除指代后的完整问题。"""
    query = query.strip()
    if not query:
        raise ValueError("检索问题不能为空")

    return retrieve(query)

@tool
def search_web_knowledge(query: str) -> list[dict]:
    """联网搜索近期指南、药品信息或本地知识库无法覆盖的医学资料。"""
    query = query.strip()
    if not query:
        raise ValueError("搜索问题不能为空")

    from medcore.capabilities.web_search.web_search import search_web

    return search_web(query, max_results=5)


EDUCATION_TOOLS = [search_local_rag, search_web_knowledge]
