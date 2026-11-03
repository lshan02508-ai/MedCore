"""CMedTEB 交叉编码器重排模型。"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from time import perf_counter
from typing import Any, Sequence

import numpy as np


ROOT = Path(__file__).resolve().parents[4]
DEFAULT_RERANKER_MODEL = ROOT / "models/bge-reranker-v2-m3"


@dataclass(frozen=True)
class RerankTiming:
    pairs: int
    seconds: float


def order_candidates(
    candidates: Sequence[dict[str, Any]],
    scores: Sequence[float],
) -> list[dict[str, Any]]:
    """按 rerank 分数降序排列；同分时保留第一阶段的顺序。"""
    if len(candidates) != len(scores):
        raise ValueError("候选数量与 rerank 分数数量不一致")

    rows: list[dict[str, Any]] = []
    for original_rank, (candidate, score) in enumerate(
        zip(candidates, scores), start=1
    ):
        row = dict(candidate)
        row["retrieval_rank"] = int(candidate.get("rank", original_rank))
        row["retrieval_score"] = float(candidate["score"])
        row["rerank_score"] = float(score)
        row["reranked"] = True
        rows.append(row)

    rows.sort(key=lambda row: (-row["rerank_score"], row["retrieval_rank"]))
    for rank, row in enumerate(rows, start=1):
        row["rank"] = rank
    return rows


class CMedReranker:
    """使用 CrossEncoder 对 query-document 对进行相关性打分。"""

    def __init__(
        self,
        model_path: Path | str = DEFAULT_RERANKER_MODEL,
        device: str | None = None,
        max_length: int = 512,
    ) -> None:
        import torch
        from sentence_transformers import CrossEncoder

        model_path = Path(model_path).resolve()
        if not model_path.exists():
            raise FileNotFoundError(
                f"没有找到 reranker 模型：{model_path}\n"
                "请先下载 BAAI/bge-reranker-v2-m3，命令见 rerank/README.md。"
            )
        if max_length < 8:
            raise ValueError("max_length 不能小于 8")

        print(f"正在加载 reranker：{model_path}")
        self.model = CrossEncoder(
            str(model_path),
            device=device,
            max_length=max_length,
            trust_remote_code=True,
            local_files_only=True,
            activation_fn=torch.nn.Identity(),
        )

    def rerank(
        self,
        query: str,
        candidates: Sequence[dict[str, Any]],
        batch_size: int = 16,
    ) -> tuple[list[dict[str, Any]], RerankTiming]:
        if not query.strip():
            raise ValueError("query 不能为空")
        if batch_size < 1:
            raise ValueError("batch_size 必须为正整数")
        if not candidates:
            return [], RerankTiming(pairs=0, seconds=0.0)
        if any(not str(row.get("text", "")).strip() for row in candidates):
            raise ValueError("每个候选必须包含非空 text 字段")

        pairs = [(query, str(row["text"])) for row in candidates]
        started = perf_counter()
        raw_scores = self.model.predict(
            pairs,
            batch_size=batch_size,
            show_progress_bar=False,
            convert_to_numpy=True,
        )
        seconds = perf_counter() - started
        scores = np.asarray(raw_scores, dtype=np.float32).reshape(-1)
        ranked = order_candidates(candidates, scores.tolist())
        return ranked, RerankTiming(pairs=len(pairs), seconds=seconds)

    def rerank_top(
        self,
        query: str,
        candidates: Sequence[dict[str, Any]],
        rerank_top_k: int = 15,
        batch_size: int = 16,
    ) -> tuple[list[dict[str, Any]], RerankTiming]:
        """仅重排候选前 K 条，其余候选保持 Dense 顺序。"""
        if not 1 <= rerank_top_k <= len(candidates):
            raise ValueError("要求 1 <= rerank_top_k <= 候选数量")

        head, timing = self.rerank(
            query,
            candidates[:rerank_top_k],
            batch_size=batch_size,
        )
        tail: list[dict[str, Any]] = []
        for original_rank, candidate in enumerate(
            candidates[rerank_top_k:], start=rerank_top_k + 1
        ):
            row = dict(candidate)
            row["retrieval_rank"] = int(candidate.get("rank", original_rank))
            row["retrieval_score"] = float(candidate["score"])
            row["rerank_score"] = None
            row["reranked"] = False
            tail.append(row)

        combined = head + tail
        for rank, row in enumerate(combined, start=1):
            row["rank"] = rank
        return combined, timing
