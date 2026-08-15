"""A documentation site, as one document.

A site is a book whose chapters are pages. Its navigation declares which
sections exist and how they nest, exactly as a PDF's embedded outline does, so
each page becomes an **authoritative section** keyed by its position in the nav
tree and the page's own ``h1``-``h6`` nesting subdivides beneath it.

This is not a new chunking strategy. It is the existing one, handed a
``Block.section`` it already knows what to do with:

* ``_merge_small`` never merges across an authoritative boundary, so a stub
  page under ``MIN_TOKENS`` keeps its own heading path instead of being
  absorbed into the next page and attributed to it. Doc sites are full of short
  pages, and without page-as-section that is a silent misattribution.
* ``_split_oversized`` subdivides a long page at its own subheadings, which is
  what a long chapter already gets.
* ``SECTION_MATCH_SQL`` filters a nav subtree with no query changes.
* ``scattered_sections`` catches a page whose chunks landed non-contiguously.

The chunker is untouched.
"""

from __future__ import annotations

import re
from typing import Any
from urllib.parse import urlsplit

from .adapters.html import parse as parse_html
from .blocks import Block, Extraction
from .crawl import CrawlResult

__all__ = ["build_extraction", "site_title"]

_WS = re.compile(r"\s+")


def _norm(text: str) -> str:
    return _WS.sub(" ", text).strip().lower()


def _section_key(section: str) -> tuple[int, ...]:
    try:
        return tuple(int(p) for p in section.split("."))
    except ValueError:
        return ()


def site_title(result: CrawlResult, override: str | None = None) -> str:
    """A name for the whole site.

    The seed page's ``<title>`` is what the publisher calls the site; the host
    is the honest fallback, and beats naming the document after whichever page
    happened to be fetched first.
    """
    if override:
        return override.strip()
    seed_page = result.pages.get(result.seed)
    if seed_page is not None:
        title, _items = parse_html(seed_page.body)
        if title.strip():
            return title.strip()
    host = urlsplit(result.seed).hostname or result.seed
    return host


def _page_path(base: list[str], item_path: list[str], page_title: str) -> list[str]:
    """Heading ancestry for one item: the nav position, then the page's own.

    A page whose first heading repeats its nav title would otherwise carry that
    title twice -- ``Guides > Install > Install > Options`` -- which reads as a
    level of structure the document does not have and splits the heading path
    that ``section_filter`` matches on.
    """
    inner = [p for p in item_path if p]
    if inner and _norm(inner[0]) == _norm(page_title):
        inner = inner[1:]
    return base + inner


def build_extraction(
    result: CrawlResult,
    *,
    title: str | None = None,
) -> Extraction:
    """Turn a crawl into the normalized intermediate the chunker consumes."""
    placements = result.hierarchy.by_url()

    # Emitted in nav order rather than fetch order. Chunk ordinals are document
    # order, and for a site the document's order is what its navigation says --
    # not the sequence a sitemap happened to list or a walk happened to reach.
    ordered = sorted(
        result.pages.values(),
        key=lambda p: (_section_key(placements[p.url].section) if p.url in placements else (),),
    )

    blocks: list[Block] = []
    offset = 0
    unplaced: list[str] = []

    for page in ordered:
        placement = placements.get(page.url)
        if placement is None:
            # nav places every fetched page, by URL path where no source named
            # it, so this is a defect rather than an ordinary outcome.
            unplaced.append(page.url)
            continue

        page_title = placement.title or page.declared_title
        html_title, items = parse_html(page.body)
        if not page_title:
            page_title = html_title or page.url
        base = [*placement.ancestry, page_title]

        for item in items:
            text = item.text.strip()
            if not text:
                continue
            blocks.append(
                Block(
                    heading_path=_page_path(base, item.heading_path, page_title),
                    locator={"offset": offset},
                    text=text,
                    section=placement.section,
                    url=page.url,
                    fragment=item.fragment,
                )
            )
            offset += len(text) + 1

    diagnostics: dict[str, Any] = {
        "structure_source": result.hierarchy.source,
        "site": {
            "seed": result.seed,
            "pages_declared": result.declared,
            "pages_fetched": len(result.pages),
            "pages_with_blocks": len({b.url for b in blocks}),
            "unreachable": [url for url, _reason in result.unreachable],
            "unreachable_reasons": [f"{url}: {reason}" for url, reason in result.unreachable],
            "placed_by_path": list(result.hierarchy.placed_by_path),
            "hierarchy_inferred": result.hierarchy.inferred,
            "canonical_merges": len(result.canonical_merges),
            "unplaced_pages": unplaced,
        },
        "notes": list(result.notes),
    }

    return Extraction(
        title=site_title(result, title),
        format="site",
        blocks=blocks,
        page_count=None,
        index_terms=[],
        pages={},
        diagnostics=diagnostics,
    )
