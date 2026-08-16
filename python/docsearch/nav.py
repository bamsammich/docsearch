"""How a documentation site's pages nest.

Hierarchy only. Which pages exist is a separate question with separate sources,
answered in :mod:`docsearch.discover`. Keeping them apart is what the probed
targets forced: a rendered sidebar is not a page set. One target has none at
all, and the other renders 19 links against 210 pages because its generator
collapses categories client-side.

So a hierarchy source is *checked against coverage before it is believed*. A
sidebar that places a tenth of the site is not the site's structure, whatever
it looks like, and the pages no source mentions are placed by their URL path
and reported rather than dropped -- dropping them discards 41 of one target's
210 pages, including every command reference.

The output is what the chunker needs: for each page, a dotted section number
and the heading ancestry above it.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from urllib.parse import urlsplit

from bs4 import BeautifulSoup
from bs4.element import Tag

from .fetch import normalize

__all__ = ["Hierarchy", "Placement", "derive", "index_page_tree", "sidebar_tree"]

#: Share of the page set a sidebar must place before it is treated as the
#: site's structure. Below this it is a fragment of a navigation, not a map of
#: one -- the case a collapsed generator sidebar produces.
MIN_COVERAGE = 0.5

#: Elements that plausibly hold a documentation navigation.
_NAV_SELECTOR = (
    "nav, aside, [class*=sidebar], [class*=Sidebar], [class*=toc], [class*=menu], [id*=sidebar]"
)

_HEADINGS = ("h1", "h2", "h3", "h4", "h5", "h6")


@dataclass(frozen=True, slots=True)
class Placement:
    """Where one page sits in the tree."""

    url: str
    #: Dotted position, e.g. "3.2". Becomes the chunk's authoritative section.
    section: str
    #: Heading ancestry above the page, root first.
    ancestry: tuple[str, ...]
    title: str


@dataclass(slots=True)
class Hierarchy:
    placements: list[Placement] = field(default_factory=list)
    #: 'sidebar_dom' | 'index_page' | 'url_path'
    source: str = "url_path"
    #: True when the structure was inferred rather than declared, so nothing
    #: exists to check it against.
    inferred: bool = True
    #: Pages no declared source mentioned, placed by URL path instead.
    placed_by_path: list[str] = field(default_factory=list)
    notes: list[str] = field(default_factory=list)

    def by_url(self) -> dict[str, Placement]:
        return {p.url: p for p in self.placements}


@dataclass(slots=True)
class _Node:
    title: str
    url: str | None = None
    children: list[_Node] = field(default_factory=list)


def _host(url: str) -> str:
    return (urlsplit(url).hostname or "").lower()


def _abs(base: str, href: str) -> str:
    from urllib.parse import urljoin

    return normalize(urljoin(base, href))


def _usable_href(tag: Tag) -> str | None:
    href = str(tag.get("href", "")).strip()
    if not href or href.startswith(("#", "mailto:", "javascript:", "tel:")):
        return None
    return href


# -- source: a rendered sidebar -------------------------------------------


def sidebar_tree(html: bytes, base: str) -> list[_Node]:
    """The richest navigation the page renders, as a tree.

    Chooses the candidate container holding the most same-host links rather
    than the first that matches, because a marketing header is a ``<nav>`` too.
    Whether the result is worth believing is decided by coverage, not here.
    """
    soup = BeautifulSoup(html, "lxml")
    for tag in soup(["script", "style"]):
        tag.decompose()

    best: Tag | None = None
    best_links = 0
    for cand in (soup.body or soup).select(_NAV_SELECTOR):
        links = [
            a
            for a in cand.find_all("a", href=True)
            if _usable_href(a) and _host(_abs(base, str(a["href"]))) == _host(base)
        ]
        if len(links) > best_links:
            best, best_links = cand, len(links)
    if best is None or best_links == 0:
        return []
    return _tree_from_lists(best, base)


def _own_anchor(li: Tag) -> Tag | None:
    """The link belonging to this item, not to one of its children.

    ``find`` searches descendants, so a category item wrapping a sublist would
    otherwise adopt its first child's link -- taking that child's title and URL
    for itself and then listing the child again beneath it.
    """
    for a in li.find_all("a", href=True):
        holder = a.find_parent(["ul", "ol"])
        if holder is not None and holder is not li.parent:
            continue
        return a if isinstance(a, Tag) else None
    return None


def _tree_from_lists(container: Tag, base: str) -> list[_Node]:
    """Build a tree from nested ``<ul>``/``<li>``, or a flat list if unnested."""

    def walk(scope: Tag, *, top: bool = False) -> list[_Node]:
        out: list[_Node] = []
        items: list[Tag] = []
        for lst in scope.find_all(["ul", "ol"], recursive=False):
            items.extend(lst.find_all("li", recursive=False))
        if not items:
            if not top:
                # A leaf item has no children. Falling through to the flat
                # branch would find the item's own link again and hang a copy
                # of the page beneath itself.
                return out
            # A navigation with no list markup at all: take the links in
            # document order, flat.
            for a in scope.find_all("a", href=True):
                href = _usable_href(a)
                if href is None:
                    continue
                out.append(_Node(title=a.get_text(" ", strip=True), url=_abs(base, href)))
            return out

        for li in items:
            anchor = _own_anchor(li)
            href = _usable_href(anchor) if anchor is not None else None
            if anchor is not None and href is not None:
                title = anchor.get_text(" ", strip=True)
                node = _Node(title=title, url=_abs(base, href))
            else:
                # A category label with no link of its own still names a level.
                label = li.find(["span", "button", "div", *_HEADINGS])
                title = label.get_text(" ", strip=True) if label else li.get_text(" ", strip=True)
                node = _Node(title=title.split("\n")[0].strip())
            node.children = walk(li)
            out.append(node)
        return out

    return walk(container, top=True)


# -- source: a hub page's headings ----------------------------------------


def index_page_tree(html: bytes, base: str) -> list[_Node]:
    """Group a hub page's links under the headings above them.

    A heading *inside* a link titles that link; a heading outside one opens a
    section. Both shapes appear on the same real page -- cards are written as
    ``<a><h3>Title</h3></a>`` under an ``<h1>`` section header -- and treating
    every heading as a section opener assigns each link the title of the one
    before it, mislabelling the whole page in a way only a slug comparison
    reveals.
    """
    soup = BeautifulSoup(html, "lxml")
    for tag in soup(["script", "style", "nav", "footer"]):
        tag.decompose()

    roots: list[_Node] = []
    stack: list[tuple[int, _Node]] = []
    seen: set[str] = set()

    for el in (soup.body or soup).find_all([*_HEADINGS, "a"]):
        if el.name == "a":
            href = _usable_href(el)
            if href is None:
                continue
            url = _abs(base, href)
            if _host(url) != _host(base) or url in seen:
                continue
            seen.add(url)
            inner = el.find(_HEADINGS)
            title = (inner or el).get_text(" ", strip=True)
            node = _Node(title=title, url=url)
            (stack[-1][1].children if stack else roots).append(node)
            continue

        if el.find_parent("a") is not None:
            # Titles a link; already consumed above.
            continue
        level = int(el.name[1])
        while stack and stack[-1][0] >= level:
            stack.pop()
        node = _Node(title=el.get_text(" ", strip=True))
        (stack[-1][1].children if stack else roots).append(node)
        stack.append((level, node))

    return roots


# -- source: the URL path --------------------------------------------------


def url_path_tree(urls: list[str], seed: str) -> list[_Node]:
    """Nest pages by their path segments below the seed.

    Inferred, with nothing to check it against, which is why it warns. It is
    still far better than a flat list: it keeps a command reference together
    instead of scattering it.
    """
    base_depth = len([s for s in urlsplit(seed).path.split("/") if s])
    roots: list[_Node] = []
    groups: dict[tuple[str, ...], _Node] = {}

    for url in urls:
        segments = [s for s in urlsplit(url).path.split("/") if s][base_depth:]
        if not segments:
            roots.append(_Node(title=_title_from_slug(urlsplit(url).path), url=url))
            continue
        parent_children = roots
        prefix: tuple[str, ...] = ()
        for seg in segments[:-1]:
            prefix += (seg,)
            group = groups.get(prefix)
            if group is None:
                group = _Node(title=_title_from_slug(seg))
                groups[prefix] = group
                parent_children.append(group)
            parent_children = group.children
        parent_children.append(_Node(title=_title_from_slug(segments[-1]), url=url))
    return roots


def _title_from_slug(slug: str) -> str:
    text = slug.strip("/").split("/")[-1]
    for suffix in (".html", ".htm", ".md"):
        text = text.removesuffix(suffix)
    return text.replace("-", " ").replace("_", " ").strip().title() or "Untitled"


# -- selection and numbering ----------------------------------------------


def _flatten(nodes: list[_Node], prefix: str = "") -> list[Placement]:
    out: list[Placement] = []

    def walk(items: list[_Node], path: str, ancestry: tuple[str, ...]) -> None:
        for i, node in enumerate(items, start=1):
            section = f"{path}.{i}" if path else str(i)
            if node.url is not None:
                out.append(
                    Placement(
                        url=node.url,
                        section=section,
                        ancestry=ancestry,
                        title=node.title or _title_from_slug(node.url),
                    )
                )
            walk(node.children, section, ancestry + ((node.title,) if node.title else ()))

    walk(nodes, prefix, ())
    # A navigation may list one page in more than one place. The first
    # position wins: a page with two section numbers is two documents to
    # anything that filters by section.
    seen: set[str] = set()
    unique: list[Placement] = []
    for p in out:
        if p.url in seen:
            continue
        seen.add(p.url)
        unique.append(p)
    return unique


def _covered(placements: list[Placement], coverage: list[str]) -> float:
    if not coverage:
        return 0.0
    placed = {p.url for p in placements}
    return len(placed & set(coverage)) / len(coverage)


def derive(
    coverage: list[str],
    seed: str,
    *,
    seed_html: bytes | None = None,
) -> Hierarchy:
    """Choose a hierarchy source, place every page, and say what happened."""
    hier = Hierarchy()
    if not coverage:
        return hier

    candidates: list[tuple[str, list[Placement]]] = []
    if seed_html is not None:
        for name, nodes in (
            ("sidebar_dom", sidebar_tree(seed_html, seed)),
            ("index_page", index_page_tree(seed_html, seed)),
        ):
            if nodes:
                candidates.append((name, _flatten(nodes)))

    best_name, best_placements, best_share = "url_path", [], 0.0
    for name, placements in candidates:
        share = _covered(placements, coverage)
        hier.notes.append(f"{name} places {share:.0%} of the page set")
        if share > best_share:
            best_name, best_placements, best_share = name, placements, share

    if best_share < MIN_COVERAGE:
        # A fragment of a navigation is not a map of one. This is the case a
        # generator that collapses its categories produces, and believing it
        # would index a tenth of the site under a confident-looking tree.
        if best_share:
            hier.notes.append(
                f"{best_name} places only {best_share:.0%} of the page set; "
                f"falling back to URL path depth"
            )
        hier.source = "url_path"
        hier.inferred = True
        hier.placements = _flatten(url_path_tree(coverage, seed))
        hier.placed_by_path = list(coverage)
        return hier

    wanted = set(coverage)
    kept = [p for p in best_placements if p.url in wanted]
    missing = [u for u in coverage if u not in {p.url for p in kept}]

    if missing:
        # Placed, not dropped. Excluding them discards 41 of one probed
        # target's 210 pages, including every command reference -- the silent
        # partial index the completeness gate exists to prevent.
        extra = _flatten(url_path_tree(missing, seed), prefix=str(len(kept) + 1))
        kept.extend(extra)
        hier.placed_by_path = missing
        hier.notes.append(f"{len(missing)} page(s) absent from {best_name}, placed by URL path")

    hier.source = best_name
    hier.inferred = False
    hier.placements = kept
    return hier
