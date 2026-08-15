"""Crawl orchestration, against fixture sites shaped like the probed targets.

The bounds are what these exercise: a budget, a site that answers 200 for
everything, a page declared but missing, a cancelled crawl. All of it against a
real HTTP server on an ephemeral port, so redirects, 304s and status codes have
their real semantics.
"""

from __future__ import annotations

import threading
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from docsearch import fetchcache
from docsearch.crawl import crawl
from docsearch.errors import IngestCancelled
from docsearch.fetch import Fetcher, normalize

ROUTES: dict[str, tuple[int, dict[str, str], bytes]] = {}
HITS: list[str] = []
SOFT_404: dict[str, bool] = {"on": False}


class _Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        HITS.append(self.path)
        if self.path in ROUTES:
            status, headers, body = ROUTES[self.path]
        elif SOFT_404["on"]:
            status, headers = 200, {"Content-Type": "text/html"}
            body = (
                f"<html><body><h1>Page not found</h1><p>We could not find "
                f"{self.path}. Try the search box or the home page.</p></body></html>"
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
    HITS.clear()
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


def _html(body: str) -> bytes:
    return f"<html><body>{body}</body></html>".encode()


def _urlset(urls: list[str]) -> bytes:
    locs = "".join(f"<url><loc>{u}</loc></url>" for u in urls)
    return (
        '<?xml version="1.0"?><urlset '
        f'xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">{locs}</urlset>'
    ).encode()


def _sitemap_site(server: str, names: list[str]) -> list[str]:
    """A generator-shaped site: a sitemap plus one page per name."""
    urls = [f"{server}/docs/{n}" for n in names]
    ROUTES["/sitemap.xml"] = (200, {}, _urlset(urls))
    ROUTES["/docs"] = (200, {"Content-Type": "text/html"}, _html("<h1>Docs</h1>"))
    for n in names:
        ROUTES[f"/docs/{n}"] = (
            200,
            {"Content-Type": "text/html"},
            _html(f"<h1>{n.title()}</h1><p>Body of the {n} page, with enough words.</p>"),
        )
    return urls


# -- the happy path --------------------------------------------------------


def test_every_declared_page_is_fetched(server: str, fetcher: Fetcher) -> None:
    urls = _sitemap_site(server, ["install", "config", "commands"])
    res = crawl(fetcher, f"{server}/docs")
    assert set(res.pages) == {normalize(u) for u in urls}
    assert res.unreachable == []
    assert res.unreachable_share == 0.0


def test_hierarchy_is_derived_from_what_was_fetched(server: str, fetcher: Fetcher) -> None:
    _sitemap_site(server, ["install", "commands/run", "commands/docker"])
    res = crawl(fetcher, f"{server}/docs")
    placed = {p.url for p in res.hierarchy.placements}
    assert placed == set(res.pages), "every fetched page must be placed"
    assert len({p.section for p in res.hierarchy.placements}) == len(res.pages)


def test_progress_reports_pages_done_over_pages_known(server: str, fetcher: Fetcher) -> None:
    _sitemap_site(server, ["a", "b", "c"])
    seen: list[tuple[str, int, int]] = []
    crawl(fetcher, f"{server}/docs", progress=lambda p, c, t: seen.append((p, c, t)))
    assert seen[0][0] == "discover"
    fetches = [s for s in seen if s[0] == "fetch"]
    assert fetches[-1][1] == fetches[-1][2] == 3


# -- bounds ----------------------------------------------------------------


def test_the_page_budget_caps_the_crawl_and_reports_the_remainder(
    server: str, fetcher: Fetcher
) -> None:
    _sitemap_site(server, [f"p{i}" for i in range(10)])
    res = crawl(fetcher, f"{server}/docs", max_pages=4)
    assert len(res.pages) == 4
    assert len(res.unreachable) == 6
    assert all("budget" in reason for _u, reason in res.unreachable)
    assert any("page budget" in n for n in res.notes)


def test_a_missing_page_is_reported_not_fatal(server: str, fetcher: Fetcher) -> None:
    """A broken link on a large site is ordinary and must not refuse the crawl."""
    _sitemap_site(server, ["a", "b"])
    ROUTES["/sitemap.xml"] = (
        200,
        {},
        _urlset([f"{server}/docs/a", f"{server}/docs/b", f"{server}/docs/gone"]),
    )
    res = crawl(fetcher, f"{server}/docs")
    assert len(res.pages) == 2
    assert res.unreachable == [(normalize(f"{server}/docs/gone"), "HTTP 404")]
    assert 0.3 < res.unreachable_share < 0.34


def test_a_non_html_page_is_skipped_with_its_reason(server: str, fetcher: Fetcher) -> None:
    _sitemap_site(server, ["a"])
    ROUTES["/sitemap.xml"] = (200, {}, _urlset([f"{server}/docs/a", f"{server}/docs/manual.pdf"]))
    ROUTES["/docs/manual.pdf"] = (200, {"Content-Type": "application/pdf"}, b"%PDF-1.4")
    res = crawl(fetcher, f"{server}/docs")
    assert normalize(f"{server}/docs/manual.pdf") not in res.pages
    assert any("not HTML" in reason for _u, reason in res.unreachable)


def test_a_soft_404_page_is_treated_as_unreachable(server: str, fetcher: Fetcher) -> None:
    """Otherwise the error page is indexed and the gate counts it a success."""
    urls = [f"{server}/docs/real", f"{server}/docs/ghost"]
    ROUTES["/sitemap.xml"] = (200, {}, _urlset(urls))
    ROUTES["/docs"] = (200, {"Content-Type": "text/html"}, _html("<h1>Docs</h1>"))
    ROUTES["/docs/real"] = (
        200,
        {"Content-Type": "text/html"},
        _html(
            "<h1>Configuring the universe</h1><p>Assign a dmx address to each "
            "patched fixture, then reload the show file.</p>"
        ),
    )
    SOFT_404["on"] = True  # /docs/ghost answers 200 with the template

    res = crawl(fetcher, f"{server}/docs")
    assert normalize(f"{server}/docs/real") in res.pages
    assert normalize(f"{server}/docs/ghost") not in res.pages
    assert any("soft 404" in reason for _u, reason in res.unreachable)
    assert any("answers 200" in n for n in res.notes)


# -- canonical -------------------------------------------------------------


def test_canonical_link_collapses_a_versioned_duplicate(server: str, fetcher: Fetcher) -> None:
    canonical = f"{server}/docs/latest/guide"
    ROUTES["/sitemap.xml"] = (200, {}, _urlset([canonical, f"{server}/docs/v1/guide"]))
    ROUTES["/docs"] = (200, {"Content-Type": "text/html"}, _html("<h1>Docs</h1>"))
    page = (
        f'<html><head><link rel="canonical" href="{canonical}"></head>'
        f"<body><h1>Guide</h1><p>The same guide either way.</p></body></html>"
    ).encode()
    ROUTES["/docs/latest/guide"] = (200, {"Content-Type": "text/html"}, page)
    ROUTES["/docs/v1/guide"] = (200, {"Content-Type": "text/html"}, page)

    res = crawl(fetcher, f"{server}/docs")
    assert list(res.pages) == [normalize(canonical)]
    assert res.canonical_merges == [(normalize(f"{server}/docs/v1/guide"), normalize(canonical))]


# -- link following --------------------------------------------------------


def test_links_are_followed_when_no_manifest_answers(server: str, fetcher: Fetcher) -> None:
    """A hand-written site with no sitemap is walked, under the depth bound."""
    ROUTES["/docs"] = (
        200,
        {"Content-Type": "text/html"},
        _html('<h1>Docs</h1><a href="/docs/a">A</a>'),
    )
    ROUTES["/docs/a"] = (
        200,
        {"Content-Type": "text/html"},
        _html('<h1>A</h1><a href="/docs/b">B</a>'),
    )
    ROUTES["/docs/b"] = (200, {"Content-Type": "text/html"}, _html("<h1>B</h1><p>Leaf.</p>"))

    res = crawl(fetcher, f"{server}/docs")
    assert normalize(f"{server}/docs/b") in res.pages, "a second-hop page was never reached"
    assert any("following links" in n for n in res.notes)


def test_link_following_respects_the_depth_bound(server: str, fetcher: Fetcher) -> None:
    """Depth counts hops of *walking*.

    Every page a coverage source declared is depth 0, including the seed's own
    links -- those were declared, not discovered. So depth 1 permits one hop
    beyond the declared set and no further.
    """
    ROUTES["/docs"] = (
        200,
        {"Content-Type": "text/html"},
        _html('<h1>Docs</h1><a href="/docs/a">A</a>'),
    )
    ROUTES["/docs/a"] = (
        200,
        {"Content-Type": "text/html"},
        _html('<h1>A</h1><a href="/docs/b">B</a>'),
    )
    ROUTES["/docs/b"] = (
        200,
        {"Content-Type": "text/html"},
        _html('<h1>B</h1><a href="/docs/c">C</a>'),
    )
    ROUTES["/docs/c"] = (200, {"Content-Type": "text/html"}, _html("<h1>C</h1>"))

    res = crawl(fetcher, f"{server}/docs", link_depth=1)
    assert normalize(f"{server}/docs/b") in res.pages, "one hop from a declared page"
    assert normalize(f"{server}/docs/c") not in res.pages, "two hops exceeds the bound"


def test_discovered_links_are_scoped_but_declared_ones_are_not(
    server: str, fetcher: Fetcher
) -> None:
    """Scope applies to what the crawl finds, never to what the seed declared.

    A hub page at `/support/avenue-arena` legitimately lists documents at
    `/support/en/*`, so prefix-filtering the seed's own links discards the
    entire site. A link found by walking has no such licence: without scope one
    probed target contributes 385 blog posts.
    """
    ROUTES["/docs"] = (
        200,
        {"Content-Type": "text/html"},
        _html('<h1>Docs</h1><a href="/elsewhere/declared">Declared</a><a href="/docs/a">A</a>'),
    )
    ROUTES["/elsewhere/declared"] = (200, {"Content-Type": "text/html"}, _html("<h1>D</h1>"))
    ROUTES["/docs/a"] = (
        200,
        {"Content-Type": "text/html"},
        _html('<h1>A</h1><a href="/blog/post">Blog</a>'),
    )
    ROUTES["/blog/post"] = (200, {"Content-Type": "text/html"}, _html("<h1>Post</h1>"))

    res = crawl(fetcher, f"{server}/docs")
    assert normalize(f"{server}/elsewhere/declared") in res.pages, "the seed declared it"
    assert normalize(f"{server}/blog/post") not in res.pages, "walking found it; scope applies"


def test_a_sitemap_suppresses_link_following(server: str, fetcher: Fetcher) -> None:
    """A manifest describes the whole site; walking it again is not a last resort."""
    ROUTES["/sitemap.xml"] = (200, {}, _urlset([f"{server}/docs/a"]))
    ROUTES["/docs"] = (200, {"Content-Type": "text/html"}, _html("<h1>Docs</h1>"))
    ROUTES["/docs/a"] = (
        200,
        {"Content-Type": "text/html"},
        _html('<h1>A</h1><a href="/docs/deep">Deep</a>'),
    )
    ROUTES["/docs/deep"] = (200, {"Content-Type": "text/html"}, _html("<h1>Deep</h1>"))

    res = crawl(fetcher, f"{server}/docs")
    assert normalize(f"{server}/docs/deep") not in res.pages
    assert not any("following links" in n for n in res.notes)


# -- cancellation and resume ----------------------------------------------


def test_cancellation_is_observed_between_pages(server: str, fetcher: Fetcher) -> None:
    _sitemap_site(server, [f"p{i}" for i in range(8)])
    calls = {"n": 0}

    def should_cancel() -> bool:
        calls["n"] += 1
        return calls["n"] > 3

    with pytest.raises(IngestCancelled):
        crawl(fetcher, f"{server}/docs", should_cancel=should_cancel)


def test_a_second_crawl_can_run_entirely_from_the_cache(server: str, fetcher: Fetcher) -> None:
    """Re-chunking an already-crawled site should cost no requests at all."""
    _sitemap_site(server, ["a", "b", "c"])
    first = crawl(fetcher, f"{server}/docs")
    before = len(HITS)
    again = crawl(fetcher, f"{server}/docs", revalidate=False)
    assert len(HITS) == before, "a cached re-crawl must not touch the server"
    assert set(again.pages) == set(first.pages)


# -- degenerate cases ------------------------------------------------------


def test_an_unreachable_seed_yields_an_empty_crawl(server: str, fetcher: Fetcher) -> None:
    res = crawl(fetcher, f"{server}/docs")
    assert res.pages == {}
    assert res.hierarchy.placements == []
    assert any("HTTP 404" in n for n in res.notes)
