"""Both processes must agree on which file types exist.

`add_document` filters a directory walk by suffix so a library folder does not
enqueue one job per image, and the worker picks an adapter by the same suffix.
Nothing connected the two lists, so teaching Python a new format would leave
the tool silently refusing files the worker can read -- a capability lag with
no failing test.

This is a guard, not the fix. The duplication exists because the Go server
decides something the worker owns; it disappears when the directory walk moves
into the `Source` abstraction and `add_document` enqueues a directory as one
job for the worker to expand.
"""

from __future__ import annotations

import re
from pathlib import Path

from docsearch.adapters import SUPPORTED_SUFFIXES

SERVER_GO = Path(__file__).resolve().parents[1] / "internal/mcpserver/server.go"
_DECL = re.compile(r"var supportedSuffixes = map\[string\]bool\{(.*?)\}", re.S)
_SUFFIX = re.compile(r'"(\.[a-z0-9]+)":\s*true')


def _go_suffixes() -> set[str]:
    body = _DECL.search(SERVER_GO.read_text())
    assert body, f"no `var supportedSuffixes` map found in {SERVER_GO}"
    return set(_SUFFIX.findall(body.group(1)))


def test_the_two_suffix_lists_are_identical() -> None:
    go, py = _go_suffixes(), set(SUPPORTED_SUFFIXES)
    assert go, "parsed no suffixes out of the Go source; the guard is not testing anything"
    assert go == py, (
        f"add_document and the ingest adapters disagree on supported file types. "
        f"Only Go: {sorted(go - py)}. Only Python: {sorted(py - go)}. "
        f"A format Python gains but Go does not is refused by the tool and accepted "
        f"by `docsearch ingest`."
    )
