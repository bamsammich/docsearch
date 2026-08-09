#!/usr/bin/env python3
"""Generate a large, structurally valid PDF for queue testing.

The real corpus ingests in about ten seconds, which is too fast to observe
lease renewal, mid-ingest invisibility, advancing progress, or cancellation at
a checkpoint. Those are the failure modes the queue exists to handle, so the
tests need an input slow enough to catch a worker in the act.

Usage:
    python scripts/make_slow_fixture.py out.pdf --pages 6000
"""

from __future__ import annotations

import argparse
import random
from pathlib import Path

import pymupdf

BODY_SIZE = 10.0
HEAD_SIZE = 17.0
LINES_PER_PAGE = 38

# Body text must vary line to line. Boilerplate detection normalizes digits to
# '#', so a filler sentence differing only by a trailing number collapses to a
# single key, clears the repetition threshold, and the whole document is
# stripped as running furniture -- yielding a valid-looking ingest of zero
# chunks. Vary the vocabulary, not just the numbers.
SUBJECTS = [
    "executor",
    "sequence",
    "cue",
    "preset",
    "fixture",
    "group",
    "macro",
    "effect",
    "world",
    "filter",
]
VERBS = ["controls", "adjusts", "stores", "recalls", "merges", "overwrites", "releases", "assigns"]
OBJECTS = [
    "playback",
    "timing",
    "intensity",
    "colour",
    "position",
    "gobo",
    "priority",
    "fade",
    "delay",
]
QUALIFIERS = (
    "in the current page",
    "for the selected fixtures",
    "while the programmer is active",
    "unless a filter excludes it",
    "after the crossfade completes",
    "when tracking is enabled",
)


def sentence(rng: random.Random) -> str:
    return (
        f"The {rng.choice(SUBJECTS)} {rng.choice(VERBS)} {rng.choice(OBJECTS)} "
        f"{rng.choice(QUALIFIERS)}, and the {rng.choice(SUBJECTS)} "
        f"{rng.choice(VERBS)} {rng.choice(OBJECTS)} as a result."
    )


def build(out: Path, pages: int, sections_per_chapter: int = 12) -> None:
    doc = pymupdf.open()
    rng = random.Random(20260802)

    # Front table of contents, so the adapter takes the reconstruction path and
    # cross-validation has something real to check.
    total_sections = max(1, pages // 6)
    toc_pages = (total_sections // 30) + 1
    entries: list[tuple[str, str, int]] = []
    n = 0
    while len(entries) < total_sections:
        n += 1
        entries.append((f"{n}.", f"Chapter {n} Operations", toc_pages + 1 + len(entries) * 6))
        for s in range(1, sections_per_chapter + 1):
            if len(entries) >= total_sections:
                break
            entries.append(
                (f"{n}.{s}.", f"Section {n}.{s} Detail", toc_pages + 1 + len(entries) * 6)
            )

    per_toc_page = -(-len(entries) // toc_pages)
    for i in range(toc_pages):
        page = doc.new_page()
        y = 80.0
        for number, title, target in entries[i * per_toc_page : (i + 1) * per_toc_page]:
            page.insert_text((60, y), number, fontsize=11.2)
            page.insert_text((130, y), title, fontsize=11.2)
            page.insert_text((520, y), str(target), fontsize=11.2)
            y += 16

    emitted = 0
    idx = 0
    while doc.page_count < pages and idx < len(entries):
        number, title, _ = entries[idx]
        idx += 1
        page = doc.new_page()
        page.insert_text((60, 70), number, fontsize=HEAD_SIZE)
        page.insert_text((110, 70), title, fontsize=HEAD_SIZE)
        y = 110.0
        for _ in range(LINES_PER_PAGE - 4):
            page.insert_text((60, y), sentence(rng), fontsize=BODY_SIZE)
            y += 14
        page.insert_text((60, 780), "Generated Fixture - docsearch", fontsize=8.8)
        emitted += 1
        # Continuation pages keep each section large enough to be worth chunking.
        for _ in range(4):
            if doc.page_count >= pages:
                break
            cont = doc.new_page()
            y = 80.0
            for _ in range(LINES_PER_PAGE):
                cont.insert_text((60, y), sentence(rng), fontsize=BODY_SIZE)
                y += 14
            cont.insert_text((60, 780), "Generated Fixture - docsearch", fontsize=8.8)

    doc.save(out, deflate=True)
    doc.close()
    print(f"wrote {out} ({out.stat().st_size / 1e6:.1f} MB, {pages} pages, {emitted} sections)")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("out", type=Path)
    ap.add_argument("--pages", type=int, default=6000)
    args = ap.parse_args()
    build(args.out, args.pages)


if __name__ == "__main__":
    main()
