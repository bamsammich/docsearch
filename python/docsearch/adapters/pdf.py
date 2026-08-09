"""PDF adapter.

Structure source, in priority order:

1. The embedded outline tree (``doc.get_toc()``).
2. A reconstructed front table of contents, when the outline was stripped by
   the producer toolchain but a printed TOC survives in the page content.
3. Font-size heuristics alone.

If none of the three yields a heading tree, extraction fails loudly. Silent
degradation to fixed token windows is how a document quietly becomes
unsearchable, so it is never the fallback.
"""

from __future__ import annotations

import re
from collections import Counter, defaultdict
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import pymupdf

from ..blocks import Block, Extraction
from ..errors import StructureValidationError
from ..tokens import estimate_tokens

ProgressFn = Callable[[str, int, int], None]

#: A line is boilerplate when it repeats (digits normalized) on at least this
#: fraction of pages. Running furniture hits ~100%; real prose never does.
BOILERPLATE_PAGE_FRACTION = 0.25

#: Below this page count, repetition is not evidence of furniture and the
#: false-positive risk dominates.
BOILERPLATE_MIN_PAGES = 8

#: A heading candidate size must exceed body size by this factor.
HEADING_SIZE_RATIO = 1.10

#: Blocks at or below this token count on a page bearing images are treated as
#: figure callouts: attached to their section, never a chunk or split point.
FIGURE_CAPTION_MAX_TOKENS = 25

#: An image xref placed on more than this fraction of pages is page furniture
#: (a corner logo), not a figure.
FIGURE_FURNITURE_PAGE_FRACTION = 0.5

#: Minimum placed area, in pt^2, for an image to count as a figure. Below this
#: it is an inline icon or a bullet glyph. 400pt^2 is roughly 20x20.
MIN_FIGURE_AREA = 400.0

_SECTION_LINE = re.compile(r"^(\d+(?:\.\d+)*)\.\s*(.*)$")
_SECTION_ONLY = re.compile(r"^(\d+(?:\.\d+)*)\.$")
_DIGITS = re.compile(r"\d+")
_PAGE_OF = re.compile(r"^\d+\s+of\s+\d+$")
#: ``term  1.2.3.   4.5.`` -- one or more trailing section references.
_INDEX_ENTRY = re.compile(r"^(.*?)\s+((?:\d+(?:\.\d+)*\.\s*)+)$")


@dataclass(slots=True)
class _Line:
    y0: float
    x0: float
    y1: float
    size: float
    text: str


def _page_lines(page: pymupdf.Page) -> list[_Line]:
    out: list[_Line] = []
    for blk in page.get_text("dict")["blocks"]:
        if blk["type"] != 0:
            continue
        for line in blk["lines"]:
            text = " ".join(sp["text"] for sp in line["spans"]).strip()
            if not text:
                continue
            out.append(
                _Line(
                    y0=line["bbox"][1],
                    x0=line["bbox"][0],
                    y1=line["bbox"][3],
                    size=round(line["spans"][0]["size"], 1),
                    text=text,
                )
            )
    out.sort(key=lambda ln: (round(ln.y0, 1), ln.x0))
    return out


def _normalize(text: str) -> str:
    return _DIGITS.sub("#", text)


def detect_boilerplate(pages: list[list[_Line]], heading_sizes: set[float]) -> set[str]:
    """Running headers and footers, found by repetition frequency.

    Deliberately not positional: in this corpus the copyright and phone lines
    are emitted *first* in extraction order despite sitting visually at the
    page bottom, so a top/bottom band test misses them entirely.

    Heading-sized lines are excluded from consideration. Digits normalize to
    '#' so every bare chapter number collapses to the same key -- "1.", "2."
    and "3." are one string here -- and on a document with few pages that
    collision alone clears the repetition threshold, classifying every chapter
    heading as furniture and erasing the structure. Running furniture is set in
    small type; a line at a heading size is never it.
    """
    if len(pages) < BOILERPLATE_MIN_PAGES:
        return set()
    seen: defaultdict[str, set[int]] = defaultdict(set)
    for i, lines in enumerate(pages):
        for ln in lines:
            if ln.size in heading_sizes:
                continue
            seen[_normalize(ln.text)].add(i)
    threshold = max(3, int(len(pages) * BOILERPLATE_PAGE_FRACTION))
    return {norm for norm, pgs in seen.items() if len(pgs) >= threshold}


def _is_boilerplate(text: str, boiler: set[str]) -> bool:
    return _normalize(text) in boiler or bool(_PAGE_OF.match(text))


def analyze_fonts(pages: list[list[_Line]]) -> tuple[float, list[float]]:
    """Return ``(body_size, heading_sizes_desc)``.

    Heading sizes are ranked largest-first so their index gives nesting depth.
    """
    volume: Counter[float] = Counter()
    lines_at: Counter[float] = Counter()
    for lines in pages:
        for ln in lines:
            volume[ln.size] += len(ln.text)
            lines_at[ln.size] += 1
    if not volume:
        return 0.0, []
    body = volume.most_common(1)[0][0]
    # Require enough occurrences to be a real level, but not so many that it is
    # body text in disguise. Two stray lines (spillover) must not become a level.
    floor = max(5, len(pages) // 400)
    heads = [
        size for size, n in lines_at.items() if size >= body * HEADING_SIZE_RATIO and n >= floor
    ]
    if heads:
        # Anything at least as large as an established level is a heading however
        # rare it is. A one-off chapter heading -- a lone "56. Index" set larger
        # than every other chapter -- fails the frequency floor, and dropping it
        # silently makes that chapter's content annex the preceding section. The
        # floor exists to reject body-sized noise, not oversized rarities.
        smallest = min(heads)
        heads = [size for size in lines_at if size >= smallest]
    return body, sorted(heads, reverse=True)


def reconstruct_front_toc(
    pages: list[list[_Line]], boiler: set[str], scan_pages: int
) -> tuple[list[tuple[str, str, int]], set[int]]:
    """Recover ``(section, title, printed_page)`` from a printed TOC.

    A printed TOC is a multi-column layout, so naive reading order interleaves
    section numbers with page numbers and strands the titles in a separate run.
    Grouping lines into y-bands and sorting each band by x restores the rows.

    Also returns the page indices that actually yielded entries. Callers use
    that to skip the TOC during body extraction -- inferring the TOC's extent
    from "page contains a bare numbered line" instead would swallow real
    chapters, since body pages carry numbered headings too.
    """
    entries: list[tuple[str, str, int]] = []
    toc_pages: set[int] = set()
    for pno, lines in enumerate(pages[:scan_pages]):
        bands: defaultdict[int, list[_Line]] = defaultdict(list)
        for ln in lines:
            if _is_boilerplate(ln.text, boiler):
                continue
            bands[int(ln.y0 // 4)].append(ln)
        for key in sorted(bands):
            cells = sorted(bands[key], key=lambda c: c.x0)
            texts = [c.text for c in cells]
            if len(texts) < 3:
                continue
            head = _SECTION_ONLY.match(texts[0])
            if head and texts[-1].isdigit():
                title = " ".join(texts[1:-1]).strip()
                if title:
                    entries.append((head.group(1), title, int(texts[-1])))
                    toc_pages.add(pno)
    return entries, toc_pages


def _normalize_title(text: str) -> str:
    return re.sub(r"\W+", " ", text.lower()).strip()


#: A page needs at least this many lines before the share of them that are
#: section titles means anything.
CONTENTS_PAGE_MIN_LINES = 5

#: Share of a page's lines that must name a known section for the page to be a
#: printed contents listing rather than prose that happens to mention sections.
CONTENTS_PAGE_MATCH_FRACTION = 0.5

#: ``Introduction .......... 11`` -- leader dots and a trailing page number.
_CONTENTS_LEADER = re.compile(r"[\s.·•\-—_]*\d*\s*$")


def _contents_pages(
    pages: list[list[_Line]],
    toc_entries: list[tuple[str, str, int]],
    boiler: set[str],
) -> set[int]:
    """Pages whose content is a printed listing of the document's own sections.

    An embedded outline names the printed contents as a section like any other,
    so its pages become chunks: text duplicating the heading structure already
    available through `outline`, matching broadly and answering nothing.

    Detected by content rather than by heading title. A page most of whose
    lines name known sections is a listing whatever the document calls it,
    where matching the words "Table of Contents" would not survive a document
    written in another language.
    """
    titles = {_normalize_title(title) for _sec, title, _pg in toc_entries if title.strip()}
    if not titles:
        return set()
    found: set[int] = set()
    for pno, lines in enumerate(pages):
        body = [ln for ln in lines if ln.text.strip() and not _is_boilerplate(ln.text, boiler)]
        if len(body) < CONTENTS_PAGE_MIN_LINES:
            continue
        matched = sum(
            1 for ln in body if _normalize_title(_CONTENTS_LEADER.sub("", ln.text)) in titles
        )
        if matched / len(body) >= CONTENTS_PAGE_MATCH_FRACTION:
            found.add(pno)
    return found


def _locate_outline_headings(
    pages: list[list[_Line]],
    toc_entries: list[tuple[str, str, int]],
    boiler: set[str],
) -> tuple[list[tuple[int, str, str, float, bool]], int]:
    """Place each embedded-outline entry at a position on the page it names.

    An embedded outline is authoritative about which sections exist, how they
    nest, and the page each begins on -- the author declared all three, and
    none of it is inferred. What it does not carry is the position within that
    page, so the entry title is matched against the page's own lines to recover
    it.

    An entry whose title does not appear on its page is placed at the top of
    that page. The outline still declared the page; only the offset inside it
    is unknown, and page granularity is the honest resolution for that entry
    rather than grounds to reject the document.

    Returns ``(placements, located)`` where a placement is
    ``(page_index, section, title, y, matched_a_line)``.
    """
    placements: list[tuple[int, str, str, float, bool]] = []
    located = 0
    for section, title, page in toc_entries:
        pno = page - 1
        if pno < 0 or pno >= len(pages):
            continue
        want = _normalize_title(title)
        y: float | None = None
        if want:
            for ln in pages[pno]:
                if _is_boilerplate(ln.text, boiler):
                    continue
                got = _normalize_title(ln.text)
                if got and (got == want or got.startswith(want)):
                    y = ln.y0
                    break
        if y is None:
            placements.append((pno, section, title, -1.0, False))
        else:
            located += 1
            placements.append((pno, section, title, y, True))
    placements.sort(key=lambda p: (p[0], p[3]))
    return placements, located


def _find_body_headings(
    pages: list[list[_Line]],
    heading_sizes: list[float],
    boiler: set[str],
    toc_page_max: int,
) -> tuple[list[tuple[int, str, str]], set[tuple[int, float]], list[str]]:
    """Locate numbered section headings in the body.

    Returns ``(headings, subdivision_points, rejected)`` where headings is
    ``(page_index, section, title)``, subdivision_points holds
    ``(page_index, y0)`` for heading-sized lines carrying no section number,
    and rejected lists candidates refused by the ordering rule.

    A numbered procedure step ("1. Tap the title bar") set at a heading size is
    indistinguishable from a chapter heading by font and regex alone. Document
    order is the discriminator: section numbering only ever advances, so a
    candidate that does not sort strictly after the last accepted heading is a
    list item, not a section. Without this a stray "1." resets the current
    section mid-book and scatters one section key across the whole document,
    which silently breaks the index_terms section -> chunk join.
    """
    headings: list[tuple[int, str, str]] = []
    subdivisions: set[tuple[int, float]] = set()
    rejected: list[str] = []
    head_set = set(heading_sizes)
    last_key: tuple[int, ...] = ()

    for pno, lines in enumerate(pages):
        if pno <= toc_page_max:
            continue
        i = 0
        while i < len(lines):
            ln = lines[i]
            if ln.size not in head_set or _is_boilerplate(ln.text, boiler):
                i += 1
                continue
            m = _SECTION_LINE.match(ln.text)
            if not m:
                subdivisions.add((pno, round(ln.y0, 1)))
                i += 1
                continue
            section, title = m.group(1), m.group(2).strip()
            key = tuple(int(part) for part in section.split("."))
            if key <= last_key:
                rejected.append(f"p{pno + 1}:{section}")
                i += 1
                continue
            if not title:
                # Chapter headings emit the number and the title as separate
                # lines at the same size; join them.
                j = i + 1
                while j < len(lines) and not lines[j].text.strip():
                    j += 1
                if j < len(lines) and lines[j].size == ln.size:
                    title = lines[j].text.strip()
                    i = j
            last_key = key
            headings.append((pno, section, title))
            i += 1
    return headings, subdivisions, rejected


def parse_index(
    pages: list[list[_Line]], boiler: set[str], start_page: int
) -> list[tuple[str, str]]:
    """Parse a back-of-book index into ``(term, section)`` pairs.

    Entries in this corpus reference section numbers rather than page numbers,
    and a single term may carry several references.
    """
    out: list[tuple[str, str]] = []
    for lines in pages[start_page:]:
        bands: defaultdict[int, list[_Line]] = defaultdict(list)
        for ln in lines:
            if _is_boilerplate(ln.text, boiler):
                continue
            bands[int(ln.y0 // 4)].append(ln)
        for key in sorted(bands):
            row = " ".join(c.text for c in sorted(bands[key], key=lambda c: c.x0))
            m = _INDEX_ENTRY.match(row.strip())
            if not m:
                continue
            term = m.group(1).strip()
            if not term:
                continue
            for ref in re.findall(r"\d+(?:\.\d+)*", m.group(2)):
                out.append((term, ref))
    return out


def _ancestors(section: str) -> list[str]:
    parts = section.split(".")
    return [".".join(parts[: i + 1]) for i in range(len(parts))]


def _filter_figures(
    raw: list[list[tuple[float, int, float]]], page_count: int
) -> tuple[list[list[tuple[float, int]]], dict[str, Any]]:
    """Reduce placed images to actual figures.

    A raw image count is not a figure count. Page furniture -- a corner logo
    drawn on every page -- is an image placement like any other, and counting
    it makes image_count a constant rather than a signal. Three filters:
    repeated xrefs are furniture, tiny placements are icons and bullets, and
    one xref placed twice on a page is one figure.
    """
    xref_pages: defaultdict[int, set[int]] = defaultdict(set)
    for pno, images in enumerate(raw):
        for _y, xref, _area in images:
            xref_pages[xref].add(pno)

    furniture = {
        xref
        for xref, pgs in xref_pages.items()
        if len(pgs) > page_count * FIGURE_FURNITURE_PAGE_FRACTION
    }

    kept: list[list[tuple[float, int]]] = []
    dropped_furniture = dropped_small = dropped_dupe = 0
    for images in raw:
        seen: set[int] = set()
        page_kept: list[tuple[float, int]] = []
        for y, xref, area in images:
            if xref in furniture:
                dropped_furniture += 1
                continue
            if area < MIN_FIGURE_AREA:
                dropped_small += 1
                continue
            if xref in seen:
                dropped_dupe += 1
                continue
            seen.add(xref)
            page_kept.append((y, 1))
        kept.append(page_kept)

    stats: dict[str, Any] = {
        "distinct_xrefs": len(xref_pages),
        "furniture_xrefs": sorted(furniture),
        "placements_total": sum(len(x) for x in raw),
        "dropped_as_furniture": dropped_furniture,
        "dropped_as_too_small": dropped_small,
        "dropped_as_duplicate_on_page": dropped_dupe,
        "figures_kept": sum(len(x) for x in kept),
        "pages_with_no_figure": sum(1 for x in kept if not x),
    }
    return kept, stats


def _emit_outline_blocks(
    pages: list[list[_Line]],
    placements: list[tuple[int, str, str, float, bool]],
    section_titles: dict[str, str],
    boiler: set[str],
    page_images: list[list[tuple[float, int]]],
    skip_pages: set[int],
) -> list[Block]:
    """Emit blocks with boundaries taken from the embedded outline.

    Kept separate from the font-located path rather than generalised into it.
    That path is pinned by committed retrieval baselines on two corpora, and
    the boundary rule here is different in kind: a heading applies from its
    position onward, instead of being recognised by an exact coordinate match.

    Section numbers are synthesised from outline nesting and appear nowhere in
    the document, so the heading path carries titles alone. Numbering that the
    document itself prints is a different thing and is rendered as such.
    """
    by_page: defaultdict[int, list[tuple[float, str, str, bool]]] = defaultdict(list)
    for pno, section, title, y, matched in placements:
        by_page[pno].append((y, section, title, matched))

    blocks: list[Block] = []
    cur_section: str | None = None
    buf: list[str] = []
    buf_page: int | None = None
    buf_page_end: int | None = None
    buf_images = 0

    def flush() -> None:
        nonlocal buf, buf_page, buf_page_end, buf_images
        text = "\n".join(x for x in buf if x.strip()).strip()
        if text and buf_page is not None:
            path_parts = (
                [t for a in _ancestors(cur_section) if (t := section_titles.get(a, "").strip())]
                if cur_section
                else []
            )
            blocks.append(
                Block(
                    heading_path=path_parts,
                    locator={"page": buf_page, "page_end": buf_page_end or buf_page},
                    text=text,
                    section=cur_section,
                    printed_page=buf_page,
                    image_count=buf_images,
                )
            )
        buf = []
        buf_page = None
        buf_page_end = None
        buf_images = 0

    for pno, lines in enumerate(pages):
        pending = sorted(by_page.get(pno, []), key=lambda h: h[0])
        if pno in skip_pages:
            # Headings on a contents page still apply: the outline entry for
            # the section a listing sits under is real, and dropping it would
            # annex that section's later pages to whatever preceded it.
            for _y, section, _title, _matched in pending:
                flush()
                cur_section = section
            continue
        imgs = page_images[pno]
        img_i = 0
        for ln in lines:
            # Headings placed at the top of a page (no title match) carry y=-1
            # and so apply before the page's first line, which is what the
            # outline claimed.
            consumed = False
            while pending and pending[0][0] <= ln.y0:
                _y, section, _title, matched = pending.pop(0)
                flush()
                cur_section = section
                consumed = consumed or matched
            if consumed:
                continue
            if _is_boilerplate(ln.text, boiler):
                continue
            while img_i < len(imgs) and imgs[img_i][0] <= ln.y0:
                buf_images += 1
                img_i += 1
            buf.append(ln.text)
            if buf_page is None:
                buf_page = pno + 1
            buf_page_end = pno + 1
        for _y, section, _title, _matched in pending:
            flush()
            cur_section = section
        buf_images += max(0, len(imgs) - img_i)
    flush()
    return blocks


def extract(path: Path, progress: ProgressFn | None = None) -> Extraction:
    doc = pymupdf.open(path)
    n = doc.page_count

    def tick(phase: str, cur: int) -> None:
        if progress:
            progress(phase, cur, n)

    pages: list[list[_Line]] = []
    page_text: dict[int, str] = {}
    raw_images: list[list[tuple[float, int, float]]] = []
    for i in range(n):
        page = doc[i]
        pages.append(_page_lines(page))
        page_text[i + 1] = page.get_text("text")
        try:
            infos = page.get_image_info(xrefs=True)
        except Exception:
            infos = []
        placed: list[tuple[float, int, float]] = []
        for im in infos:
            bbox = im["bbox"]
            area = abs(bbox[2] - bbox[0]) * abs(bbox[3] - bbox[1])
            placed.append((float(bbox[1]), int(im.get("xref", 0)), area))
        raw_images.append(sorted(placed))
        if i % 50 == 0:
            tick("extract", i)
    tick("extract", n)

    page_images, image_stats = _filter_figures(raw_images, n)

    # Font analysis first: boilerplate detection needs to know which sizes are
    # headings so it can leave them alone.
    body_size, heading_sizes = analyze_fonts(pages)
    boiler = detect_boilerplate(pages, set(heading_sizes))

    diagnostics: dict[str, Any] = {
        "body_font_size": body_size,
        "heading_font_sizes": heading_sizes,
        "boilerplate_lines_stripped": len(boiler),
        "figures": image_stats,
    }

    # --- structure source ----------------------------------------------------
    native = doc.get_toc(simple=True)
    toc_entries: list[tuple[str, str, int]] = []
    toc_page_max = -1
    if native:
        diagnostics["structure_source"] = "outline"
        counters: list[int] = []
        for lvl, title, pg in native:
            del counters[lvl:]
            while len(counters) < lvl:
                counters.append(0)
            counters[lvl - 1] += 1
            toc_entries.append((".".join(str(c) for c in counters), title.strip(), pg))
    else:
        scan = max(40, n // 20)
        toc_entries, toc_pages = reconstruct_front_toc(pages, boiler, scan)
        if toc_entries:
            diagnostics["structure_source"] = "front_toc"
            toc_page_max = max(toc_pages)
        elif heading_sizes:
            diagnostics["structure_source"] = "font_heuristic"
        else:
            raise StructureValidationError(
                f"{path.name}: no outline tree, no printed table of contents, and no "
                "usable font hierarchy -- cannot derive chunk boundaries. Refusing to "
                "fall back to fixed token windows."
            )

    diagnostics["toc_entries"] = len(toc_entries)
    toc_titles = {sec: title for sec, title, _ in toc_entries}

    # An embedded outline carries positions as well as names, so it does not
    # need font-detected body headings to place its sections and is not
    # validated against them. Requiring that corroboration refuses documents
    # whose headings are distinguished by weight or colour rather than size --
    # a shape this pipeline would otherwise handle better than any other,
    # because nothing about the structure is inferred.
    if diagnostics["structure_source"] == "outline":
        placements, located = _locate_outline_headings(pages, toc_entries, boiler)
        contents = _contents_pages(pages, toc_entries, boiler)
        diagnostics["outline_placement"] = {
            "entries": len(toc_entries),
            "located_by_title": located,
            "placed_at_page_top": len(placements) - located,
            "contents_pages_skipped": len(contents),
        }
        diagnostics["printed_page_offset"] = 0
        outline_blocks = _emit_outline_blocks(
            pages, placements, toc_titles, boiler, page_images, contents
        )
        for b in outline_blocks:
            if b.image_count > 0 and estimate_tokens(b.text) <= FIGURE_CAPTION_MAX_TOKENS:
                b.figure_only = True
        title = (doc.metadata or {}).get("title") or path.stem
        return Extraction(
            title=title.strip() or path.stem,
            format="pdf",
            blocks=outline_blocks,
            page_count=n,
            index_terms=[],
            pages=page_text,
            diagnostics=diagnostics,
        )

    headings, subdivisions, rejected = _find_body_headings(
        pages, heading_sizes, boiler, toc_page_max
    )
    diagnostics["body_headings_found"] = len(headings)
    diagnostics["candidates_rejected_by_ordering"] = rejected

    # --- cross-validation: every TOC section must exist in the body ----------
    body_sections = {sec for _p, sec, _t in headings}
    missing = sorted(
        (s for s in toc_titles if s not in body_sections),
        key=lambda s: [int(p) for p in s.split(".")],
    )
    extra = sorted(
        (s for s in body_sections if toc_titles and s not in toc_titles),
        key=lambda s: [int(p) for p in s.split(".")],
    )
    # Set difference alone cannot see a section detected twice: the duplicate
    # carries a number that is legitimately in the TOC, so both differences stay
    # empty while the boundaries are wrong. Count detections explicitly.
    detected = Counter(sec for _p, sec, _t in headings)
    duplicates = sorted(
        (s for s, c in detected.items() if c > 1),
        key=lambda s: [int(p) for p in s.split(".")],
    )
    diagnostics["cross_validation"] = {
        "toc_sections": len(toc_titles),
        "body_sections": len(body_sections),
        "in_toc_not_in_body": missing,
        "in_body_not_in_toc": extra,
        "detected_more_than_once": duplicates,
    }

    section_titles = dict(toc_titles)
    for _p, sec, title in headings:
        section_titles.setdefault(sec, title)

    # printed-page offset, measured against the TOC rather than assumed
    toc_page_of = {sec: pg for sec, _t, pg in toc_entries}
    offsets = [(pno + 1) - toc_page_of[sec] for pno, sec, _t in headings if sec in toc_page_of]
    page_offset = Counter(offsets).most_common(1)[0][0] if offsets else 0
    diagnostics["printed_page_offset"] = page_offset

    # --- index ---------------------------------------------------------------
    index_terms: list[tuple[str, str]] = []
    index_section = next(
        (sec for sec, title, _ in toc_entries if title.strip().lower() == "index"), None
    )
    if index_section is not None:
        start = next(
            ((pno) for pno, sec, _t in headings if sec == index_section),
            None,
        )
        if start is not None:
            index_terms = parse_index(pages, boiler, start)
            known = set(section_titles)
            resolved = sum(1 for _t, s in index_terms if s in known)
            diagnostics["index"] = {
                "start_page": start + 1,
                "entries": len(index_terms),
                "refs_resolving_to_known_sections": resolved,
            }
            index_terms = [(t, s) for t, s in index_terms if s in known]

    # --- block emission ------------------------------------------------------
    head_by_page: defaultdict[int, list[tuple[float, str, str]]] = defaultdict(list)
    for pno, sec, title in headings:
        y = 0.0
        for ln in pages[pno]:
            m = _SECTION_LINE.match(ln.text)
            if m and m.group(1) == sec:
                y = ln.y0
                break
        head_by_page[pno].append((y, sec, title))

    blocks: list[Block] = []
    cur_section: str | None = None
    buf: list[str] = []
    buf_page: int | None = None
    buf_page_end: int | None = None
    buf_images = 0
    buf_sub = False

    def flush() -> None:
        nonlocal buf, buf_page, buf_page_end, buf_images, buf_sub
        text = "\n".join(x for x in buf if x.strip()).strip()
        if text and buf_page is not None:
            path_parts = (
                [f"{a}. {section_titles.get(a, '')}".strip() for a in _ancestors(cur_section)]
                if cur_section
                else []
            )
            blocks.append(
                Block(
                    heading_path=path_parts,
                    locator={"page": buf_page, "page_end": buf_page_end or buf_page},
                    text=text,
                    section=cur_section,
                    printed_page=buf_page - page_offset,
                    image_count=buf_images,
                    subdivision=buf_sub,
                )
            )
        buf = []
        buf_page = None
        buf_page_end = None
        buf_images = 0
        buf_sub = False

    for pno, lines in enumerate(pages):
        if pno <= toc_page_max:
            continue
        if index_section is not None and cur_section == index_section:
            break
        heads = {round(y, 1): (sec, title) for y, sec, title in head_by_page.get(pno, [])}
        imgs = page_images[pno]
        img_i = 0
        for ln in lines:
            if _is_boilerplate(ln.text, boiler):
                continue
            while img_i < len(imgs) and imgs[img_i][0] <= ln.y0:
                buf_images += 1
                img_i += 1
            hit = heads.get(round(ln.y0, 1))
            if hit is not None:
                flush()
                cur_section = hit[0]
                if index_section is not None and cur_section == index_section:
                    break
                continue
            starts_subsection = (pno, round(ln.y0, 1)) in subdivisions
            if starts_subsection:
                flush()
                buf_sub = True
            buf.append(ln.text)
            if buf_page is None:
                buf_page = pno + 1
            buf_page_end = pno + 1
        buf_images += max(0, len(imgs) - img_i)
        if pno % 50 == 0:
            tick("chunk", pno)
    flush()

    # Figure callouts: short blocks on image-bearing pages never stand alone.
    for b in blocks:
        if b.image_count > 0 and estimate_tokens(b.text) <= FIGURE_CAPTION_MAX_TOKENS:
            b.figure_only = True

    title = (doc.metadata or {}).get("title") or path.stem
    return Extraction(
        title=title.strip() or path.stem,
        format="pdf",
        blocks=blocks,
        page_count=n,
        index_terms=index_terms,
        pages=page_text,
        diagnostics=diagnostics,
    )
