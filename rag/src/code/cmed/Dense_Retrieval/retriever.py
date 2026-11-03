"""CMedTEB Dense Retrieval 的模型加载、Milvus 检索和父文档聚合。"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from time import perf_counter
from typing import Any


ROOT = Path(__file__).resolve().parents[4]
DEFAULT_MODEL = ROOT / "models/bge-m3"
DEFAULT_URI = "http://localhost:19530"
DEFAULT_COLLECTION = "cmedteb_retrieval_1000"
EXPECTED_DIMENSION = 1024


@dataclass(frozen=True)
class SearchTiming:
    query_count: int
    encode_seconds: float
    search_seconds: float


def aggregate_chunk_hits(
    hits: list[Any],
    parent_top_k: int,
    include_text: bool = False,
) -> list[dict[str, Any]]:
    """同一 parent_doc_id 只保留分数最高的 chunk。"""
    parents: dict[str, dict[str, Any]] = {}

    for hit in hits:
        entity = hit["entity"]
        parent_id = str(entity["parent_doc_id"])
        score = float(hit["distance"])
        previous = parents.get(parent_id)
        if previous is not None and previous["score"] >= score:
            continue

        row: dict[str, Any] = {
            "parent_doc_id": parent_id,
            "score": score,
            "chunk_id": str(entity["chunk_id"]),
            "chunk_index": int(entity["chunk_index"]),
        }
        if include_text:
            row["title"] = entity.get("title", "")
            row["text"] = entity.get("text", "")
        parents[parent_id] = row

    ranked = sorted(
        parents.values(),
        key=lambda item: (-item["score"], item["parent_doc_id"]),
    )[:parent_top_k]
    for rank, row in enumerate(ranked, start=1):
        row["rank"] = rank
    return ranked


class CMedDenseRetriever:
    """使用 BGE-M3 编码查询，并在 Milvus 中检索 CMedTEB chunks。"""

    def __init__(
        self,
        model_path: Path | str = DEFAULT_MODEL,
        uri: str = DEFAULT_URI,
        collection: str = DEFAULT_COLLECTION,
        device: str | None = None,
    ) -> None:
        from pymilvus import MilvusClient
        from sentence_transformers import SentenceTransformer

        self.collection = collection
        self.client = MilvusClient(uri=uri)
        if not self.client.has_collection(collection):
            self.close()
            raise RuntimeError(f"Collection 不存在：{collection}")

        self._validate_collection()
        self.client.load_collection(collection)

        kwargs: dict[str, Any] = {"trust_remote_code": True}
        if device:
            kwargs["device"] = device
        print(f"正在加载模型：{model_path}")
        self.model = SentenceTransformer(str(model_path), **kwargs)

        if hasattr(self.model, "get_embedding_dimension"):
            dimension = self.model.get_embedding_dimension()
        else:
            dimension = self.model.get_sentence_embedding_dimension()
        if dimension != EXPECTED_DIMENSION:
            self.close()
            raise RuntimeError(
                f"模型向量维度={dimension}，预期={EXPECTED_DIMENSION}"
            )

    def _validate_collection(self) -> None:
        description = self.client.describe_collection(self.collection)
        fields = {field["name"]: field for field in description["fields"]}
        required = {"chunk_id", "parent_doc_id", "chunk_index", "embedding"}
        missing = required - fields.keys()
        if missing:
            raise RuntimeError(f"Collection 缺少字段：{sorted(missing)}")
        dimension = int(fields["embedding"].get("params", {}).get("dim", 0))
        if dimension != EXPECTED_DIMENSION:
            raise RuntimeError(
                f"Collection 向量维度={dimension}，预期={EXPECTED_DIMENSION}"
            )

    def search_batch(
        self,
        queries: list[str],
        chunk_top_k: int = 200,
        parent_top_k: int = 100,
        embedding_batch_size: int = 32,
        include_text: bool = False,
    ) -> tuple[list[list[dict[str, Any]]], SearchTiming]:
        if not queries or any(not query.strip() for query in queries):
            raise ValueError("queries 不能为空")
        if chunk_top_k < parent_top_k:
            raise ValueError("chunk_top_k 不能小于 parent_top_k")
        if parent_top_k < 1 or embedding_batch_size < 1:
            raise ValueError("parent_top_k 和 embedding_batch_size 必须为正整数")

        encode_started = perf_counter()
        vectors = self.model.encode(
            queries,
            batch_size=embedding_batch_size,
            normalize_embeddings=True,
            convert_to_numpy=True,
            show_progress_bar=False,
        )
        encode_seconds = perf_counter() - encode_started

        output_fields = ["chunk_id", "parent_doc_id", "chunk_index"]
        if include_text:
            output_fields.extend(["title", "text"])

        search_started = perf_counter()
        raw_results = self.client.search(
            collection_name=self.collection,
            data=vectors.tolist(),
            anns_field="embedding",
            search_params={"metric_type": "IP", "params": {}},
            limit=chunk_top_k,
            output_fields=output_fields,
        )
        search_seconds = perf_counter() - search_started

        results = [
            aggregate_chunk_hits(
                list(hits),
                parent_top_k=parent_top_k,
                include_text=include_text,
            )
            for hits in raw_results
        ]
        return results, SearchTiming(
            query_count=len(queries),
            encode_seconds=encode_seconds,
            search_seconds=search_seconds,
        )

    def close(self) -> None:
        client = getattr(self, "client", None)
        if client is not None:
            client.close()
            self.client = None

    def __enter__(self) -> "CMedDenseRetriever":
        return self

    def __exit__(self, exc_type, exc_value, traceback) -> None:
        self.close()
