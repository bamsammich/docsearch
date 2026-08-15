"""Pre-flight reconnaissance on a document, without ingesting it.

Whether a document will be searchable is decided by what structure can be
derived from it, and that is knowable before any of it is written. The same
questions were once answered by hand for one file -- is there a text layer, an
outline, a printed table of contents, a usable font hierarchy -- and the
answers are what made that ingest work.

This reports them, so a document that will fail, or will succeed badly, says so
before it costs a worker an hour.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

from .adapters import UnsupportedFormatError, for_path
from .adapters.html import parse as parse_html
from .structure import SITE_INCOMPLETE_FATAL_SHARE
from .tokens import estimate_tokens, uncalibrated_letter_share

#: A document with a text layer on fewer than this share of pages is scanned
#: images. Extraction yields nothing to chunk and OCR is the prerequisite, not
#: a tuning problem.
TEXT_LAYER_MIN_PAGE_SHARE = 0.5

#: Pages sampled for the script check. Enough to characterise a document
#: without reading a 1,800-page manual twice.
SCRIPT_SAMPLE_PAGES = 40


@dataclass(slots=True)
class Finding:
    #: 'ok' | 'warn' | 'blocked'
    level: str
    label: str
    detail: str


@dataclass(slots=True)
class InspectReport:
    #: A path for a file, a seed URL for a site.
    target: str
    format: str
    page_count: int | None = None
    predicted_source: str = "unknown"
    predicted_tier: str = ""
    findings: list[Finding] = field(default_factory=list)

    @property
    def blocked(self) -> bool:
        return any(f.level == "blocked" for f in self.findings)

    @property
    def display(self) -> str:
        return self.target if "://" in self.target else Path(self.target).name


def _pdf_report(path: Path, rep: InspectReport) -> None:
    import pymupdf

    from .adapters.pdf import (
        _is_boilerplate,
        _page_lines,
        analyze_fonts,
        detect_boilerplate,
        reconstruct_front_toc,
    )

    doc = pymupdf.open(path)
    n = doc.page_count
    rep.page_count = n
    add = rep.findings.append

    pages = [_page_lines(doc[i]) for i in range(n)]
    with_text = sum(1 for lines in pages if lines)
    if n and with_text / n < TEXT_LAYER_MIN_PAGE_SHARE:
        add(
            Finding(
                "blocked",
                "text layer",
                f"only {with_text} of {n} pages carry extractable text. This document is "
                f"page images; run it through OCR (ocrmypdf) before ingesting, because "
                f"extraction has nothing to read.",
            )
        )
    else:
        add(
            Finding(
                "ok",
                "text layer",
                f"present on {with_text} of {n} pages; no OCR needed",
            )
        )

    outline = doc.get_toc(simple=True)
    if outline:
        depths = sorted({lvl for lvl, _t, _p in outline})
        rep.predicted_source = "outline"
        rep.predicted_tier = "authoritative"
        add(
            Finding(
                "ok",
                "outline",
                f"{len(outline)} entries, nesting depth {depths[0]}-{depths[-1]}. Sections, "
                f"nesting and the page each begins on are all declared, so none of the "
                f"structure has to be inferred.",
            )
        )
    else:
        add(
            Finding(
                "warn",
                "outline",
                "absent. The producer toolchain stripped bookmarks, or none were authored, "
                "so structure must come from the page content instead.",
            )
        )

    body_size, heading_sizes = analyze_fonts(pages)
    boiler = detect_boilerplate(pages, set(heading_sizes))
    if heading_sizes:
        add(
            Finding(
                "ok",
                "font hierarchy",
                f"body text at {body_size}pt; heading candidates at "
                f"{', '.join(f'{s}pt' for s in heading_sizes[:6])}",
            )
        )
    else:
        add(
            Finding(
                "warn",
                "font hierarchy",
                f"no size stands above the {body_size}pt body text. Headings in this document "
                f"are styled by weight or colour rather than size, so font detection cannot "
                f"find them and cannot corroborate anything.",
            )
        )

    if not outline:
        scan = max(40, n // 20)
        entries, _pages_used = reconstruct_front_toc(pages, boiler, scan)
        if entries:
            rep.predicted_source = "front_toc"
            rep.predicted_tier = "declared, recovered by parsing"
            add(
                Finding(
                    "ok",
                    "printed contents",
                    f"{len(entries)} entries reconstructed from the first {scan} pages. "
                    f"Author-declared, but recovered by parsing, so it will be checked "
                    f"against body headings and a disagreement fails the ingest.",
                )
            )
        elif heading_sizes:
            rep.predicted_source = "font_heuristic"
            rep.predicted_tier = "inferred, no source to check it against"
            add(
                Finding(
                    "warn",
                    "printed contents",
                    "none reconstructable. Structure will be inferred from font sizes alone, "
                    "with nothing to validate it against.",
                )
            )
        else:
            rep.predicted_source = "none"
            add(
                Finding(
                    "blocked",
                    "structure",
                    "no outline, no printed contents, and no font hierarchy. Ingest will "
                    "refuse rather than cut the text into fixed windows.",
                )
            )

    boiler_tokens = 0
    total_tokens = 0
    for lines in pages:
        for ln in lines:
            t = estimate_tokens(ln.text)
            total_tokens += t
            if _is_boilerplate(ln.text, boiler):
                boiler_tokens += t
    if total_tokens:
        share = boiler_tokens / total_tokens
        add(
            Finding(
                "warn" if share > 0.15 else "ok",
                "page furniture",
                f"{len(boiler)} repeated line(s), {share:.0%} of the document's token mass. "
                f"Stripped before chunking; a high share means a large part of the raw text "
                f"is a running header or footer.",
            )
        )

    sample = "\n".join(ln.text for lines in pages[:SCRIPT_SAMPLE_PAGES] for ln in lines)
    share = uncalibrated_letter_share(sample)
    if share >= 0.10:
        add(
            Finding(
                "warn",
                "script",
                f"{share:.0%} of letters are in a script the token estimator has no "
                f"calibration for, so chunk sizes will be in an unknown unit and the "
                f"document may be chunked coarser than intended.",
            )
        )
    doc.close()


#: A page yielding less extractable text than this, while carrying far more
#: script than text, is rendered in the browser. Ingesting it stores an empty
#: shell that looks like a successful page.
CLIENT_RENDERED_MAX_TEXT_CHARS = 200

#: Script bytes per character of extractable text, above which the page is
#: mostly application rather than document.
CLIENT_RENDERED_SCRIPT_RATIO = 4.0


def _client_rendered(body: bytes) -> bool:
    """Whether a page's content arrives only once a browser runs it.

    Named rather than ingested: an empty shell that reached status 'ready' is
    indistinguishable from a page that genuinely says little, and headless
    rendering is a follow-up rather than a dependency in the critical path.
    Both probed targets pre-render, so this reports rather than blocks.
    """
    from bs4 import BeautifulSoup

    soup = BeautifulSoup(body, "lxml")
    script_chars = sum(len(s.get_text()) for s in soup.find_all("script"))
    for tag in soup(["script", "style"]):
        tag.decompose()
    text_chars = len((soup.body or soup).get_text(" ", strip=True))
    if text_chars > CLIENT_RENDERED_MAX_TEXT_CHARS:
        return False
    return script_chars >= max(1, text_chars) * CLIENT_RENDERED_SCRIPT_RATIO


def inspect_site(
    seed: str,
    *,
    cache_path: Path,
    max_pages: int = 500,
    guard: object = None,
    addr_guard: object = None,
    interval: float | None = None,
) -> InspectReport:
    """Crawl ``seed`` and report what ingest would make of it, writing nothing.

    A live dry run: it fetches, because every question worth asking about a
    site -- how many pages there are, whether a navigation places them, whether
    they carry text at all -- is a question about what the server actually
    returns.
    """
    from . import fetchcache
    from .crawl import crawl
    from .fetch import DEFAULT_INTERVAL, Fetcher
    from .site import chrome_texts
    from .urlguard import BlockedURLError, addr_allowed, check

    rep = InspectReport(target=seed, format="site")
    add = rep.findings.append

    real_guard = guard or check
    try:
        real_guard(seed)  # type: ignore[operator]
    except BlockedURLError:
        add(
            Finding(
                "blocked",
                "address",
                "this URL is not permitted. Only public http(s) addresses are fetched: "
                "loopback, private ranges, link-local and local-only name suffixes are "
                "refused, because the server can reach what the caller cannot.",
            )
        )
        return rep

    cache = fetchcache.connect(cache_path)
    with Fetcher(
        cache,
        guard=real_guard,  # type: ignore[arg-type]
        addr_guard=addr_guard or addr_allowed,  # type: ignore[arg-type]
        interval=DEFAULT_INTERVAL if interval is None else interval,
    ) as fetcher:
        result = crawl(fetcher, seed, max_pages=max_pages)

    rep.page_count = len(result.pages)
    sources = result.coverage.sources
    if not result.pages:
        add(
            Finding(
                "blocked",
                "coverage",
                "no page of this site could be fetched. "
                + ("; ".join(result.notes) or "nothing answered at the seed URL."),
            )
        )
        return rep

    add(
        Finding(
            "ok" if sources else "warn",
            "coverage",
            f"{len(result.pages)} page(s) fetched. Sources that answered: "
            + (", ".join(sources) if sources else "none; the page set came from following links")
            + ".",
        )
    )

    share = result.unreachable_share
    if result.unreachable:
        add(
            Finding(
                "blocked" if share >= SITE_INCOMPLETE_FATAL_SHARE else "warn",
                "reachability",
                f"{len(result.unreachable)} of {result.declared} known page(s) ({share:.0%}) "
                f"could not be fetched. At or above {SITE_INCOMPLETE_FATAL_SHARE:.0%} the "
                f"ingest is refused rather than producing an index over part of a site. "
                f"First few: " + "; ".join(f"{u} ({why})" for u, why in result.unreachable[:3]),
            )
        )

    hierarchy = result.hierarchy
    rep.predicted_source = hierarchy.source
    if hierarchy.inferred:
        rep.predicted_tier = "inferred, no source to check it against"
        add(
            Finding(
                "warn",
                "hierarchy",
                "no navigation source placed enough of the page set to be believed, so "
                "pages will be nested by URL path. That is inference: it keeps a command "
                "reference together, but nothing corroborates it.",
            )
        )
    else:
        rep.predicted_tier = "declared, recovered by parsing"
        add(
            Finding(
                "ok",
                "hierarchy",
                f"'{hierarchy.source}' places the page set and will supply each page's "
                f"section number and ancestry.",
            )
        )
    if hierarchy.placed_by_path:
        add(
            Finding(
                "warn",
                "unplaced pages",
                f"{len(hierarchy.placed_by_path)} page(s) are named by no navigation source "
                f"and will be nested by URL path instead. They are placed, never dropped -- "
                f"dropping them is how a command reference goes missing from an index that "
                f"reports success.",
            )
        )

    shells = [url for url, page in result.pages.items() if _client_rendered(page.body)]
    if shells:
        add(
            Finding(
                "warn",
                "client-rendered",
                f"{len(shells)} page(s) carry far more script than text and are rendered in "
                f"the browser. Ingesting them stores an empty shell that looks like a "
                f"successful page. First few: " + ", ".join(shells[:3]),
            )
        )

    parsed = [(url, parse_html(page.body)[1]) for url, page in result.pages.items()]
    chrome = chrome_texts(parsed)
    if chrome:
        add(
            Finding(
                "ok",
                "page furniture",
                f"{len(chrome)} block(s) repeat across at least half the site and will be "
                f"stripped as navigation before chunking.",
            )
        )
    return rep


def inspect_document(path: Path) -> InspectReport:
    """Report what structure could be derived from ``path``, without ingesting."""
    try:
        for_path(path)
    except UnsupportedFormatError as exc:
        rep = InspectReport(target=str(path), format="unsupported")
        rep.findings.append(Finding("blocked", "format", str(exc)))
        return rep

    suffix = path.suffix.lower()
    rep = InspectReport(target=str(path), format=suffix.lstrip("."))
    if suffix == ".pdf":
        _pdf_report(path, rep)
    else:
        rep.predicted_source = "markup"
        rep.predicted_tier = "authoritative"
        rep.findings.append(
            Finding(
                "ok",
                "structure",
                "headings are declared by the markup itself, so nothing about the structure "
                "is inferred and there is nothing to reconstruct.",
            )
        )
    return rep


def format_report(rep: InspectReport) -> str:
    label = "site" if rep.format == "site" else "file"
    out = [f"{label:<11} {rep.display}", f"format      {rep.format}"]
    if rep.page_count is not None:
        unit = "pages" if rep.format != "site" else "pages found"
        out.append(f"{unit:<11} {rep.page_count}")
    out.append("")
    for f in rep.findings:
        out.append(f"[{f.level.upper():^7}] {f.label}")
        for line in _wrap(f.detail, 72):
            out.append(f"            {line}")
    out.append("")
    if rep.blocked:
        out.append("This document cannot be ingested as it stands.")
    else:
        out.append(f"structure source: {rep.predicted_source} ({rep.predicted_tier})")
    return "\n".join(out)


def _wrap(text: str, width: int) -> list[str]:
    lines: list[str] = []
    current = ""
    for word in text.split():
        if current and len(current) + 1 + len(word) > width:
            lines.append(current)
            current = word
        else:
            current = f"{current} {word}".strip()
    if current:
        lines.append(current)
    return lines
