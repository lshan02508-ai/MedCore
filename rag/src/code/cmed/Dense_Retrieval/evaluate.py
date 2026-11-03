"""使用 CMedTEB test_queries 和 qrels 评测 Dense Retrieval。"""

from __future__ import annotations

import argparse
import json
import math
from collections import defaultdict
from pathlib import Path
from statistics import fmean
from time import perf_counter

from retriever import (
    CMedDenseRetriever,
    DEFAULT_COLLECTION,
    DEFAULT_MODEL,
    DEFAULT_URI,
    ROOT,
)


DEFAULT_QUERIES = ROOT / "src/data/CMedTEB/retrieval/test_queries.jsonl"
DEFAULT_QRELS = ROOT / "src/data/CMedTEB/retrieval/test_qrels.jsonl"
DEFAULT_SUMMARY = ROOT / "outputs/cmed_retrieval_summary.json"
METRIC_KS = (1, 5, 10, 20, 50, 100)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="评测 CMedTEB BGE-M3 + Milvus Dense Retrieval。"
    )
    parser.add_argument("--queries", type=Path, default=DEFAULT_QUERIES)
    parser.add_argument("--qrels", type=Path, default=DEFAULT_QRELS)
    parser.add_argument("--model", type=Path, default=DEFAULT_MODEL)
    parser.add_argument("--uri", default=DEFAULT_URI)
    parser.add_argument("--collection", default=DEFAULT_COLLECTION)
    parser.add_argument("--device", default=None)
    parser.add_argument("--query-batch-size", type=int, default=16)
    parser.add_argument("--embedding-batch-size", type=int, default=32)
    parser.add_argument("--chunk-top-k", type=int, default=200)
    parser.add_argument("--parent-top-k", type=int, default=100)
    parser.add_argument("--limit", type=int, default=None)
    parser.add_argument("--summary", type=Path, default=DEFAULT_SUMMARY)
    parser.add_argument(
        "--details",
        type=Path,
        default=None,
        help="可选：保存逐查询明细 JSONL；默认不生成。",
    )
    return parser.parse_args()


def validate_args(args: argparse.Namespace) -> None:
    for name in (
        "query_batch_size",
        "embedding_batch_size",
        "chunk_top_k",
        "parent_top_k",
    ):
        if getattr(args, name) < 1:
            raise ValueError(f"--{name.replace('_', '-')} 必须为正整数")
    if args.limit is not None and args.limit < 1:
        raise ValueError("--limit 必须为正整数")
    if args.chunk_top_k < args.parent_top_k:
        raise ValueError("--chunk-top-k 不能小于 --parent-top-k")
    if args.parent_top_k < max(METRIC_KS):
        raise ValueError(
            f"--parent-top-k 至少为 {max(METRIC_KS)}，否则无法计算全部指标"
        )
    for path in (args.queries, args.qrels):
        if not path.is_file():
            raise FileNotFoundError(path)
    if not args.model.exists():
        raise FileNotFoundError(args.model)


def load_queries(path: Path, limit: int | None) -> list[dict[str, str]]:
    rows: list[dict[str, str]] = []
    seen: set[str] = set()
    with path.open(encoding="utf-8") as source:
        for line_number, line in enumerate(source, start=1):
            if not line.strip():
                continue
            try:
                row = json.loads(line)
                qid = str(row["id"])
                query = str(row["text"]).strip()
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
                row = json.loads(line)
                if float(row["relevance"]) > 0:
                    relevant[str(row["qid"])].add(str(row["docid"]))
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
            for rank, docid in enumerate(top, start=1)
            if docid in relevant
        )
        ideal_count = min(len(relevant), k)
        idcg = sum(
            1.0 / math.log2(rank + 1)
            for rank in range(1, ideal_count + 1)
        )
        metrics[f"ndcg@{k}"] = dcg / idcg

        reciprocal_rank = 0.0
        for rank, docid in enumerate(top, start=1):
            if docid in relevant:
                reciprocal_rank = 1.0 / rank
                break
        metrics[f"mrr@{k}"] = reciprocal_rank

    relevant_seen = 0
    precision_sum = 0.0
    for rank, docid in enumerate(retrieved[:100], start=1):
        if docid in relevant:
            relevant_seen += 1
            precision_sum += relevant_seen / rank
    metrics["map@100"] = precision_sum / min(len(relevant), 100)
    return metrics


def run_evaluation(args: argparse.Namespace) -> dict:
    queries = load_queries(args.queries, args.limit)
    qrels = load_qrels(args.qrels)
    missing = [row["qid"] for row in queries if not qrels.get(row["qid"])]
    if missing:
        raise RuntimeError(f"查询没有正相关文档：qid={missing[0]}")

    args.summary.parent.mkdir(parents=True, exist_ok=True)
    details_file = None
    if args.details:
        args.details.parent.mkdir(parents=True, exist_ok=True)
        details_file = args.details.open("w", encoding="utf-8")

    metric_values: dict[str, list[float]] = defaultdict(list)
    encode_seconds = 0.0
    search_seconds = 0.0
    insufficient_results = 0
    evaluated = 0
    started = perf_counter()

    try:
        with CMedDenseRetriever(
            model_path=args.model,
            uri=args.uri,
            collection=args.collection,
            device=args.device,
        ) as retriever:
            for start in range(0, len(queries), args.query_batch_size):
                batch = queries[start : start + args.query_batch_size]
                results, timing = retriever.search_batch(
                    [row["query"] for row in batch],
                    chunk_top_k=args.chunk_top_k,
                    parent_top_k=args.parent_top_k,
                    embedding_batch_size=args.embedding_batch_size,
                    include_text=False,
                )
                encode_seconds += timing.encode_seconds
                search_seconds += timing.search_seconds

                for query_row, parents in zip(batch, results):
                    qid = query_row["qid"]
                    relevant = qrels[qid]
                    retrieved = [row["parent_doc_id"] for row in parents]
                    metrics = query_metrics(retrieved, relevant)
                    for name, value in metrics.items():
                        metric_values[name].append(value)
                    insufficient_results += int(len(parents) < args.parent_top_k)
                    evaluated += 1

                    if details_file:
                        details_file.write(
                            json.dumps(
                                {
                                    "qid": qid,
                                    "query": query_row["query"],
                                    "relevant_doc_ids": sorted(relevant),
                                    "metrics": metrics,
                                    "retrieved": [
                                        {
                                            **row,
                                            "is_relevant": row["parent_doc_id"]
                                            in relevant,
                                        }
                                        for row in parents
                                    ],
                                },
                                ensure_ascii=False,
                            )
                            + "\n"
                        )
                print(f"已评测 {evaluated}/{len(queries)}", end="\r", flush=True)
    finally:
        if details_file:
            details_file.close()

    elapsed = perf_counter() - started
    summary = {
        "queries": len(queries),
        "collection": args.collection,
        "chunk_top_k": args.chunk_top_k,
        "parent_top_k": args.parent_top_k,
        "metrics": {
            name: fmean(values)
            for name, values in sorted(metric_values.items())
        },
        "performance": {
            "elapsed_seconds": elapsed,
            "encode_seconds": encode_seconds,
            "milvus_search_seconds": search_seconds,
            "queries_per_second": len(queries) / elapsed,
        },
        "diagnostics": {
            "queries_with_fewer_than_parent_top_k_results": insufficient_results,
        },
    }
    if args.details:
        summary["details_path"] = str(args.details)
    args.summary.write_text(
        json.dumps(summary, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    print(" " * 80, end="\r")
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return summary


def main() -> None:
    args = parse_args()
    validate_args(args)
    run_evaluation(args)


if __name__ == "__main__":
    main()
