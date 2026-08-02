"""DOCX adapter: structure from paragraph heading styles."""

from __future__ import annotations

import re
from pathlib import Path

import docx

from ..blocks import Block, Extraction

_HEADING_STYLE = re.compile(r"^Heading\s+(\d+)$", re.IGNORECASE)


def extract(path: Path, progress: object = None) -> Extraction:
    document = docx.Document(str(path))
    stack: list[str] = []
    blocks: list[Block] = []
    offset = 0
    title = ""

    for para in document.paragraphs:
        text = para.text.strip()
        if not text:
            continue
        style = (para.style.name or "") if para.style else ""
        m = _HEADING_STYLE.match(style)
        if style.lower() == "title" and not title:
            title = text
            continue
        if m:
            level = int(m.group(1))
            del stack[level - 1 :]
            while len(stack) < level - 1:
                stack.append("")
            stack.append(text)
            continue
        blocks.append(Block(heading_path=list(stack), locator={"offset": offset}, text=text))
        offset += len(text) + 1

    if not title:
        core = document.core_properties
        title = (core.title or "").strip() or path.stem
    return Extraction(
        title=title,
        format="docx",
        blocks=blocks,
        diagnostics={"structure_source": "heading_styles"},
    )
