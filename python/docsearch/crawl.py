"""Visiting a documentation site: the frontier, and what bounds it.

:mod:`docsearch.discover` says which pages exist and :mod:`docsearch.nav` says
how they nest. This is what actually visits them, and it is a separate module
because the bounds are the hard part -- a seed that addresses far more than a
manual, a site that answers 200 for everything, a crawl killed halfway through
-- and none of those are questions about discovery or hierarchy.

The frontier is the coverage set, scoped. The path prefix is a containment
check applied to discovered links, never the source of them: the Resolume seed
sits at ``/support/avenue-arena`` while every document is at ``/support/en/*``,
so deriving the frontier from the seed's prefix yields nothing at all.

Link-following is the only part of this that generates URLs rather than
filtering them. It runs only when no site-wide manifest answered -- no sitemap
and no ``llms.txt`` -- and then under an explicit depth and page budget. Links
are harvested from pages as they are fetched rather than in a pass of their
own, so no page is requested twice and the known-page count rises as the crawl
proceeds, which is what the progress figure reports.
"""

from __future__ import annotations

from collections import deque
from collections.abc import Callable
from dataclasses import dataclass, field
from urllib.parse import urljoin, urlsplit

from bs4 import BeautifulSoup

from .discover import (
    Coverage,
    discover,
    in_prefix_scope,
    index_page_candidates,
    looks_absent,
    not_found_signature,
)
from .errors import IngestCancelled
from .fetch import Fetched, Fetcher, FetchError, normalize
from .nav import Hierarchy, derive

__all__ = ["CrawlResult", "Page", "crawl"]

#: Pages one crawl will fetch. A backstop against a seed that turns out to
#: address a whole site rather than one manual; the fetcher's own max_fetches
#: sits below this as a floor under a bug.
DEFAULT_MAX_PAGES = 500

#: How far link-following will walk from the seed. Only reached when no
#: site-wide manifest answered, at which point there is no declared structure
#: to trust and depth is the only bound left.
DEFAULT_LINK_DEPTH = 3

#: Content types worth extracting. A documentation site that serves a PDF at a
#: URL is a file ingest wearing a URL, not a page of the site, and the adapters
#: pick by suffix on a local path rather than by content type on a response.
_HTML_TYPES = ("text/html", "application/xhtml+xml")

#: ``(phase, current, total)``; phase is 'discover' | 'fetch'.
ProgressFn = Callable[[str, int, int], None]
CancelFn = Callable[[], bool]


@dataclass(slots=True)
class Page:
    """One fetched page of the site."""

    url: str
    final_url: str
    body: bytes
    #: Title a coverage source supplied, where one did. The page's own ``<h1>``
    #: is usually a better title, but that belongs to extraction.
    declared_title: str = ""


@dataclass(slots=True)
class CrawlResult:
    """What a crawl visited, and everything it could not."""

    seed: str
    coverage: Coverage
    hierarchy: Hierarchy
    #: Normalized URL -> page, in the order the frontier visited them.
    pages: dict[str, Page] = field(default_factory=dict)
    #: ``(url, reason)`` for every declared page that produced no content.
    unreachable: list[tuple[str, str]] = field(default_factory=list)
    #: ``(duplicate, canonical)`` for URLs collapsed by a canonical link.
    canonical_merges: list[tuple[str, str]] = field(default_factory=list)
    notes: list[str] = field(default_factory=list)

    @property
    def declared(self) -> int:
        """Pages the crawl knew about: fetched, plus those it could not reach."""
        return len(self.pages) + len(self.unreachable)

    @property
    def unreachable_share(self) -> float:
        """Share of known pages that produced nothing.

        The completeness gate reads this. A broken link on a large site is
        ordinary; a large share of them means the site was not navigable and
        the index would be a silent partial.
        """
        if not self.declared:
            return 0.0
        return len(self.unreachable) / self.declared


def _is_html(content_type: str | None) -> bool:
    if not content_type:
        # A server that declares nothing gets the benefit of the doubt:
        # refusing an unlabelled response would drop pages from generators
        # that serve static files without a type.
        return True
    head = content_type.split(";", 1)[0].strip().lower()
    return head in _HTML_TYPES


def _canonical_of(body: bytes, base: str) -> str | None:
    """The URL a page declares as its own, if it declares one.

    Versioned documentation produces duplicates whenever a seed spans
    ``latest/`` and ``v2.1/``, and a canonical link is the publisher saying
    which spelling is the page.
    """
    soup = BeautifulSoup(body, "lxml")
    for link in soup.find_all("link", href=True):
        rel = link.get("rel") or []
        if isinstance(rel, str):
            rel = [rel]
        if "canonical" not in [str(r).lower() for r in rel]:
            continue
        href = str(link["href"]).strip()
        if href:
            return normalize(urljoin(base, href))
    return None


def crawl(
    fetcher: Fetcher,
    seed: str,
    *,
    progress: ProgressFn | None = None,
    should_cancel: CancelFn | None = None,
    max_pages: int = DEFAULT_MAX_PAGES,
    link_depth: int = DEFAULT_LINK_DEPTH,
    revalidate: bool = True,
) -> CrawlResult:
    """Fetch every page ``seed`` covers, and report what could not be reached.

    ``revalidate=False`` serves the whole crawl from the fetch cache without a
    single request, which is what re-chunking an already-crawled site wants.
    """
    seed = normalize(seed)
    result = CrawlResult(seed=seed, coverage=Coverage(), hierarchy=Hierarchy())

    def tick(phase: str, cur: int, tot: int) -> None:
        if progress:
            progress(phase, cur, tot)

    def checkpoint() -> None:
        if should_cancel and should_cancel():
            raise IngestCancelled()

    tick("discover", 0, 0)
    checkpoint()

    try:
        seed_page: Fetched | None = fetcher.fetch(seed, revalidate=revalidate)
    except FetchError as exc:
        result.notes.append(f"seed {seed}: {exc}")
        seed_page = None
    if seed_page is not None and seed_page.status != 200:
        result.notes.append(f"seed {seed}: HTTP {seed_page.status}")
        seed_page = None

    result.coverage = discover(fetcher, seed, seed_page=seed_page, revalidate=revalidate)
    checkpoint()

    # A manifest describes the whole site, so its absence is what licenses
    # walking links. With one present, the declared set is the frontier.
    follow_links = not result.coverage.from_sitemap and not result.coverage.from_llms_txt
    if follow_links:
        result.notes.append(
            f"no sitemap or llms.txt; following links from fetched pages to depth {link_depth}"
        )

    frontier = list(result.coverage.urls)
    if len(frontier) > max_pages:
        for dropped in frontier[max_pages:]:
            result.unreachable.append((dropped, f"page budget of {max_pages} exhausted"))
        result.notes.append(
            f"page budget of {max_pages} reached; {len(frontier) - max_pages} declared "
            f"page(s) were not fetched and are reported as unreachable"
        )
        frontier = frontier[:max_pages]

    # Computed once, before the frontier is walked: a site that answers 200 for
    # everything would otherwise feed its error page to the index, and the
    # completeness gate would score that a success.
    origin = f"{urlsplit(seed).scheme}://{urlsplit(seed).netloc}"
    signature = not_found_signature(fetcher, origin, revalidate=revalidate)
    if signature is not None:
        result.notes.append(
            "this site answers 200 for pages that do not exist; responses matching its "
            "not-found template are treated as unreachable"
        )

    queue: deque[tuple[str, int]] = deque((u, 0) for u in frontier)
    known: set[str] = set(frontier)
    done = 0

    while queue:
        checkpoint()
        url, depth = queue.popleft()
        # Total rises as link-following discovers more, which is the honest
        # figure: nothing knows the page count up front on a walked site.
        tick("fetch", done, len(known))
        done += 1

        if url in result.pages:
            continue
        try:
            res = fetcher.fetch(url, revalidate=revalidate)
        except FetchError as exc:
            result.unreachable.append((url, str(exc)))
            continue
        if res.status != 200:
            result.unreachable.append((url, f"HTTP {res.status}"))
            continue
        if not _is_html(res.content_type):
            result.unreachable.append((url, f"not HTML: {res.content_type}"))
            continue
        if looks_absent(res.body, signature):
            result.unreachable.append((url, "soft 404: matches the site's not-found template"))
            continue

        key = url
        canonical = _canonical_of(res.body, url)
        if canonical is not None and canonical != url and in_prefix_scope(canonical, seed):
            # The publisher says this is one page under two spellings. Keep the
            # one it named, so a versioned site is not indexed once per version.
            result.canonical_merges.append((url, canonical))
            if canonical in result.pages:
                continue
            key = canonical
            known.add(canonical)

        result.pages[key] = Page(
            url=key,
            final_url=res.final_url,
            body=res.body,
            declared_title=result.coverage.titles.get(url, ""),
        )

        if follow_links and depth < link_depth:
            for link in index_page_candidates(res.body, url):
                if len(known) >= max_pages:
                    break
                # Scope applies to discovered links, never to declared ones.
                if link in known or not in_prefix_scope(link, seed):
                    continue
                known.add(link)
                queue.append((link, depth + 1))

    tick("fetch", done, len(known))

    if result.canonical_merges:
        result.notes.append(
            f"{len(result.canonical_merges)} page(s) collapsed into the URL they declare "
            f"as canonical"
        )
    if result.unreachable:
        result.notes.append(
            f"{len(result.unreachable)} of {result.declared} known page(s) could not be "
            f"fetched ({result.unreachable_share:.0%})"
        )

    # Derived against what was actually fetched rather than what was declared:
    # a hierarchy source that places pages the crawl never got is not placing
    # anything a caller can reach.
    result.hierarchy = derive(
        list(result.pages),
        seed,
        seed_html=seed_page.body if seed_page is not None else None,
    )
    result.notes.extend(result.hierarchy.notes)
    return result
