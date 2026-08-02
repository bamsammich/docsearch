"""The structure-validation policy and how a failing job reports itself."""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

import pytest

from docsearch import db, ingest
from docsearch.blocks import Block, Extraction
from docsearch.errors import StructureValidationError
from docsearch.ingest import ingest_file
from docsearch.structure import StructureReport, from_diagnostics, scattered_sections
from docsearch.worker import Worker, WorkerConfig


def _report(**kw: object) -> StructureReport:
    return StructureReport(structure_source="front_toc", **kw)  # type: ignore[arg-type]


def test_empty_symmetric_difference_is_not_fatal() -> None:
    assert not _report().fatal
    assert _report().quality() == "ok"


def test_missing_body_section_is_fatal() -> None:
    rep = _report(in_toc_not_in_body=["12.3"])
    assert rep.fatal
    assert rep.quality() == "failed"


def test_extra_body_heading_is_fatal() -> None:
    rep = _report(in_body_not_in_toc=["99.1"])
    assert rep.fatal


def test_font_heuristic_source_has_no_toc_to_validate_against() -> None:
    """Without a table of contents there is no symmetric difference to take."""
    rep = StructureReport(structure_source="font_heuristic", in_toc_not_in_body=["3"])
    assert not rep.validatable
    assert not rep.fatal


def test_duplicates_are_degraded_not_fatal() -> None:
    rep = _report(detected_more_than_once=["4"])
    assert not rep.fatal
    assert rep.degraded
    assert rep.quality() == "degraded"


def test_failure_message_names_the_sections_and_the_consequence() -> None:
    msg = _report(in_toc_not_in_body=["12.3", "12.4"]).failure_message()
    assert "12.3" in msg and "12.4" in msg
    assert "not indexed" in msg
    assert "table of contents" in msg


def test_scattered_sections_detects_gaps_not_subdivisions() -> None:
    class C:
        def __init__(self, section: str, ordinal: int) -> None:
            self.section, self.ordinal = section, ordinal

    subdivided = [C("7.1", 4), C("7.1", 5), C("7.1", 6)]
    assert scattered_sections(subdivided) == []

    misfired = [C("1", 0), C("1", 148), C("1", 226)]
    assert scattered_sections(misfired) == ["1"]


def test_from_diagnostics_round_trips_through_json() -> None:
    rep = from_diagnostics(
        {
            "structure_source": "front_toc",
            "candidates_rejected_by_ordering": ["p315:1"],
            "cross_validation": {
                "toc_sections": 827,
                "body_sections": 827,
                "in_toc_not_in_body": [],
                "in_body_not_in_toc": [],
                "detected_more_than_once": [],
            },
        }
    )
    payload = json.loads(rep.to_json())
    assert payload["quality"] == "ok"
    assert payload["toc_sections"] == 827
    assert payload["candidates_rejected_by_ordering"] == ["p315:1"]


# -- end to end -----------------------------------------------------------


@pytest.fixture
def mismatching_adapter(monkeypatch: pytest.MonkeyPatch) -> None:
    """An adapter whose TOC and body disagree by one section."""

    def fake(path: Path, progress: object = None) -> Extraction:
        return Extraction(
            title="Mismatched",
            format="pdf",
            page_count=10,
            blocks=[
                Block(heading_path=["1. A"], locator={"page": 1}, text="body " * 60, section="1")
            ],
            diagnostics={
                "structure_source": "front_toc",
                "cross_validation": {
                    "toc_sections": 2,
                    "body_sections": 1,
                    "in_toc_not_in_body": ["2"],
                    "in_body_not_in_toc": [],
                    "detected_more_than_once": [],
                },
            },
        )

    monkeypatch.setattr(ingest, "for_path", lambda _p: fake)


def test_structure_mismatch_fails_ingest_and_writes_nothing(
    conn: sqlite3.Connection, md_file: Path, mismatching_adapter: None
) -> None:
    with pytest.raises(StructureValidationError) as excinfo:
        ingest_file(conn, md_file)
    assert "2" in str(excinfo.value)
    assert conn.execute("SELECT COUNT(*) c FROM documents").fetchone()["c"] == 0
    assert conn.execute("SELECT COUNT(*) c FROM chunks").fetchone()["c"] == 0


def test_worker_fails_the_job_permanently_on_structure_mismatch(
    tmp_path: Path, md_file: Path, mismatching_adapter: None
) -> None:
    """It must not complete with a note, and must not retry."""
    path = tmp_path / "q.db"
    conn = db.connect(path)
    conn.execute(
        "INSERT INTO ingest_jobs (source_path, title, status, created_at, updated_at)"
        " VALUES (?,NULL,'queued',datetime('now'),datetime('now'))",
        (str(md_file),),
    )
    Worker(WorkerConfig(db_path=str(path), once=True)).run()

    row = conn.execute("SELECT * FROM ingest_jobs").fetchone()
    assert row["status"] == "failed"
    assert "StructureValidationError" in row["error"]
    assert "not indexed" in row["error"]
    assert row["permanent"] == 1
    assert row["attempts"] == 1, "a permanent failure is not retried, and says so as a flag"
    assert conn.execute("SELECT COUNT(*) c FROM documents").fetchone()["c"] == 0
