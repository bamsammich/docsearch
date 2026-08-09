"""Which pages a documentation site consists of.

Coverage only. How those pages nest is a different question with different
sources, answered elsewhere -- conflating them is what made an earlier design
derive the page set from a rendered sidebar, which on one probed target does
not exist and on another lists 19 of 210 pages.

Sources contribute a union rather than competing, because each is incomplete
in its own way and the disagreements are worth reporting rather than resolving
silently:

``sitemap.xml``
    The most complete when present, and the authority on canonical spelling.
    It describes the whole site, so it is filtered to the seed's path prefix --
    without that, one probed target contributes 385 blog posts to a
    documentation crawl.

the seed page's own links
    What the publisher put on the page as its index. Scoped by *declaration*
    rather than by prefix: a hub page at ``/support/avenue-arena`` legitimately
    lists documents at ``/support/en/*``, and a prefix filter derived from the
    seed would discard every one of them.

``llms.txt``
    Ordered and titled, and **not authoritative for coverage** -- on one probed
    target it holds 169 of 210 documentation pages, omitting an entire command
    reference. It contributes to the union and never bounds it.

Link-following is the last resort, and belongs to the crawler under a budget.
"""

from __future__ import annotations

import re
from collections import Counter
from dataclasses import dataclass, field
from urllib.parse import urljoin, urlsplit
from xml.etree import ElementTree

from bs4 import BeautifulSoup

from .fetch import Fetched, Fetcher, FetchError, normalize

__all__ = ["Coverage", "discover", "in_prefix_scope", "not_found_signature", "resembles"]

#: Nested sitemap indexes deeper than this are a loop or a mistake.
MAX_SITEMAP_DEPTH = 3

#: How much of the not-found template a page must contain to be treated as
#: absent. Used only against a site's own error page, never to compare
#: documents to each other.
RESEMBLANCE = 0.9

#: How alike two probe responses must be to count as the same template.
#:
#: Looser than RESEMBLANCE on purpose. The probes differ by exactly the request
#: echoed back, which is a large fraction of a short error page and a small one
#: of a long page -- so the same threshold would recognise verbose templates
#: and miss terse ones. Whatever they share is intersected out afterwards, so a
#: loose gate here costs nothing.
PROBE_AGREEMENT = 0.7

#: A template shorter than this cannot be told apart from a short page that
#: happens to share a few ordinary words.
MIN_SIGNATURE_TOKENS = 8

_WORD = re.compile(r"[^\W_]+", re.UNICODE)


@dataclass(slots=True)
class Coverage:
    """The page set, and where each part of it came from."""

    urls: list[str] = field(default_factory=list)
    from_sitemap: set[str] = field(default_factory=set)
    from_index_page: list[str] = field(default_factory=list)
    from_llms_txt: list[str] = field(default_factory=list)
    #: Titles keyed by URL, where a source supplied one.
    titles: dict[str, str] = field(default_factory=dict)
    notes: list[str] = field(default_factory=list)

    @property
    def sources(self) -> list[str]:
        out = []
        if self.from_sitemap:
            out.append("sitemap")
        if self.from_index_page:
            out.append("index_page")
        if self.from_llms_txt:
            out.append("llms_txt")
        return out


def _origin(url: str) -> str:
    p = urlsplit(url)
    return f"{p.scheme}://{p.netloc}"


def _host(url: str) -> str:
    return (urlsplit(url).hostname or "").lower()


def in_prefix_scope(url: str, seed: str) -> bool:
    """Whether ``url`` sits under the seed's path, on the seed's host.

    Applied to sources that describe a whole site. Not applied to links the
    seed itself declares: those are in scope because the publisher put them
    there, and requiring them to share the seed's prefix discards the ordinary
    case of an index page that lives beside what it indexes.
    """
    if _host(url) != _host(seed):
        return False
    base = urlsplit(seed).path
    path = urlsplit(url).path
    if not base.endswith("/"):
        base += "/"
    return path == urlsplit(seed).path or path.startswith(base)


def tokens(text: str) -> Counter[str]:
    return Counter(m.group(0).lower() for m in _WORD.finditer(text))


def resembles(a: Counter[str], b: Counter[str]) -> float:
    """Symmetric token overlap, 0..1. Used to compare two probes to each other."""
    if not a or not b:
        return 0.0
    shared = sum((a & b).values())
    return shared / max(sum(a.values()), sum(b.values()))


def covers(signature: Counter[str], page: Counter[str]) -> float:
    """How much of ``signature`` appears in ``page``, 0..1.

    Containment rather than similarity, because a not-found page is the
    template *plus* whatever it echoes back about the request. Comparing the
    two symmetrically scores a longer page lower for saying more, which is
    backwards -- the question is whether the template is present, not whether
    the two are the same length.
    """
    if not signature or not page:
        return 0.0
    return sum((signature & page).values()) / sum(signature.values())


def page_text(body: bytes) -> str:
    soup = BeautifulSoup(body, "lxml")
    for tag in soup(["script", "style"]):
        tag.decompose()
    return (soup.body or soup).get_text(" ", strip=True)


def not_found_signature(fetcher: Fetcher, origin: str) -> Counter[str] | None:
    """What this site's not-found page looks like, if it answers 200 for one.

    Two improbable paths are probed rather than one. A site that soft-404s
    returns near-identical bodies for both, and what they share is the
    template; the parts that differ are the requested path echoed back. One
    probe could not tell a template from a real page.

    Returns None for a site that answers an honest status, which is most of
    them and needs no further checking.
    """
    probes = []
    # The two paths share no tokens on purpose. A site that echoes the request
    # back puts those tokens in the body, and anything the probes have in
    # common survives the intersection below -- so a shared word here would be
    # mistaken for part of the template.
    for name in ("zqxvj7-nonexistent", "kwmbp3-missingpage"):
        try:
            res = fetcher.fetch(f"{origin}/{name}")
        except FetchError:
            return None
        if res.status != 200:
            return None
        probes.append(tokens(page_text(res.body)))
    if len(probes) != 2 or resembles(probes[0], probes[1]) < PROBE_AGREEMENT:
        # Two different bodies for two absent pages: this site is serving
        # something real, or something random. Either way there is no template
        # to match against.
        return None
    signature = probes[0] & probes[1]
    if sum(signature.values()) < MIN_SIGNATURE_TOKENS:
        # Too little to tell a template from a short page that happens to share
        # a few common words.
        return None
    return signature


def looks_absent(body: bytes, signature: Counter[str] | None) -> bool:
    """Whether a 200 response is really this site's not-found page.

    A site answering 200 for everything would otherwise feed its error page to
    the index, and the completeness gate would score it a success.
    """
    if signature is None:
        return False
    return covers(signature, tokens(page_text(body))) >= RESEMBLANCE


# -- sources ---------------------------------------------------------------


def _sitemap_locs(body: bytes) -> tuple[list[str], list[str]]:
    """Return (page URLs, nested sitemap URLs) from one sitemap document."""
    try:
        root = ElementTree.fromstring(body)
    except ElementTree.ParseError:
        return [], []
    pages: list[str] = []
    nested: list[str] = []
    # The document element decides what its <loc>s mean: a <sitemapindex>
    # holds sitemaps, a <urlset> holds pages.
    is_index = root.tag.rsplit("}", 1)[-1] == "sitemapindex"
    for el in root.iter():
        loc = (el.text or "").strip()
        if el.tag.rsplit("}", 1)[-1] != "loc" or not loc:
            continue
        (nested if is_index else pages).append(loc)
    return pages, nested


def sitemap_candidates(fetcher: Fetcher, seed: str) -> tuple[list[str], list[str]]:
    """Every URL the site's sitemaps declare, and notes about getting them."""
    origin = _origin(seed)
    notes: list[str] = []
    roots: list[str] = []

    declared = _sitemaps_from_robots(fetcher, origin)
    if declared:
        roots.extend(declared)
        notes.append(f"robots.txt names {len(declared)} sitemap(s)")
    else:
        roots.append(f"{origin}/sitemap.xml")

    seen: set[str] = set()
    pages: list[str] = []
    queue = [(u, 0) for u in roots]
    while queue:
        url, depth = queue.pop(0)
        if url in seen or depth > MAX_SITEMAP_DEPTH:
            continue
        seen.add(url)
        try:
            res = fetcher.fetch(url)
        except FetchError as exc:
            notes.append(f"sitemap {url}: {exc}")
            continue
        if res.status != 200:
            notes.append(f"sitemap {url}: HTTP {res.status}")
            continue
        found, nested = _sitemap_locs(res.body)
        pages.extend(found)
        queue.extend((n, depth + 1) for n in nested)
    return pages, notes


def _sitemaps_from_robots(fetcher: Fetcher, origin: str) -> list[str]:
    from . import fetchcache

    body = fetchcache.get_robots(fetcher.cache, _host(origin))
    if body is None:
        # Populate the cache through the fetcher's own robots handling rather
        # than requesting it a second time.
        fetcher.allowed_by_robots(f"{origin}/")
        body = fetchcache.get_robots(fetcher.cache, _host(origin)) or ""
    out = []
    for line in body.splitlines():
        name, _, value = line.partition(":")
        if name.strip().lower() == "sitemap" and value.strip():
            out.append(value.strip())
    return out


def llms_txt_candidates(fetcher: Fetcher, seed: str) -> tuple[list[tuple[str, str]], list[str]]:
    """``(title, url)`` pairs from ``llms.txt``, in the order it lists them."""
    try:
        res = fetcher.fetch(f"{_origin(seed)}/llms.txt")
    except FetchError as exc:
        return [], [f"llms.txt: {exc}"]
    if res.status != 200:
        return [], []
    text = res.body.decode("utf-8", errors="replace")
    pairs = re.findall(r"^\s*[-*]\s*\[([^\]]*)\]\((https?://[^)\s]+)\)", text, re.M)
    return [(t.strip(), u) for t, u in pairs], []


def index_page_candidates(html: bytes, base: str) -> list[str]:
    """Same-host links the seed page declares, in document order, deduped.

    Duplicates are ordinary: a page that renders one navigation for desktop and
    another for mobile lists everything twice.
    """
    soup = BeautifulSoup(html, "lxml")
    for tag in soup(["script", "style"]):
        tag.decompose()
    out: list[str] = []
    seen: set[str] = set()
    for a in soup.find_all("a", href=True):
        href = a["href"].strip()
        if not href or href.startswith(("#", "mailto:", "javascript:", "tel:")):
            continue
        url = normalize(urljoin(base, href))
        if _host(url) != _host(base) or url in seen:
            continue
        seen.add(url)
        out.append(url)
    return out


# -- the union -------------------------------------------------------------


def discover(fetcher: Fetcher, seed: str, *, seed_page: Fetched | None = None) -> Coverage:
    """Assemble the page set for ``seed`` from every source that answers."""
    seed = normalize(seed)
    cov = Coverage()

    sitemap, notes = sitemap_candidates(fetcher, seed)
    cov.notes.extend(notes)
    scoped = [normalize(u) for u in sitemap if in_prefix_scope(normalize(u), seed)]
    cov.from_sitemap = set(scoped)
    if sitemap:
        cov.notes.append(
            f"sitemap declares {len(sitemap)} URL(s), {len(cov.from_sitemap)} under the seed"
        )

    if seed_page is None:
        try:
            seed_page = fetcher.fetch(seed)
        except FetchError as exc:
            cov.notes.append(f"seed page: {exc}")
            seed_page = None
    if seed_page is not None and seed_page.status == 200:
        # Declared by the seed, so in scope by declaration rather than prefix.
        cov.from_index_page = [u for u in index_page_candidates(seed_page.body, seed)]

    pairs, notes = llms_txt_candidates(fetcher, seed)
    cov.notes.extend(notes)
    for title, url in pairs:
        n = normalize(url)
        if in_prefix_scope(n, seed):
            cov.from_llms_txt.append(n)
            if title:
                cov.titles.setdefault(n, title)

    # Union, in a stable order: the sitemap is the authority on spelling, the
    # index page supplies the publisher's own ordering, llms.txt fills gaps.
    ordered: list[str] = []
    seen: set[str] = set()
    for url in cov.from_index_page + sorted(cov.from_sitemap) + cov.from_llms_txt:
        if url not in seen:
            seen.add(url)
            ordered.append(url)
    cov.urls = ordered

    only_llms = set(cov.from_llms_txt) - cov.from_sitemap
    if cov.from_sitemap and only_llms:
        cov.notes.append(f"{len(only_llms)} llms.txt URL(s) absent from the sitemap")
    missing_from_llms = cov.from_sitemap - set(cov.from_llms_txt)
    if cov.from_llms_txt and missing_from_llms:
        cov.notes.append(
            f"llms.txt omits {len(missing_from_llms)} page(s) the sitemap declares; "
            f"it is not authoritative for coverage"
        )
    return cov
