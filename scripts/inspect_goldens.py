"""Write what Python's reconnaissance makes of the committed PDF fixtures.

internal/adapter/pdf.Inspect must report the same findings. The PDF engines
differ, so the parity claim is the one the spike settled: the questions and
their answers agree, even where the two libraries read the page differently.

Rerun after changing either reconnaissance:

    uv run python scripts/inspect_goldens.py

Retired with the Python pipeline.
"""

from __future__ import annotations

import dataclasses
import json
from pathlib import Path

from docsearch.inspect import inspect_document

FIXTURES = Path("testdata/adapters")
OUT = Path("testdata/inspect")


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    for pdf in sorted(FIXTURES.glob("*.pdf")):
        report = inspect_document(pdf)
        payload = {
            "format": report.format,
            "page_count": report.page_count,
            "predicted_source": report.predicted_source,
            "predicted_tier": report.predicted_tier,
            "blocked": report.blocked,
            "findings": [dataclasses.asdict(f) for f in report.findings],
        }
        (OUT / f"{pdf.name}.json").write_text(
            json.dumps(payload, ensure_ascii=False, indent=1, sort_keys=True) + "\n"
        )
        print(f"{pdf.name:>16}  {report.predicted_source:>16}  {len(report.findings)} findings")


if __name__ == "__main__":
    main()
