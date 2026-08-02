"""Markdown adapter: structure from ATX heading levels."""

from __future__ import annotations

import re
from pathlib import Path

from ..blocks import Block, Extraction

_ATX = re.compile(r"^(#{1,6})\s+(.*?)\s*#*$")
_FENCE = re.compile(r"^\s*(```|~~~)")


def extract(path: Path, progress: object = None) -> Extraction:
    text = path.read_text(encoding="utf-8", errors="replace")
    stack: list[str] = []
    blocks: list[Block] = []
    para: list[str] = []
    para_offset = 0
    offset = 0
    in_fence = False

    def flush() -> None:
        nonlocal para
        body = "\n".join(para).strip()
        if body:
            blocks.append(
                Block(heading_path=list(stack), locator={"offset": para_offset}, text=body)
            )
        para = []

    for line in text.splitlines(keepends=True):
        stripped = line.rstrip("\n")
        if _FENCE.match(stripped):
            in_fence = not in_fence
            para.append(stripped)
            offset += len(line)
            continue
        m = None if in_fence else _ATX.match(stripped)
        if m:
            flush()
            level = len(m.group(1))
            del stack[level - 1 :]
            while len(stack) < level - 1:
                stack.append("")
            stack.append(m.group(2).strip())
        elif not stripped.strip():
            flush()
            para_offset = offset + len(line)
        else:
            if not para:
                para_offset = offset
            para.append(stripped)
        offset += len(line)
    flush()

    first_h1 = next((b.heading_path[0] for b in blocks if b.heading_path), None)
    return Extraction(
        title=first_h1 or path.stem,
        format="markdown",
        blocks=blocks,
        diagnostics={"structure_source": "atx_headings"},
    )
