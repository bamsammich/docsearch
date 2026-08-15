"""HTML adapter: structure from h1-h6 nesting.

:func:`parse` is the shared half. A local ``.html`` file and a page of a
crawled site are the same parsing problem, and the parts that are easy to get
wrong -- reading a ``<pre>`` verbatim, not letting a parent re-flatten a nested
one -- should not exist twice. :mod:`docsearch.site` calls :func:`parse` and
supplies its own locators, sections and addresses.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any

from bs4 import BeautifulSoup

from ..blocks import Block, Extraction

_HEADINGS = {f"h{i}": i for i in range(1, 7)}
_BLOCK_TAGS = ("p", "li", "pre", "blockquote", "td", "dd", "dt")

#: Stripped before parsing. Chrome is not content, and a navigation repeated on
#: every page of a site is the single largest source of duplicated term mass.
_CHROME_TAGS = ("script", "style", "nav", "footer")


@dataclass(slots=True)
class HtmlItem:
    """One block of text, with the heading ancestry above it."""

    heading_path: list[str]
    text: str
    #: id of the nearest heading at or above this item, without the '#'.
    fragment: str | None = None


def _code_text(el: Any) -> str:
    """Verbatim text of a ``<pre>``, less the blank lines framing it.

    Prose is read with ``get_text(" ", strip=True)``, which strips every text
    node and joins the results with a space. A hand-written ``<pre>`` holds one
    text node and survives that, but generated documentation wraps each token
    in its own ``<span>`` -- so every newline and every run of indentation
    becomes a single space, and a code sample arrives as one line of tokens
    with spaces around the brackets.
    """
    lines = el.get_text().splitlines()
    while lines and not lines[0].strip():
        lines.pop(0)
    while lines and not lines[-1].strip():
        lines.pop()
    return "\n".join(lines)


def parse(html: bytes | str) -> tuple[str, list[HtmlItem]]:
    """Return ``(title, items)`` for one HTML document."""
    soup = BeautifulSoup(html, "lxml")
    for tag in soup(list(_CHROME_TAGS)):
        tag.decompose()

    stack: list[str] = []
    items: list[HtmlItem] = []
    fragment: str | None = None
    body = soup.body or soup

    # Code is read out first and the element emptied. A <pre> inside a list
    # item or table cell is emitted as its own item, so its ancestor must not
    # also contribute a flattened copy of it; emptying rather than removing
    # keeps the element in document order for the walk below. Nested <pre> is
    # invalid and would be destroyed by clearing its parent, so only outermost
    # ones are collected -- that keeps this list aligned with what the walk
    # finds.
    codes: list[str] = []
    for pre in body.find_all("pre"):
        if pre.find_parent("pre") is not None:
            continue
        codes.append(_code_text(pre))
        pre.clear()
    code_iter = iter(codes)

    for el in body.find_all([*_HEADINGS, *_BLOCK_TAGS]):
        name = el.name.lower()
        if name == "pre":
            # Emitted whatever it is nested in: code is the content a reader
            # came for, and flattening it into a parent loses its lines.
            text = next(code_iter, "")
            if not text:
                continue
            items.append(HtmlItem(heading_path=list(stack), text=text, fragment=fragment))
            continue

        text = el.get_text(" ", strip=True)
        if not text:
            continue
        if name in _HEADINGS:
            level = _HEADINGS[name]
            del stack[level - 1 :]
            while len(stack) < level - 1:
                stack.append("")
            stack.append(text)
            # An anchor is how a citation lands on the section rather than the
            # top of the page, so it is tracked from the heading that owns it.
            anchor = str(el.get("id") or "").strip()
            if not anchor:
                inner = el.find(attrs={"id": True})
                anchor = str(inner.get("id")).strip() if inner is not None else ""
            fragment = anchor or None
            continue
        # Skip nested block tags whose text a parent already contributed.
        if el.find_parent(_BLOCK_TAGS) is not None:
            continue
        items.append(HtmlItem(heading_path=list(stack), text=text, fragment=fragment))

    title = (soup.title.string.strip() if soup.title and soup.title.string else "") or ""
    return title, items


def extract(path: Path, progress: object = None) -> Extraction:
    title, items = parse(path.read_text(encoding="utf-8", errors="replace"))
    blocks: list[Block] = []
    offset = 0
    for item in items:
        blocks.append(
            Block(heading_path=item.heading_path, locator={"offset": offset}, text=item.text)
        )
        offset += len(item.text) + 1
    return Extraction(
        title=title or path.stem,
        format="html",
        blocks=blocks,
        diagnostics={"structure_source": "h1_h6_nesting"},
    )
