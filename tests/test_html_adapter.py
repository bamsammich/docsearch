"""HTML extraction, exercised through the adapter's public entry point."""

from __future__ import annotations

from pathlib import Path

from docsearch.adapters.html import extract


def _write(tmp_path: Path, body: str) -> Path:
    path = tmp_path / "page.html"
    path.write_text(f"<html><head><title>Page</title></head><body>{body}</body></html>")
    return path


def _texts(tmp_path: Path, body: str) -> list[str]:
    return [b.text for b in extract(_write(tmp_path, body)).blocks]


def test_highlighted_code_keeps_its_lines_and_indentation(tmp_path: Path) -> None:
    """Generated docs wrap every token in a span; the lines must survive it."""
    body = """
    <h1>Guide</h1>
    <pre><code><span class="k">def</span> <span class="nf">hello</span>():
    <span class="nb">print</span>(<span class="s">"hi"</span>)
    <span class="k">return</span> <span class="mi">1</span>
</code></pre>
    """
    code = _texts(tmp_path, body)[0]
    assert code.splitlines() == [
        "def hello():",
        '    print("hi")',
        "    return 1",
    ]


def test_plain_code_block_is_unchanged(tmp_path: Path) -> None:
    body = """
    <h1>Guide</h1>
    <pre><code>npm install
npm run build
</code></pre>
    """
    assert _texts(tmp_path, body) == ["npm install\nnpm run build"]


def test_code_inside_a_list_item_is_its_own_block(tmp_path: Path) -> None:
    """A numbered step with a code sample is the commonest shape in docs."""
    body = """
    <h1>Guide</h1>
    <ol>
      <li>First, run:
        <pre><code>npm install
npm run build</code></pre>
      </li>
    </ol>
    """
    texts = _texts(tmp_path, body)
    assert texts == ["First, run:", "npm install\nnpm run build"]


def test_code_inside_a_table_cell_is_its_own_block(tmp_path: Path) -> None:
    body = """
    <h1>API</h1>
    <table><tr><td>Returns a handle.
      <pre><code>h = open()
h.close()</code></pre>
    </td></tr></table>
    """
    texts = _texts(tmp_path, body)
    assert texts == ["Returns a handle.", "h = open()\nh.close()"]


def test_interior_blank_lines_survive_but_framing_ones_do_not(tmp_path: Path) -> None:
    body = """
    <h1>Guide</h1>
    <pre><code>
first()

second()

</code></pre>
    """
    assert _texts(tmp_path, body) == ["first()\n\nsecond()"]


def test_prose_extraction_is_unaffected(tmp_path: Path) -> None:
    """Documents with no code must extract exactly as they did before."""
    body = """
    <h1>Guide</h1>
    <p>Install <em>it</em> first.</p>
    <h2>Steps</h2>
    <ul><li>One</li><li>Two</li></ul>
    """
    result = extract(_write(tmp_path, body))
    assert [b.text for b in result.blocks] == ["Install it first.", "One", "Two"]
    assert [b.heading_path for b in result.blocks] == [
        ["Guide"],
        ["Guide", "Steps"],
        ["Guide", "Steps"],
    ]


def test_empty_code_block_emits_nothing(tmp_path: Path) -> None:
    body = """
    <h1>Guide</h1>
    <pre></pre>
    <p>After.</p>
    """
    assert _texts(tmp_path, body) == ["After."]


def test_heading_path_applies_to_code_blocks(tmp_path: Path) -> None:
    body = """
    <h1>Guide</h1>
    <h2>Install</h2>
    <pre><code>go build ./...</code></pre>
    """
    blocks = extract(_write(tmp_path, body)).blocks
    assert blocks[0].heading_path == ["Guide", "Install"]
