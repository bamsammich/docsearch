"""Grading of chunk quality, independent of database integrity.

The failure modes under test are the ones the pipeline reports as success: a
document whose structure collapsed or shattered ingests cleanly, reaches
status 'ready' and is searchable, while being useless to search.
"""

from __future__ import annotations

import itertools
import sqlite3
from pathlib import Path

from docsearch.ingest import ingest_file
from docsearch.verify import (
    VERDICT_DEGRADED,
    VERDICT_GOOD,
    VERDICT_UNUSABLE,
    ChunkStat,
    grade,
    verify_document,
)

PROSE = "The console stores each cue in a sequence and plays it back on an executor. "

_uid = itertools.count()


def stat(
    tokens: int,
    *,
    numbered: bool = False,
    depth: int = 3,
    images: int = 0,
    text: str = "",
    heading: str | None = None,
) -> ChunkStat:
    # Distinct body text per chunk: identical text repeated across a corpus is
    # itself a graded defect, and would otherwise fire in fixtures built to
    # exercise an unrelated check.
    n = next(_uid)
    return ChunkStat(
        tokens=tokens,
        numbered=numbered,
        depth=depth,
        image_count=images,
        text=text or f"Topic {n} covers this. " + PROSE * max(1, tokens // 12),
        heading_path=f"Manual > Chapter {n}" if heading is None else heading,
    )


def codes(findings: list) -> set[str]:
    return {f.code for f in findings}


def verdict_of(findings: list) -> str:
    if any(f.severity == VERDICT_UNUSABLE for f in findings):
        return VERDICT_UNUSABLE
    if findings:
        return VERDICT_DEGRADED
    return VERDICT_GOOD


def test_healthy_distribution_grades_good() -> None:
    stats = [stat(300 + (i % 7) * 40, depth=2 + i % 3) for i in range(80)]
    assert grade(stats) == []


def test_collapsed_structure_is_unusable() -> None:
    """No derivable headings leaves whole chapters as single chunks."""
    stats = [stat(9000, depth=1) for _ in range(20)] + [stat(400) for _ in range(10)]
    findings = grade(stats)
    assert "oversized" in codes(findings)
    assert verdict_of(findings) == VERDICT_UNUSABLE


def test_a_few_oversized_chunks_only_degrade() -> None:
    stats = [stat(2400) for _ in range(9)] + [stat(400) for _ in range(51)]
    findings = grade(stats)
    assert "oversized" in codes(findings)
    assert verdict_of(findings) == VERDICT_DEGRADED


def test_shattered_structure_is_unusable() -> None:
    """A heading level detected too eagerly splits prose into fragments."""
    stats = [stat(12) for _ in range(70)] + [stat(300) for _ in range(10)]
    findings = grade(stats)
    assert "fragmented" in codes(findings)
    assert verdict_of(findings) == VERDICT_UNUSABLE


def test_small_numbered_sections_are_not_fragmentation() -> None:
    """A document that numbers its own short sections declared those boundaries.

    Merge-forward deliberately does not apply to them, so a reference index of
    hundreds of short numbered entries is correct output, not a shattered
    document, and must not be graded as one.
    """
    stats = [stat(35, numbered=True) for _ in range(300)] + [
        stat(400, numbered=True) for _ in range(40)
    ]
    assert "fragmented" not in codes(grade(stats))


def test_one_short_unnumbered_chunk_in_a_numbered_document_is_not_fragmentation() -> None:
    """A rate needs a denominator.

    A document that numbers nearly every section leaves a mergeable population
    of one or two chunks. Judging fragmentation from that reports whatever the
    single sample happens to be, at full confidence.
    """
    stats = [stat(400, numbered=True) for _ in range(943)] + [stat(20, numbered=False)]
    assert "fragmented" not in codes(grade(stats))


def test_flat_hierarchy_is_flagged_only_when_it_costs_something() -> None:
    """Flatness disables orientation, which only matters once search is deep.

    The small case sits above the floor for grading a distribution at all, so
    it pins the hierarchy gate rather than passing for an unrelated reason.
    """
    flat_large = [stat(300, depth=1) for _ in range(120)]
    assert "flat_hierarchy" in codes(grade(flat_large))

    flat_small = [stat(300, depth=1) for _ in range(30)]
    assert "flat_hierarchy" not in codes(grade(flat_small))


def test_budget_sliced_document_is_unaddressable() -> None:
    """The failure the pipeline reports as success.

    With no derivable structure the chunker cuts on the token budget alone.
    Every slice is well-sized and the ingest is clean, so no size-based check
    fires -- but consecutive slices inherit one heading, so `section_filter`
    cannot separate them and `outline` describes the document in a handful of
    entries. This is the shape a real 70-page manual produced.
    """
    stats = [stat(1190, heading=f"Chapter {i // 7}") for i in range(35)]
    findings = grade(stats)
    assert "unaddressable" in codes(findings)
    assert verdict_of(findings) == VERDICT_UNUSABLE
    assert "oversized" not in codes(findings), "sizes are legal; only addressability fails"


def test_headless_chunks_are_named_in_the_evidence() -> None:
    stats = [stat(400, heading="") for _ in range(12)] + [
        stat(400, heading=f"Chapter {i % 3}") for i in range(18)
    ]
    detail = next(f for f in grade(stats) if f.code == "unaddressable").detail
    assert "12 chunks carry no heading" in detail


def test_a_well_structured_document_is_addressable() -> None:
    """Guards the threshold against firing on documents known to chunk well.

    Real corpora that retrieve correctly measure 0.84 and 0.93 distinct
    heading paths per chunk; this sits between them.
    """
    stats = [stat(400, heading=f"Manual > {i // 10} > Section {i}") for i in range(200)]
    stats += [stat(400, heading="Manual > Appendix") for _ in range(30)]
    assert "unaddressable" not in codes(grade(stats))


def test_figure_dominated_document_is_flagged() -> None:
    stats = [stat(20, images=3) for _ in range(40)] + [stat(400) for _ in range(60)]
    findings = grade(stats)
    assert "figure_dominated" in codes(findings)


def test_unstripped_boilerplate_is_flagged_with_the_offending_line() -> None:
    footer = "(c) 2019 MA Lighting Technology GmbH - Waldbuettelbrunn - Germany"
    stats = [
        ChunkStat(
            tokens=300,
            numbered=False,
            depth=2,
            image_count=0,
            text=f"{footer}\n{PROSE * 20}\nPage {i} of 400",
        )
        for i in range(60)
    ]
    findings = grade(stats)
    assert "boilerplate" in codes(findings)
    assert footer[:30] in next(f for f in findings if f.code == "boilerplate").detail


def test_every_finding_states_its_evidence_and_consequence() -> None:
    stats = [stat(9000, depth=1) for _ in range(60)]
    assert grade(stats), "fixture must produce findings for this to assert anything"
    for f in grade(stats):
        assert any(ch.isdigit() for ch in f.detail), f"{f.code} cites no numbers"
        assert len(f.detail) > 40, f"{f.code} detail is too thin to act on"


def test_clean_ingest_verdicts_good_end_to_end(
    conn: sqlite3.Connection, md_file: Path
) -> None:
    result = ingest_file(conn, md_file)
    report = verify_document(conn, result.doc_id)
    assert report.problems == []
    assert report.verdict == VERDICT_GOOD
    assert report.findings == []
