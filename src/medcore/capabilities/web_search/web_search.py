# To install: pip install tavily-python
import os
from dotenv import load_dotenv
from tavily import TavilyClient
load_dotenv()  # Load environment variables from .env file
api_key = os.getenv("TAVILY_API_KEY")
client = TavilyClient(api_key)

def search_web(query: str,max_results: int =5) -> list[dict[str,str]]:
    query = query.strip()
    if not query:
        raise ValueError("Query cannot be empty.")
    if not 1<=max_results<=10:
        raise ValueError("max_results must be between 1 and 10.")
    
    try:
        response = client.search(
            query=query,
            max_results=max_results,
            search_depth="basic",
            timeout=10  # Set a timeout of 10 seconds
        )
    except Exception as e:
        raise RuntimeError("联网搜索不可用") from e
    results = []
    for item in response.get("results", []):
        url = item.get("url", "").strip()
        content = item.get("content", "").strip()
        if url and content:
            results.append({
                "title": item.get("title", "").strip() or url,
                "url": url,
                "content": content,
            })
    return results