"""不连接 Milvus 的聚合与指标单元测试。"""

import math
import unittest

from evaluate import query_metrics
from retriever import aggregate_chunk_hits


class RetrievalTests(unittest.TestCase):
    def test_chunk_aggregation_keeps_best_parent_chunk(self):
        hits = [
            {
                "distance": 0.8,
                "entity": {
                    "parent_doc_id": "a",
                    "chunk_id": "a:0",
                    "chunk_index": 0,
                },
            },
            {
                "distance": 0.9,
                "entity": {
                    "parent_doc_id": "a",
                    "chunk_id": "a:1",
                    "chunk_index": 1,
                },
            },
            {
                "distance": 0.85,
                "entity": {
                    "parent_doc_id": "b",
                    "chunk_id": "b:0",
                    "chunk_index": 0,
                },
            },
        ]
        rows = aggregate_chunk_hits(hits, parent_top_k=10)
        self.assertEqual([row["parent_doc_id"] for row in rows], ["a", "b"])
        self.assertEqual(rows[0]["chunk_id"], "a:1")

    def test_metrics(self):
        metrics = query_metrics(
            retrieved=["x", "a", "b", "y"],
            relevant={"a", "b", "c"},
        )
        self.assertAlmostEqual(metrics["recall@5"], 2 / 3)
        self.assertAlmostEqual(metrics["precision@5"], 2 / 5)
        self.assertEqual(metrics["hit_rate@1"], 0.0)
        self.assertEqual(metrics["hit_rate@5"], 1.0)
        self.assertAlmostEqual(metrics["mrr@10"], 1 / 2)
        expected_dcg = 1 / math.log2(3) + 1 / math.log2(4)
        expected_idcg = 1 / math.log2(2) + 1 / math.log2(3) + 1 / math.log2(4)
        self.assertAlmostEqual(metrics["ndcg@5"], expected_dcg / expected_idcg)


if __name__ == "__main__":
    unittest.main()
