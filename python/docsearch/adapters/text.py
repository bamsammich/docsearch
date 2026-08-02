"""Plain-text adapter: no structure, blank-line paragraph blocks."""

from __future__ import annotations

from pathlib import Path

from ..blocks import Block, Extraction


def extract(path: Path, progress: object = None) -> Extraction:
    raw = path.read_text(encoding="utf-8", errors="replace")
    blocks: list[Block] = []
    offset = 0
    for para in raw.split("\n\n"):
        body = para.strip()
        if body:
            blocks.append(Block(heading_path=[], locator={"offset": offset}, text=body))
        offset += len(para) + 2
    return Extraction(
        title=path.stem,
        format="text",
        blocks=blocks,
        diagnostics={"structure_source": "none (blank-line paragraphs)"},
    )
