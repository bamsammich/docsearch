from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from docsearch import db


@pytest.fixture
def conn(tmp_path: Path) -> sqlite3.Connection:
    return db.connect(tmp_path / "test.db")


@pytest.fixture
def md_file(tmp_path: Path) -> Path:
    path = tmp_path / "guide.md"
    path.write_text(
        "# Operator Guide\n\n"
        "Intro paragraph about the console and its layout.\n\n"
        "## Executors\n\n"
        "Executors run sequences. " * 40 + "\n\n"
        "### Assigning Sequences\n\n"
        "Press Assign then select the sequence. " * 30 + "\n\n"
        "## Glossary\n\nShort.\n",
        encoding="utf-8",
    )
    return path


@pytest.fixture
def html_file(tmp_path: Path) -> Path:
    path = tmp_path / "notes.html"
    path.write_text(
        "<html><head><title>Network Notes</title></head><body>"
        "<h1>Networking</h1><p>" + "Session hosts share a show file. " * 40 + "</p>"
        "<h2>Session Setup</h2><p>" + "Join the session from the setup menu. " * 40 + "</p>"
        "</body></html>",
        encoding="utf-8",
    )
    return path


@pytest.fixture
def txt_file(tmp_path: Path) -> Path:
    path = tmp_path / "readme.txt"
    path.write_text(
        "\n\n".join(f"Paragraph number {i} with some content to index." for i in range(20)),
        encoding="utf-8",
    )
    return path
