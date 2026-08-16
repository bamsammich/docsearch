"""Pre-flight reconnaissance on a site, writing nothing.

A live dry run, because every question worth asking about a site -- how many
pages there are, whether a navigation places them, whether they carry text at
all -- is a question about what the server actually returns.
"""

from __future__ import annotations

import threading
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from docsearch.inspect import format_report, inspect_site

ROUTES: dict[str, tuple[int, dict[str, str], bytes]] = {}


class _Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
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
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    try:
        yield f"http://127.0.0.1:{httpd.server_address[1]}"
    finally:
        httpd.shutdown()
        httpd.server_close()


def _inspect(seed: str, tmp_path: Path):
    return inspect_site(
        seed,
        cache_path=tmp_path / "c.db",
        guard=lambda u: None,
        addr_guard=lambda a: True,
        interval=0.0,
    )


def _page(title: str, body: str) -> bytes:
    return (
        f"<html><head><title>{title}</title></head><body><h1>{title}</h1>{body}</body></html>"
    ).encode()


PROSE = "<p>" + ("The console stores each cue in a sequence and plays it back. " * 20) + "</p>"


def _sitemap(urls: list[str]) -> bytes:
    locs = "".join(f"<url><loc>{u}</loc></url>" for u in urls)
    return (
        '<?xml version="1.0"?><urlset '
        f'xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">{locs}</urlset>'
    ).encode()


def _build(server: str, names: list[str]) -> None:
    ROUTES["/sitemap.xml"] = (200, {}, _sitemap([f"{server}/docs/{n}" for n in names]))
    ROUTES["/docs"] = (200, {"Content-Type": "text/html"}, _page("Docs", "<p>Welcome.</p>"))
    for n in names:
        ROUTES[f"/docs/{n}"] = (200, {"Content-Type": "text/html"}, _page(n.title(), PROSE))


def _finding(rep, label: str):
    return next(f for f in rep.findings if f.label == label)


def test_a_healthy_site_reports_its_sources_and_page_count(server: str, tmp_path: Path) -> None:
    _build(server, ["install", "configure", "commands"])
    rep = _inspect(f"{server}/docs", tmp_path)
    assert not rep.blocked
    assert rep.page_count == 3
    assert "sitemap" in _finding(rep, "coverage").detail


def test_inspecting_writes_nothing_to_any_index(server: str, tmp_path: Path) -> None:
    _build(server, ["install"])
    _inspect(f"{server}/docs", tmp_path)
    assert not (tmp_path / "docsearch.db").exists()


def test_an_unreachable_site_is_blocked_with_the_reason(server: str, tmp_path: Path) -> None:
    rep = _inspect(f"{server}/docs", tmp_path)
    assert rep.blocked
    assert _finding(rep, "coverage").level == "blocked"
    assert "cannot be ingested" in format_report(rep)


def test_a_mostly_broken_site_is_blocked_before_it_costs_an_ingest(
    server: str, tmp_path: Path
) -> None:
    names = [f"p{i}" for i in range(10)]
    _build(server, names)
    for n in names[:6]:
        del ROUTES[f"/docs/{n}"]
    rep = _inspect(f"{server}/docs", tmp_path)
    assert rep.blocked
    assert _finding(rep, "reachability").level == "blocked"


def test_an_inferred_hierarchy_warns_rather_than_claiming_structure(
    server: str, tmp_path: Path
) -> None:
    _build(server, [f"p{i}" for i in range(10)])
    rep = _inspect(f"{server}/docs", tmp_path)
    assert rep.predicted_source == "url_path"
    assert rep.predicted_tier == "inferred, no source to check it against"
    assert _finding(rep, "hierarchy").level == "warn"


def test_client_rendered_pages_are_named_not_silently_ingested(server: str, tmp_path: Path) -> None:
    """An empty shell that reaches 'ready' looks exactly like a thin page."""
    names = ["a", "b", "c", "d", "e", "f"]
    _build(server, names)
    shell = (
        b"<html><head><title>App</title></head><body><div id='root'></div>"
        b"<script>" + (b"var x = 1; function render(){ return 42; } " * 60) + b"</script>"
        b"</body></html>"
    )
    for n in names[:2]:
        ROUTES[f"/docs/{n}"] = (200, {"Content-Type": "text/html"}, shell)

    rep = _inspect(f"{server}/docs", tmp_path)
    finding = _finding(rep, "client-rendered")
    assert finding.level == "warn"
    assert "2 page(s)" in finding.detail


def test_a_blocked_address_is_refused_before_any_request(tmp_path: Path) -> None:
    """The real guard, not the injected one: this is the boundary."""
    rep = inspect_site("http://169.254.169.254/latest/meta-data/", cache_path=tmp_path / "c.db")
    assert rep.blocked
    assert _finding(rep, "address").level == "blocked"
