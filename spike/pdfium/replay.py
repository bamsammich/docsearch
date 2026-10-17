"""PyMuPDF versus go-pdfium, measured through the real PDF pipeline.

The adapter reads five things from PyMuPDF and nothing else: per-page text
lines with a font size and position, per-page plain text, per-page image
placements, the outline, and the metadata title. This script dumps those
primitives from PyMuPDF in the same JSON shape the Go program emits, then
replays either dump through ``docsearch.adapters.pdf.extract`` unchanged by
standing a fake document in for ``pymupdf.open``. Comparing the two replays
compares what the port would have to reproduce, down to chunk boundaries.

    uv run python spike/pdfium/replay.py dump    FILE.pdf OUT.json
    uv run python spike/pdfium/replay.py compare FILE.pdf PYMUPDF.json PDFIUM.json
    uv run python spike/pdfium/replay.py index   FILE.pdf PRIMITIVES.json OUT.db [EXTRA_FILE...]

``compare`` and ``index`` run with the merged-cell contents parser from
``tocvariant.py`` on both sides; it changes nothing for PyMuPDF's lines.
``index`` builds an eval database from one engine's primitives, plus any extra
non-PDF files the query set needs, ingested normally.
"""

from __future__ import annotations

import json
import sys
import time
from collections import Counter
from pathlib import Path
from typing import Any
from unittest import mock

import pymupdf
import tocvariant

from docsearch import db
from docsearch.adapters import pdf as adapter
from docsearch.chunker import chunk
from docsearch.ingest import FileSource, ingest_source

# -- dump --------------------------------------------------------------------


def dump(path: Path) -> dict[str, Any]:
    start = time.perf_counter()
    doc = pymupdf.open(path)
    pages = []
    for i in range(doc.page_count):
        page = doc[i]
        lines = [[ln.y0, ln.x0, ln.y1, ln.size, ln.text] for ln in adapter._page_lines(page)]
        try:
            infos = page.get_image_info(xrefs=True)
        except Exception:
            infos = []
        images = []
        for im in infos:
            b = im["bbox"]
            area = abs(b[2] - b[0]) * abs(b[3] - b[1])
            images.append([float(b[1]), str(im.get("xref", 0)), area])
        pages.append({"lines": lines, "text": page.get_text("text"), "images": images})
    return {
        "engine": "pymupdf",
        "page_count": doc.page_count,
        "title": (doc.metadata or {}).get("title") or "",
        "toc": [[lvl, title, pg] for lvl, title, pg in doc.get_toc(simple=True)],
        "pages": pages,
        "seconds": round(time.perf_counter() - start, 2),
    }


# -- replay ------------------------------------------------------------------


class _FakePage:
    def __init__(self, data: dict[str, Any], image_ids: dict[str, int]) -> None:
        self._data = data
        self._ids = image_ids

    def get_text(self, mode: str) -> Any:
        if mode == "text":
            return self._data["text"]
        assert mode == "dict", mode
        blocks = []
        for y0, x0, y1, size, text in self._data["lines"]:
            span = {"text": text, "size": size}
            blocks.append({"type": 0, "lines": [{"bbox": [x0, y0, x0, y1], "spans": [span]}]})
        return {"blocks": blocks}

    def get_image_info(self, xrefs: bool = False) -> list[dict[str, Any]]:
        out = []
        for y0, ident, area in self._data["images"]:
            # _filter_figures reads bbox[1] and the bbox area, and counts xref
            # identity across pages. A 1pt-tall box of width `area` keeps both.
            xref = self._ids.setdefault(ident, len(self._ids) + 1)
            out.append({"bbox": [0.0, y0, area, y0 + 1.0], "xref": xref})
        return out


class _FakeDoc:
    def __init__(self, prim: dict[str, Any]) -> None:
        self._prim = prim
        self._ids: dict[str, int] = {}
        self.page_count = prim["page_count"]
        self.metadata = {"title": prim["title"]}

    def __getitem__(self, i: int) -> _FakePage:
        return _FakePage(self._prim["pages"][i], self._ids)

    def get_toc(self, simple: bool = True) -> list[list[Any]]:
        return [list(e) for e in self._prim["toc"]]


def replay(pdf: Path, prim: dict[str, Any]) -> tuple[Any, list[Any]]:
    with (
        mock.patch.object(adapter.pymupdf, "open", return_value=_FakeDoc(prim)),
        mock.patch.object(adapter, "reconstruct_front_toc", tocvariant.reconstruct),
    ):
        ext = adapter.extract(pdf)
    return ext, chunk(ext)


def index(pdf: Path, prim: dict[str, Any], out: Path, extras: list[Path]) -> None:
    for stale in out.parent.glob(out.name + "*"):
        stale.unlink()
    conn = db.connect(out)
    with (
        mock.patch.object(adapter.pymupdf, "open", return_value=_FakeDoc(prim)),
        mock.patch.object(adapter, "reconstruct_front_toc", tocvariant.reconstruct),
    ):
        r = ingest_source(conn, FileSource(path=pdf))
    print(f"{out}: {r.doc_id} {r.chunk_count} chunks")
    for extra in extras:
        r = ingest_source(conn, FileSource(path=extra))
        print(f"{out}: {r.doc_id} {r.chunk_count} chunks")


# -- compare -----------------------------------------------------------------


def _jaccard(a: set[Any], b: set[Any]) -> float:
    return len(a & b) / len(a | b) if a | b else 1.0


def _size_volume(prim: dict[str, Any]) -> Counter[float]:
    vol: Counter[float] = Counter()
    for p in prim["pages"]:
        for _y0, _x0, _y1, size, text in p["lines"]:
            vol[size] += len(text)
    return vol


def _norm(text: str) -> str:
    return " ".join(text.split())


def compare(pdf: Path, a: dict[str, Any], b: dict[str, Any]) -> dict[str, Any]:
    n = a["page_count"]
    ea, ca = replay(pdf, a)
    eb, cb = replay(pdf, b)

    toc_a = [(lvl, t.strip(), pg) for lvl, t, pg in a["toc"]]
    toc_b = [(lvl, t.strip(), pg) for lvl, t, pg in b["toc"]]

    lines_a = sum(len(p["lines"]) for p in a["pages"])
    lines_b = sum(len(p["lines"]) for p in b["pages"])
    text_a = sum(len(_norm(p["text"])) for p in a["pages"])
    text_b = sum(len(_norm(p["text"])) for p in b["pages"])
    same_text_pages = sum(
        1
        for pa, pb in zip(a["pages"], b["pages"], strict=True)
        if _norm(pa["text"]) == _norm(pb["text"])
    )
    vol_a, vol_b = _size_volume(a), _size_volume(b)
    top_a = [s for s, _ in vol_a.most_common(6)]
    top_b = [s for s, _ in vol_b.most_common(6)]

    keys = (
        "structure_source",
        "body_font_size",
        "heading_font_sizes",
        "boilerplate_lines_stripped",
        "toc_entries",
        "figures",
        "outline_placement",
    )
    diag = {k: [ea.diagnostics.get(k), eb.diagnostics.get(k)] for k in keys}

    paths_a = {c.heading_path for c in ca}
    paths_b = {c.heading_path for c in cb}
    starts_a = {(c.heading_path, c.page_start) for c in ca}
    starts_b = {(c.heading_path, c.page_start) for c in cb}

    return {
        "pages": n,
        "seconds": [a["seconds"], b["seconds"]],
        "outline": {
            "entries": [len(toc_a), len(toc_b)],
            "identical": toc_a == toc_b,
            "title_and_page_match": sum(1 for x, y in zip(toc_a, toc_b, strict=False) if x == y),
        },
        "lines": [lines_a, lines_b],
        "page_text_chars": [text_a, text_b],
        "pages_with_identical_text": same_text_pages,
        "top_font_sizes_by_volume": [top_a, top_b],
        "raw_images": [
            sum(len(p["images"]) for p in a["pages"]),
            sum(len(p["images"]) for p in b["pages"]),
        ],
        "diagnostics": diag,
        "blocks": [len(ea.blocks), len(eb.blocks)],
        "chunks": [len(ca), len(cb)],
        "heading_paths_jaccard": round(_jaccard(paths_a, paths_b), 4),
        "chunk_start_jaccard": round(_jaccard(starts_a, starts_b), 4),
        "chunk_image_count": [sum(c.image_count for c in ca), sum(c.image_count for c in cb)],
        "only_in_pymupdf": sorted(paths_a - paths_b)[:8],
        "only_in_pdfium": sorted(paths_b - paths_a)[:8],
    }


def main(argv: list[str]) -> None:
    cmd = argv[1]
    if cmd == "dump":
        Path(argv[3]).write_text(json.dumps(dump(Path(argv[2]))))
    elif cmd == "compare":
        a = json.loads(Path(argv[3]).read_text())
        b = json.loads(Path(argv[4]).read_text())
        print(json.dumps(compare(Path(argv[2]), a, b), indent=2, default=str))
    elif cmd == "summary":
        d = json.loads(Path(argv[2]).read_text())
        dg = d["diagnostics"]
        located = [p["located_by_title"] if p else "-" for p in dg["outline_placement"]]
        print(
            f"{Path(argv[2]).name.removesuffix('.compare.json')[:28]:28} {d['pages']:>5}pp "
            f"src={dg['structure_source'][1]} located={located} toc={dg['toc_entries']} "
            f"chunks={d['chunks']} paths={d['heading_paths_jaccard']} "
            f"starts={d['chunk_start_jaccard']} images={d['chunk_image_count']} "
            f"seconds={d['seconds']}"
        )
    elif cmd == "index":
        prim = json.loads(Path(argv[3]).read_text())
        index(Path(argv[2]), prim, Path(argv[4]), [Path(p) for p in argv[5:]])
    else:
        raise SystemExit(__doc__)


if __name__ == "__main__":
    main(sys.argv)
