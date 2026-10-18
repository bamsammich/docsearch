"""Printed-contents parser that also accepts a row whose first cell is "N.N. Title".

``reconstruct_front_toc`` in the adapter needs a contents row's section number
alone in the first cell. MuPDF splits "7.17." from its title even when the
gap before the title's tab stop is under half an em; no gap threshold on
PDFium's runs reproduces that for every row, because a long section number
leaves almost no gap at all. Splitting a merged cell here instead recovers all
827 contents rows of the manual it was measured on. The Go port rewrites this
parser anyway, so the tolerance belongs in the parser rather than in line
grouping.
"""

from __future__ import annotations

import re
from collections import defaultdict

from docsearch.adapters import pdf as adapter

_MERGED = re.compile(r"^(\d+(?:\.\d+)*)\.\s+(.+)$")


def reconstruct(
    pages: list[list[adapter._Line]], boiler: set[str], scan_pages: int
) -> tuple[list[tuple[str, str, int]], set[int]]:
    """``adapter.reconstruct_front_toc``, plus the merged-cell split."""
    entries: list[tuple[str, str, int]] = []
    toc_pages: set[int] = set()
    for pno, lines in enumerate(pages[:scan_pages]):
        bands: defaultdict[int, list[adapter._Line]] = defaultdict(list)
        for ln in lines:
            if adapter._is_boilerplate(ln.text, boiler):
                continue
            bands[int(ln.y0 // 4)].append(ln)
        for key in sorted(bands):
            texts = [c.text for c in sorted(bands[key], key=lambda c: c.x0)]
            if len(texts) >= 2 and not adapter._SECTION_ONLY.match(texts[0]):
                merged = _MERGED.match(texts[0])
                if merged:
                    texts = [merged.group(1) + ".", merged.group(2), *texts[1:]]
            if len(texts) < 3:
                continue
            head = adapter._SECTION_ONLY.match(texts[0])
            if head and texts[-1].isdigit():
                title = " ".join(texts[1:-1]).strip()
                if title:
                    entries.append((head.group(1), title, int(texts[-1])))
                    toc_pages.add(pno)
    return entries, toc_pages
