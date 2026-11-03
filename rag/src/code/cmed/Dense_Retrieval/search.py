"""CMedTEB 单条和交互式 Dense Retrieval。"""

from __future__ import annotations

import argparse
from pathlib import Path

from retriever import (
    CMedDenseRetriever,
    DEFAULT_COLLECTION,
    DEFAULT_MODEL,
    DEFAULT_URI,
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="CMedTEB Dense Retrieval 检索。")
    parser.add_argument("--query", default=None)
    parser.add_argument("--model", type=Path, default=DEFAULT_MODEL)
    parser.add_argument("--uri", default=DEFAULT_URI)
    parser.add_argument("--collection", default=DEFAULT_COLLECTION)
    parser.add_argument("--device", default=None)
    parser.add_argument("--chunk-top-k", type=int, default=200)
    parser.add_argument("--top-k", type=int, default=10)
    parser.add_argument("--embedding-batch-size", type=int, default=32)
    return parser.parse_args()


def print_results(query: str, results: list[dict]) -> None:
    print(f"\n问题：{query}")
    for row in results:
        print(
            f"\nTop {row['rank']} | score={row['score']:.6f} | "
            f"parent={row['parent_doc_id']} | chunk={row['chunk_id']}"
        )
        print(row.get("text", ""))


def search_once(
    retriever: CMedDenseRetriever,
    query: str,
    args: argparse.Namespace,
) -> None:
    results, _ = retriever.search_batch(
        [query],
        chunk_top_k=args.chunk_top_k,
        parent_top_k=args.top_k,
        embedding_batch_size=args.embedding_batch_size,
        include_text=True,
    )
    print_results(query, results[0])


def main() -> None:
    args = parse_args()
    if args.top_k < 1 or args.chunk_top_k < args.top_k:
        raise ValueError("必须满足 1 <= top-k <= chunk-top-k")

    with CMedDenseRetriever(
        model_path=args.model,
        uri=args.uri,
        collection=args.collection,
        device=args.device,
    ) as retriever:
        if args.query:
            search_once(retriever, args.query, args)
            return

        while True:
            query = input("\n请输入问题（q 退出）：").strip()
            if query.lower() == "q":
                break
            if query:
                search_once(retriever, query, args)


if __name__ == "__main__":
    main()
