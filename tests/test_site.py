"""A site as one document, end to end through the real ingest path.

The claim under test is that a site needs no new chunking strategy: page-as-
authoritative-section is a ``Block.section`` the chunker already knows what to
do with. So these assert chunker behaviour reached through site ingest, not a
second implementation of it.
"""

from __future__ import annotations

import sqlite3
import threading
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from docsearch.chunker import MIN_TOKENS
from docsearch.db import delete_document_rows
from docsearch.errors import StructureValidationError
from docsearch.ingest import SiteSource, ingest_source
from docsearch.tokens import estimate_tokens

ROUTES: dict[str, tuple[int, dict[str, str], bytes]] = {}
HITS: list[str] = []


class _Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        HITS.append(self.path)
        status, headers, body = ROUTES.get(self.path, (404, {}, b"<html><body>gone</body></html>"))
        self.send_response(status)
        for k, v in headers.items():
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args: object) -> None:
        pass


@pytest.fixture
def server() -> Iterator[str]:
    ROUTES.clear()
    HITS.clear()
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    try:
        yield f"http://127.0.0.1:{httpd.server_address[1]}"
    finally:
        httpd.shutdown()
        httpd.server_close()


def _source(seed: str, tmp_path: Path, **kw: object) -> SiteSource:
    return SiteSource(
        seed=seed,
        cache_path=tmp_path / "fetch-cache.db",
        guard=lambda u: None,
        addr_guard=lambda a: True,
        interval=0.0,  # politeness is the fetcher's own test, not paid for here
        **kw,  # type: ignore[arg-type]
    )


def _page(title: str, body: str) -> bytes:
    return (
        f"<html><head><title>{title}</title></head><body><h1>{title}</h1>{body}</body></html>"
    ).encode()


PROSE = "<p>" + ("The console stores each cue in a sequence and plays it back. " * 30) + "</p>"


def _sitemap(urls: list[str]) -> bytes:
    locs = "".join(f"<url><loc>{u}</loc></url>" for u in urls)
    return (
        '<?xml version="1.0"?><urlset '
        f'xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">{locs}</urlset>'
    ).encode()


def _build_site(server: str, names: list[str], body: str = PROSE) -> None:
    urls = [f"{server}/docs/{n}" for n in names]
    ROUTES["/sitemap.xml"] = (200, {}, _sitemap(urls))
    ROUTES["/docs"] = (
        200,
        {"Content-Type": "text/html"},
        _page("Product Documentation", "<p>Welcome to the manual.</p>"),
    )
    for n in names:
        ROUTES[f"/docs/{n}"] = (
            200,
            {"Content-Type": "text/html"},
            _page(n.replace("-", " ").title(), body),
        )


# -- the model -------------------------------------------------------------


def test_a_site_becomes_one_document(conn: sqlite3.Connection, server: str, tmp_path: Path) -> None:
    _build_site(server, ["install", "configure", "commands"])
    result = ingest_source(conn, _source(f"{server}/docs", tmp_path))

    assert result.outcome == "ingested"
    row = conn.execute(
        "SELECT source_kind, format, status, page_count FROM documents WHERE doc_id=?",
        (result.doc_id,),
    ).fetchone()
    assert row["source_kind"] == "site"
    assert row["format"] == "site"
    assert row["status"] == "ready"
    assert row["page_count"] is None, "a site is not paginated"
    assert conn.execute("SELECT COUNT(*) c FROM documents").fetchone()["c"] == 1


def test_every_page_is_an_authoritative_section(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    _build_site(server, ["install", "configure", "commands"])
    result = ingest_source(conn, _source(f"{server}/docs", tmp_path))
    sections = {
        r["section"]
        for r in conn.execute(
            "SELECT DISTINCT section FROM chunks WHERE doc_id=?", (result.doc_id,)
        )
    }
    assert None not in sections, "every chunk of a site carries its page's section"
    assert len(sections) >= 3


def test_a_stub_page_is_not_absorbed_into_the_next_one(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    """The reason page-as-section exists.

    Merge-forward never crosses an authoritative boundary. Without one, a short
    page is absorbed into the page after it and attributed to that page's
    heading -- a silent content-attribution bug, and doc sites are full of
    short pages.
    """
    _build_site(server, ["long-one", "stub", "long-two"])
    ROUTES["/docs/stub"] = (
        200,
        {"Content-Type": "text/html"},
        _page("Stub", "<p>Two sentences. That is the whole page.</p>"),
    )
    result = ingest_source(conn, _source(f"{server}/docs", tmp_path))

    rows = conn.execute(
        "SELECT heading_path, text FROM chunks WHERE doc_id=?", (result.doc_id,)
    ).fetchall()
    stub = [r for r in rows if "Stub" in r["heading_path"]]
    assert stub, "the stub page lost its own heading path"
    assert estimate_tokens(stub[0]["text"]) < MIN_TOKENS, "it stayed small, as declared"
    assert "That is the whole page" not in "".join(
        r["text"] for r in rows if "Stub" not in r["heading_path"]
    )


def test_chunks_carry_the_address_they_were_read_from(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    _build_site(server, ["install"])
    ROUTES["/docs/install"] = (
        200,
        {"Content-Type": "text/html"},
        _page("Install", f'<h2 id="requirements">Requirements</h2>{PROSE}'),
    )
    result = ingest_source(conn, _source(f"{server}/docs", tmp_path))

    rows = conn.execute(
        "SELECT url, fragment, heading_path FROM chunks WHERE doc_id=? AND url IS NOT NULL",
        (result.doc_id,),
    ).fetchall()
    assert rows, "no chunk recorded the page it came from"
    assert all(r["url"].startswith(server) for r in rows)
    anchored = [r for r in rows if r["fragment"] == "requirements"]
    assert anchored, "a heading id must reach the chunk, so a citation lands on the section"


def test_the_page_title_is_not_repeated_in_the_heading_path(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    """`Guides > Install > Install > Options` reads as structure that is not there."""
    _build_site(server, ["install"])
    result = ingest_source(conn, _source(f"{server}/docs", tmp_path))
    for r in conn.execute("SELECT heading_path FROM chunks WHERE doc_id=?", (result.doc_id,)):
        parts = r["heading_path"].split(" > ")
        assert len(parts) == len(dict.fromkeys(parts)), f"repeated level in {r['heading_path']!r}"


def test_a_local_file_is_untouched_by_any_of_this(conn: sqlite3.Connection, md_file: Path) -> None:
    """One pipeline, two sources: the file path must behave exactly as before."""
    from docsearch.ingest import FileSource

    result = ingest_source(conn, FileSource(md_file))
    row = conn.execute(
        "SELECT source_kind, format FROM documents WHERE doc_id=?", (result.doc_id,)
    ).fetchone()
    assert row["source_kind"] == "file"
    assert row["format"] == "markdown"
    urls = conn.execute(
        "SELECT COUNT(*) c FROM chunks WHERE doc_id=? AND url IS NOT NULL", (result.doc_id,)
    ).fetchone()["c"]
    assert urls == 0, "a file has no URL to cite"


# -- the completeness gate -------------------------------------------------


def test_a_few_broken_links_are_reported_not_fatal(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    """Broken links are ordinary on a large site."""
    names = [f"p{i}" for i in range(20)]
    _build_site(server, names)
    del ROUTES["/docs/p7"]  # one 404 in twenty

    result = ingest_source(conn, _source(f"{server}/docs", tmp_path))
    assert result.outcome == "ingested"
    assert result.report is not None
    assert result.report.unreachable_pages
    assert not result.report.incomplete
    assert any("could not be fetched" in n for n in result.report.notes())


def test_a_mostly_unreachable_site_is_refused_and_writes_nothing(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    """The web's version of the unaddressable failure.

    An index over a fraction of a site is legally formed, internally
    consistent, and answers confidently from the part it happens to hold.
    Nothing a caller can see reveals the rest is missing, so it fails here.
    """
    names = [f"p{i}" for i in range(10)]
    _build_site(server, names)
    for n in names[:6]:
        del ROUTES[f"/docs/{n}"]

    with pytest.raises(StructureValidationError) as excinfo:
        ingest_source(conn, _source(f"{server}/docs", tmp_path))
    assert "completeness gate" in str(excinfo.value)
    assert conn.execute("SELECT COUNT(*) c FROM documents").fetchone()["c"] == 0
    assert conn.execute("SELECT COUNT(*) c FROM chunks").fetchone()["c"] == 0


# -- lifecycle -------------------------------------------------------------


def test_reingesting_an_unchanged_site_is_a_noop(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    _build_site(server, ["install", "configure"])
    first = ingest_source(conn, _source(f"{server}/docs", tmp_path))
    second = ingest_source(conn, _source(f"{server}/docs", tmp_path))
    assert second.outcome == "unchanged"
    assert second.doc_id == first.doc_id
    assert conn.execute("SELECT COUNT(*) c FROM documents").fetchone()["c"] == 1


def test_a_changed_page_replaces_the_document_cleanly(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    _build_site(server, ["install", "configure"])
    first = ingest_source(conn, _source(f"{server}/docs", tmp_path))
    ROUTES["/docs/install"] = (
        200,
        {"Content-Type": "text/html"},
        _page("Install", "<p>Completely rewritten installation instructions now.</p>" * 20),
    )
    second = ingest_source(conn, _source(f"{server}/docs", tmp_path))

    assert second.outcome == "replaced"
    assert second.doc_id == first.doc_id
    assert conn.execute("SELECT COUNT(*) c FROM documents").fetchone()["c"] == 1
    chunks = conn.execute("SELECT COUNT(*) c FROM chunks").fetchone()["c"]
    fts = conn.execute("SELECT COUNT(*) c FROM chunks_fts").fetchone()["c"]
    assert chunks == fts, "replacement left orphaned FTS rows"


def test_the_site_is_searchable_by_its_own_words(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    """The point of all of it."""
    _build_site(server, ["install", "configure"])
    ROUTES["/docs/configure"] = (
        200,
        {"Content-Type": "text/html"},
        _page("Configure", "<p>Set the dmx universe and patch each fixture.</p>" * 20),
    )
    ingest_source(conn, _source(f"{server}/docs", tmp_path))
    hits = conn.execute(
        "SELECT c.heading_path, c.url FROM chunks_fts f JOIN chunks c ON c.id = f.rowid"
        " WHERE chunks_fts MATCH 'universe'"
    ).fetchall()
    assert hits
    assert all(h["url"] for h in hits), "a result must carry an address to cite"


# -- cross-page chrome -----------------------------------------------------


def test_a_sidebar_repeated_on_every_page_is_stripped(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    """The parse drops <nav> and <footer>; plenty of generators use neither.

    A sidebar rendered as a div full of links survives as a block on every
    single page, and left in it is a share of the term mass that every chunk
    matches equally.
    """
    names = [f"p{i}" for i in range(8)]
    sidebar = (
        '<div class="sidebar"><ul>'
        "<li>Getting Started</li><li>API Reference</li><li>Changelog</li>"
        "</ul></div>"
    )
    _build_site(server, names)
    for n in names:
        ROUTES[f"/docs/{n}"] = (
            200,
            {"Content-Type": "text/html"},
            _page(n.title(), sidebar + PROSE),
        )

    result = ingest_source(conn, _source(f"{server}/docs", tmp_path))
    body = " ".join(
        r["text"] for r in conn.execute("SELECT text FROM chunks WHERE doc_id=?", (result.doc_id,))
    )
    assert "Getting Started" not in body
    assert "API Reference" not in body
    assert "console stores each cue" in body, "real content must survive"
    site = (result.diagnostics or {}).get("site")
    assert isinstance(site, dict) and site["chrome_blocks_dropped"] > 0


def test_content_appearing_on_a_couple_of_pages_is_not_chrome(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    """Repetition alone is not furniture; it has to be site-wide."""
    names = [f"p{i}" for i in range(8)]
    _build_site(server, names)
    for n in names[:2]:
        ROUTES[f"/docs/{n}"] = (
            200,
            {"Content-Type": "text/html"},
            _page(n.title(), "<p>This feature is deprecated since version 4.</p>" + PROSE),
        )

    result = ingest_source(conn, _source(f"{server}/docs", tmp_path))
    body = " ".join(
        r["text"] for r in conn.execute("SELECT text FROM chunks WHERE doc_id=?", (result.doc_id,))
    )
    assert "deprecated since version 4" in body


def test_a_small_site_is_never_chrome_stripped(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    """Below the floor, repetition is not evidence and the risk dominates."""
    names = ["a", "b", "c"]
    _build_site(server, names)
    for n in names:
        ROUTES[f"/docs/{n}"] = (
            200,
            {"Content-Type": "text/html"},
            _page(n.title(), "<p>Shared note on every page here.</p>" + PROSE),
        )

    result = ingest_source(conn, _source(f"{server}/docs", tmp_path))
    body = " ".join(
        r["text"] for r in conn.execute("SELECT text FROM chunks WHERE doc_id=?", (result.doc_id,))
    )
    assert "Shared note on every page" in body


# -- refresh ---------------------------------------------------------------


def test_rechunking_from_the_cache_makes_no_requests(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    """What re-indexing after a chunker change wants, and it works offline.

    A chunker change should re-index a whole site with no requests at all --
    one of the four things the fetch cache exists to buy.
    """
    _build_site(server, ["install", "configure", "commands"])
    first = ingest_source(conn, _source(f"{server}/docs", tmp_path))
    # Dropped through the one function that gets the ordering right: the FTS
    # index is maintained by a trigger on chunks, so they cannot go last.
    delete_document_rows(conn, first.doc_id)
    HITS.clear()

    again = ingest_source(conn, _source(f"{server}/docs", tmp_path, revalidate=False))
    assert HITS == [], "a cached re-chunk must not touch the network"
    assert again.chunk_count == first.chunk_count


def test_a_refreshed_site_picks_up_a_changed_page(
    conn: sqlite3.Connection, server: str, tmp_path: Path
) -> None:
    _build_site(server, ["install", "configure"])
    ingest_source(conn, _source(f"{server}/docs", tmp_path))
    ROUTES["/docs/configure"] = (
        200,
        {"Content-Type": "text/html"},
        _page("Configure", "<p>A newly documented calibration procedure.</p>" * 20),
    )
    after = ingest_source(conn, _source(f"{server}/docs", tmp_path))

    assert after.outcome == "replaced"
    hit = conn.execute(
        "SELECT COUNT(*) c FROM chunks_fts WHERE chunks_fts MATCH 'calibration'"
    ).fetchone()["c"]
    assert hit, "the refreshed page is not searchable"
