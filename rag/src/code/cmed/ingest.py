"""Split, embed, and ingest the CMedTEB retrieval corpus into Milvus.

Documents with at most ``--split-threshold`` characters are kept intact.
Longer documents are split on paragraph/sentence boundaries.  Every Milvus
row retains ``parent_doc_id`` so chunk-level search results can later be
deduplicated and evaluated against CMedTEB's document-level qrels.
"""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable


ROOT = Path(__file__).resolve().parents[3]
DEFAULT_DATA = ROOT / "src/data/CMedTEB/retrieval/corpus.jsonl"
DEFAULT_MODEL = ROOT / "models/bge-m3"

BOUNDARY_CHARS = frozenset("\n。！？!?；;")
VECTOR_DIMENSION = 1024


@dataclass(frozen=True)
class Chunk:
    chunk_id: str
    parent_doc_id: str
    chunk_index: int
    chunk_count: int
    title: str
    text: str
    original_char_length: int
    is_chunked: bool


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="将 CMedTEB retrieval corpus 切分、向量化并写入 Milvus。"
    )
    parser.add_argument("--data", type=Path, default=DEFAULT_DATA)
    parser.add_argument("--model", type=Path, default=DEFAULT_MODEL)
    parser.add_argument("--uri", default="http://localhost:19530")
    parser.add_argument("--collection", default="cmedteb_retrieval_1000")
    parser.add_argument(
        "--split-threshold",
        type=int,
        default=1000,
        help="超过该字符数才切分，默认 1000。",
    )
    parser.add_argument(
        "--chunk-size",
        type=int,
        default=1000,
        help="每个 chunk 的最大字符数（包括重复的标题），默认 1000。",
    )
    parser.add_argument(
        "--overlap",
        type=int,
        default=100,
        help="相邻正文 chunk 的重叠字符数，默认 100。",
    )
    parser.add_argument("--read-batch-size", type=int, default=64)
    parser.add_argument("--embedding-batch-size", type=int, default=8)
    parser.add_argument("--log-every", type=int, default=1000)
    parser.add_argument("--limit", type=int, default=None)
    parser.add_argument(
        "--device",
        default=None,
        help="例如 cuda、cuda:0 或 cpu；默认由 SentenceTransformer 自动选择。",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="只校验数据并输出切分统计，不连接 Milvus、不加载模型。",
    )
    return parser.parse_args()


def validate_args(args: argparse.Namespace) -> None:
    positive_names = (
        "split_threshold",
        "chunk_size",
        "read_batch_size",
        "embedding_batch_size",
        "log_every",
    )
    for name in positive_names:
        if getattr(args, name) < 1:
            raise ValueError(f"--{name.replace('_', '-')} 必须为正整数")
    if args.limit is not None and args.limit < 1:
        raise ValueError("--limit 必须为正整数")
    if args.split_threshold > args.chunk_size:
        raise ValueError("--split-threshold 不能大于 --chunk-size")
    if not 0 <= args.overlap < args.chunk_size:
        raise ValueError("--overlap 必须满足 0 <= overlap < chunk-size")
    if not args.data.is_file():
        raise FileNotFoundError(args.data)
    if not args.dry_run and not args.model.exists():
        raise FileNotFoundError(args.model)


def extract_title_and_body(text: str, chunk_size: int) -> tuple[str, str]:
    """Treat a short first line as a title that can be repeated per chunk."""
    title, separator, body = text.partition("\n")
    title = title.strip()
    body = body.strip()
    max_title_chars = min(200, chunk_size // 3)
    if separator and title and body and len(title) <= max_title_chars:
        return title, body
    return "", text.strip()


def choose_end(text: str, start: int, hard_end: int) -> int:
    """Choose the latest natural boundary in the final 40% of the window."""
    if hard_end >= len(text):
        return len(text)
    minimum = start + max(1, int((hard_end - start) * 0.6))
    for index in range(hard_end - 1, minimum - 1, -1):
        if text[index] in BOUNDARY_CHARS:
            return index + 1
    return hard_end


def split_body(text: str, max_chars: int, overlap: int) -> list[str]:
    if len(text) <= max_chars:
        return [text]
    if not 0 <= overlap < max_chars:
        raise ValueError("overlap 必须小于正文 chunk 可用字符数")

    chunks: list[str] = []
    start = 0
    while start < len(text):
        hard_end = min(start + max_chars, len(text))
        end = choose_end(text, start, hard_end)
        piece = text[start:end].strip()
        if not piece:
            raise ValueError(f"切分产生空文本：start={start}, end={end}")
        chunks.append(piece)
        if end >= len(text):
            break
        next_start = end - overlap
        if next_start <= start:
            next_start = end
        start = next_start
    return chunks


def chunk_document(
    doc_id: str,
    text: str,
    split_threshold: int,
    chunk_size: int,
    overlap: int,
) -> list[Chunk]:
    text = text.strip()
    if not doc_id:
        raise ValueError("文档 id 不能为空")
    if not text:
        raise ValueError(f"文档 text 不能为空：id={doc_id}")

    original_length = len(text)
    if original_length <= split_threshold:
        pieces = [text]
        title = text.partition("\n")[0].strip()
        if len(title) > 200:
            title = ""
    else:
        title, body = extract_title_and_body(text, chunk_size)
        prefix = f"{title}\n" if title else ""
        available = chunk_size - len(prefix)
        if available < 1:
            raise ValueError(f"标题占满整个 chunk：id={doc_id}")
        body_overlap = min(overlap, max(0, available - 1))
        body_pieces = split_body(body, available, body_overlap)
        pieces = [f"{prefix}{piece}" for piece in body_pieces]

    chunk_count = len(pieces)
    result = []
    for index, piece in enumerate(pieces):
        if len(piece) > chunk_size:
            raise AssertionError(
                f"chunk 超过限制：id={doc_id}, index={index}, chars={len(piece)}"
            )
        result.append(
            Chunk(
                chunk_id=f"{doc_id}:{index}",
                parent_doc_id=doc_id,
                chunk_index=index,
                chunk_count=chunk_count,
                title=title,
                text=piece,
                original_char_length=original_length,
                is_chunked=chunk_count > 1,
            )
        )
    return result


def read_documents(path: Path, limit: int | None) -> Iterable[tuple[str, str]]:
    emitted = 0
    with path.open(encoding="utf-8") as source:
        for line_number, line in enumerate(source, start=1):
            if not line.strip():
                continue
            try:
                row = json.loads(line)
                doc_id = str(row["id"]).strip()
                text = str(row["text"])
            except (json.JSONDecodeError, KeyError, TypeError) as exc:
                raise ValueError(f"无效数据：{path}:{line_number}: {exc}") from exc
            if not doc_id or not text.strip():
                raise ValueError(f"id/text 为空：{path}:{line_number}")
            yield doc_id, text
            emitted += 1
            if limit is not None and emitted >= limit:
                return


def batched(items: Iterable[tuple[str, str]], size: int):
    batch: list[tuple[str, str]] = []
    for item in items:
        batch.append(item)
        if len(batch) >= size:
            yield batch
            batch = []
    if batch:
        yield batch


def expand_batch(batch: list[tuple[str, str]], args: argparse.Namespace) -> list[Chunk]:
    chunks: list[Chunk] = []
    for doc_id, text in batch:
        chunks.extend(
            chunk_document(
                doc_id=doc_id,
                text=text,
                split_threshold=args.split_threshold,
                chunk_size=args.chunk_size,
                overlap=args.overlap,
            )
        )
    return chunks


def run_dry_run(args: argparse.Namespace) -> None:
    documents = 0
    long_documents = 0
    chunks = 0
    max_chunks = 0
    max_chunk_chars = 0
    max_chunk_bytes = 0

    for batch in batched(read_documents(args.data, args.limit), args.read_batch_size):
        expanded = expand_batch(batch, args)
        documents += len(batch)
        long_documents += sum(len(text.strip()) > args.split_threshold for _, text in batch)
        chunks += len(expanded)
        if expanded:
            max_chunks = max(max_chunks, max(item.chunk_count for item in expanded))
            max_chunk_chars = max(max_chunk_chars, max(len(item.text) for item in expanded))
            max_chunk_bytes = max(
                max_chunk_bytes,
                max(len(item.text.encode("utf-8")) for item in expanded),
            )

    summary = {
        "documents": documents,
        "documents_over_threshold": long_documents,
        "chunks": chunks,
        "extra_chunks": chunks - documents,
        "max_chunks_per_document": max_chunks,
        "max_chunk_chars": max_chunk_chars,
        "max_chunk_utf8_bytes": max_chunk_bytes,
        "split_threshold": args.split_threshold,
        "chunk_size": args.chunk_size,
        "overlap": args.overlap,
    }
    print(json.dumps(summary, ensure_ascii=False, indent=2))


def collection_text_max_bytes(chunk_size: int) -> int:
    # A Unicode code point occupies at most four UTF-8 bytes.
    return max(4096, chunk_size * 4 + 16)


def create_collection_if_needed(client, args: argparse.Namespace) -> None:
    from pymilvus import DataType

    if client.has_collection(args.collection):
        return

    text_max_bytes = collection_text_max_bytes(args.chunk_size)
    if text_max_bytes > 65535:
        raise ValueError("--chunk-size 过大，超过 Milvus VARCHAR 可配置范围")

    schema = client.create_schema(auto_id=False, enable_dynamic_field=False)
    schema.add_field(
        field_name="chunk_id",
        datatype=DataType.VARCHAR,
        is_primary=True,
        max_length=128,
    )
    schema.add_field(
        field_name="parent_doc_id",
        datatype=DataType.VARCHAR,
        max_length=128,
    )
    schema.add_field(field_name="chunk_index", datatype=DataType.INT64)
    schema.add_field(field_name="chunk_count", datatype=DataType.INT64)
    schema.add_field(
        field_name="title",
        datatype=DataType.VARCHAR,
        max_length=2048,
    )
    schema.add_field(
        field_name="text",
        datatype=DataType.VARCHAR,
        max_length=text_max_bytes,
    )
    schema.add_field(field_name="original_char_length", datatype=DataType.INT64)
    schema.add_field(field_name="is_chunked", datatype=DataType.BOOL)
    schema.add_field(
        field_name="embedding",
        datatype=DataType.FLOAT_VECTOR,
        dim=VECTOR_DIMENSION,
    )

    indexes = client.prepare_index_params()
    indexes.add_index(
        field_name="embedding",
        index_type="AUTOINDEX",
        metric_type="IP",
    )
    indexes.add_index(field_name="parent_doc_id", index_type="INVERTED")
    client.create_collection(
        collection_name=args.collection,
        schema=schema,
        index_params=indexes,
    )
    print(f"已创建 Collection：{args.collection}")


def validate_collection(client, args: argparse.Namespace) -> None:
    description = client.describe_collection(args.collection)
    fields = {field["name"]: field for field in description["fields"]}
    required = {
        "chunk_id",
        "parent_doc_id",
        "chunk_index",
        "chunk_count",
        "title",
        "text",
        "original_char_length",
        "is_chunked",
        "embedding",
    }
    missing = required - fields.keys()
    if missing:
        raise RuntimeError(f"Collection 缺少字段：{sorted(missing)}")

    embedding_dim = int(fields["embedding"].get("params", {}).get("dim", 0))
    if embedding_dim != VECTOR_DIMENSION:
        raise RuntimeError(
            f"Collection 向量维度={embedding_dim}，预期={VECTOR_DIMENSION}"
        )
    text_max_bytes = int(fields["text"].get("params", {}).get("max_length", 0))
    if text_max_bytes < collection_text_max_bytes(args.chunk_size):
        raise RuntimeError("Collection text 字段长度小于当前 --chunk-size 的需要")


def existing_chunk_ids(client, collection: str, chunk_ids: list[str]) -> set[str]:
    if not chunk_ids:
        return set()
    expression = f"chunk_id in {json.dumps(chunk_ids, ensure_ascii=False)}"
    rows = client.query(
        collection_name=collection,
        filter=expression,
        output_fields=["chunk_id"],
        limit=len(chunk_ids),
        consistency_level="Strong",
    )
    return {str(row["chunk_id"]) for row in rows}


def encode_texts(model, texts: list[str], initial_batch_size: int):
    import torch

    batch_size = initial_batch_size
    while True:
        try:
            vectors = model.encode(
                texts,
                batch_size=batch_size,
                normalize_embeddings=True,
                convert_to_numpy=True,
                show_progress_bar=False,
            )
            if vectors.shape != (len(texts), VECTOR_DIMENSION):
                raise RuntimeError(f"Embedding 形状异常：{vectors.shape}")
            return vectors
        except torch.OutOfMemoryError:
            if torch.cuda.is_available():
                torch.cuda.empty_cache()
            if batch_size == 1:
                raise
            batch_size = max(1, batch_size // 2)
            print(f"GPU 显存不足，embedding batch size 降为 {batch_size}")


def insert_chunks(client, model, chunks: list[Chunk], args: argparse.Namespace) -> tuple[int, int]:
    known = existing_chunk_ids(
        client,
        args.collection,
        [item.chunk_id for item in chunks],
    )
    pending = [item for item in chunks if item.chunk_id not in known]
    if not pending:
        return 0, len(chunks)

    vectors = encode_texts(
        model,
        [item.text for item in pending],
        args.embedding_batch_size,
    )
    records = []
    for item, vector in zip(pending, vectors):
        records.append(
            {
                "chunk_id": item.chunk_id,
                "parent_doc_id": item.parent_doc_id,
                "chunk_index": item.chunk_index,
                "chunk_count": item.chunk_count,
                "title": item.title,
                "text": item.text,
                "original_char_length": item.original_char_length,
                "is_chunked": item.is_chunked,
                "embedding": vector.tolist(),
            }
        )
    result = client.insert(collection_name=args.collection, data=records)
    inserted = int(result["insert_count"])
    if inserted != len(records):
        raise RuntimeError(f"写入数量异常：预期 {len(records)}，实际 {inserted}")
    return inserted, len(chunks) - len(pending)


def run_ingestion(args: argparse.Namespace) -> None:
    from pymilvus import MilvusClient
    from sentence_transformers import SentenceTransformer

    client = MilvusClient(uri=args.uri)
    try:
        create_collection_if_needed(client, args)
        validate_collection(client, args)
        client.load_collection(args.collection)

        print(f"正在加载模型：{args.model}")
        model_kwargs = {"trust_remote_code": True}
        if args.device:
            model_kwargs["device"] = args.device
        model = SentenceTransformer(str(args.model), **model_kwargs)
        dimension = model.get_sentence_embedding_dimension()
        if dimension != VECTOR_DIMENSION:
            raise RuntimeError(
                f"模型向量维度={dimension}，预期={VECTOR_DIMENSION}"
            )

        scanned_documents = 0
        generated_chunks = 0
        inserted_chunks = 0
        existing_chunks = 0

        documents = read_documents(args.data, args.limit)
        for batch in batched(documents, args.read_batch_size):
            chunks = expand_batch(batch, args)
            inserted, existing = insert_chunks(client, model, chunks, args)
            scanned_documents += len(batch)
            generated_chunks += len(chunks)
            inserted_chunks += inserted
            existing_chunks += existing
            if scanned_documents % args.log_every < len(batch):
                print(
                    f"已扫描文档={scanned_documents} | "
                    f"生成 chunks={generated_chunks} | "
                    f"新增={inserted_chunks} | 已存在={existing_chunks}"
                )

        client.flush(args.collection)
        stats = client.get_collection_stats(args.collection)
        print("\n========== 入库完成 ==========")
        print(f"原始文档：{scanned_documents}")
        print(f"生成 chunks：{generated_chunks}")
        print(f"本次新增：{inserted_chunks}")
        print(f"断点续传跳过：{existing_chunks}")
        print(f"Collection 行数：{stats['row_count']}")
    finally:
        client.close()


def main() -> None:
    args = parse_args()
    validate_args(args)
    if args.dry_run:
        run_dry_run(args)
    else:
        run_ingestion(args)


if __name__ == "__main__":
    main()
