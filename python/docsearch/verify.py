"""Post-ingest structural checks.

Eyeballing the token distribution and the extreme chunks catches structural
extraction failures immediately -- a heading source that silently collapsed
shows up as a handful of enormous chunks, and a shattered one as hundreds of
near-empty chunks. Run it after every ingest.
"""

from __future__ import annotations

import itertools
import re
import sqlite3
from collections import defaultdict
from dataclasses import dataclass, field

from .chunker import MAX_TOKENS, MIN_TOKENS, PATH_SEP
from .db import chunks_in_section
from .tokens import estimate_tokens

VERDICT_GOOD = "good"
VERDICT_DEGRADED = "degraded"
VERDICT_UNUSABLE = "unusable"

#: Grading a distribution needs a distribution. Below this, rate-based checks
#: are noise: one short chunk in a four-chunk document is not fragmentation.
GRADE_MIN_CHUNKS = 25

#: Size thresholds are expressed against the chunker's own declared bounds so
#: the two notions of a well-sized chunk cannot drift apart. A chunk above
#: MAX_TOKENS survived a subdivision pass, meaning it offered no internal
#: boundary to split on.
OVERSIZED_DEGRADED_RATE = 0.10
OVERSIZED_UNUSABLE_RATE = 0.35

#: Merge-forward already absorbs short unnumbered units, so what is left below
#: MIN_TOKENS is what merging could not fix.
FRAGMENTED_DEGRADED_RATE = 0.30
FRAGMENTED_UNUSABLE_RATE = 0.60

#: Orientation is the largest measured retrieval lever and a document with no
#: hierarchy has none to offer. It costs nothing until there are enough chunks
#: for a cold search to get lost in.
FLAT_MIN_CHUNKS = 50

#: Distinct heading paths per chunk. When boundaries come from the token budget
#: rather than from the document, consecutive slices inherit one heading, and
#: `section_filter` can no longer address them apart. Well-structured corpora
#: measure 0.84 and 0.93; a manual whose structure was not derived at all, and
#: which was therefore sliced into fixed windows, measures 0.29.
ADDRESSABLE_DEGRADED_RATIO = 0.50
ADDRESSABLE_UNUSABLE_RATIO = 0.35

#: A chunk carrying images and almost no text holds its content in the picture,
#: where no amount of retrieval will reach it.
FIGURE_MAX_TOKENS = 40
FIGURE_DEGRADED_RATE = 0.25

#: Mirrors the PDF adapter's own rule: a line repeated across this fraction of
#: the document is furniture. Running headers and footers are short, so a long
#: repeated passage is duplicated content -- a different defect, not this one.
BOILERPLATE_CHUNK_FRACTION = 0.25
BOILERPLATE_MIN_CHUNKS = 8
BOILERPLATE_MAX_LINE_CHARS = 120

_DIGITS = re.compile(r"\d+")
_SPACE = re.compile(r"\s+")


@dataclass(frozen=True, slots=True)
class Finding:
    """One graded defect, its evidence, and what it costs the caller."""

    code: str
    severity: str
    detail: str


@dataclass(frozen=True, slots=True)
class ChunkStat:
    """The per-chunk facts grading reads. Decoupled from the row shape so the
    grader can be exercised on constructed distributions."""

    tokens: int
    numbered: bool
    depth: int
    image_count: int
    text: str
    heading_path: str = ""

    @property
    def figure_dominated(self) -> bool:
        return self.image_count > 0 and self.tokens < FIGURE_MAX_TOKENS


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
    scattered_sections: list[str] = field(default_factory=list)
    chunks_with_images: int = 0
    problems: list[str] = field(default_factory=list)
    findings: list[Finding] = field(default_factory=list)

    @property
    def verdict(self) -> str:
        """Chunk quality only. Database integrity is reported in `problems`:
        the two answer different questions and a document can fail either."""
        if any(f.severity == VERDICT_UNUSABLE for f in self.findings):
            return VERDICT_UNUSABLE
        return VERDICT_DEGRADED if self.findings else VERDICT_GOOD


def _repeated_lines(stats: list[ChunkStat]) -> list[tuple[int, str]]:
    """Short lines occurring in many chunks, with a verbatim example of each.

    Digits are normalized so that a page footer counts as one line rather than
    as one distinct line per page.
    """
    counts: defaultdict[str, int] = defaultdict(int)
    example: dict[str, str] = {}
    for s in stats:
        for norm, raw in {
            _DIGITS.sub("#", _SPACE.sub(" ", ln.strip())): ln.strip()
            for ln in s.text.splitlines()
            if ln.strip() and len(ln.strip()) <= BOILERPLATE_MAX_LINE_CHARS
        }.items():
            counts[norm] += 1
            example.setdefault(norm, raw)
    floor = max(BOILERPLATE_MIN_CHUNKS, int(len(stats) * BOILERPLATE_CHUNK_FRACTION))
    return sorted(
        ((n, example[k]) for k, n in counts.items() if n >= floor), key=lambda p: -p[0]
    )


def grade(stats: list[ChunkStat]) -> list[Finding]:
    """Assess whether chunking produced a searchable document.

    Separate from the integrity checks: every defect here is compatible with a
    clean ingest that reaches status 'ready'. A document can be perfectly
    consistent and still be shaped so that retrieval cannot work on it.
    """
    total = len(stats)
    if total < GRADE_MIN_CHUNKS:
        return []
    out: list[Finding] = []

    oversized = [s for s in stats if s.tokens > MAX_TOKENS]
    if oversized:
        rate = len(oversized) / total
        biggest = max(s.tokens for s in oversized)
        if rate >= OVERSIZED_DEGRADED_RATE:
            out.append(
                Finding(
                    code="oversized",
                    severity=(
                        VERDICT_UNUSABLE if rate >= OVERSIZED_UNUSABLE_RATE else VERDICT_DEGRADED
                    ),
                    detail=(
                        f"{len(oversized)} of {total} chunks ({rate:.0%}) exceed the "
                        f"{MAX_TOKENS}-token chunk cap, the largest at {biggest:,}. Subdivision "
                        f"found no boundary inside them, which means the heading structure was "
                        f"too coarse or was not derived at all. Search returns whole chapters."
                    ),
                )
            )

    # The mergeable population needs to be large enough to carry a rate in its
    # own right. A document that numbers nearly everything leaves a handful of
    # mergeable chunks, and one short chunk out of one is not a distribution.
    mergeable = [s for s in stats if not s.numbered and not s.figure_dominated]
    fragments = [s for s in mergeable if s.tokens < MIN_TOKENS]
    if len(mergeable) >= GRADE_MIN_CHUNKS and fragments:
        rate = len(fragments) / len(mergeable)
        if rate >= FRAGMENTED_DEGRADED_RATE:
            out.append(
                Finding(
                    code="fragmented",
                    severity=(
                        VERDICT_UNUSABLE if rate >= FRAGMENTED_UNUSABLE_RATE else VERDICT_DEGRADED
                    ),
                    detail=(
                        f"{len(fragments)} of {len(mergeable)} mergeable chunks ({rate:.0%}) are "
                        f"under {MIN_TOKENS} tokens after merge-forward. A heading level was "
                        f"detected too eagerly and split prose into fragments, so no single "
                        f"chunk carries enough context to answer a question."
                    ),
                )
            )

    if total >= FLAT_MIN_CHUNKS and max(s.depth for s in stats) <= 1:
        out.append(
            Finding(
                code="flat_hierarchy",
                severity=VERDICT_DEGRADED,
                detail=(
                    f"All {total} chunks sit at heading depth 1, so the document has no "
                    f"hierarchy. `outline` cannot orient a caller and `section_filter` cannot "
                    f"narrow a search -- the largest measured retrieval lever is unavailable "
                    f"and every query falls back to cold keyword search."
                ),
            )
        )

    figures = [s for s in stats if s.figure_dominated]
    if figures and len(figures) / total >= FIGURE_DEGRADED_RATE:
        rate = len(figures) / total
        out.append(
            Finding(
                code="figure_dominated",
                severity=VERDICT_DEGRADED,
                detail=(
                    f"{len(figures)} of {total} chunks ({rate:.0%}) carry an image and under "
                    f"{FIGURE_MAX_TOKENS} tokens of text. Their content is in the picture, which "
                    f"no retrieval reaches; a caller receives the caption and must be told to "
                    f"look at the page."
                ),
            )
        )

    distinct = len({s.heading_path for s in stats})
    ratio = distinct / total
    if ratio < ADDRESSABLE_DEGRADED_RATIO:
        headless = sum(1 for s in stats if not s.heading_path.strip())
        extra = (
            f" {headless} chunks carry no heading at all and cannot be reached by "
            f"heading, filtered, or described in an outline."
            if headless
            else ""
        )
        out.append(
            Finding(
                code="unaddressable",
                severity=(
                    VERDICT_UNUSABLE if ratio < ADDRESSABLE_UNUSABLE_RATIO else VERDICT_DEGRADED
                ),
                detail=(
                    f"{total} chunks share only {distinct} distinct heading paths "
                    f"({ratio:.2f} per chunk). Consecutive chunks inherit one heading, which "
                    f"happens when boundaries came from the token budget rather than from the "
                    f"document -- structure was not derived and the text was cut into fixed "
                    f"windows. `section_filter` cannot separate them and `outline` describes "
                    f"the document in {distinct} entries.{extra}"
                ),
            )
        )

    repeated = _repeated_lines(stats)
    if repeated:
        worst, line = repeated[0]
        out.append(
            Finding(
                code="boilerplate",
                severity=VERDICT_DEGRADED,
                detail=(
                    f"{len(repeated)} line(s) repeat across a quarter or more of the document, "
                    f"the most frequent in {worst} of {total} chunks: {line[:80]!r}. Running "
                    f"headers and footers were not stripped, so a share of the BM25 term mass "
                    f"is furniture and every chunk matches it equally."
                ),
            )
        )

    return out


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

    rep.findings = grade(
        [
            ChunkStat(
                tokens=t,
                numbered=bool(r["section"]),
                depth=len(r["heading_path"].split(PATH_SEP)),
                image_count=r["image_count"] or 0,
                text=r["text"],
                heading_path=r["heading_path"],
            )
            for t, r in sized
        ]
    )

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

    # A section split across non-adjacent ordinals was never subdivided -- a
    # boundary misfired and scattered one section key across the document,
    # which silently breaks the index_terms section -> chunk join.
    by_section: dict[str, list[int]] = {}
    for r in rows:
        if r["section"]:
            by_section.setdefault(r["section"], []).append(r["ordinal"])
    rep.scattered_sections = sorted(
        s
        for s, ords in by_section.items()
        if len(ords) > 1 and ords != list(range(ords[0], ords[0] + len(ords)))
    )

    rep.index_terms = conn.execute(
        "SELECT COUNT(*) c FROM index_terms WHERE doc_id = ?", (doc_id,)
    ).fetchone()["c"]
    if rep.index_terms:
        # Subtree semantics via the shared rule in db.SECTION_MATCH_SQL: an
        # index entry pointing at chapter 4 refers to the whole chapter, and a
        # chapter whose preamble folded into its first child has no chunk of
        # its own to match exactly.
        sections = [
            r["section"]
            for r in conn.execute(
                "SELECT DISTINCT section FROM index_terms WHERE doc_id = ? ORDER BY section",
                (doc_id,),
            )
        ]
        rep.unjoinable_index_sections = [
            s for s in sections if not chunks_in_section(conn, doc_id, s)
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
    if rep.scattered_sections:
        rep.problems.append(
            f"{len(rep.scattered_sections)} sections span non-adjacent chunks "
            f"(boundary misfire): {', '.join(rep.scattered_sections[:8])}"
        )
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
    summary = {
        VERDICT_GOOD: "chunking looks healthy",
        VERDICT_DEGRADED: "searchable, with findings that cap retrieval quality",
        VERDICT_UNUSABLE: "structure extraction effectively failed",
    }[rep.verdict]
    a(f"VERDICT     {rep.verdict} - {summary}")
    if rep.chunk_count < GRADE_MIN_CHUNKS:
        a(
            f"            (only {rep.chunk_count} chunks; below {GRADE_MIN_CHUNKS} the "
            f"distribution is too small to grade)"
        )
    for f in rep.findings:
        a("")
        a(f"  [{f.severity}] {f.code}")
        for line in _wrap(f.detail, 76):
            a(f"      {line}")

    a("")
    if rep.problems:
        a("PROBLEMS:")
        for p in rep.problems:
            a(f"  - {p}")
    else:
        a("No structural problems detected.")
    return "\n".join(out)


def _wrap(text: str, width: int) -> list[str]:
    lines: list[str] = []
    current = ""
    for word in text.split():
        if current and len(current) + 1 + len(word) > width:
            lines.append(current)
            current = word
        else:
            current = f"{current} {word}".strip()
    if current:
        lines.append(current)
    return lines
