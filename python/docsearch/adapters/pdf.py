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

import fitz

from ..blocks import Block, Extraction
from ..tokens import estimate_tokens

ProgressFn = Callable[[str, int, int], None]

#: A line is boilerplate when it repeats (digits normalized) on at least this
#: fraction of pages. Running furniture hits ~100%; real prose never does.
BOILERPLATE_PAGE_FRACTION = 0.25

#: A heading candidate size must exceed body size by this factor.
HEADING_SIZE_RATIO = 1.10

#: Blocks at or below this token count on a page bearing images are treated as
#: figure callouts: attached to their section, never a chunk or split point.
FIGURE_CAPTION_MAX_TOKENS = 25

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


def _page_lines(page: fitz.Page) -> list[_Line]:
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


def detect_boilerplate(pages: list[list[_Line]]) -> set[str]:
    """Running headers and footers, found by repetition frequency.

    Deliberately not positional: in this corpus the copyright and phone lines
    are emitted *first* in extraction order despite sitting visually at the
    page bottom, so a top/bottom band test misses them entirely.
    """
    seen: defaultdict[str, set[int]] = defaultdict(set)
    for i, lines in enumerate(pages):
        for ln in lines:
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


def _find_body_headings(
    pages: list[list[_Line]],
    heading_sizes: list[float],
    boiler: set[str],
    toc_page_max: int,
) -> tuple[list[tuple[int, str, str]], set[tuple[int, float]]]:
    """Locate numbered section headings in the body.

    Returns ``(headings, subdivision_points)`` where headings is
    ``(page_index, section, title)`` and subdivision_points holds
    ``(page_index, y0)`` for heading-sized lines carrying no section number.
    """
    headings: list[tuple[int, str, str]] = []
    subdivisions: set[tuple[int, float]] = set()
    head_set = set(heading_sizes)

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
            if not title:
                # Chapter headings emit the number and the title as separate
                # lines at the same size; join them.
                j = i + 1
                while j < len(lines) and not lines[j].text.strip():
                    j += 1
                if j < len(lines) and lines[j].size == ln.size:
                    title = lines[j].text.strip()
                    i = j
            headings.append((pno, section, title))
            i += 1
    return headings, subdivisions


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


def extract(path: Path, progress: ProgressFn | None = None) -> Extraction:
    doc = fitz.open(path)
    n = doc.page_count

    def tick(phase: str, cur: int) -> None:
        if progress:
            progress(phase, cur, n)

    pages: list[list[_Line]] = []
    page_text: dict[int, str] = {}
    page_images: list[list[tuple[float, int]]] = []
    for i in range(n):
        page = doc[i]
        pages.append(_page_lines(page))
        page_text[i + 1] = page.get_text("text")
        try:
            infos = page.get_image_info()
        except Exception:
            infos = []
        page_images.append(sorted((float(im["bbox"][1]), 1) for im in infos))
        if i % 50 == 0:
            tick("extract", i)
    tick("extract", n)

    boiler = detect_boilerplate(pages)
    body_size, heading_sizes = analyze_fonts(pages)

    diagnostics: dict[str, Any] = {
        "body_font_size": body_size,
        "heading_font_sizes": heading_sizes,
        "boilerplate_lines_stripped": len(boiler),
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
            raise ValueError(
                f"{path.name}: no outline tree, no printed table of contents, and no "
                "usable font hierarchy -- cannot derive chunk boundaries. Refusing to "
                "fall back to fixed token windows."
            )

    diagnostics["toc_entries"] = len(toc_entries)
    toc_titles = {sec: title for sec, title, _ in toc_entries}

    headings, subdivisions = _find_body_headings(pages, heading_sizes, boiler, toc_page_max)
    diagnostics["body_headings_found"] = len(headings)

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
    diagnostics["cross_validation"] = {
        "toc_sections": len(toc_titles),
        "body_sections": len(body_sections),
        "in_toc_not_in_body": missing,
        "in_body_not_in_toc": extra,
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
