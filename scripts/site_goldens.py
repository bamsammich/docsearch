"""Write the site pipeline's synthetic sites and the Python crawl of each.

The Go port in internal/site must reproduce the Python crawl and extraction
exactly. Probing a real documentation site would make the reference depend on
a stranger's uptime and on whatever they publish this week, so each fixture
here is a site this script invents, serves over loopback and crawls. Both the
site and the crawl of it are committed, and the Go specs serve the same
routes from the same files.

Rerun after changing a fixture:

    uv run python scripts/site_goldens.py

Retired with the Python pipeline.
"""

from __future__ import annotations

import dataclasses
import http.server
import json
import socket
import tempfile
import threading
from collections.abc import Iterator
from contextlib import contextmanager
from ipaddress import ip_address
from pathlib import Path
from urllib.parse import urlsplit

from docsearch import fetchcache
from docsearch.chunker import chunk
from docsearch.crawl import crawl
from docsearch.fetch import Fetcher
from docsearch.site import build_extraction
from docsearch.urlguard import Target

OUT = Path("testdata/site")

#: Stands in for the server's origin wherever a fixture or a golden has to
#: name it. The port is chosen at run time, so neither the sitemap that the
#: fixture serves nor the URLs the crawl records can be written down.
ORIGIN = "{{ORIGIN}}"

# -- the page set ----------------------------------------------------------

#: Rendered on every page of the declared site, as a generator would: a div
#: full of links rather than a <nav>, which is what makes it survive the HTML
#: parse and reach the chrome check.
SIDEBAR = """<div class="sidebar"><ul>
<li><a href="/docs/">Overview</a></li>
<li><span>Installing</span><ul>
<li><a href="/docs/install">Install</a></li>
<li><a href="/docs/install/linux">Linux</a></li>
<li><a href="/docs/install/macos">macOS</a></li>
</ul></li>
<li><a href="/docs/usage">Usage</a></li>
<li><a href="/docs/api">API reference</a></li>
<li><a href="/docs/faq">Questions</a></li>
<li><a href="/docs/changes">Changes</a></li>
</ul></div>"""

#: Repeated under the content of every page, where a generator puts a notice
#: that is furniture however it reads.
COLOPHON = "<p>Built with the documentation generator. Last updated this week.</p>"


def page(title: str, body: str, *, head: str = "") -> str:
    """One page of the declared site, sidebar and colophon included."""
    return (
        f"<!doctype html><html><head><title>{title}</title>{head}</head><body>"
        f"{SIDEBAR}<main>{body}</main>{COLOPHON}</body></html>\n"
    )


DECLARED_PAGES = {
    "index.html": page(
        "Widget Docs",
        "<h1>Widget Docs</h1>"
        "<p>Widget turns a pile of parts into a working assembly, and this "
        "manual covers running it.</p>"
        "<h2>Before you start</h2>"
        "<p>A workstation with four gigabytes of memory and a supported "
        "operating system is enough for every task described here.</p>"
        '<p>The <a href="https://elsewhere.example/forum">community forum</a> '
        "answers questions this manual does not.</p>",
    ),
    "install.html": page(
        "Install",
        "<h1>Install</h1>"
        "<p>Installation takes a few minutes and needs no administrator "
        "rights on any supported platform.</p>"
        '<h2 id="packages">Packages</h2>'
        "<p>Packages are published for every release and are signed with the "
        "project key.</p>"
        "<h3>Verifying a package</h3>"
        "<p>Check the signature before unpacking anything downloaded over a "
        "network you do not control.</p>",
    ),
    "install-linux.html": page(
        "Linux",
        "<h1>Linux</h1>"
        "<p>Every distribution with a package manager can install Widget "
        "from the project repository.</p>"
        "<h2>Debian and Ubuntu</h2>"
        "<p>Add the repository, refresh the package lists and install the "
        "widget package.</p>"
        "<h2>Fedora</h2>"
        "<p>Enable the copr and install the same package by name.</p>",
    ),
    "install-macos.html": page(
        "macOS",
        "<h1>macOS</h1>"
        "<p>The signed disk image installs Widget into the applications "
        "folder and registers its command line tool.</p>",
    ),
    "usage.html": page(
        "Usage",
        "<h1>Usage</h1>"
        "<p>Widget reads an assembly description and writes the parts list "
        "it implies.</p>"
        '<h2 id="running">Running an assembly</h2>'
        "<p>Point the tool at a description file and it prints the result on "
        "standard output.</p>"
        "<h3>Watching for changes</h3>"
        "<p>The watch flag reruns the assembly whenever the description "
        "changes on disk.</p>"
        '<h2 id="profiles">Profiles</h2>'
        "<p>A profile names the defaults a project uses so nobody has to "
        "remember them.</p>",
    ),
    "api.html": page(
        "API reference",
        "<h1>API reference</h1>"
        "<p>Every endpoint answers JSON and reports failure with a status "
        "code rather than a body field.</p>"
        '<h2 id="assemblies">Assemblies</h2>'
        "<p>An assembly is created by posting a description and is fetched "
        "by its identifier afterwards.</p>"
        "<h3>Creating an assembly</h3>"
        "<p>Post the description as the request body. The response carries "
        "the identifier the assembly was given.</p>"
        "<h3>Fetching an assembly</h3>"
        "<p>Get the assembly by identifier. A missing assembly answers 404 "
        "with an empty body.</p>"
        '<h2 id="parts">Parts</h2>'
        "<p>Parts are read only. They are derived from the assembly and "
        "cannot be edited on their own.</p>",
    ),
    "faq.html": page(
        "Questions",
        "<h1>Questions</h1>"
        "<h2>Does Widget run without a network?</h2>"
        "<p>Yes. Nothing in the assembly path makes a request.</p>"
        "<h2>Which licence covers Widget?</h2>"
        "<p>The permissive one named in the repository root.</p>",
    ),
    "changes.html": page(
        "Changes",
        "<h1>Changes</h1>"
        "<p>Release notes for every published version, newest first.</p>"
        "<h2>Version two</h2>"
        "<p>Profiles were added and the watch flag learned to debounce.</p>"
        "<h2>Version one</h2>"
        "<p>The first release anybody outside the project used.</p>",
        head='<link rel="canonical" href="/docs/changes">',
    ),
    "404.html": (
        "<!doctype html><html><head><title>Not found</title></head>"
        "<body><h1>Not found</h1><p>No page answers at this address.</p>"
        "</body></html>\n"
    ),
    "robots.txt": f"User-agent: *\nAllow: /\nSitemap: {ORIGIN}/sitemap.xml\n",
    "sitemap.xml": "".join(
        [
            '<?xml version="1.0" encoding="UTF-8"?>\n',
            '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n',
        ]
        + [
            f"<url><loc>{ORIGIN}{path}</loc></url>\n"
            for path in (
                "/docs/",
                "/docs/install",
                "/docs/install/linux",
                "/docs/install/macos",
                "/docs/usage",
                "/docs/api",
                "/docs/faq",
                "/docs/changes",
                "/docs/changelog",
                "/docs/retired",
                "/blog/announcement",
            )
        ]
        + ["</urlset>\n"]
    ),
}

#: The declared site: a sitemap names the page set, so nothing is walked.
#: /docs/changelog serves the changes page under a second spelling and
#: declares which one is canonical; /docs/retired is declared and gone.
DECLARED_ROUTES = {
    "seed": "/docs/",
    "not_found": {"file": "404.html", "status": 404},
    "routes": {
        "/robots.txt": {"file": "robots.txt"},
        "/sitemap.xml": {"file": "sitemap.xml"},
        "/docs/": {"file": "index.html"},
        "/docs/install": {"file": "install.html"},
        "/docs/install/linux": {"file": "install-linux.html"},
        "/docs/install/macos": {"file": "install-macos.html"},
        "/docs/usage": {"file": "usage.html"},
        "/docs/api": {"file": "api.html"},
        "/docs/faq": {"file": "faq.html"},
        "/docs/changes": {"file": "changes.html"},
        "/docs/changelog": {"file": "changes.html"},
    },
}

# -- the walked site -------------------------------------------------------

#: The walked site answers 200 for every address, including the ones that do
#: not exist, so the crawl has to recognize its not-found template rather than
#: trust the status line.
WALKED_TEMPLATE = (
    "<!doctype html><html><head><title>Handbook</title></head><body>"
    '<div class="chrome"><a href="/handbook/">Handbook</a></div>'
    "<main>{body}</main></body></html>\n"
)


def walked(body: str) -> str:
    return WALKED_TEMPLATE.format(body=body)


WALKED_PAGES = {
    "index.html": walked(
        "<h1>Handbook</h1>"
        "<p>How the workshop runs, written down so nobody has to ask twice.</p>"
        '<h2>Opening</h2><p>See <a href="/handbook/opening">opening</a>.</p>'
        '<h2>Closing</h2><p>See <a href="/handbook/closing">closing</a>.</p>'
        '<p>The <a href="/handbook/missing">rota</a> has moved.</p>'
        '<p>Unrelated: <a href="/shop/">the shop</a>.</p>'
    ),
    "opening.html": walked(
        "<h1>Opening</h1>"
        "<p>Unlock the side door, switch on the extraction and check the "
        "first aid kit is where it should be.</p>"
        '<p>Then read <a href="/handbook/opening/checks">the checks</a>.</p>'
    ),
    "opening-checks.html": walked(
        "<h1>Checks</h1>"
        "<p>Every powered machine is tested before the first person uses "
        "it, and the result goes in the log.</p>"
    ),
    "closing.html": walked(
        "<h1>Closing</h1>"
        "<p>Sweep, empty the extraction, and leave the log on the bench for "
        "whoever opens tomorrow.</p>"
    ),
    "soft404.html": walked(
        "<h1>Nothing here</h1>"
        "<p>This address does not name a page of the handbook. Try the "
        "handbook index instead, or ask whoever sent you the link.</p>"
    ),
}

#: No sitemap and no llms.txt, so link-following is what finds the pages, and
#: every unknown address answers 200 with the soft-404 template.
WALKED_ROUTES = {
    "seed": "/handbook/",
    "not_found": {"file": "soft404.html", "status": 200},
    "routes": {
        "/handbook/": {"file": "index.html"},
        "/handbook/opening": {"file": "opening.html"},
        "/handbook/opening/checks": {"file": "opening-checks.html"},
        "/handbook/closing": {"file": "closing.html"},
    },
}

FIXTURES = (
    ("declared", DECLARED_PAGES, DECLARED_ROUTES),
    ("walked", WALKED_PAGES, WALKED_ROUTES),
)

# -- serving ---------------------------------------------------------------

_TYPES = {
    ".html": "text/html; charset=utf-8",
    ".txt": "text/plain; charset=utf-8",
    ".xml": "application/xml",
}


def _handler(root: Path, manifest: dict, origin: str) -> type:
    """A request handler serving exactly what the manifest declares.

    The Go specs serve the same manifest, so anything the two servers could
    disagree about -- a directory listing, a guessed content type, the body of
    a 404 -- is written down here rather than left to a static file server.
    """
    routes: dict[str, dict] = manifest["routes"]
    absent: dict = manifest["not_found"]

    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_GET(self) -> None:
            route = routes.get(urlsplit(self.path).path, absent)
            body = (root / route["file"]).read_bytes().replace(ORIGIN.encode(), origin.encode())
            self.send_response(route.get("status", 200))
            self.send_header("Content-Type", _TYPES[Path(route["file"]).suffix])
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args: object) -> None:
            pass

    return Handler


@contextmanager
def _serve(root: Path, manifest: dict) -> Iterator[str]:
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    origin = f"http://127.0.0.1:{port}"
    server = http.server.ThreadingHTTPServer(("127.0.0.1", port), _handler(root, manifest, origin))
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield origin
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def _loopback_guard(raw: str) -> Target:
    """The guard the fixtures run under.

    The real one refuses loopback, which is exactly what a fixture server is.
    Its own tests cover that refusal; these cover the pipeline above it.
    """
    return Target(url=raw, host="127.0.0.1", addrs=(ip_address("127.0.0.1"),))


# -- the goldens -----------------------------------------------------------


def _golden(obj: object, origin: str) -> str:
    raw = json.dumps(obj, ensure_ascii=False, indent=1, sort_keys=True) + "\n"
    return raw.replace(origin, ORIGIN)


def build(name: str, pages: dict[str, str], manifest: dict) -> None:
    root = OUT / name
    (root / "pages").mkdir(parents=True, exist_ok=True)
    for filename, body in pages.items():
        (root / "pages" / filename).write_text(body, encoding="utf-8")
    (root / "site.json").write_text(json.dumps(manifest, indent=1, sort_keys=True) + "\n")

    with _serve(root / "pages", manifest) as origin, tempfile.TemporaryDirectory() as tmp:
        cache = fetchcache.connect(Path(tmp) / "cache.db")
        with Fetcher(
            cache,
            guard=_loopback_guard,
            addr_guard=lambda _addr: True,
            interval=0.0,
        ) as fetcher:
            result = crawl(fetcher, origin + manifest["seed"])
        cache.close()

    extraction = build_extraction(result)
    (root / "extraction.golden.json").write_text(_golden(dataclasses.asdict(extraction), origin))
    chunks = chunk(extraction)
    (root / "chunks.golden.json").write_text(
        _golden([dataclasses.asdict(c) for c in chunks], origin)
    )
    print(
        f"{name}: {len(result.pages)} page(s), {len(extraction.blocks)} block(s), "
        f"{len(chunks)} chunk(s), hierarchy from {result.hierarchy.source}"
    )


def main() -> None:
    for name, pages, manifest in FIXTURES:
        build(name, pages, manifest)


if __name__ == "__main__":
    main()
