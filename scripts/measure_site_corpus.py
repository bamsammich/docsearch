#!/usr/bin/env python3
"""Measure what the grading constants would say about a corpus of sites.

The thresholds in ``structure.py`` and ``verify.py`` were measured on four
manual corpora. Doc sites are shorter-paged, shallower and code-heavy, and the
plan defers recalibrating them until there is a doc-site corpus to measure --
this is what measures it.

It reads an index that already holds ingested sites and reports, per document,
every quantity a threshold is applied to, so a constant can be moved against
evidence rather than taste. It changes nothing.

    uv run python scripts/measure_site_corpus.py --db var/corpus.db

Reference figures from the manual corpora, for comparison:

    addressability   0.93, 0.84, 0.81 where retrieval works
                     0.29 on a manual whose structure was never derived
    headless rate    1 chunk in 944 on the numbered reference corpus
"""

from __future__ import annotations

import argparse
import json
import sqlite3
from pathlib import Path

import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "python"))

from docsearch.chunker import MAX_TOKENS, MIN_TOKENS, PATH_SEP  # noqa: E402
from docsearch.tokens import estimate_tokens  # noqa: E402
from docsearch.verify import ChunkStat, grade, verify_document  # noqa: E402


def _pct(values: list[int], q: float) -> int:
    if not values:
        return 0
    return sorted(values)[min(len(values) - 1, int(len(values) * q))]


def measure(conn: sqlite3.Connection, doc_id: str) -> dict[str, object]:
    rows = conn.execute(
        "SELECT section, heading_path, text, image_count FROM chunks WHERE doc_id=? ORDER BY ordinal",
        (doc_id,),
    ).fetchall()
    doc = conn.execute(
        "SELECT title, source_kind, warnings FROM documents WHERE doc_id=?", (doc_id,)
    ).fetchone()
    warnings = json.loads(doc["warnings"] or "{}")

    tokens = [estimate_tokens(r["text"]) for r in rows]
    paths = {r["heading_path"] for r in rows}
    headless = sum(1 for r in rows if not r["heading_path"].strip())
    depths = [len(r["heading_path"].split(PATH_SEP)) for r in rows]
    numbered = [r for r in rows if r["section"]]
    mergeable = [r for r in rows if not r["section"]]

    stats = [
        ChunkStat(
            tokens=t,
            numbered=bool(r["section"]),
            depth=len(r["heading_path"].split(PATH_SEP)),
            image_count=r["image_count"] or 0,
            text=r["text"],
            heading_path=r["heading_path"],
        )
        for t, r in zip(tokens, rows, strict=True)
    ]
    report = verify_document(conn, doc_id)

    return {
        "doc_id": doc_id,
        "kind": doc["source_kind"],
        "pages": warnings.get("pages_fetched", 0),
        "declared": warnings.get("pages_declared", 0),
        "unreachable": len(warnings.get("unreachable_pages", []) or []),
        "source": warnings.get("structure_source", "?"),
        "chunks": len(rows),
        "paths": len(paths),
        # The ratio ADDRESSABLE_MIN and ADDRESSABLE_DEGRADED_RATIO are applied to.
        "addressability": round(len(paths) / len(rows), 3) if rows else 0.0,
        "chunks_per_page": round(len(rows) / warnings["pages_fetched"], 2)
        if warnings.get("pages_fetched")
        else None,
        "headless": headless,
        "headless_rate": round(headless / len(rows), 4) if rows else 0.0,
        "numbered_share": round(len(numbered) / len(rows), 2) if rows else 0.0,
        "mergeable": len(mergeable),
        "max_depth": max(depths) if depths else 0,
        "tok_median": _pct(tokens, 0.5),
        "tok_p95": _pct(tokens, 0.95),
        "tok_max": max(tokens) if tokens else 0,
        "oversized_rate": round(sum(1 for t in tokens if t > MAX_TOKENS) / len(rows), 3)
        if rows
        else 0.0,
        "fragmented_rate": round(
            sum(1 for r, t in zip(rows, tokens, strict=True) if not r["section"] and t < MIN_TOKENS)
            / len(mergeable),
            3,
        )
        if mergeable
        else None,
        "verdict": report.verdict,
        "findings": [f.code for f in grade(stats)],
    }


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--db", required=True)
    args = ap.parse_args()

    conn = sqlite3.connect(args.db)
    conn.row_factory = sqlite3.Row
    docs = [
        r["doc_id"]
        for r in conn.execute("SELECT doc_id FROM documents WHERE status='ready' ORDER BY doc_id")
    ]
    results = [measure(conn, d) for d in docs]

    cols = [
        ("doc_id", 34),
        ("source", 12),
        ("pages", 6),
        ("chunks", 7),
        ("paths", 6),
        ("addressability", 15),
        ("chunks_per_page", 16),
        ("headless_rate", 14),
        ("tok_median", 11),
        ("oversized_rate", 15),
        ("verdict", 10),
    ]
    print("  ".join(f"{name:<{w}}" for name, w in cols))
    for r in results:
        print("  ".join(f"{str(r[name]):<{w}}" for name, w in cols))

    print("\n--- findings per document ---")
    for r in results:
        print(f"  {r['doc_id'][:40]:<42} {', '.join(r['findings']) or 'none'}")

    print("\n--- full detail ---")
    print(json.dumps(results, indent=2))


if __name__ == "__main__":
    main()
