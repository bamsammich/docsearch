"""Policy on structure that was never really validated.

Agreement between two empty sets satisfies every comparison there is. A
document from which nothing was derived therefore passes a cross-validation
gate that a document with real structure could fail, which inverts the gate.
"""

from __future__ import annotations

import json

from docsearch.structure import StructureReport


def report(**kw: object) -> StructureReport:
    return StructureReport(**kw)  # type: ignore[arg-type]


def healthy(**overrides: object) -> dict[str, object]:
    """A report clean on every axis, so a test varies exactly one thing."""
    base: dict[str, object] = {
        "structure_source": "front_toc",
        "toc_sections": 100,
        "body_sections": 100,
        "chunks": 100,
        "distinct_heading_paths": 90,
    }
    base.update(overrides)
    return base


def test_empty_agreement_is_not_cross_validation() -> None:
    r = report(structure_source="front_toc", toc_sections=0, body_sections=0)
    assert r.symmetric_difference == []
    assert not r.cross_validated
    assert "not cross-validated" in " ".join(r.notes())


def test_real_agreement_is_cross_validation() -> None:
    r = report(structure_source="front_toc", toc_sections=827, body_sections=827)
    assert r.cross_validated
    assert not r.fatal
    assert r.quality() == "ok"


def test_disagreement_still_fails() -> None:
    r = report(
        structure_source="front_toc",
        toc_sections=827,
        body_sections=800,
        in_toc_not_in_body=[str(i) for i in range(27)],
    )
    assert r.fatal
    assert r.quality() == "failed"


def test_an_authoritative_source_needs_no_corroboration() -> None:
    """An embedded outline declares sections, nesting and position.

    Requiring font-detected body headings to confirm it rejects documents
    whose headings are styled by weight rather than size, whose structure is
    nonetheless fully declared.
    """
    r = report(structure_source="outline", toc_sections=0, body_sections=0)
    assert r.authoritative
    assert not r.validatable
    assert not r.fatal


def test_budget_sliced_structure_is_degraded_whatever_produced_it() -> None:
    r = report(
        structure_source="font_heuristic",
        chunks=35,
        distinct_heading_paths=10,
        headless_chunks=4,
    )
    assert r.unaddressable
    assert r.degraded
    assert r.quality() == "degraded"
    note = " ".join(r.notes())
    assert "35 chunks share only 10" in note
    assert "4 chunk(s) carry no heading path" in note


def test_one_unreachable_chunk_does_not_downgrade_a_whole_document() -> None:
    """A blemish is not a symptom.

    The numbered reference corpus carries exactly one headless chunk in 944.
    Downgrading the document for it would report a corpus that retrieves
    correctly as structurally suspect.
    """
    r = report(
        structure_source="front_toc",
        toc_sections=827,
        body_sections=827,
        chunks=944,
        distinct_heading_paths=881,
        headless_chunks=1,
    )
    assert not r.mostly_headless
    assert r.quality() == "ok"
    assert "1 chunk(s) carry no heading path" in " ".join(r.notes())


def test_well_structured_documents_are_not_degraded() -> None:
    """Guards the bound against the corpora known to retrieve correctly."""
    for chunks, paths in ((944, 881), (265, 222), (318, 257)):
        r = report(structure_source="front_toc", chunks=chunks, distinct_heading_paths=paths)
        assert not r.unaddressable, f"{paths}/{chunks} wrongly flagged"


def test_addressability_needs_a_distribution() -> None:
    r = report(structure_source="font_heuristic", chunks=4, distinct_heading_paths=1)
    assert not r.unaddressable


def test_an_uncalibrated_script_is_reported_then_downgrades() -> None:
    """Chunk sizes for such a document are in an unknown unit.

    Nothing looks wrong: the ingest completes and the chunks are well formed.
    Only the size thresholds applied to them are meaningless.
    """
    noted = report(**healthy(uncalibrated_script_share=0.2))
    assert "unknown unit" in " ".join(noted.notes())
    assert noted.quality() == "ok"

    downgraded = report(**healthy(uncalibrated_script_share=0.95))
    assert downgraded.quality() == "degraded"


def test_latin_and_cjk_documents_are_calibrated() -> None:
    r = report(**healthy(uncalibrated_script_share=0.0))
    assert r.notes() == []
    assert r.quality() == "ok"


def test_notes_and_addressability_reach_the_persisted_payload() -> None:
    """The worker is headless; a finding that is not serialised is lost."""
    r = report(
        structure_source="font_heuristic",
        chunks=35,
        distinct_heading_paths=10,
        headless_chunks=4,
    )
    payload = json.loads(r.to_json())
    assert payload["quality"] == "degraded"
    assert payload["addressability"] == 0.286
    assert any("section_filter cannot narrow" in n for n in payload["notes"])
