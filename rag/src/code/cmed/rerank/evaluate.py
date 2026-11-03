"""在同一候选集上比较 Dense 与 Dense + Reranker。"""

from __future__ import annotations

import argparse
import json
import math
import sys
from collections import defaultdict
from pathlib import Path
from statistics import fmean
from time import perf_counter


HERE = Path(__file__).resolve().parent
DENSE_DIR = HERE.parent / "Dense_Retrieval"
sys.path.insert(0, str(DENSE_DIR))

from retriever import (  # noqa: E402
    CMedDenseRetriever,
    DEFAULT_COLLECTION,
    DEFAULT_MODEL,
    DEFAULT_URI,
    ROOT,
)

from reranker import CMedReranker, DEFAULT_RERANKER_MODEL  # noqa: E402


DEFAULT_QUERIES = ROOT / "src/data/CMedTEB/retrieval/test_queries.jsonl"
DEFAULT_QRELS = ROOT / "src/data/CMedTEB/retrieval/test_qrels.jsonl"
DEFAULT_SUMMARY = ROOT / "outputs/cmed_rerank_summary.json"
METRIC_KS = (1, 5, 10, 15)
MAP_K = 15


def load_queries(path: Path, limit: int | None) -> list[dict[str, str]]:
    rows: list[dict[str, str]] = []
    seen: set[str] = set()
    with path.open(encoding="utf-8") as source:
        for line_number, line in enumerate(source, start=1):
            if not line.strip():
                continue
            try:
                raw = json.loads(line)
                qid = str(raw["id"])
                query = str(raw["text"]).strip()
            except (json.JSONDecodeError, KeyError, TypeError) as exc:
                raise ValueError(f"无效查询：{path}:{line_number}: {exc}") from exc
            if not qid or not query or qid in seen:
                raise ValueError(f"查询为空或 qid 重复：{path}:{line_number}")
            seen.add(qid)
            rows.append({"qid": qid, "query": query})
            if limit is not None and len(rows) >= limit:
                break
    if not rows:
        raise ValueError(f"没有读取到查询：{path}")
    return rows


def load_qrels(path: Path) -> dict[str, set[str]]:
    relevant: dict[str, set[str]] = defaultdict(set)
    with path.open(encoding="utf-8") as source:
        for line_number, line in enumerate(source, start=1):
            if not line.strip():
                continue
            try:
                raw = json.loads(line)
                if float(raw["relevance"]) > 0:
                    relevant[str(raw["qid"])].add(str(raw["docid"]))
            except (json.JSONDecodeError, KeyError, TypeError, ValueError) as exc:
                raise ValueError(f"无效 qrels：{path}:{line_number}: {exc}") from exc
    return dict(relevant)


def query_metrics(retrieved: list[str], relevant: set[str]) -> dict[str, float]:
    if not relevant:
        raise ValueError("评测查询没有正相关文档")
    metrics: dict[str, float] = {}
    for k in METRIC_KS:
        top = retrieved[:k]
        hits = len(set(top) & relevant)
        metrics[f"recall@{k}"] = hits / len(relevant)
        metrics[f"precision@{k}"] = hits / k
        metrics[f"hit_rate@{k}"] = float(hits > 0)

        dcg = sum(
            1.0 / math.log2(rank + 1)
            for rank, doc_id in enumerate(top, start=1)
            if doc_id in relevant
        )
        ideal_count = min(len(relevant), k)
        idcg = sum(
            1.0 / math.log2(rank + 1)
            for rank in range(1, ideal_count + 1)
        )
        metrics[f"ndcg@{k}"] = dcg / idcg
        metrics[f"mrr@{k}"] = next(
            (
                1.0 / rank
                for rank, doc_id in enumerate(top, start=1)
                if doc_id in relevant
            ),
            0.0,
        )

    relevant_seen = 0
    precision_sum = 0.0
    for rank, doc_id in enumerate(retrieved[:MAP_K], start=1):
        if doc_id in relevant:
            relevant_seen += 1
            precision_sum += relevant_seen / rank
    metrics[f"map@{MAP_K}"] = precision_sum / min(len(relevant), MAP_K)
    return metrics


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="评测 Dense + BGE Reranker。")
    parser.add_argument("--queries", type=Path, default=DEFAULT_QUERIES)
    parser.add_argument("--qrels", type=Path, default=DEFAULT_QRELS)
    parser.add_argument("--dense-model", type=Path, default=DEFAULT_MODEL)
    parser.add_argument("--reranker-model", type=Path, default=DEFAULT_RERANKER_MODEL)
    parser.add_argument("--uri", default=DEFAULT_URI)
    parser.add_argument("--collection", default=DEFAULT_COLLECTION)
    parser.add_argument("--device", default=None)
    parser.add_argument("--chunk-top-k", type=int, default=50)
    parser.add_argument("--candidate-top-k", type=int, default=15)
    parser.add_argument("--rerank-top-k", type=int, default=15)
    parser.add_argument("--embedding-batch-size", type=int, default=32)
    parser.add_argument("--rerank-batch-size", type=int, default=16)
    parser.add_argument("--max-length", type=int, default=512)
    parser.add_argument("--limit", type=int, default=None)
    parser.add_argument("--summary", type=Path, default=DEFAULT_SUMMARY)
    parser.add_argument("--details", type=Path, default=None)
    return parser.parse_args()


def validate_args(args: argparse.Namespace) -> None:
    if args.candidate_top_k < max(METRIC_KS):
        raise ValueError(f"--candidate-top-k 至少为 {max(METRIC_KS)}")
    if args.chunk_top_k < args.candidate_top_k:
        raise ValueError("--chunk-top-k 不能小于 --candidate-top-k")
    if not 1 <= args.rerank_top_k <= args.candidate_top_k:
        raise ValueError("要求 1 <= rerank-top-k <= candidate-top-k")
    for name in ("embedding_batch_size", "rerank_batch_size", "max_length"):
        if getattr(args, name) < 1:
            raise ValueError(f"--{name.replace('_', '-')} 必须为正整数")
    if args.limit is not None and args.limit < 1:
        raise ValueError("--limit 必须为正整数")
    for path in (args.queries, args.qrels, args.dense_model, args.reranker_model):
        if not path.exists():
            raise FileNotFoundError(path)


def main() -> None:
    args = parse_args()
    validate_args(args)
    queries = load_queries(args.queries, args.limit)
    qrels = load_qrels(args.qrels)
    missing = [row["qid"] for row in queries if not qrels.get(row["qid"])]
    if missing:
        raise RuntimeError(f"查询没有正相关文档：qid={missing[0]}")

    args.summary.parent.mkdir(parents=True, exist_ok=True)
    details_file = None
    if args.details is not None:
        args.details.parent.mkdir(parents=True, exist_ok=True)
        details_file = args.details.open("w", encoding="utf-8")

    before_values: dict[str, list[float]] = defaultdict(list)
    after_values: dict[str, list[float]] = defaultdict(list)
    encode_seconds = 0.0
    search_seconds = 0.0
    rerank_seconds = 0.0
    reranked_pairs = 0
    started = perf_counter()

    reranker = CMedReranker(
        args.reranker_model,
        device=args.device,
        max_length=args.max_length,
    )
    try:
        with CMedDenseRetriever(
            model_path=args.dense_model,
            uri=args.uri,
            collection=args.collection,
            device=args.device,
        ) as retriever:
            for number, row in enumerate(queries, start=1):
                candidates_batch, retrieval_timing = retriever.search_batch(
                    [row["query"]],
                    chunk_top_k=args.chunk_top_k,
                    parent_top_k=args.candidate_top_k,
                    embedding_batch_size=args.embedding_batch_size,
                    include_text=True,
                )
                candidates = candidates_batch[0]
                ranked, rerank_timing = reranker.rerank_top(
                    row["query"],
                    candidates,
                    rerank_top_k=args.rerank_top_k,
                    batch_size=args.rerank_batch_size,
                )
                encode_seconds += retrieval_timing.encode_seconds
                search_seconds += retrieval_timing.search_seconds
                rerank_seconds += rerank_timing.seconds
                reranked_pairs += rerank_timing.pairs

                relevant = qrels[row["qid"]]
                before = query_metrics(
                    [item["parent_doc_id"] for item in candidates], relevant
                )
                after = query_metrics(
                    [item["parent_doc_id"] for item in ranked], relevant
                )
                for name, value in before.items():
                    before_values[name].append(value)
                for name, value in after.items():
                    after_values[name].append(value)

                if details_file is not None:
                    details_file.write(
                        json.dumps(
                            {
                                "qid": row["qid"],
                                "query": row["query"],
                                "relevant_doc_ids": sorted(relevant),
                                "dense_metrics": before,
                                "rerank_metrics": after,
                                "reranked": [
                                    {
                                        "rank": item["rank"],
                                        "parent_doc_id": item["parent_doc_id"],
                                        "rerank_score": item["rerank_score"],
                                        "retrieval_rank": item["retrieval_rank"],
                                        "retrieval_score": item["retrieval_score"],
                                        "is_relevant": item["parent_doc_id"] in relevant,
                                    }
                                    for item in ranked
                                ],
                            },
                            ensure_ascii=False,
                        )
                        + "\n"
                    )
                print(f"已评测 {number}/{len(queries)}", end="\r", flush=True)
    finally:
        if details_file is not None:
            details_file.close()

    elapsed = perf_counter() - started
    dense_metrics = {
        name: fmean(values) for name, values in sorted(before_values.items())
    }
    rerank_metrics = {
        name: fmean(values) for name, values in sorted(after_values.items())
    }
    summary = {
        "queries": len(queries),
        "pipeline": "BGE-M3 Dense -> BGE Reranker v2 M3",
        "collection": args.collection,
        "chunk_top_k": args.chunk_top_k,
        "candidate_top_k": args.candidate_top_k,
        "rerank_top_k": args.rerank_top_k,
        "max_length": args.max_length,
        "dense_metrics": dense_metrics,
        "rerank_metrics": rerank_metrics,
        "metric_deltas": {
            name: rerank_metrics[name] - dense_metrics[name]
            for name in dense_metrics
        },
        "performance": {
            "elapsed_seconds": elapsed,
            "dense_encode_seconds": encode_seconds,
            "milvus_search_seconds": search_seconds,
            "rerank_seconds": rerank_seconds,
            "reranked_pairs": reranked_pairs,
            "queries_per_second": len(queries) / elapsed,
        },
    }
    if args.details is not None:
        summary["details_path"] = str(args.details.resolve())
    args.summary.write_text(
        json.dumps(summary, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    print(" " * 80, end="\r")
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
