"""Write the grading fixtures and the Python verdict on each.

internal/domain.Grade must reproduce docsearch.verify.grade exactly, detail
strings included: the detail is what an operator reads, and a divergence in a
percentage or a separator is a divergence in the report.

The distributions are the ones tests/test_verify_grading.py exercises, plus a
few that pin the formatting. Rerun after changing either grader:

    uv run python scripts/grade_goldens.py

Retired with the Python pipeline.
"""

from __future__ import annotations

import dataclasses
import itertools
import json
from pathlib import Path

from docsearch.verify import ChunkStat, grade

OUT = Path("testdata/grading")

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


def healthy() -> list[ChunkStat]:
    return [stat(300 + (i % 7) * 40, depth=2 + i % 3) for i in range(80)]


def collapsed() -> list[ChunkStat]:
    """No derivable headings leaves whole chapters as single chunks."""
    return [stat(9000, depth=1) for _ in range(20)] + [stat(400) for _ in range(10)]


def a_few_oversized() -> list[ChunkStat]:
    return [stat(2400) for _ in range(9)] + [stat(400) for _ in range(51)]


def shattered() -> list[ChunkStat]:
    """A heading level detected too eagerly splits prose into fragments."""
    return [stat(12) for _ in range(70)] + [stat(300) for _ in range(10)]


def numbered_reference() -> list[ChunkStat]:
    """Short numbered entries are declared boundaries, not fragmentation."""
    return [stat(35, numbered=True) for _ in range(300)] + [
        stat(400, numbered=True) for _ in range(40)
    ]


def one_mergeable_chunk() -> list[ChunkStat]:
    """A rate needs a denominator."""
    return [stat(400, numbered=True) for _ in range(943)] + [stat(20, numbered=False)]


def flat_large() -> list[ChunkStat]:
    return [stat(300, depth=1) for _ in range(120)]


def flat_small() -> list[ChunkStat]:
    return [stat(300, depth=1) for _ in range(30)]


def budget_sliced() -> list[ChunkStat]:
    """The failure the pipeline reports as success."""
    return [stat(1190, heading=f"Chapter {i // 7}") for i in range(35)]


def headless() -> list[ChunkStat]:
    return [stat(400, heading="") for _ in range(30)]


def figure_dominated() -> list[ChunkStat]:
    return [stat(20, images=2) for _ in range(30)] + [stat(400) for _ in range(30)]


def boilerplate() -> list[ChunkStat]:
    footer = "Copyright 2026 The Console Company, all rights reserved"
    return [stat(400, text=f"Topic {i} covers this. {PROSE * 30}\n{footer}") for i in range(40)]


def numbered_steps() -> list[ChunkStat]:
    """Procedure steps repeat and are the document's own instructions."""
    return [stat(400, text=f"Topic {i} covers this. {PROSE * 30}\n1\n2\n3") for i in range(40)]


def below_the_floor() -> list[ChunkStat]:
    """Under GRADE_MIN_CHUNKS nothing is graded at all."""
    return [stat(9000, depth=1) for _ in range(10)]


CASES = {
    "healthy": healthy,
    "collapsed": collapsed,
    "a-few-oversized": a_few_oversized,
    "shattered": shattered,
    "numbered-reference": numbered_reference,
    "one-mergeable-chunk": one_mergeable_chunk,
    "flat-large": flat_large,
    "flat-small": flat_small,
    "budget-sliced": budget_sliced,
    "headless": headless,
    "figure-dominated": figure_dominated,
    "boilerplate": boilerplate,
    "numbered-steps": numbered_steps,
    "below-the-floor": below_the_floor,
}


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    for name, build in CASES.items():
        stats = build()
        findings = grade(stats)
        payload = {
            "chunks": [dataclasses.asdict(s) for s in stats],
            "findings": [dataclasses.asdict(f) for f in findings],
        }
        path = OUT / f"{name}.json"
        path.write_text(json.dumps(payload, ensure_ascii=False, indent=1, sort_keys=True) + "\n")
        codes = ", ".join(f.code for f in findings) or "none"
        print(f"{name:>22}  {len(stats):>4} chunks  {codes}")


if __name__ == "__main__":
    main()
