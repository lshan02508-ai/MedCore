"""CMedTEB 中文字符 n-gram BM25 索引。"""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path
from time import perf_counter
from typing import Iterator

import joblib
import numpy as np
from scipy import sparse
from sklearn.feature_extraction.text import CountVectorizer


ROOT = Path(__file__).resolve().parents[4]
DEFAULT_CORPUS = ROOT / "src/data/CMedTEB/retrieval/corpus.jsonl"
DEFAULT_INDEX_DIR = ROOT / "indexes/cmedteb_bm25_char_2_3"


@dataclass(frozen=True)
class SearchHit:
    rank: int
    doc_id: str
    score: float
    row_index: int


class BM25Index:
    """加载持久化 BM25 文档-词项矩阵并执行检索。"""

    def __init__(self, index_dir: Path, corpus_path: Path | None = None) -> None:
        self.index_dir = index_dir.resolve()
        metadata_path = self.index_dir / "metadata.json"
        if not metadata_path.is_file():
            raise FileNotFoundError(
                f"没有找到 BM25 索引：{metadata_path}\n"
                "请先运行 bm25_index.py 构建索引。"
            )

        self.metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
        self.corpus_path = (
            corpus_path.resolve()
            if corpus_path is not None
            else Path(self.metadata["corpus_path"])
        )
        self.vectorizer: CountVectorizer = joblib.load(
            self.index_dir / "vectorizer.joblib"
        )
        self.matrix = sparse.load_npz(self.index_dir / "bm25_matrix.npz").tocsr()
        self.doc_ids = np.load(self.index_dir / "doc_ids.npy", allow_pickle=False)
        self.offsets = np.load(self.index_dir / "corpus_offsets.npy")

        expected_shape = (len(self.doc_ids), len(self.vectorizer.vocabulary_))
        if self.matrix.shape != expected_shape or len(self.offsets) != len(self.doc_ids):
            raise RuntimeError(
                f"索引文件不一致：matrix={self.matrix.shape}, "
                f"expected={expected_shape}, offsets={len(self.offsets)}"
            )

    def search(self, query: str, top_k: int = 100) -> list[SearchHit]:
        if top_k < 1:
            raise ValueError("top_k 必须为正整数")
        query = query.strip()
        if not query:
            raise ValueError("查询文本不能为空")

        query_vector = self.vectorizer.transform([query]).tocsr()
        if query_vector.nnz == 0:
            return []

        # 标准 BM25 通常不额外放大查询中重复出现的词项。
        query_vector.data.fill(1.0)
        scores = (self.matrix @ query_vector.T).tocoo()
        if scores.nnz == 0:
            return []

        count = min(top_k, scores.nnz)
        if count == scores.nnz:
            candidates = np.arange(scores.nnz)
        else:
            candidates = np.argpartition(scores.data, -count)[-count:]

        # 分数相同时按文档行号稳定排序，保证重复运行结果一致。
        ordered = candidates[
            np.lexsort((scores.row[candidates], -scores.data[candidates]))
        ]
        return [
            SearchHit(
                rank=rank,
                doc_id=str(self.doc_ids[scores.row[position]]),
                score=float(scores.data[position]),
                row_index=int(scores.row[position]),
            )
            for rank, position in enumerate(ordered, start=1)
        ]

    def read_texts(self, row_indices: list[int]) -> dict[int, str]:
        """按建库时记录的字节偏移读取少量原文，用于交互检索展示。"""
        if not self.corpus_path.is_file():
            raise FileNotFoundError(self.corpus_path)
        result: dict[int, str] = {}
        with self.corpus_path.open("rb") as source:
            for row_index in row_indices:
                source.seek(int(self.offsets[row_index]))
                row = json.loads(source.readline())
                result[row_index] = str(row["text"])
        return result


def _iter_corpus(
    corpus_path: Path,
    doc_ids: list[str],
    offsets: list[int],
    limit: int | None,
) -> Iterator[str]:
    with corpus_path.open("rb") as source:
        line_number = 0
        while True:
            offset = source.tell()
            raw_line = source.readline()
            if not raw_line:
                break
            line_number += 1
            if not raw_line.strip():
                continue
            try:
                row = json.loads(raw_line)
                doc_id = str(row["id"])
                text = str(row["text"]).strip()
            except (json.JSONDecodeError, KeyError, TypeError) as exc:
                raise ValueError(
                    f"无效语料：{corpus_path}:{line_number}: {exc}"
                ) from exc
            if not doc_id or not text:
                raise ValueError(f"文档 id 或 text 为空：{corpus_path}:{line_number}")
            doc_ids.append(doc_id)
            offsets.append(offset)
            if len(doc_ids) % 10_000 == 0:
                print(f"已读取 {len(doc_ids):,} 篇文档", flush=True)
            yield text
            if limit is not None and len(doc_ids) >= limit:
                break


def build_index(
    corpus_path: Path,
    index_dir: Path,
    ngram_min: int = 2,
    ngram_max: int = 3,
    min_df: int = 1,
    max_features: int | None = None,
    k1: float = 1.5,
    b: float = 0.75,
    limit: int | None = None,
) -> dict:
    if not corpus_path.is_file():
        raise FileNotFoundError(corpus_path)
    if ngram_min < 1 or ngram_max < ngram_min:
        raise ValueError("n-gram 范围无效")
    if min_df < 1:
        raise ValueError("min_df 必须为正整数")
    if max_features is not None and max_features < 1:
        raise ValueError("max_features 必须为正整数或 None")
    if k1 <= 0 or not 0 <= b <= 1:
        raise ValueError("要求 k1 > 0 且 0 <= b <= 1")

    index_dir.mkdir(parents=True, exist_ok=True)
    started = perf_counter()
    doc_ids: list[str] = []
    offsets: list[int] = []
    vectorizer = CountVectorizer(
        analyzer="char",
        ngram_range=(ngram_min, ngram_max),
        min_df=min_df,
        max_features=max_features,
        lowercase=True,
        dtype=np.float32,
    )

    print("正在统计字符 n-gram 并构建词频矩阵……", flush=True)
    counts = vectorizer.fit_transform(
        _iter_corpus(corpus_path, doc_ids, offsets, limit)
    ).tocsr()
    if counts.shape[0] == 0 or counts.shape[1] == 0:
        raise RuntimeError("语料为空，或没有词项满足 min_df")
    if len(set(doc_ids)) != len(doc_ids):
        raise RuntimeError("corpus 中存在重复文档 id")

    print(
        f"文档={counts.shape[0]:,} | 词项={counts.shape[1]:,} | "
        f"非零项={counts.nnz:,}",
        flush=True,
    )
    document_lengths = np.asarray(counts.sum(axis=1)).ravel().astype(np.float32)
    average_length = float(document_lengths.mean())
    document_frequency = np.bincount(
        counts.indices, minlength=counts.shape[1]
    ).astype(np.float32)
    idf = np.log1p(
        (counts.shape[0] - document_frequency + 0.5)
        / (document_frequency + 0.5)
    ).astype(np.float32)

    row_factors = k1 * (1.0 - b + b * document_lengths / average_length)
    repeated_factors = np.repeat(row_factors, np.diff(counts.indptr))
    tf = counts.data
    counts.data = (
        idf[counts.indices]
        * (tf * (k1 + 1.0))
        / (tf + repeated_factors)
    ).astype(np.float32)
    counts.eliminate_zeros()

    max_id_length = max(len(value) for value in doc_ids)
    print("正在保存索引……", flush=True)
    sparse.save_npz(index_dir / "bm25_matrix.npz", counts, compressed=True)
    joblib.dump(vectorizer, index_dir / "vectorizer.joblib", compress=3)
    np.save(index_dir / "doc_ids.npy", np.asarray(doc_ids, dtype=f"U{max_id_length}"))
    np.save(index_dir / "corpus_offsets.npy", np.asarray(offsets, dtype=np.int64))

    elapsed = perf_counter() - started
    corpus_stat = corpus_path.stat()
    metadata = {
        "type": "BM25",
        "tokenization": "character_ngram",
        "corpus_path": str(corpus_path.resolve()),
        "corpus_size_bytes": corpus_stat.st_size,
        "corpus_mtime_ns": corpus_stat.st_mtime_ns,
        "documents": counts.shape[0],
        "terms": counts.shape[1],
        "nonzero_values": counts.nnz,
        "ngram_range": [ngram_min, ngram_max],
        "min_df": min_df,
        "max_features": max_features,
        "k1": k1,
        "b": b,
        "average_document_length": average_length,
        "limited_corpus": limit is not None,
        "build_seconds": elapsed,
    }
    (index_dir / "metadata.json").write_text(
        json.dumps(metadata, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    print(json.dumps(metadata, ensure_ascii=False, indent=2))
    return metadata


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="构建 CMedTEB 字符 n-gram BM25 索引。")
    parser.add_argument("--corpus", type=Path, default=DEFAULT_CORPUS)
    parser.add_argument("--index-dir", type=Path, default=DEFAULT_INDEX_DIR)
    parser.add_argument("--ngram-min", type=int, default=2)
    parser.add_argument("--ngram-max", type=int, default=3)
    parser.add_argument("--min-df", type=int, default=1)
    parser.add_argument(
        "--max-features",
        type=int,
        default=0,
        help="词表上限；0 表示不限制。",
    )
    parser.add_argument("--k1", type=float, default=1.5)
    parser.add_argument("--b", type=float, default=0.75)
    parser.add_argument("--limit", type=int, default=None, help="仅用于冒烟测试。")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if args.limit is not None and args.limit < 1:
        raise ValueError("--limit 必须为正整数")
    build_index(
        corpus_path=args.corpus.resolve(),
        index_dir=args.index_dir.resolve(),
        ngram_min=args.ngram_min,
        ngram_max=args.ngram_max,
        min_df=args.min_df,
        max_features=args.max_features or None,
        k1=args.k1,
        b=args.b,
        limit=args.limit,
    )


if __name__ == "__main__":
    main()
