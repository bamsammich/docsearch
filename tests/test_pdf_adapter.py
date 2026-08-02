"""PDF adapter behaviour, exercised against generated PDFs."""

from __future__ import annotations

from pathlib import Path

import fitz
import pytest

from docsearch.adapters.pdf import extract

BODY = 10.0
HEAD = 17.0


def _build(path: Path, pages: list[list[tuple[float, str]]]) -> Path:
    """Write a PDF where each page is a list of ``(font_size, text)`` lines."""
    doc = fitz.open()
    for lines in pages:
        page = doc.new_page()
        y = 90.0
        for size, text in lines:
            page.insert_text((72, y), text, fontsize=size)
            y += size * 2.2
    doc.save(path)
    doc.close()
    return path


def _body_lines(n: int, word: str = "content") -> list[tuple[float, str]]:
    return [(BODY, f"{word} line {i} with enough words to register") for i in range(n)]


@pytest.fixture
def numbered_pdf(tmp_path: Path) -> Path:
    """Three chapters, with a numbered procedure step set at heading size.

    The stray "1." on the last page is the trap: by font and regex it is
    indistinguishable from the chapter-1 heading.
    """
    return _build(
        tmp_path / "manual.pdf",
        [
            [(HEAD, "1."), (HEAD, "Introduction"), *_body_lines(6, "intro")],
            [(HEAD, "2."), (HEAD, "Installation"), *_body_lines(6, "install")],
            [
                (HEAD, "3."),
                (HEAD, "Operation"),
                *_body_lines(4, "operate"),
                (HEAD, "1. Tap the title bar and adjust the window options"),
                *_body_lines(4, "afterlist"),
            ],
        ],
    )


def test_numbered_list_item_does_not_open_a_new_section(numbered_pdf: Path) -> None:
    ex = extract(numbered_pdf)
    sections = [b.section for b in ex.blocks if b.section]
    assert set(sections) == {"1", "2", "3"}
    # The stray "1." must not reopen chapter 1 after chapter 3.
    assert sections == sorted(sections, key=lambda s: [int(p) for p in s.split(".")])
    assert ex.diagnostics["candidates_rejected_by_ordering"]


def test_each_section_is_detected_exactly_once(numbered_pdf: Path) -> None:
    ex = extract(numbered_pdf)
    assert ex.diagnostics["cross_validation"]["detected_more_than_once"] == []


def test_text_after_a_rejected_candidate_stays_in_its_real_section(
    numbered_pdf: Path,
) -> None:
    ex = extract(numbered_pdf)
    trailing = [b for b in ex.blocks if "afterlist" in b.text]
    assert trailing
    assert all(b.section == "3" for b in trailing)


def test_repeated_image_is_treated_as_furniture_not_a_figure(tmp_path: Path) -> None:
    """A logo drawn on every page must not register as a figure."""
    pdf = tmp_path / "logos.pdf"
    doc = fitz.open()
    pix = fitz.Pixmap(fitz.csRGB, fitz.IRect(0, 0, 64, 64))
    pix.set_rect(pix.irect, (200, 30, 30))
    png = pix.tobytes("png")
    for i in range(8):
        page = doc.new_page()
        page.insert_image(fitz.Rect(20, 20, 80, 80), stream=png)  # same logo everywhere
        page.insert_text((72, 120), f"{i + 1}.", fontsize=HEAD)
        page.insert_text((72, 150), f"Chapter {i + 1}", fontsize=HEAD)
        for j, (s, t) in enumerate(_body_lines(5, f"topic{i}")):
            page.insert_text((72, 180 + j * 16), t, fontsize=s)
    doc.save(pdf)
    doc.close()

    ex = extract(pdf)
    stats = ex.diagnostics["figures"]
    assert stats["dropped_as_furniture"] > 0
    assert stats["figures_kept"] == 0
    assert all(b.image_count == 0 for b in ex.blocks)
