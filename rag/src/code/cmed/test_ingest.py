import importlib.util
import sys
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("ingest.py")
SPEC = importlib.util.spec_from_file_location("cmed_ingest", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


class ChunkingTests(unittest.TestCase):
    def test_short_document_is_unchanged(self):
        text = "标题\n短答案。"
        chunks = MODULE.chunk_document("1", text, 1000, 1000, 100)
        self.assertEqual([item.text for item in chunks], [text])
        self.assertEqual(chunks[0].chunk_id, "1:0")
        self.assertFalse(chunks[0].is_chunked)

    def test_long_document_splits_and_repeats_title(self):
        text = "医疗问题\n" + ("第一句话。第二句话。" * 150)
        chunks = MODULE.chunk_document("7", text, 1000, 1000, 100)
        self.assertGreater(len(chunks), 1)
        self.assertTrue(all(item.text.startswith("医疗问题\n") for item in chunks))
        self.assertTrue(all(len(item.text) <= 1000 for item in chunks))
        self.assertEqual(
            [item.chunk_index for item in chunks],
            list(range(len(chunks))),
        )
        self.assertTrue(all(item.chunk_count == len(chunks) for item in chunks))
        self.assertTrue(all(item.parent_doc_id == "7" for item in chunks))

    def test_hard_split_without_boundaries(self):
        text = "字" * 2500
        chunks = MODULE.chunk_document("9", text, 1000, 1000, 100)
        self.assertEqual(len(chunks), 3)
        self.assertTrue(all(0 < len(item.text) <= 1000 for item in chunks))


if __name__ == "__main__":
    unittest.main()
