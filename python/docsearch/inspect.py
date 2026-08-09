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
    path: Path
    format: str
    page_count: int | None = None
    predicted_source: str = "unknown"
    predicted_tier: str = ""
    findings: list[Finding] = field(default_factory=list)

    @property
    def blocked(self) -> bool:
        return any(f.level == "blocked" for f in self.findings)


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


def inspect_document(path: Path) -> InspectReport:
    """Report what structure could be derived from ``path``, without ingesting."""
    try:
        for_path(path)
    except UnsupportedFormatError as exc:
        rep = InspectReport(path=path, format="unsupported")
        rep.findings.append(Finding("blocked", "format", str(exc)))
        return rep

    suffix = path.suffix.lower()
    rep = InspectReport(path=path, format=suffix.lstrip("."))
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
    out = [f"file        {rep.path.name}", f"format      {rep.format}"]
    if rep.page_count is not None:
        out.append(f"pages       {rep.page_count}")
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
