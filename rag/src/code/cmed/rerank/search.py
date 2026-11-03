"""Dense 召回后使用交叉编码器重排一条查询。"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path


HERE = Path(__file__).resolve().parent
DENSE_DIR = HERE.parent / "Dense_Retrieval"
sys.path.insert(0, str(DENSE_DIR))

from retriever import (  # noqa: E402
    CMedDenseRetriever,
    DEFAULT_COLLECTION,
    DEFAULT_MODEL,
    DEFAULT_URI,
)

from reranker import CMedReranker, DEFAULT_RERANKER_MODEL  # noqa: E402


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="CMedTEB Dense + Reranker 检索。")
    parser.add_argument("--query", required=True)
    parser.add_argument("--dense-model", type=Path, default=DEFAULT_MODEL)
    parser.add_argument("--reranker-model", type=Path, default=DEFAULT_RERANKER_MODEL)
    parser.add_argument("--uri", default=DEFAULT_URI)
    parser.add_argument("--collection", default=DEFAULT_COLLECTION)
    parser.add_argument("--device", default=None)
    parser.add_argument("--chunk-top-k", type=int, default=50)
    parser.add_argument("--candidate-top-k", type=int, default=15)
    parser.add_argument("--rerank-top-k", type=int, default=15)
    parser.add_argument("--top-k", type=int, default=10)
    parser.add_argument("--embedding-batch-size", type=int, default=32)
    parser.add_argument("--rerank-batch-size", type=int, default=16)
    parser.add_argument("--max-length", type=int, default=512)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if not (
        1
        <= args.top_k
        <= args.rerank_top_k
        <= args.candidate_top_k
        <= args.chunk_top_k
    ):
        raise ValueError(
            "要求 1 <= top-k <= rerank-top-k <= "
            "candidate-top-k <= chunk-top-k"
        )

    reranker = CMedReranker(
        args.reranker_model,
        device=args.device,
        max_length=args.max_length,
    )
    with CMedDenseRetriever(
        model_path=args.dense_model,
        uri=args.uri,
        collection=args.collection,
        device=args.device,
    ) as retriever:
        results, retrieval_timing = retriever.search_batch(
            [args.query],
            chunk_top_k=args.chunk_top_k,
            parent_top_k=args.candidate_top_k,
            embedding_batch_size=args.embedding_batch_size,
            include_text=True,
        )
    ranked, rerank_timing = reranker.rerank_top(
        args.query,
        results[0],
        rerank_top_k=args.rerank_top_k,
        batch_size=args.rerank_batch_size,
    )

    print(f"\n问题：{args.query}")
    print(
        f"Dense编码={retrieval_timing.encode_seconds:.3f}s | "
        f"Milvus={retrieval_timing.search_seconds:.3f}s | "
        f"Rerank={rerank_timing.seconds:.3f}s"
    )
    for row in ranked[: args.top_k]:
        print(
            f"\nTop {row['rank']} | rerank={row['rerank_score']:.6f} | "
            f"dense={row['retrieval_score']:.6f} | "
            f"原始排名={row['retrieval_rank']} | parent={row['parent_doc_id']}"
        )
        print(row["text"])


if __name__ == "__main__":
    main()
