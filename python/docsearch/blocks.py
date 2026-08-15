"""The normalized intermediate that every format adapter emits.

The chunker consumes :class:`Block` and never inspects the source format.
Adding a format is one adapter, not a chunker change.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass(slots=True)
class Block:
    """One structural unit of a document, format-agnostic."""

    #: Full heading ancestry, root-first. Joined with " > " for storage.
    heading_path: list[str]
    #: ``{"page": int}`` for paginated formats, ``{"offset": int}`` otherwise.
    locator: dict[str, int]
    text: str
    #: Numbered section this block belongs to (e.g. ``"5.2.1"``), when the
    #: format supplies an authoritative numbering. ``None`` for formats whose
    #: structure is nesting-only (Markdown, HTML, DOCX, plain text).
    section: str | None = None
    #: Printed page number as it appears on the page furniture, when it differs
    #: from the physical page index. Stored, never derived at read time.
    printed_page: int | None = None
    #: Raster images on the page this block came from.
    image_count: int = 0
    #: True when the block is a caption or callout stranded from its figure.
    #: Such blocks never start a chunk and are never subdivision points.
    figure_only: bool = False
    #: True when the block is a heading line eligible as a subdivision point
    #: (heading-sized but not carrying a section number).
    subdivision: bool = False
    #: Page this block was read from, for a source addressed by URL. ``None``
    #: for a local file, which is addressed by its path on the document.
    url: str | None = None
    #: In-page anchor, without the leading ``#``. Carries the nearest heading's
    #: id, so a citation can land on the section rather than the page top.
    fragment: str | None = None


@dataclass(slots=True)
class Extraction:
    """What an adapter returns for one source file."""

    title: str
    format: str
    blocks: list[Block]
    page_count: int | None = None
    #: ``term -> [section, ...]`` from a back-of-book index, where parseable.
    index_terms: list[tuple[str, str]] = field(default_factory=list)
    #: ``page -> text`` for paginated formats.
    pages: dict[int, str] = field(default_factory=dict)
    #: Free-form notes surfaced by ``docsearch verify`` — structure source,
    #: cross-validation mismatches, boilerplate stripped.
    diagnostics: dict[str, Any] = field(default_factory=dict)
