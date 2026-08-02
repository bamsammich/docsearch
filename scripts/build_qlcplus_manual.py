#!/usr/bin/env python3
"""Assemble the QLC+ HTML documentation into one Markdown manual.

Phase 4 needs a second corpus that is non-paginated AND structurally
unnumbered. The grandMA2 manual is neither: every one of its 827 sections
carries a number, so the chunker's merge-forward rule -- which only applies to
units the document did not number -- has never executed against real data, and
offset locators, a NULL page_count and the no-pages path of get_context have
never been exercised outside unit tests. A second PDF manual would exercise
none of it.

This is a mechanical transform of real upstream content, not authored prose.
Each source page becomes one `##` section under a single `#` title, with the
page's own h2/h3 nesting shifted down to match. Ordering follows the upstream
index so the result reads in the order its authors intended.

    python scripts/build_qlcplus_manual.py <html_dir> <out.md>
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

from bs4 import BeautifulSoup, Tag

SKIP = {"index.html", "index_pdf.html"}


def ordered_pages(html_dir: Path) -> list[Path]:
    """Source pages in upstream index order, then any stragglers."""
    order: list[str] = []
    index = html_dir / "index.html"
    if index.exists():
        soup = BeautifulSoup(index.read_text(encoding="utf-8", errors="replace"), "lxml")
        for a in soup.find_all("a", href=True):
            href = str(a["href"]).split("#")[0]
            if href.endswith(".html") and href not in SKIP and href not in order:
                order.append(href)
    seen = set(order)
    rest = sorted(
        p.name for p in html_dir.glob("*.html") if p.name not in SKIP and p.name not in seen
    )
    return [html_dir / n for n in order + rest if (html_dir / n).exists()]


def page_title(soup: BeautifulSoup, fallback: str) -> str:
    for tag in ("h1", "h2", "title"):
        el = soup.find(tag)
        if el:
            text = el.get_text(" ", strip=True)
            text = re.sub(r"\s*[-–|]\s*Q Light Controller.*$", "", text, flags=re.I).strip()
            if text:
                return text
    return fallback


#: Lines that are punctuation-only, or CSS/JS residue that survived tag
#: removal, carry no signal and would pollute BM25 term statistics.
_JUNK = re.compile(r"^[\s{}();:,.\[\]|<>/*+-]*$")


def render(el: Tag, depth_shift: int) -> list[str]:
    name = el.name.lower()
    text = el.get_text(" ", strip=True)
    if not text or _JUNK.match(text):
        return []
    if name not in ("h1", "h2", "h3", "h4", "h5") and len(text) < 25:
        # A fragment this short outside a heading is a stray table cell or a
        # label stranded from its control, not a sentence.
        return []
    if name in ("h1", "h2", "h3", "h4", "h5"):
        level = min(6, int(name[1]) + depth_shift)
        return ["", "#" * level + " " + text, ""]
    if name == "li":
        return ["- " + text]
    if name == "pre":
        return ["", "```", text, "```", ""]
    return ["", text, ""]


def build(html_dir: Path, out: Path) -> None:
    lines: list[str] = ["# Q Light Controller Plus User Documentation", ""]
    pages = ordered_pages(html_dir)
    used = 0

    for path in pages:
        soup = BeautifulSoup(path.read_text(encoding="utf-8", errors="replace"), "lxml")
        for junk in soup(["script", "style", "nav"]):
            junk.decompose()
        body = soup.body or soup

        title = page_title(soup, path.stem.replace("_", " ").title())
        section: list[str] = ["", f"## {title}", ""]

        first_heading_skipped = False
        # Table cells are deliberately excluded: QLC+ uses tables for layout,
        # and lifting cells individually emits sentence fragments in an order
        # that reads as corrupted prose.
        for el in body.find_all(["h1", "h2", "h3", "h4", "h5", "p", "li", "pre"]):
            if el.name.lower() in ("h1", "h2") and not first_heading_skipped:
                # The page's own title becomes the '##' above; do not repeat it.
                first_heading_skipped = True
                continue
            if el.name.lower() in ("li", "p") and el.find_parent(["li", "pre"]):
                continue
            section.extend(render(el, depth_shift=1))

        if len("".join(section).strip()) > 200:
            lines.extend(section)
            used += 1

    text = re.sub(r"\n{3,}", "\n\n", "\n".join(lines)).strip() + "\n"
    out.write_text(text, encoding="utf-8")
    words = len(text.split())
    print(f"wrote {out} from {used}/{len(pages)} pages: {len(text):,} chars, ~{words:,} words")


def main() -> None:
    if len(sys.argv) != 3:
        print(__doc__)
        raise SystemExit(2)
    build(Path(sys.argv[1]), Path(sys.argv[2]))


if __name__ == "__main__":
    main()
