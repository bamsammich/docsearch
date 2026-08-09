"""Pre-flight reconnaissance.

The questions here decide whether a document will be searchable, and all of
them are answerable before anything is written. A document that will fail, or
succeed badly, should say so before it costs a worker an hour.
"""

from __future__ import annotations

from pathlib import Path

import pymupdf
import pytest

from docsearch.inspect import format_report, inspect_document


def _pdf(path: Path, pages: list[list[tuple[str, float]]], outline: list | None = None) -> Path:
    doc = pymupdf.open()
    for lines in pages:
        page = doc.new_page()
        y = 72.0
        for text, size in lines:
            page.insert_text((72, y), text, fontsize=size)
            y += size * 1.6
    if outline:
        doc.set_toc(outline)
    doc.save(path)
    doc.close()
    return path


def test_a_scanned_document_is_blocked_with_the_remedy_named(tmp_path: Path) -> None:
    """Extraction has nothing to read, and no amount of tuning changes that."""
    path = _pdf(tmp_path / "scan.pdf", [[] for _ in range(12)])
    rep = inspect_document(path)
    assert rep.blocked
    text_layer = next(f for f in rep.findings if f.label == "text layer")
    assert text_layer.level == "blocked"
    assert "OCR" in text_layer.detail
    assert "cannot be ingested" in format_report(rep)


def test_an_embedded_outline_is_predicted_as_authoritative(tmp_path: Path) -> None:
    pages = [[(f"Body text on page {i} of the manual.", 10.0)] for i in range(12)]
    path = _pdf(
        tmp_path / "outlined.pdf",
        pages,
        outline=[[1, "Introduction", 1], [2, "Setup", 3], [1, "Appendix", 9]],
    )
    rep = inspect_document(path)
    assert not rep.blocked
    assert rep.predicted_source == "outline"
    assert rep.predicted_tier == "authoritative"


def test_a_document_with_no_derivable_structure_is_blocked(tmp_path: Path) -> None:
    """Uniform body text: no outline, no printed contents, no size hierarchy.

    Ingest refuses this rather than cutting it into fixed windows, so inspect
    must say so before the attempt.
    """
    pages = [[(f"Uniform body text page {i} without any heading at all.", 10.0)] for i in range(12)]
    path = _pdf(tmp_path / "flat.pdf", pages)
    rep = inspect_document(path)
    assert rep.blocked
    assert rep.predicted_source == "none"
    assert any("refuse" in f.detail for f in rep.findings)


def test_markup_formats_declare_their_own_structure(md_file: Path) -> None:
    rep = inspect_document(md_file)
    assert not rep.blocked
    assert rep.predicted_tier == "authoritative"
    assert rep.page_count is None


def test_an_unsupported_format_is_blocked(tmp_path: Path) -> None:
    bad = tmp_path / "archive.zip"
    bad.write_bytes(b"PK\x03\x04not really")
    rep = inspect_document(bad)
    assert rep.blocked
    assert rep.format == "unsupported"


@pytest.mark.parametrize("label", ["text layer", "page furniture"])
def test_every_finding_carries_actionable_detail(tmp_path: Path, label: str) -> None:
    pages = [[(f"Body text on page {i} of the manual.", 10.0)] for i in range(12)]
    path = _pdf(tmp_path / "doc.pdf", pages, outline=[[1, "Intro", 1]])
    rep = inspect_document(path)
    finding = next(f for f in rep.findings if f.label == label)
    assert len(finding.detail) > 30
    assert any(ch.isdigit() for ch in finding.detail)
