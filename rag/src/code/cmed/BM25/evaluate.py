"""使用 CMedTEB test_queries/test_qrels 评测 BM25。"""

from __future__ import annotations

import argparse
import json
import math
from collections import defaultdict
from pathlib import Path
from statistics import fmean
from time import perf_counter

from bm25_index import BM25Index, DEFAULT_INDEX_DIR, ROOT


DEFAULT_QUERIES = ROOT / "src/data/CMedTEB/retrieval/test_queries.jsonl"
DEFAULT_QRELS = ROOT / "src/data/CMedTEB/retrieval/test_qrels.jsonl"
DEFAULT_SUMMARY = ROOT / "outputs/cmed_bm25_retrieval_summary.json"
METRIC_KS = (1, 5, 10, 20, 50, 100)


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
    for rank, doc_id in enumerate(retrieved[:100], start=1):
        if doc_id in relevant:
            relevant_seen += 1
            precision_sum += relevant_seen / rank
    metrics["map@100"] = precision_sum / min(len(relevant), 100)
    return metrics


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="评测 CMedTEB BM25 检索。")
    parser.add_argument("--queries", type=Path, default=DEFAULT_QUERIES)
    parser.add_argument("--qrels", type=Path, default=DEFAULT_QRELS)
    parser.add_argument("--index-dir", type=Path, default=DEFAULT_INDEX_DIR)
    parser.add_argument("--top-k", type=int, default=100)
    parser.add_argument("--limit", type=int, default=None)
    parser.add_argument("--summary", type=Path, default=DEFAULT_SUMMARY)
    parser.add_argument(
        "--details",
        type=Path,
        default=None,
        help="可选：保存逐查询明细 JSONL；默认不生成。",
    )
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if args.top_k < max(METRIC_KS):
        raise ValueError(f"--top-k 至少为 {max(METRIC_KS)}")
    if args.limit is not None and args.limit < 1:
        raise ValueError("--limit 必须为正整数")
    for path in (args.queries, args.qrels):
        if not path.is_file():
            raise FileNotFoundError(path)

    queries = load_queries(args.queries, args.limit)
    qrels = load_qrels(args.qrels)
    missing = [row["qid"] for row in queries if not qrels.get(row["qid"])]
    if missing:
        raise RuntimeError(f"查询没有正相关文档：qid={missing[0]}")

    index = BM25Index(args.index_dir)
    if index.metadata.get("limited_corpus"):
        raise RuntimeError("当前索引由 --limit 构建，不能用于正式评测")

    args.summary.parent.mkdir(parents=True, exist_ok=True)
    details_file = None
    if args.details is not None:
        args.details.parent.mkdir(parents=True, exist_ok=True)
        details_file = args.details.open("w", encoding="utf-8")

    metric_values: dict[str, list[float]] = defaultdict(list)
    insufficient_results = 0
    search_seconds = 0.0
    started = perf_counter()
    try:
        for number, row in enumerate(queries, start=1):
            search_started = perf_counter()
            hits = index.search(row["query"], args.top_k)
            search_seconds += perf_counter() - search_started
            retrieved = [hit.doc_id for hit in hits]
            metrics = query_metrics(retrieved, qrels[row["qid"]])
            for name, value in metrics.items():
                metric_values[name].append(value)
            insufficient_results += int(len(hits) < args.top_k)

            if details_file is not None:
                details_file.write(
                    json.dumps(
                        {
                            "qid": row["qid"],
                            "query": row["query"],
                            "relevant_doc_ids": sorted(qrels[row["qid"]]),
                            "metrics": metrics,
                            "retrieved": [
                                {
                                    "rank": hit.rank,
                                    "doc_id": hit.doc_id,
                                    "score": hit.score,
                                    "is_relevant": hit.doc_id in qrels[row["qid"]],
                                }
                                for hit in hits
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
    summary = {
        "queries": len(queries),
        "retriever": "BM25 character n-gram",
        "index_dir": str(args.index_dir.resolve()),
        "top_k": args.top_k,
        "index": {
            key: index.metadata[key]
            for key in (
                "documents",
                "terms",
                "ngram_range",
                "min_df",
                "max_features",
                "k1",
                "b",
                "average_document_length",
            )
        },
        "metrics": {
            name: fmean(values)
            for name, values in sorted(metric_values.items())
        },
        "performance": {
            "elapsed_seconds": elapsed,
            "search_seconds": search_seconds,
            "queries_per_second": len(queries) / elapsed,
        },
        "diagnostics": {
            "queries_with_fewer_than_top_k_results": insufficient_results,
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
