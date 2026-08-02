"""HTML adapter: structure from h1-h6 nesting."""

from __future__ import annotations

from pathlib import Path

from bs4 import BeautifulSoup

from ..blocks import Block, Extraction

_HEADINGS = {f"h{i}": i for i in range(1, 7)}
_BLOCK_TAGS = ("p", "li", "pre", "blockquote", "td", "dd", "dt")


def extract(path: Path, progress: object = None) -> Extraction:
    soup = BeautifulSoup(path.read_text(encoding="utf-8", errors="replace"), "lxml")
    for tag in soup(["script", "style", "nav", "footer"]):
        tag.decompose()

    stack: list[str] = []
    blocks: list[Block] = []
    offset = 0
    body = soup.body or soup

    for el in body.find_all([*_HEADINGS, *_BLOCK_TAGS]):
        name = el.name.lower()
        text = el.get_text(" ", strip=True)
        if not text:
            continue
        if name in _HEADINGS:
            level = _HEADINGS[name]
            del stack[level - 1 :]
            while len(stack) < level - 1:
                stack.append("")
            stack.append(text)
            continue
        # Skip nested block tags whose text a parent already contributed.
        if el.find_parent(_BLOCK_TAGS) is not None:
            continue
        blocks.append(Block(heading_path=list(stack), locator={"offset": offset}, text=text))
        offset += len(text) + 1

    title = (soup.title.string.strip() if soup.title and soup.title.string else "") or path.stem
    return Extraction(
        title=title,
        format="html",
        blocks=blocks,
        diagnostics={"structure_source": "h1_h6_nesting"},
    )
