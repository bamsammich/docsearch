#!/usr/bin/env python3
"""THROWAWAY PROBE. Not a feature. Not imported by anything.

Question: would dense reranking rescue the conceptual-slang misses that BM25
and the keyword-reference filter left behind?

Settle it empirically before committing to a retrieval pipeline. This script
adds no production dependency, touches no schema, and is run by hand:

    uv run --with sentence-transformers --with torch \\
        python scripts/probe_embedding_rerank.py candidates.json

It reads the candidate dump emitted by `docsearch-eval --dump-candidates`, so
it reranks exactly what the server retrieves rather than a reimplementation.

Two numbers matter equally:

  RESCUED    a query whose correct chunk was outside the top 3 and moves in
  DISPLACED  a query whose correct chunk was inside the top 3 and falls out

heading-term sits at 88% top-3. A reranker that lifts conceptual phrasing
while degrading heading-term is a bad trade, and the displacement count is the
only thing that reveals it.

Worth watching: this corpus uses "executor", "programmer", "fixture" and
"grand master" as console-specific terms of art that mean something else in
general web text. A general-purpose sentence embedder may carry actively wrong
priors. The probe answers that empirically rather than assuming either way.
"""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

MODEL = "sentence-transformers/all-MiniLM-L6-v2"

TOP_N = int(os.environ.get("PROBE_TOP_N", "3"))
#: Chunk text is truncated before embedding: MiniLM has a 256-token window and
#: silently truncates anyway, so this makes the limit explicit.
MAX_CHARS = 1200


def covers(ref: str, section: str) -> bool:
    """The component-wise subtree rule, mirrored from store.SectionCovers."""
    if not ref or not section:
        return False
    return section == ref or section.startswith(ref + ".")


def is_hit(entry: dict, cand: dict) -> bool:
    expect = entry.get("expect")
    if not expect:
        return False
    if entry["doc"] == "grandma2":
        return covers(expect, cand.get("section") or "")
    return expect.lower() in (cand.get("heading_path") or "").lower()


def main() -> None:
    if len(sys.argv) != 2:
        print(__doc__)
        raise SystemExit(2)

    from sentence_transformers import SentenceTransformer
    from sentence_transformers.util import cos_sim

    entries = json.loads(Path(sys.argv[1]).read_text())
    model = SentenceTransformer(MODEL)
    print(f"model: {MODEL}   success criterion: correct chunk within top-{TOP_N}\n")

    rescued: list[str] = []
    displaced: list[str] = []
    unchanged_hit = 0
    unchanged_miss = 0
    rows = []

    for e in entries:
        cands = e["candidates"]
        if not cands:
            continue
        bm25_positions = [i for i, c in enumerate(cands) if is_hit(e, c)]
        bm25_top3 = any(i < TOP_N for i in bm25_positions)

        texts = [(c["heading_path"] + "\n" + c["text"])[:MAX_CHARS] for c in cands]
        qv = model.encode([e["query"]], normalize_embeddings=True)
        cv = model.encode(texts, normalize_embeddings=True, batch_size=32)
        scores = cos_sim(qv, cv)[0].tolist()
        order = sorted(range(len(cands)), key=lambda i: -scores[i])
        rerank_positions = [rank for rank, i in enumerate(order) if is_hit(e, cands[i])]
        rr_top3 = any(r < TOP_N for r in rerank_positions)

        if rr_top3 and not bm25_top3:
            rescued.append(e["id"])
            verdict = "RESCUED"
        elif bm25_top3 and not rr_top3:
            displaced.append(e["id"])
            verdict = "DISPLACED"
        elif bm25_top3:
            unchanged_hit += 1
            verdict = "kept"
        else:
            unchanged_miss += 1
            verdict = "still missing"

        rows.append(
            (
                e["id"],
                e["category"],
                verdict,
                (bm25_positions[0] + 1) if bm25_positions else None,
                (rerank_positions[0] + 1) if rerank_positions else None,
                e["query"],
            )
        )

    print(f"{'ID':<5} {'CATEGORY':<18} {'VERDICT':<14} {'BM25':>5} {'RERANK':>7}  QUERY")
    for qid, cat, verdict, bp, rp, q in rows:
        print(f"{qid:<5} {cat:<18} {verdict:<14} {bp or '-'!s:>5} {rp or '-'!s:>7}  {q[:44]}")

    by_cat: dict[str, list[str]] = {}
    for _qid, cat, verdict, *_ in rows:
        by_cat.setdefault(cat, []).append(verdict)

    print("\n--- verdict by category ---")
    for cat, verdicts in sorted(by_cat.items()):
        r = verdicts.count("RESCUED")
        d = verdicts.count("DISPLACED")
        print(f"  {cat:<18} n={len(verdicts):<3} rescued={r:<3} displaced={d:<3} net={r - d:+d}")

    print(f"\nRESCUED   {len(rescued):>3}  {rescued}")
    print(f"DISPLACED {len(displaced):>3}  {displaced}")
    print(f"net change in top-3 hits: {len(rescued) - len(displaced):+d}")
    print(f"(kept {unchanged_hit}, still missing {unchanged_miss})")


if __name__ == "__main__":
    main()
