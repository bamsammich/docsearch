"""Hierarchy derivation, against the two shapes the probed targets have."""

from __future__ import annotations

from docsearch.nav import Hierarchy, derive, index_page_tree, sidebar_tree, url_path_tree

BASE = "https://x.dev/docs"


def _urls(hier: Hierarchy) -> list[str]:
    return [p.url for p in hier.placements]


# -- a hub page: the heading lives inside the anchor -----------------------

HUB = b"""<html><body>
  <nav><a href="/">Home</a><a href="/software">Software</a></nav>
  <h1>FAQ</h1>
  <a href="/support/en/difference"><h3>Difference between Avenue and Arena</h3></a>
  <a href="/support/en/community"><h3>Community</h3></a>
  <h1>Controlling Resolume</h1>
  <a href="/support/en/mcp"><h3>MCP Servers</h3></a>
  <a href="/support/en/params"><h3>Parameters</h3></a>
</body></html>"""


def test_a_heading_inside_a_link_titles_that_link() -> None:
    """Treating every heading as a section opener assigns each link the title
    of the one before it, mislabelling every page on the site."""
    tree = index_page_tree(HUB, "https://x.dev/support/avenue-arena")
    faq = tree[0]
    assert faq.title == "FAQ"
    assert [c.title for c in faq.children] == [
        "Difference between Avenue and Arena",
        "Community",
    ]
    assert faq.children[0].url == "https://x.dev/support/en/difference"


def test_hub_page_sections_become_ancestry_and_numbers() -> None:
    coverage = [
        "https://x.dev/support/en/difference",
        "https://x.dev/support/en/community",
        "https://x.dev/support/en/mcp",
        "https://x.dev/support/en/params",
    ]
    hier = derive(coverage, "https://x.dev/support/avenue-arena", seed_html=HUB)
    assert hier.source == "index_page"
    assert hier.inferred is False

    by_url = hier.by_url()
    first = by_url["https://x.dev/support/en/difference"]
    assert first.ancestry == ("FAQ",)
    assert first.section == "1.1"

    mcp = by_url["https://x.dev/support/en/mcp"]
    assert mcp.ancestry == ("Controlling Resolume",)
    assert mcp.section == "2.1"


def test_the_marketing_nav_does_not_become_the_hierarchy() -> None:
    """The only <nav> on one probed target is a marketing header. It places
    none of the documentation, so coverage rejects it."""
    coverage = ["https://x.dev/support/en/difference", "https://x.dev/support/en/community"]
    hier = derive(coverage, "https://x.dev/support/avenue-arena", seed_html=HUB)
    assert hier.source != "sidebar_dom"


# -- a generator sidebar that renders a fraction of the site ---------------


def _collapsed_sidebar(n_rendered: int) -> bytes:
    items = "".join(f'<li><a href="/docs/p{i}">Page {i}</a></li>' for i in range(n_rendered))
    return f'<html><body><nav class="menu"><ul>{items}</ul></nav></body></html>'.encode()


def test_a_sidebar_placing_most_of_the_site_is_used() -> None:
    coverage = [f"https://x.dev/docs/p{i}" for i in range(10)]
    hier = derive(coverage, BASE, seed_html=_collapsed_sidebar(10))
    assert hier.source == "sidebar_dom"
    assert hier.inferred is False
    assert set(_urls(hier)) == set(coverage)


def test_a_collapsed_sidebar_is_rejected_in_favour_of_url_paths() -> None:
    """One probed target renders 19 links against 210 pages, because its
    generator collapses categories client-side. Believing it would index a
    tenth of the site under a confident-looking tree."""
    coverage = [f"https://x.dev/docs/p{i}" for i in range(100)]
    hier = derive(coverage, BASE, seed_html=_collapsed_sidebar(9))
    assert hier.source == "url_path"
    assert hier.inferred is True
    assert set(_urls(hier)) == set(coverage), "every page is still placed"
    assert any("only 9%" in n for n in hier.notes)


# -- pages no source mentions are placed, never dropped --------------------


def test_pages_absent_from_the_source_are_placed_by_path() -> None:
    """Excluding them discards 41 of one target's 210 pages, including every
    command reference -- the silent partial the completeness gate prevents."""
    rendered = [f"https://x.dev/docs/p{i}" for i in range(8)]
    unmentioned = [
        "https://x.dev/docs/commands/docker",
        "https://x.dev/docs/commands/run",
    ]
    hier = derive(rendered + unmentioned, BASE, seed_html=_collapsed_sidebar(8))

    assert hier.source == "sidebar_dom"
    assert set(_urls(hier)) == set(rendered + unmentioned)
    assert set(hier.placed_by_path) == set(unmentioned)
    assert any("placed by URL path" in n for n in hier.notes)

    # The command reference stays together rather than scattering.
    docker = hier.by_url()["https://x.dev/docs/commands/docker"]
    run = hier.by_url()["https://x.dev/docs/commands/run"]
    assert docker.ancestry == run.ancestry == ("Commands",)


def test_every_page_gets_exactly_one_placement() -> None:
    coverage = [f"https://x.dev/docs/p{i}" for i in range(20)]
    hier = derive(coverage, BASE, seed_html=_collapsed_sidebar(5))
    sections = [p.section for p in hier.placements]
    assert len(sections) == len(coverage)
    assert len(set(sections)) == len(sections), "section numbers must be unique"


# -- url path grouping -----------------------------------------------------


def test_url_paths_nest_below_the_seed() -> None:
    tree = url_path_tree(
        [
            "https://x.dev/docs/install",
            "https://x.dev/docs/commands/run",
            "https://x.dev/docs/commands/docker",
        ],
        BASE,
    )
    titles = [n.title for n in tree]
    assert "Install" in titles
    commands = next(n for n in tree if n.title == "Commands")
    assert [c.title for c in commands.children] == ["Run", "Docker"]


def test_no_coverage_yields_an_empty_hierarchy() -> None:
    hier = derive([], BASE, seed_html=HUB)
    assert hier.placements == []


def test_a_sidebar_without_lists_still_yields_links() -> None:
    html = b"""<html><body><aside class="sidebar">
      <a href="/docs/a">A</a><a href="/docs/b">B</a>
    </aside></body></html>"""
    nodes = sidebar_tree(html, BASE)
    assert [n.url for n in nodes] == ["https://x.dev/docs/a", "https://x.dev/docs/b"]


def test_nested_sidebar_lists_become_ancestry() -> None:
    html = b"""<html><body><nav class="menu"><ul>
      <li><span>Getting started</span><ul>
        <li><a href="/docs/install">Install</a></li>
        <li><a href="/docs/setup">Setup</a></li>
      </ul></li>
      <li><a href="/docs/api">API</a></li>
    </ul></nav></body></html>"""
    coverage = ["https://x.dev/docs/install", "https://x.dev/docs/setup", "https://x.dev/docs/api"]
    hier = derive(coverage, BASE, seed_html=html)
    assert hier.source == "sidebar_dom"
    by_url = hier.by_url()
    assert by_url["https://x.dev/docs/install"].ancestry == ("Getting started",)
    assert by_url["https://x.dev/docs/install"].section == "1.1"
    assert by_url["https://x.dev/docs/api"].ancestry == ()
    assert by_url["https://x.dev/docs/api"].section == "2"
