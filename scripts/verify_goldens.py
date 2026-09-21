"""Write what Python's verify report looks like, for the Go CLI to reproduce.

`docsearch verify` prints this text, and step 7g moves the printing to Go.
The reports here are built by hand rather than read from an index: the
formatter is what is under test, and a hand-built report reaches the branches
a real document rarely has, such as a back-of-book index whose entries join
nothing.

Chunk ids equal ordinals in these reports. Go names an extreme chunk by its
ordinal where Python names it by its row id, a divergence step 7d recorded,
and making the two agree here keeps the formatter's parity claim about the
formatter.

Rerun after changing either formatter:

    uv run python scripts/verify_goldens.py

Retired with the Python pipeline.
"""

from __future__ import annotations

import dataclasses
import json
from pathlib import Path

from docsearch.verify import Finding, VerifyReport, format_report

OUT = Path("testdata/verify")


def healthy() -> VerifyReport:
    return VerifyReport(
        doc_id="healthy-manual",
        title="A Manual That Chunked Well",
        format="pdf",
        status="ready",
        chunk_count=412,
        page_count=318,
        token_min=64,
        token_median=511,
        token_p95=1204,
        token_max=1613,
        token_mean=548,
        total_tokens=225776,
        chunks_with_images=37,
        longest=[(i, 1613 - i * 11, f"4. Operation > 4.{i} Step {i}") for i in range(1, 11)],
        shortest=[(200 + i, 64 + i * 3, f"A. Appendix > A.{i} Table {i}") for i in range(1, 11)],
    )


def degraded() -> VerifyReport:
    rep = healthy()
    rep.doc_id = "degraded-manual"
    rep.title = "A Manual With Findings"
    rep.uncovered_pages = list(range(40, 61))
    rep.index_terms = 880
    rep.unjoinable_index_sections = ["7. Troubleshooting", "8. Service"]
    rep.findings = [
        Finding(
            code="oversized",
            severity="degraded",
            detail=(
                "41 of 412 chunks (10%) exceed 1,200 tokens, which is where a "
                "retrieved chunk stops fitting a prompt window alongside the rest "
                "of a conversation and starts being truncated by whoever reads it."
            ),
        ),
        Finding(
            code="flat_hierarchy",
            severity="unusable",
            detail=(
                "388 of 412 chunks carry a one-level heading path, so a section "
                "filter can only ever name the document itself."
            ),
        ),
    ]
    rep.problems = [
        "21 pages are covered by no chunk, so a search can never return them",
        "2 index sections join no chunk",
    ]
    return rep


def small() -> VerifyReport:
    return VerifyReport(
        doc_id="short-note",
        title="Eleven Chunks",
        format="md",
        status="ready",
        chunk_count=11,
        page_count=None,
        token_min=90,
        token_median=240,
        token_p95=602,
        token_max=640,
        token_mean=268,
        total_tokens=2948,
        chunks_with_images=0,
        longest=[(i, 640 - i * 40, f"Notes > Part {i}") for i in range(1, 12)],
        shortest=[(i, 90 + i * 12, f"Notes > Part {i}") for i in range(1, 12)],
    )


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    for name, build in (("healthy", healthy), ("degraded", degraded), ("small", small)):
        report = build()
        fields = dataclasses.asdict(report)
        # Named rather than positional: Go reads these without having to
        # guess what the second element of a triple was.
        for key in ("longest", "shortest"):
            fields[key] = [
                {"ordinal": cid, "tokens": tok, "path": path} for cid, tok, path in fields[key]
            ]
        payload = {
            "report": fields,
            "verdict": report.verdict,
            "text": format_report(report),
        }
        (OUT / f"{name}.json").write_text(
            json.dumps(payload, ensure_ascii=False, indent=1, sort_keys=True) + "\n"
        )
        print(f"{name:>10}  {report.verdict:<9} {len(payload['text'].splitlines())} lines")


if __name__ == "__main__":
    main()
