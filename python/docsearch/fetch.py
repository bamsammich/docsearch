"""Fetching, with the manners and the guards a crawler owes a stranger's site.

Everything here answers to the cache: a response that arrives is stored, and a
response that has not changed is served from storage without transferring a
body. Nothing else in the ingest path talks to the network.
"""

from __future__ import annotations

import hashlib
import sqlite3
import time
import urllib.robotparser
from collections.abc import Callable
from dataclasses import dataclass
from datetime import UTC, datetime
from urllib.parse import urljoin, urlsplit, urlunsplit

import httpx

from . import fetchcache, urlguard
from .urlguard import BlockedURLError

__all__ = ["FetchError", "Fetched", "Fetcher", "normalize"]

#: Identifies the crawler and where to complain about it.
USER_AGENT = "docsearch/0.1 (+https://github.com/bamsammich/docsearch)"

#: Seconds between requests to one host. A documentation site is someone
#: else's server, and a crawl of a few hundred pages has no reason to hurry.
DEFAULT_INTERVAL = 0.5

#: A redirect chain longer than this is a loop or a trap, not navigation.
MAX_REDIRECTS = 5

#: Backstop against a seed that turns out to address far more than a manual.
#: The crawler applies the real budget; this is the floor under a bug.
MAX_FETCHES = 5000

DEFAULT_TIMEOUT = 20.0

#: Status codes that carry a Location rather than a body.
_REDIRECTS = frozenset({301, 302, 303, 307, 308})


class FetchError(Exception):
    """A URL could not be fetched. Carries a reason for the operator."""


@dataclass(slots=True, frozen=True)
class Fetched:
    url: str
    final_url: str
    status: int
    body: bytes
    content_type: str | None
    from_cache: bool


def _remove_dot_segments(path: str) -> str:
    """RFC 3986 5.2.4.

    Written out rather than delegated to ``posixpath.normpath``, which strips a
    trailing slash. That slash is not decoration: ``/docs/`` and ``/docs`` are
    different resources, and on at least one real generator one of them is the
    page and the other is a 404.
    """
    out: list[str] = []
    for seg in path.split("/"):
        if seg == ".":
            continue
        if seg == "..":
            if len(out) > 1:
                out.pop()
            continue
        out.append(seg)
    joined = "/".join(out)
    # split() drops no information, so a path that ended in "." or ".." must
    # regain the slash those segments stood in for.
    if path.endswith(("/.", "/..")) and not joined.endswith("/"):
        joined += "/"
    return joined


def normalize(raw: str) -> str:
    """One spelling per resource, so the frontier and the cache agree.

    Lowercases scheme and host, drops the default port and the fragment, and
    resolves dot segments. Deliberately leaves the trailing slash, the query
    and its ordering alone: all three can change which resource is addressed.
    """
    parts = urlsplit(raw)
    scheme = parts.scheme.lower()
    try:
        host = (parts.hostname or "").lower()
        port = parts.port
    except ValueError as exc:
        raise FetchError(f"unparseable URL: {raw}") from exc
    if (scheme == "http" and port == 80) or (scheme == "https" and port == 443):
        port = None

    netloc = f"[{host}]" if ":" in host else host
    if port is not None:
        netloc = f"{netloc}:{port}"

    path = _remove_dot_segments(parts.path) or "/"
    return urlunsplit((scheme, netloc, path, parts.query, ""))


def _host_of(url: str) -> str:
    return (urlsplit(url).hostname or "").lower()


def _origin_of(url: str) -> str:
    parts = urlsplit(url)
    return f"{parts.scheme}://{parts.netloc}"


class Fetcher:
    """Fetches URLs through the cache, the guard, robots and a rate limit.

    ``guard`` is injectable so tests can exercise the transport against a local
    server; the default refuses loopback, which is exactly what a test server
    is. That the default blocks it is covered by the guard's own tests.
    """

    def __init__(
        self,
        cache: sqlite3.Connection,
        *,
        client: httpx.Client | None = None,
        guard: Callable[[str], object] = urlguard.check,
        addr_guard: Callable[[str], bool] = urlguard.addr_allowed,
        user_agent: str = USER_AGENT,
        interval: float = DEFAULT_INTERVAL,
        max_redirects: int = MAX_REDIRECTS,
        max_fetches: int = MAX_FETCHES,
        obey_robots: bool = True,
    ) -> None:
        self.cache = cache
        self.guard = guard
        self.addr_guard = addr_guard
        self.user_agent = user_agent
        self.interval = interval
        self.max_redirects = max_redirects
        self.max_fetches = max_fetches
        self.obey_robots = obey_robots
        self._client = client or httpx.Client(
            follow_redirects=False,
            timeout=DEFAULT_TIMEOUT,
            headers={"User-Agent": user_agent},
        )
        self._owns_client = client is None
        self._last_request: dict[str, float] = {}
        self._robots: dict[str, urllib.robotparser.RobotFileParser] = {}
        self._fetches = 0

    def close(self) -> None:
        if self._owns_client:
            self._client.close()

    def __enter__(self) -> Fetcher:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    # -- politeness --------------------------------------------------------
    def _wait(self, host: str) -> None:
        last = self._last_request.get(host)
        if last is not None:
            remaining = self.interval - (time.monotonic() - last)
            if remaining > 0:
                time.sleep(remaining)
        self._last_request[host] = time.monotonic()

    def _robots_for(self, url: str) -> urllib.robotparser.RobotFileParser:
        host = _host_of(url)
        cached = self._robots.get(host)
        if cached is not None:
            return cached

        parser = urllib.robotparser.RobotFileParser()
        body = fetchcache.get_robots(self.cache, host)
        if body is None:
            body = ""
            try:
                # Not routed through fetch(): asking robots.txt whether
                # robots.txt may be fetched does not terminate. It is still
                # streamed and peer-checked, because this was the one request
                # the fetcher made that nothing verified had arrived from the
                # address the guard approved.
                robots_url = urljoin(_origin_of(url), "/robots.txt")
                self.guard(robots_url)
                self._wait(host)
                with self._client.stream("GET", robots_url) as res:
                    self._check_peer(res)
                    if res.status_code == 200:
                        body = res.read().decode("utf-8", errors="replace")
            except (BlockedURLError, FetchError, httpx.HTTPError):
                # A host that will not serve robots.txt has not disallowed
                # anything. Absent is permission, per the standard.
                body = ""
            fetchcache.put_robots(self.cache, host, body)
        parser.parse(body.splitlines())
        self._robots[host] = parser
        return parser

    def allowed_by_robots(self, url: str) -> bool:
        if not self.obey_robots:
            return True
        return self._robots_for(url).can_fetch(self.user_agent, url)

    def _check_peer(self, res: httpx.Response) -> None:
        """Refuse a response that arrived from an address the guard rejects.

        The name was validated before the request, but a resolver is free to
        answer differently the second time. Checked here, while the connection
        is open and before any body is read, so a rebound name cannot get its
        content into the index.

        A backstop rather than the boundary: the request has already gone out.
        Pinning the connection to the address that was validated would close
        the window entirely, and needs the transport to accept a resolved
        address while still presenting the original name for TLS.

        Fails closed when the peer cannot be determined. Returning quietly
        meant a transport that exposes no address disabled the check silently,
        which is the shape of gap that survives review: nothing errors, the
        body is read, and the only evidence is an absence. Every transport this
        fetcher uses does expose one, so refusing costs nothing and stops a
        future one from turning the check off by accident.
        """
        stream = res.extensions.get("network_stream")
        if stream is None:
            raise FetchError(
                "the transport exposed no connection to inspect, so the peer address "
                "could not be confirmed against the guard"
            )
        info = stream.get_extra_info("server_addr")
        if not info:
            raise FetchError(
                "the connection reported no peer address, so it could not be confirmed "
                "against the guard"
            )
        host = info[0] if isinstance(info, tuple) else str(info)
        if not self.addr_guard(str(host)):
            raise FetchError("response arrived from an address that is not permitted")

    # -- fetching ----------------------------------------------------------
    def fetch(self, raw: str, *, revalidate: bool = True) -> Fetched:
        """Fetch one URL, serving an unchanged body from the cache.

        ``revalidate=False`` returns any stored copy without asking the server,
        which is what re-chunking an already-crawled site wants.
        """
        url = normalize(raw)
        cached = fetchcache.get(self.cache, url)
        if cached is not None and not revalidate:
            return _from_cache(cached)

        if not self.allowed_by_robots(url):
            raise FetchError(f"robots.txt disallows {url}")
        if self._fetches >= self.max_fetches:
            raise FetchError(f"fetch budget of {self.max_fetches} exhausted")

        headers: dict[str, str] = {}
        if cached is not None:
            # Conditional: a site that answers 304 costs a round trip and no
            # body, which is what makes refreshing a large site cheap.
            if cached.etag:
                headers["If-None-Match"] = cached.etag
            if cached.last_modified:
                headers["If-Modified-Since"] = cached.last_modified

        current = url
        for _ in range(self.max_redirects + 1):
            self.guard(current)  # every hop, not just the seed
            self._wait(_host_of(current))
            self._fetches += 1
            try:
                # Streamed so the peer can be checked while the connection is
                # still open, and before a byte of body is read. Once the
                # response is buffered the socket is back in the pool and its
                # address is no longer available to ask.
                with self._client.stream("GET", current, headers=headers) as res:
                    self._check_peer(res)
                    status = res.status_code
                    location = res.headers.get("location")
                    content_type = res.headers.get("content-type")
                    etag = res.headers.get("etag")
                    last_modified = res.headers.get("last-modified")
                    body = b"" if status in _REDIRECTS or status == 304 else res.read()
            except httpx.HTTPError as exc:
                raise FetchError(f"{current}: {type(exc).__name__}") from exc

            if status in _REDIRECTS:
                if not location:
                    raise FetchError(f"{current}: redirect without a location")
                current = normalize(urljoin(current, location))
                continue

            if status == 304 and cached is not None:
                fetchcache.touch(self.cache, url)
                return _from_cache(cached)

            entry = fetchcache.CachedResponse(
                url=url,
                final_url=current,
                status=status,
                content_type=content_type,
                etag=etag,
                last_modified=last_modified,
                body=body,
                sha256=hashlib.sha256(body).hexdigest(),
                fetched_at=datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ"),
            )
            fetchcache.put(self.cache, entry)
            return Fetched(
                url=url,
                final_url=current,
                status=status,
                body=body,
                content_type=content_type,
                from_cache=False,
            )

        raise FetchError(f"{url}: more than {self.max_redirects} redirects")


def _from_cache(entry: fetchcache.CachedResponse) -> Fetched:
    return Fetched(
        url=entry.url,
        final_url=entry.final_url,
        status=entry.status,
        body=entry.body,
        content_type=entry.content_type,
        from_cache=True,
    )
