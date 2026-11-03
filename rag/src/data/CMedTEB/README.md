---
license: cc-by-nc-sa-4.0
language:
- zh
task_categories:
- text-retrieval
- sentence-similarity
pretty_name: CMedTEB
---

# CMedTEB

This export organizes CMedTEB into retrieval, rerank, and synonym STS tasks.

## Layout

- shared_train/retrieval_rerank_train.jsonl: one shared 20,000-row train split for both retrieval and rerank.
- retrieval/corpus.jsonl: retrieval corpus.
- retrieval/test_queries.jsonl: retrieval test queries.
- retrieval/test_qrels.jsonl: retrieval qrels.
- rerank/test.jsonl: rerank test set.
- sts/train.jsonl: synonym STS train set.
- sts/test.jsonl: synonym STS test set.
- data_stats.json: source counts, exported counts, and file sizes.

## Counts

| Task | Train | Test |
|---|---:|---:|
| Retrieval | 20,000 shared | 734 queries |
| Rerank | 20,000 shared | 1,128 |
| Synonym STS | 10,000 | 5,000 |

## Schemas

Retrieval/rerank train rows use query, pos, neg, and optional source fields such as pos_id. Retrieval evaluation uses id/text corpus and query files plus qid/docid/relevance qrels. STS rows use query, pos, neg, prompt, and type.

## Reference

```
@inproceedings{jiang-etal-2026-benchmarking,
    title = "Benchmarking and Enabling Efficient {C}hinese Medical Retrieval via Asymmetric Encoders",
    author = "Jiang, Angqing  and
      Chen, Jianlyu  and
      Zhefang  and
      Wang, Yongcan  and
      Li, Xinpeng  and
      Ding, Keyu  and
      Lian, Defu",
    editor = "Liakata, Maria  and
      Moreira, Viviane P.  and
      Zhang, Jiajun  and
      Jurgens, David",
    booktitle = "Proceedings of the 64th Annual Meeting of the {A}ssociation for {C}omputational {L}inguistics (Volume 1: Long Papers)",
    month = jul,
    year = "2026",
    address = "San Diego, California, United States",
    publisher = "Association for Computational Linguistics",
    url = "https://aclanthology.org/2026.acl-long.916/",
    doi = "10.18653/v1/2026.acl-long.916",
    pages = "20000--20020",
    ISBN = "979-8-89176-390-6",
    abstract = "Effective medical text retrieval requires both high accuracy and low latency. While LLM-based embedding models possess powerful retrieval capabilities, their prohibitive latency and high computational cost limit their application in real-time scenarios. Furthermore, the lack of comprehensive and high-fidelity benchmarks hinders progress in Chinese medical text retrieval. In this work, we introduce the **C**hinese **Med**ical **T**ext **E**mbedding **B**enchmark (**CMedTEB**), a benchmark spanning three kinds of practical embedding tasks: retrieval, reranking, and semantic textual similarity (STS). Distinct from purely automated datasets, CMedTEB is curated via a rigorous multi-LLM voting pipeline validated by clinical experts, ensuring gold-standard label quality while effectively mitigating annotation noise. On this foundation, we propose the **C**hinese Medical **A**symmetric **RE**triever (**CARE**), an asymmetric architecture that pairs a lightweight BERT-style encoder for online query encoding with a powerful LLM-based encoder for offline document encoding. However, optimizing such an asymmetric retriever with two structurally different encoders presents distinctive challenges. To address this, we introduce a novel two-stage training strategy that progressively bridges the query and document representations. Extensive experiments demonstrate that CARE surpasses state-of-the-art symmetric models on CMedTEB, achieving superior retrieval performance without increasing inference latency."
}
```
