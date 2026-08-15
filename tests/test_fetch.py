"""Fetching, exercised against a real HTTP server on an ephemeral port.

Real semantics rather than a mocked client: redirects, 304, robots, and the
status codes a crawler actually meets. The URL guard is injected as a
permissive stub because the real one refuses loopback -- which is exactly what
a test server is, and which its own tests already cover.
"""

from __future__ import annotations

import threading
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from docsearch import fetchcache
from docsearch.fetch import Fetcher, FetchError, normalize

# url -> (status, headers, body). Rewritten per test.
ROUTES: dict[str, tuple[int, dict[str, str], bytes]] = {}
HITS: list[str] = []


class _Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        HITS.append(self.path)
        status, headers, body = ROUTES.get(self.path, (404, {}, b"not found"))

        # Honour conditional requests so the 304 path is real rather than
        # simulated by the test.
        etag = headers.get("ETag")
        if etag and self.headers.get("If-None-Match") == etag:
            self.send_response(304)
            self.end_headers()
            return

        self.send_response(status)
        for k, v in headers.items():
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if status not in (301, 302, 303, 307, 308):
            self.wfile.write(body)

    def log_message(self, *args: object) -> None:
        pass


@pytest.fixture
def server() -> Iterator[str]:
    ROUTES.clear()
    HITS.clear()
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{httpd.server_address[1]}"
    finally:
        httpd.shutdown()
        httpd.server_close()


@pytest.fixture
def fetcher(tmp_path: Path) -> Iterator[Fetcher]:
    cache = fetchcache.connect(tmp_path / "cache.db")
    # interval=0: politeness is asserted separately, not paid for in every test.
    with Fetcher(cache, guard=lambda url: None, addr_guard=lambda a: True, interval=0.0) as f:
        yield f


# -- normalization ---------------------------------------------------------


@pytest.mark.parametrize(
    "raw,want",
    [
        ("HTTP://Example.COM/Docs", "http://example.com/Docs"),
        ("http://example.com:80/docs", "http://example.com/docs"),
        ("https://example.com:443/docs", "https://example.com/docs"),
        ("https://example.com:8443/docs", "https://example.com:8443/docs"),
        ("https://example.com", "https://example.com/"),
        ("https://example.com/docs#install", "https://example.com/docs"),
        ("https://example.com/a/./b/../c", "https://example.com/a/c"),
        ("https://example.com/docs?b=2&a=1", "https://example.com/docs?b=2&a=1"),
    ],
)
def test_normalization_gives_one_spelling_per_resource(raw: str, want: str) -> None:
    assert normalize(raw) == want


def test_the_trailing_slash_is_preserved() -> None:
    """/docs/ and /docs are different resources, and on real generators one of
    them is the page and the other is a 404."""
    assert normalize("https://example.com/docs/") == "https://example.com/docs/"
    assert normalize("https://example.com/docs") == "https://example.com/docs"


# -- fetching --------------------------------------------------------------


def test_a_page_is_fetched_and_cached(server: str, fetcher: Fetcher) -> None:
    ROUTES["/a"] = (200, {"Content-Type": "text/html"}, b"<h1>A</h1>")
    got = fetcher.fetch(f"{server}/a")
    assert got.status == 200
    assert got.body == b"<h1>A</h1>"
    assert got.from_cache is False

    entry = fetchcache.get(fetcher.cache, normalize(f"{server}/a"))
    assert entry is not None and entry.body == b"<h1>A</h1>"


def test_an_unchanged_page_transfers_no_body(server: str, fetcher: Fetcher) -> None:
    ROUTES["/a"] = (200, {"Content-Type": "text/html", "ETag": '"v1"'}, b"<h1>A</h1>")
    fetcher.fetch(f"{server}/a")
    again = fetcher.fetch(f"{server}/a")
    assert again.from_cache is True
    assert again.body == b"<h1>A</h1>"
    assert HITS.count("/a") == 2, "the second call should revalidate, not skip the server"


def test_revalidate_false_does_not_touch_the_server(server: str, fetcher: Fetcher) -> None:
    ROUTES["/a"] = (200, {"Content-Type": "text/html"}, b"<h1>A</h1>")
    fetcher.fetch(f"{server}/a")
    before = len(HITS)
    again = fetcher.fetch(f"{server}/a", revalidate=False)
    assert again.from_cache is True
    assert len(HITS) == before, "re-chunking a crawled site should cost no requests"


def test_redirects_are_followed_and_the_final_url_recorded(server: str, fetcher: Fetcher) -> None:
    ROUTES["/old"] = (301, {"Location": "/new"}, b"")
    ROUTES["/new"] = (200, {"Content-Type": "text/html"}, b"<h1>New</h1>")
    got = fetcher.fetch(f"{server}/old")
    assert got.body == b"<h1>New</h1>"
    assert got.final_url == normalize(f"{server}/new")
    assert got.url == normalize(f"{server}/old"), "cached under what was asked for"


def test_a_redirect_loop_is_refused(server: str, fetcher: Fetcher) -> None:
    ROUTES["/loop"] = (302, {"Location": "/loop"}, b"")
    with pytest.raises(FetchError, match="redirects"):
        fetcher.fetch(f"{server}/loop")


def test_every_redirect_hop_is_guarded(server: str, tmp_path: Path) -> None:
    """Validating only the seed leaves the guard trivially bypassable."""
    seen: list[str] = []

    def guard(url: str) -> None:
        seen.append(url)

    ROUTES["/old"] = (301, {"Location": "/new"}, b"")
    ROUTES["/new"] = (200, {}, b"ok")
    cache = fetchcache.connect(tmp_path / "c.db")
    with Fetcher(cache, guard=guard, addr_guard=lambda a: True, interval=0.0) as f:
        f.fetch(f"{server}/old")
    # robots.txt is guarded too, so assert on the pages rather than a count.
    assert normalize(f"{server}/old") in seen, f"the seed was not guarded: {seen}"
    assert normalize(f"{server}/new") in seen, f"the redirect hop was not guarded: {seen}"


def test_a_404_is_returned_rather_than_raised(server: str, fetcher: Fetcher) -> None:
    """The crawler decides what a missing page means; the fetcher reports it."""
    got = fetcher.fetch(f"{server}/absent")
    assert got.status == 404


# -- robots ----------------------------------------------------------------


def test_robots_disallow_is_obeyed(server: str, fetcher: Fetcher) -> None:
    ROUTES["/robots.txt"] = (200, {}, b"User-agent: *\nDisallow: /private\n")
    ROUTES["/private/x"] = (200, {}, b"secret")
    ROUTES["/public/x"] = (200, {}, b"fine")

    with pytest.raises(FetchError, match="robots"):
        fetcher.fetch(f"{server}/private/x")
    assert fetcher.fetch(f"{server}/public/x").status == 200


def test_a_missing_robots_txt_permits_everything(server: str, fetcher: Fetcher) -> None:
    """Absent is permission, per the standard."""
    ROUTES["/a"] = (200, {}, b"ok")
    assert fetcher.fetch(f"{server}/a").status == 200


def test_robots_is_fetched_once_per_host(server: str, fetcher: Fetcher) -> None:
    ROUTES["/robots.txt"] = (200, {}, b"User-agent: *\nDisallow:\n")
    for path in ("/a", "/b", "/c"):
        ROUTES[path] = (200, {}, b"ok")
        fetcher.fetch(f"{server}{path}")
    assert HITS.count("/robots.txt") == 1


def test_robots_can_be_overridden_for_a_site_the_operator_runs(server: str, tmp_path: Path) -> None:
    ROUTES["/robots.txt"] = (200, {}, b"User-agent: *\nDisallow: /\n")
    ROUTES["/a"] = (200, {}, b"ok")
    cache = fetchcache.connect(tmp_path / "c.db")
    with Fetcher(
        cache, guard=lambda u: None, addr_guard=lambda a: True, interval=0.0, obey_robots=False
    ) as f:
        assert f.fetch(f"{server}/a").status == 200


# -- guards and budgets ----------------------------------------------------


def test_a_blocked_url_never_reaches_the_network(server: str, tmp_path: Path) -> None:
    def guard(url: str) -> None:
        raise AssertionError("blocked")

    cache = fetchcache.connect(tmp_path / "c.db")
    with (
        Fetcher(
            cache, guard=guard, addr_guard=lambda a: True, interval=0.0, obey_robots=False
        ) as f,
        pytest.raises(AssertionError),
    ):
        f.fetch(f"{server}/a")
    assert HITS == []


def test_the_fetch_budget_is_a_floor_under_a_bug(server: str, tmp_path: Path) -> None:
    for i in range(5):
        ROUTES[f"/p{i}"] = (200, {}, b"ok")
    cache = fetchcache.connect(tmp_path / "c.db")
    with Fetcher(
        cache, guard=lambda u: None, addr_guard=lambda a: True, interval=0.0, max_fetches=2
    ) as f:
        f.fetch(f"{server}/p0")
        f.fetch(f"{server}/p1")
        with pytest.raises(FetchError, match="budget"):
            f.fetch(f"{server}/p2")


def test_requests_to_one_host_are_spaced(server: str, tmp_path: Path) -> None:
    import time

    ROUTES["/a"] = (200, {}, b"ok")
    ROUTES["/b"] = (200, {}, b"ok")
    cache = fetchcache.connect(tmp_path / "c.db")
    with Fetcher(
        cache, guard=lambda u: None, addr_guard=lambda a: True, interval=0.15, obey_robots=False
    ) as f:
        start = time.monotonic()
        f.fetch(f"{server}/a")
        f.fetch(f"{server}/b")
        assert time.monotonic() - start >= 0.15


# -- the cache is disposable ----------------------------------------------


def test_a_cache_from_another_version_is_rebuilt(tmp_path: Path) -> None:
    path = tmp_path / "c.db"
    conn = fetchcache.connect(path)
    fetchcache.put(
        conn,
        fetchcache.CachedResponse(
            url="https://x/1",
            final_url="https://x/1",
            status=200,
            content_type="text/html",
            etag=None,
            last_modified=None,
            body=b"body",
            sha256="abc",
            fetched_at="2026-01-01T00:00:00Z",
        ),
    )
    conn.execute("PRAGMA user_version=999")
    conn.close()

    rebuilt = fetchcache.connect(path)
    assert fetchcache.get(rebuilt, "https://x/1") is None, "a stale cache is bandwidth, not data"
    assert int(rebuilt.execute("PRAGMA user_version").fetchone()[0]) == fetchcache.CACHE_VERSION


# -- the peer check fails closed ------------------------------------------


def test_a_response_from_a_disallowed_peer_is_refused(server: str, tmp_path: Path) -> None:
    """The rebinding backstop: the name passed, the address it answered from did not."""
    ROUTES["/a"] = (200, {}, b"secret")
    cache = fetchcache.connect(tmp_path / "c.db")
    with (
        Fetcher(
            cache,
            guard=lambda u: None,
            addr_guard=lambda a: False,
            interval=0.0,
            obey_robots=False,
        ) as f,
        pytest.raises(FetchError, match="not permitted"),
    ):
        f.fetch(f"{server}/a")


def test_an_undeterminable_peer_fails_closed(server: str, tmp_path: Path) -> None:
    """Returning quietly disabled the check with nothing to show for it.

    A transport exposing no connection is not evidence that the peer was
    acceptable, and treating it that way is the shape of gap that survives
    review: nothing errors, the body is read, and the only trace is an absence.
    """
    ROUTES["/a"] = (200, {}, b"secret")
    cache = fetchcache.connect(tmp_path / "c.db")
    with Fetcher(
        cache, guard=lambda u: None, addr_guard=lambda a: True, interval=0.0, obey_robots=False
    ) as f:
        # Strip the extension the check reads, standing in for a transport
        # that does not expose one.
        original = f._client.stream

        class _Blind:
            def __init__(self, cm):
                self._cm = cm

            def __enter__(self):
                res = self._cm.__enter__()
                res.extensions = {}
                return res

            def __exit__(self, *a):
                return self._cm.__exit__(*a)

        f._client.stream = lambda *a, **kw: _Blind(original(*a, **kw))  # type: ignore[method-assign]
        with pytest.raises(FetchError, match="could not be confirmed"):
            f.fetch(f"{server}/a")


def test_robots_is_peer_checked_too(server: str, tmp_path: Path) -> None:
    """It was the one request nothing verified the origin of."""
    ROUTES["/robots.txt"] = (200, {}, b"User-agent: *\nDisallow:\n")
    ROUTES["/a"] = (200, {}, b"ok")
    cache = fetchcache.connect(tmp_path / "c.db")
    # robots is refused rather than trusted, so it records no permissions; the
    # page fetch then fails its own peer check.
    with (
        Fetcher(cache, guard=lambda u: None, addr_guard=lambda a: False, interval=0.0) as f,
        pytest.raises(FetchError),
    ):
        f.fetch(f"{server}/a")
    assert fetchcache.get_robots(cache, "127.0.0.1") == "", "a refused robots must not be trusted"
