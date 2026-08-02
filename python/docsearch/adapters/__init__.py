"""Format adapter registry.

Adding a format is one entry here plus one module. The chunker is untouched.
"""

from __future__ import annotations

from collections.abc import Callable
from pathlib import Path
from typing import Protocol

from ..blocks import Extraction
from ..errors import UnsupportedFormatError
from . import docx as docx_adapter
from . import html as html_adapter
from . import markdown as markdown_adapter
from . import pdf as pdf_adapter
from . import text as text_adapter


class Adapter(Protocol):
    def __call__(self, path: Path, progress: object = None) -> Extraction: ...


_BY_SUFFIX: dict[str, Callable[..., Extraction]] = {
    ".pdf": pdf_adapter.extract,
    ".md": markdown_adapter.extract,
    ".markdown": markdown_adapter.extract,
    ".html": html_adapter.extract,
    ".htm": html_adapter.extract,
    ".docx": docx_adapter.extract,
    ".txt": text_adapter.extract,
    ".text": text_adapter.extract,
}

SUPPORTED_SUFFIXES = frozenset(_BY_SUFFIX)


__all__ = ["SUPPORTED_SUFFIXES", "Adapter", "UnsupportedFormatError", "for_path", "is_supported"]


def for_path(path: Path) -> Callable[..., Extraction]:
    try:
        return _BY_SUFFIX[path.suffix.lower()]
    except KeyError:
        raise UnsupportedFormatError(
            f"no adapter for '{path.suffix or path.name}'; supported: "
            + ", ".join(sorted(SUPPORTED_SUFFIXES))
        ) from None


def is_supported(path: Path) -> bool:
    return path.suffix.lower() in _BY_SUFFIX
