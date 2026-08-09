"""Coverage discovery, against fixture sites shaped like the probed targets.

Two shapes, because they broke different assumptions. One publishes a sitemap
and a partial llms.txt and buries its documentation among far more blog posts.
The other publishes neither and puts everything on a hub page that does not
share a path prefix with what it links to.
"""

from __future__ import annotations

import threading
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from docsearch import fetchcache
from docsearch.discover import (
    Coverage,
    discover,
    in_prefix_scope,
    index_page_candidates,
    looks_absent,
    not_found_signature,
)
from docsearch.fetch import Fetcher, normalize

ROUTES: dict[str, tuple[int, dict[str, str], bytes]] = {}
SOFT_404: dict[str, bool] = {"on": False}


class _Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if self.path in ROUTES:
            status, headers, body = ROUTES[self.path]
        elif SOFT_404["on"]:
            # A site that answers 200 for everything, echoing the path back so
            # two probes differ in exactly the way a real one would.
            status, headers = 200, {"Content-Type": "text/html"}
            body = (
                f"<html><body><h1>Page not found</h1><p>We could not find "
                f"{self.path}. Try the search box or the home page.</p>"
                f"</body></html>"
            ).encode()
        else:
            status, headers, body = 404, {}, b"<html><body>gone</body></html>"

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
    SOFT_404["on"] = False
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    try:
        yield f"http://127.0.0.1:{httpd.server_address[1]}"
    finally:
        httpd.shutdown()
        httpd.server_close()


@pytest.fixture
def fetcher(tmp_path: Path) -> Iterator[Fetcher]:
    cache = fetchcache.connect(tmp_path / "c.db")
    with Fetcher(cache, guard=lambda u: None, addr_guard=lambda a: True, interval=0.0) as f:
        yield f


def _urlset(urls: list[str]) -> bytes:
    locs = "".join(f"<url><loc>{u}</loc></url>" for u in urls)
    return (
        '<?xml version="1.0"?><urlset '
        f'xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">{locs}</urlset>'
    ).encode()


# -- scope -----------------------------------------------------------------


def test_prefix_scope_keeps_the_seed_and_its_children() -> None:
    seed = "https://x.dev/docs"
    assert in_prefix_scope("https://x.dev/docs", seed)
    assert in_prefix_scope("https://x.dev/docs/install", seed)
    assert not in_prefix_scope("https://x.dev/blog/post", seed)
    assert not in_prefix_scope("https://other.dev/docs/x", seed)
    # A sibling that merely shares a string prefix is not a child.
    assert not in_prefix_scope("https://x.dev/docsearch", seed)


# -- the generator shape ---------------------------------------------------


def test_a_sitemap_is_scoped_to_the_seed(server: str, fetcher: Fetcher) -> None:
    """Without scoping, one probed target contributes 385 blog posts."""
    docs = [f"{server}/docs", f"{server}/docs/install", f"{server}/docs/config"]
    blog = [f"{server}/blog/{i}" for i in range(20)]
    ROUTES["/sitemap.xml"] = (200, {"Content-Type": "application/xml"}, _urlset(docs + blog))
    ROUTES["/docs"] = (200, {}, b"<html><body><h1>Docs</h1></body></html>")

    cov = discover(fetcher, f"{server}/docs")
    assert cov.from_sitemap == {normalize(u) for u in docs}
    assert not any("/blog/" in u for u in cov.urls)


def test_a_sitemap_index_is_followed(server: str, fetcher: Fetcher) -> None:
    ROUTES["/sitemap.xml"] = (
        200,
        {},
        (
            '<?xml version="1.0"?><sitemapindex '
            'xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">'
            f"<sitemap><loc>{server}/sitemap-docs.xml</loc></sitemap>"
            "</sitemapindex>"
        ).encode(),
    )
    ROUTES["/sitemap-docs.xml"] = (200, {}, _urlset([f"{server}/docs/a", f"{server}/docs/b"]))
    ROUTES["/docs"] = (200, {}, b"<html><body>docs</body></html>")

    cov = discover(fetcher, f"{server}/docs")
    assert cov.from_sitemap == {normalize(f"{server}/docs/a"), normalize(f"{server}/docs/b")}


def test_robots_sitemap_directive_is_used(server: str, fetcher: Fetcher) -> None:
    ROUTES["/robots.txt"] = (200, {}, f"Sitemap: {server}/custom.xml\n".encode())
    ROUTES["/custom.xml"] = (200, {}, _urlset([f"{server}/docs/a"]))
    ROUTES["/docs"] = (200, {}, b"<html><body>docs</body></html>")

    cov = discover(fetcher, f"{server}/docs")
    assert normalize(f"{server}/docs/a") in cov.from_sitemap


def test_llms_txt_contributes_but_does_not_bound_coverage(server: str, fetcher: Fetcher) -> None:
    """On a probed target it holds 169 of 210 pages, omitting a whole command
    reference. Using it as the page set would drop them silently."""
    sitemap_pages = [f"{server}/docs/{n}" for n in ("a", "b", "c", "commands")]
    ROUTES["/sitemap.xml"] = (200, {}, _urlset(sitemap_pages))
    ROUTES["/llms.txt"] = (
        200,
        {},
        (
            f"# Site\n\n## Table of Contents\n\n- [A]({server}/docs/a)\n- [B]({server}/docs/b)\n"
        ).encode(),
    )
    ROUTES["/docs"] = (200, {}, b"<html><body>docs</body></html>")

    cov = discover(fetcher, f"{server}/docs")
    assert normalize(f"{server}/docs/commands") in cov.urls
    assert cov.titles[normalize(f"{server}/docs/a")] == "A"
    assert any("not authoritative" in n for n in cov.notes)


# -- the hub-page shape ----------------------------------------------------


def test_the_seed_page_supplies_coverage_when_nothing_else_does(
    server: str, fetcher: Fetcher
) -> None:
    """A hub page's links are in scope by declaration, not by prefix: the seed
    sits at /support/avenue-arena while every document is at /support/en/*."""
    links = "".join(f'<h3><a href="/support/en/p{i}">Page {i}</a></h3>' for i in range(5))
    ROUTES["/support/avenue-arena"] = (
        200,
        {"Content-Type": "text/html"},
        f"<html><body><h1>Docs</h1>{links}</body></html>".encode(),
    )
    cov = discover(fetcher, f"{server}/support/avenue-arena")
    assert len(cov.from_index_page) == 5
    assert all("/support/en/p" in u for u in cov.from_index_page)
    assert cov.sources == ["index_page"]


def test_index_page_links_are_deduped_in_document_order() -> None:
    """A page rendering one navigation for desktop and another for mobile
    lists everything twice."""
    html = b"""<html><body>
      <a href="/a">A</a><a href="/b">B</a>
      <nav class="mobile"><a href="/a">A</a><a href="/b">B</a></nav>
      <a href="#frag">skip</a><a href="mailto:x@y.z">skip</a>
      <a href="https://elsewhere.example/x">offsite</a>
    </body></html>"""
    got = index_page_candidates(html, "https://x.dev/docs")
    assert got == ["https://x.dev/a", "https://x.dev/b"]


# -- soft 404s -------------------------------------------------------------


def test_an_honest_404_needs_no_signature(server: str, fetcher: Fetcher) -> None:
    assert not_found_signature(fetcher, server) is None


def test_a_soft_404_site_is_recognised(server: str, fetcher: Fetcher) -> None:
    """A site answering 200 for everything would otherwise feed its error page
    to the index, and the completeness gate would score it a success."""
    SOFT_404["on"] = True
    sig = not_found_signature(fetcher, server)
    assert sig is not None, "two 200 responses for absent pages should yield a template"

    absent = fetcher.fetch(f"{server}/docs/definitely-not-here")
    assert looks_absent(absent.body, sig)


def test_a_real_page_is_not_mistaken_for_the_not_found_template(
    server: str, fetcher: Fetcher
) -> None:
    SOFT_404["on"] = True
    ROUTES["/docs/real"] = (
        200,
        {"Content-Type": "text/html"},
        b"<html><body><h1>Configuring the universe</h1><p>Assign a dmx address "
        b"to each patched fixture, then reload the show file.</p></body></html>",
    )
    sig = not_found_signature(fetcher, server)
    real = fetcher.fetch(f"{server}/docs/real")
    assert not looks_absent(real.body, sig)


def test_nothing_is_absent_without_a_signature() -> None:
    assert looks_absent(b"<html><body>anything</body></html>", None) is False


# -- notes -----------------------------------------------------------------


def test_coverage_reports_which_sources_answered(server: str, fetcher: Fetcher) -> None:
    ROUTES["/sitemap.xml"] = (200, {}, _urlset([f"{server}/docs/a"]))
    ROUTES["/docs"] = (200, {}, b'<html><body><a href="/docs/b">B</a></body></html>')
    cov = discover(fetcher, f"{server}/docs")
    assert set(cov.sources) == {"sitemap", "index_page"}
    assert normalize(f"{server}/docs/a") in cov.urls
    assert normalize(f"{server}/docs/b") in cov.urls


def test_a_site_with_no_sources_reports_an_empty_coverage(server: str, fetcher: Fetcher) -> None:
    cov = discover(fetcher, f"{server}/docs")
    assert isinstance(cov, Coverage)
    assert cov.urls == []
    assert cov.sources == []
