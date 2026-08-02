"""Post-ingest structural checks.

Eyeballing the token distribution and the extreme chunks catches structural
extraction failures immediately -- a heading source that silently collapsed
shows up as a handful of enormous chunks, and a shattered one as hundreds of
near-empty chunks. Run it after every ingest.
"""

from __future__ import annotations

import itertools
import sqlite3
from dataclasses import dataclass, field

from .tokens import estimate_tokens


@dataclass(slots=True)
class VerifyReport:
    doc_id: str
    title: str
    format: str
    status: str
    chunk_count: int
    page_count: int | None
    token_min: int = 0
    token_median: int = 0
    token_p95: int = 0
    token_max: int = 0
    token_mean: int = 0
    total_tokens: int = 0
    missing_locator: int = 0
    uncovered_pages: list[int] = field(default_factory=list)
    ordinal_gaps: list[int] = field(default_factory=list)
    longest: list[tuple[int, int, str]] = field(default_factory=list)
    shortest: list[tuple[int, int, str]] = field(default_factory=list)
    index_terms: int = 0
    unjoinable_index_sections: list[str] = field(default_factory=list)
    chunks_with_images: int = 0
    problems: list[str] = field(default_factory=list)


def _pct(values: list[int], q: float) -> int:
    if not values:
        return 0
    idx = min(len(values) - 1, int(len(values) * q))
    return values[idx]


def verify_document(conn: sqlite3.Connection, doc_id: str) -> VerifyReport:
    doc = conn.execute(
        "SELECT doc_id, title, format, status, page_count, chunk_count"
        " FROM documents WHERE doc_id = ?",
        (doc_id,),
    ).fetchone()
    if doc is None:
        raise KeyError(f"no such document: {doc_id}")

    rows = conn.execute(
        "SELECT id, ordinal, section, page_start, page_end, image_count, heading_path, text"
        " FROM chunks WHERE doc_id = ? ORDER BY ordinal",
        (doc_id,),
    ).fetchall()

    rep = VerifyReport(
        doc_id=doc["doc_id"],
        title=doc["title"],
        format=doc["format"],
        status=doc["status"],
        chunk_count=len(rows),
        page_count=doc["page_count"],
    )

    if not rows:
        rep.problems.append("document has no chunks")
        return rep

    sized = [(estimate_tokens(r["text"]), r) for r in rows]
    counts = sorted(t for t, _ in sized)
    rep.token_min = counts[0]
    rep.token_median = _pct(counts, 0.5)
    rep.token_p95 = _pct(counts, 0.95)
    rep.token_max = counts[-1]
    rep.total_tokens = sum(counts)
    rep.token_mean = rep.total_tokens // len(counts)

    by_size = sorted(sized, key=lambda x: x[0])
    rep.shortest = [(r["id"], t, r["heading_path"]) for t, r in by_size[:10]]
    rep.longest = [(r["id"], t, r["heading_path"]) for t, r in reversed(by_size[-10:])]

    rep.missing_locator = sum(
        1 for r in rows if r["page_start"] is None and doc["page_count"] is not None
    )
    rep.chunks_with_images = sum(1 for r in rows if (r["image_count"] or 0) > 0)

    if doc["page_count"]:
        covered: set[int] = set()
        for r in rows:
            if r["page_start"] is None:
                continue
            covered.update(range(r["page_start"], (r["page_end"] or r["page_start"]) + 1))
        rep.uncovered_pages = [p for p in range(1, doc["page_count"] + 1) if p not in covered]

    ordinals = [r["ordinal"] for r in rows]
    rep.ordinal_gaps = [a + 1 for a, b in itertools.pairwise(ordinals) if b != a + 1]

    rep.index_terms = conn.execute(
        "SELECT COUNT(*) c FROM index_terms WHERE doc_id = ?", (doc_id,)
    ).fetchone()["c"]
    if rep.index_terms:
        rep.unjoinable_index_sections = [
            r["section"]
            for r in conn.execute(
                "SELECT DISTINCT it.section FROM index_terms it"
                " WHERE it.doc_id = ? AND NOT EXISTS ("
                "   SELECT 1 FROM chunks c WHERE c.doc_id = it.doc_id AND c.section = it.section)"
                " ORDER BY it.section",
                (doc_id,),
            )
        ]

    if doc["status"] != "ready":
        rep.problems.append(f"status is '{doc['status']}', not 'ready'")
    if doc["chunk_count"] is not None and doc["chunk_count"] != len(rows):
        rep.problems.append(
            f"documents.chunk_count={doc['chunk_count']} but {len(rows)} chunk rows exist"
        )
    if rep.ordinal_gaps:
        rep.problems.append(f"{len(rep.ordinal_gaps)} gaps in chunk ordinals")
    if rep.missing_locator:
        rep.problems.append(f"{rep.missing_locator} chunks have no page locator")
    if rep.unjoinable_index_sections:
        rep.problems.append(f"{len(rep.unjoinable_index_sections)} index sections join to no chunk")
    fts = conn.execute("SELECT COUNT(*) c FROM chunks_fts WHERE doc_id = ?", (doc_id,)).fetchone()[
        "c"
    ]
    if fts != len(rows):
        rep.problems.append(f"FTS row count {fts} != chunk row count {len(rows)}")
    return rep


def format_report(rep: VerifyReport) -> str:
    out: list[str] = []
    a = out.append
    a(f"doc_id      {rep.doc_id}")
    a(f"title       {rep.title}")
    a(f"format      {rep.format}   status: {rep.status}")
    a(f"pages       {rep.page_count}")
    a(f"chunks      {rep.chunk_count}")
    a(f"tokens      total={rep.total_tokens:,}  mean={rep.token_mean}")
    a(
        f"            min={rep.token_min}  median={rep.token_median}"
        f"  p95={rep.token_p95}  max={rep.token_max}"
    )
    a(f"images      {rep.chunks_with_images} chunks reference at least one image")
    if rep.index_terms:
        a(f"index       {rep.index_terms} terms; {len(rep.unjoinable_index_sections)} unjoinable")
    if rep.uncovered_pages:
        span = rep.uncovered_pages
        preview = ", ".join(str(p) for p in span[:12])
        more = f" ... (+{len(span) - 12} more)" if len(span) > 12 else ""
        a(f"gaps        {len(span)} pages covered by no chunk: {preview}{more}")
    else:
        a("gaps        none - every page is covered by a chunk")

    a("")
    a("10 longest chunks:")
    for cid, tok, path in rep.longest:
        a(f"  {tok:>6} tok  #{cid:<7} {path[:88]}")
    a("")
    a("10 shortest chunks:")
    for cid, tok, path in rep.shortest:
        a(f"  {tok:>6} tok  #{cid:<7} {path[:88]}")

    a("")
    if rep.problems:
        a("PROBLEMS:")
        for p in rep.problems:
            a(f"  - {p}")
    else:
        a("No structural problems detected.")
    return "\n".join(out)
