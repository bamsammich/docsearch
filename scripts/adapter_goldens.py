"""Write the format adapters' test fixtures and the Python extraction of each.

The Go adapters in internal/adapter must reproduce the Python adapters
exactly. These fixtures are synthetic, written here to exercise the cases each
adapter handles, so both they and the goldens can be committed and the Go
tests run anywhere. Rerun after changing a fixture:

    uv run python scripts/adapter_goldens.py

Retired with the Python pipeline.
"""

from __future__ import annotations

import dataclasses
import json
from pathlib import Path

import docx
import pymupdf
from docx.enum.text import WD_BREAK
from docx.oxml import OxmlElement
from docx.oxml.ns import qn

from docsearch.adapters import for_path

OUT = Path("testdata/adapters")

# Exotic whitespace is built from code points, so the source stays plain and
# says which character each case exercises.
NBSP = chr(0x00A0)  # NO-BREAK SPACE
IDEOGRAPHIC_SPACE = chr(0x3000)
LINE_SEPARATOR = chr(0x2028)

MARKDOWN = """# Operator Guide

Intro paragraph about the console and its layout.
It continues on a second line.

## Executors ##

Executors run sequences. Executors run sequences. Executors run sequences.

#NoSpace is not a heading.

### Assigning Sequences

Press Assign then select the sequence.

```
# not a heading, inside a fence
code line
```

~~~
## also not a heading
~~~

#### Deep, under a skipped level

Tabs\tand   spaces   survive inside a paragraph.
   \t
## Glossary

Short. Ünïcödé text: 調整音量 and — punctuation.
"""

TEXT = "\n\n".join(f"Paragraph number {i} with some content to index." for i in range(6))

HTML = """<!doctype html>
<html><head><title>Network &amp; Notes</title>
<style>p { color: red }</style><script>var x = "<p>not content</p>";</script></head>
<body>
<nav><a href="/">Home</a> <a href="/docs">Docs</a></nav>
<h1 id="networking">Networking</h1>
<p>Session hosts share a <em>show file</em>.</p>
<h2><a id="setup"></a>Session Setup</h2>
<ol>
  <li>First, run:
    <pre><code><span class="k">def</span> <span class="nf">hello</span>():
    <span class="nb">print</span>(<span class="s">"hi"</span>)
</code></pre>
  </li>
  <li><p>A paragraph inside an item.</p></li>
</ol>
<blockquote><p>Quoted advice.</p></blockquote>
<table><tr><td>Returns a handle.
  <pre><code>h = open()
h.close()</code></pre>
</td><td>Second cell</td></tr></table>
<dl><dt>Term</dt><dd>Definition of the term.</dd></dl>
<h3>Empty code</h3>
<pre></pre>
<pre><code>
first()

second()

</code></pre>
<!-- a comment that must not appear -->
<footer><p>Copyright notice.</p></footer>
</body></html>
"""


# Markup lxml and the HTML5 algorithm both repair the same way, plus the
# attribute and entity cases the adapter reads.
HTML_EDGES = f"""<html><head><title>
  Edge &amp; Cases{NBSP}</title></head><body>
<h1 id="  top  ">Top&nbsp;level &#128; heading</h1>
<p>one<p>two, after an unclosed paragraph</p>
<ul><li>first item<li>second item, unclosed</ul>
<ul><li><h2><span id=""></span><a id="deep"></a>Heading in a list</h2>item text</li></ul>
<p>Under the list heading.</p>
<table><tr><td>cell<p>paragraph in a cell</p></td></tr></table>
<pre>outer<pre>inner</pre>tail</pre>
<pre>next block</pre>
<h3></h3>
<p>Empty heading above changes nothing.</p>
<h2 id="">Empty id</h2>
<p>{IDEOGRAPHIC_SPACE}Ideographic space around.{IDEOGRAPHIC_SPACE}</p>
</body></html>
"""

# Separators Python's splitlines breaks at, and Unicode whitespace its \\s
# matches, in heading and fence positions.
MARKDOWN_EDGES = (
    f"#\tTab Heading{IDEOGRAPHIC_SPACE}#{IDEOGRAPHIC_SPACE}\n"
    "Line one\x0cstill a line break\n"
    f"Line two{LINE_SEPARATOR}after a line separator\x85after next line\n"
    "\n"
    "####### seven hashes is text\n"
    f"{IDEOGRAPHIC_SPACE}```\n"
    "# fenced after an ideographic space\n"
    "```\n"
    "## Closing hashes ##   \n"
    f"{NBSP}\n"
    "After a no-break-space line.\n"
)


def _write_docx_runs(path: Path) -> None:
    """Run content python-docx turns into text, and runs it does not read."""
    d = docx.Document()
    d.core_properties.title = ""
    d.add_heading("Runs", level=1)
    p = d.add_paragraph("tab")
    r = p.add_run()
    for tag, attrs in (
        ("w:ptab", {"w:relativeTo": "margin", "w:alignment": "left", "w:leader": "none"}),
        ("w:noBreakHyphen", {}),
        ("w:cr", {}),
        ("w:br", {"w:type": "page"}),
        ("w:br", {"w:type": "column"}),
        ("w:br", {"w:type": "textWrapping"}),
    ):
        el = OxmlElement(tag)
        for k, v in attrs.items():
            el.set(qn(k), v)
        r._r.append(el)
    r._r.append(_text_element("end"))
    hyperlink = OxmlElement("w:hyperlink")
    hyperlink.append(_run_element(" linked"))
    p._p.append(hyperlink)
    inserted = OxmlElement("w:ins")
    inserted.append(_run_element(" tracked insertion, unread"))
    p._p.append(inserted)
    d.add_paragraph("Custom heading-like style", style="List Number")
    d.add_heading("Title-styled twice", level=0)
    d.add_heading("Second title paragraph", level=0)
    d.save(str(path))


def _text_element(text: str) -> OxmlElement:
    t = OxmlElement("w:t")
    t.text = text
    t.set(qn("xml:space"), "preserve")
    return t


def _run_element(text: str) -> OxmlElement:
    r = OxmlElement("w:r")
    r.append(_text_element(text))
    return r


BODY = 10.0
HEAD = 16.0
WORDS = [
    "amber",
    "birch",
    "cedar",
    "delta",
    "ember",
    "fjord",
    "grove",
    "harbor",
    "iris",
    "juniper",
    "kestrel",
    "lumen",
]


def _pdf_page(doc: pymupdf.Document, rows: list[list[tuple[float, float, str]]]) -> None:
    """One page; each row is ``(x, size, text)`` cells sharing a baseline."""
    page = doc.new_page()
    y = 90.0
    for row in rows:
        for x, size, text in row:
            page.insert_text((x, y), text, fontsize=size)
        y += max(size for _x, size, _t in row) * 2.2


def _line(size: float, text: str) -> list[tuple[float, float, str]]:
    return [(72.0, size, text)]


def _body(n: int, topic: str) -> list[list[tuple[float, float, str]]]:
    """Prose lines that differ from page to page even with digits masked, so
    none repeats enough to read as running furniture."""
    return [
        _line(
            BODY,
            f"The {topic} notes cover {WORDS[i % len(WORDS)]} "
            f"and {WORDS[(i * 5 + 3) % len(WORDS)]} work.",
        )
        for i in range(n)
    ]


def _write_numbered_pdf(path: Path) -> None:
    """Structure from font sizes: numbered headings at a larger size, an
    unnumbered heading-sized line that subdivides, a chapter number and title
    set as two lines, and a numbered step at heading size that document
    order rejects."""
    doc = pymupdf.open()
    doc.set_metadata({"title": "Numbered Guide"})
    _pdf_page(doc, [_line(HEAD, "1. Overview"), *_body(8, "overview")])
    _pdf_page(
        doc,
        [_line(HEAD, "2. Setup"), *_body(6, "setup"), _line(HEAD, "Wiring"), *_body(6, "wiring")],
    )
    _pdf_page(
        doc,
        [
            _line(HEAD, "2.1. Power"),
            *_body(5, "power"),
            _line(HEAD, "1. Tap the switch"),
            *_body(3, "step"),
        ],
    )
    _pdf_page(doc, [_line(HEAD, "3."), _line(HEAD, "Maintenance"), *_body(9, "maintenance")])
    later = ["cleaning", "storage", "transport", "repair", "disposal", "warranty"]
    for i, topic in enumerate(later):
        _pdf_page(doc, [_line(HEAD, f"{4 + i}. {topic.title()}"), *_body(10, topic)])
    doc.save(str(path))
    doc.close()


def _write_outline_pdf(path: Path) -> None:
    """Structure from an embedded outline, with one entry whose title is not
    on its page."""
    doc = pymupdf.open()
    _pdf_page(
        doc,
        [
            _line(HEAD, "Getting Started"),
            *_body(6, "start"),
            _line(HEAD, "First Steps"),
            *_body(6, "steps"),
        ],
    )
    _pdf_page(doc, [_line(HEAD, "Configuration"), *_body(10, "configuration")])
    _pdf_page(doc, [*_body(12, "unmarked")])
    doc.set_toc(
        [
            [1, "Getting Started", 1],
            [2, "First Steps", 1],
            [1, "Configuration", 2],
            [2, "Advanced Options", 3],
        ]
    )
    doc.save(str(path))
    doc.close()


def _write_contents_pdf(path: Path) -> None:
    """Structure from a printed table of contents, with a back-of-book index
    whose entries cite section numbers."""
    doc = pymupdf.open()
    chapters = [
        ("1", "Introduction", 2),
        ("2", "Controls", 3),
        ("2.1", "Faders", 3),
        ("3", "Index", 4),
    ]
    _pdf_page(
        doc,
        [_line(HEAD, "Contents")]
        + [
            [(72.0, BODY, f"{sec}."), (110.0, BODY, title), (400.0, BODY, str(pg))]
            for sec, title, pg in chapters
        ],
    )
    _pdf_page(doc, [_line(HEAD, "1. Introduction"), *_body(10, "introduction")])
    _pdf_page(
        doc,
        [
            _line(HEAD, "2. Controls"),
            *_body(5, "controls"),
            _line(HEAD, "2.1. Faders"),
            *_body(5, "faders"),
        ],
    )
    _pdf_page(
        doc,
        [
            _line(HEAD, "3. Index"),
            [(72.0, BODY, "channel strip"), (300.0, BODY, "2.1.")],
            [(72.0, BODY, "master"), (300.0, BODY, "2. 2.1.")],
            [(72.0, BODY, "unknown term"), (300.0, BODY, "9.")],
        ],
    )
    doc.save(str(path))
    doc.close()


def _write_docx(path: Path, *, title: str | None, core_title: str) -> None:
    d = docx.Document()
    d.core_properties.title = core_title
    if title:
        d.add_paragraph(title, style="Title")
    d.add_heading("Introduction", level=1)
    d.add_paragraph("The console stores cues.")
    d.add_paragraph("")
    d.add_heading("Playback", level=2)
    p = d.add_paragraph("Press Go")
    run = p.add_run("\tto fire")
    run.add_break(WD_BREAK.LINE)
    p.add_run("the next cue.")
    d.add_heading("Deep section", level=4)
    d.add_paragraph("Nested below a skipped level.")
    table = d.add_table(rows=1, cols=1)
    table.cell(0, 0).text = "Table text is not a body paragraph."
    d.add_heading("Appendix", level=1)
    d.add_paragraph("Last words.", style="Quote")
    d.save(str(path))


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    (OUT / "guide.md").write_text(MARKDOWN, encoding="utf-8")
    (OUT / "crlf.md").write_bytes(MARKDOWN.replace("\n", "\r\n").encode())
    (OUT / "readme.txt").write_text(TEXT, encoding="utf-8")
    (OUT / "crlf.txt").write_bytes(TEXT.replace("\n", "\r\n").encode())
    # Invalid UTF-8: a truncated three-byte sequence, a lone continuation
    # byte, and a byte that never starts a sequence.
    (OUT / "invalid.txt").write_bytes(b"Before \xe2\x82A after.\n\nLone \x80 byte and \xff here.\n")
    (OUT / "page.html").write_text(HTML, encoding="utf-8")
    (OUT / "edges.html").write_text(HTML_EDGES, encoding="utf-8")
    (OUT / "edges.md").write_text(MARKDOWN_EDGES, encoding="utf-8")
    _write_docx_runs(OUT / "runs.docx")
    _write_numbered_pdf(OUT / "numbered.pdf")
    _write_outline_pdf(OUT / "outline.pdf")
    _write_contents_pdf(OUT / "contents.pdf")
    _write_docx(OUT / "styled.docx", title="Operator Handbook", core_title="Core Title")
    _write_docx(OUT / "untitled.docx", title=None, core_title="From Core Properties")
    _write_docx(OUT / "bare.docx", title=None, core_title="")

    for path in sorted(OUT.iterdir()):
        if path.name.endswith(".golden.json"):
            continue
        ext = for_path(path)(path, None)
        golden = OUT / f"{path.name}.golden.json"
        golden.write_text(
            json.dumps(dataclasses.asdict(ext), ensure_ascii=False, indent=1, sort_keys=True) + "\n"
        )
        print(f"{len(ext.blocks):>3} blocks  {path.name}")


if __name__ == "__main__":
    main()
