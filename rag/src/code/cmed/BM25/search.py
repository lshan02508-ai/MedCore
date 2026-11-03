"""交互测试 CMedTEB BM25 检索结果。"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from bm25_index import BM25Index, DEFAULT_CORPUS, DEFAULT_INDEX_DIR


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="查询 CMedTEB BM25 索引。")
    parser.add_argument("query", help="中文查询文本")
    parser.add_argument("--top-k", type=int, default=10)
    parser.add_argument("--index-dir", type=Path, default=DEFAULT_INDEX_DIR)
    parser.add_argument("--corpus", type=Path, default=DEFAULT_CORPUS)
    parser.add_argument("--no-text", action="store_true", help="不读取并展示原文")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    index = BM25Index(args.index_dir, args.corpus)
    hits = index.search(args.query, args.top_k)
    texts = (
        {}
        if args.no_text
        else index.read_texts([hit.row_index for hit in hits])
    )
    result = {
        "query": args.query,
        "top_k": args.top_k,
        "hits": [
            {
                "rank": hit.rank,
                "doc_id": hit.doc_id,
                "score": hit.score,
                **({"text": texts[hit.row_index]} if not args.no_text else {}),
            }
            for hit in hits
        ],
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
