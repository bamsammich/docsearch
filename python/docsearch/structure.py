"""Structural quality findings, and the policy that acts on them.

Diagnostics printed to a terminal are lost when the worker runs headless, so
findings are captured as data, persisted on the job and the document, and
surfaced through the status and listing tools.
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from typing import Any

#: An embedded outline declares which sections exist, how they nest and the
#: page each begins on. None of that is inferred, so no more reliable source
#: exists to check it against and none is required.
AUTHORITATIVE = ("outline",)

#: Equally author-declared, but recovered by parsing a printed page. The parse
#: can misread, so it is checked against the body and a disagreement fails.
_VALIDATABLE = ("front_toc",)

#: Distinct heading paths per chunk, below which the derived structure does not
#: separate the document's own chunks: `section_filter` cannot narrow and
#: `outline` describes the document in too few entries to orient a caller.
#: Measured across four corpora -- 0.93, 0.84 and 0.81 where retrieval works,
#: 0.29 on a manual whose structure was never derived and whose text was
#: therefore cut into fixed windows.
ADDRESSABLE_MIN = 0.50

#: A ratio over fewer chunks than this is not a distribution.
ADDRESSABILITY_MIN_CHUNKS = 25

#: Share of chunks with no heading path at which the document as a whole is
#: downgraded. A stray unreachable chunk is worth reporting but is not a
#: property of the document: one in 944 is a blemish, one in nine is a symptom.
HEADLESS_DEGRADED_RATE = 0.02


@dataclass(slots=True)
class StructureReport:
    """What extraction and chunking noticed about a document's structure."""

    structure_source: str = "unknown"
    toc_sections: int = 0
    body_sections: int = 0
    in_toc_not_in_body: list[str] = field(default_factory=list)
    in_body_not_in_toc: list[str] = field(default_factory=list)
    detected_more_than_once: list[str] = field(default_factory=list)
    scattered_sections: list[str] = field(default_factory=list)
    candidates_rejected_by_ordering: list[str] = field(default_factory=list)
    chunks: int = 0
    distinct_heading_paths: int = 0
    headless_chunks: int = 0

    @property
    def validatable(self) -> bool:
        return self.structure_source in _VALIDATABLE

    @property
    def authoritative(self) -> bool:
        return self.structure_source in AUTHORITATIVE

    @property
    def cross_validated(self) -> bool:
        """Whether a comparison actually happened.

        Two empty sets have an empty symmetric difference, so a document from
        which nothing was derived satisfies every agreement test there is.
        Reporting that as agreement is how a document with no structure passed
        as sound; it is the absence of evidence, not evidence.
        """
        return self.validatable and self.toc_sections > 0 and self.body_sections > 0

    @property
    def addressability(self) -> float:
        """Distinct heading paths per chunk."""
        return self.distinct_heading_paths / self.chunks if self.chunks else 0.0

    @property
    def unaddressable(self) -> bool:
        """Structure that does not separate the document's own chunks.

        Deliberately independent of the source: this measures the outcome, not
        its provenance. Whatever produced the boundaries, a document whose
        chunks cannot be told apart by heading is one where `section_filter`
        cannot narrow and orientation has nothing to work with.
        """
        return self.chunks >= ADDRESSABILITY_MIN_CHUNKS and self.addressability < ADDRESSABLE_MIN

    def notes(self) -> list[str]:
        """Findings a caller should see, in the caller's terms."""
        out: list[str] = []
        if self.unaddressable:
            out.append(
                f"{self.chunks} chunks share only {self.distinct_heading_paths} distinct "
                f"heading paths ({self.addressability:.2f} per chunk): boundaries came from "
                f"the token budget rather than the document, so section_filter cannot narrow "
                f"within it and outline describes it in {self.distinct_heading_paths} entries"
            )
        if self.headless_chunks:
            out.append(
                f"{self.headless_chunks} chunk(s) carry no heading path and cannot be "
                f"reached by heading, filtered, or described in an outline"
            )
        if self.validatable and not self.cross_validated:
            out.append(
                f"structure source '{self.structure_source}' was not cross-validated: "
                f"{self.toc_sections} table-of-contents section(s) and "
                f"{self.body_sections} body heading(s) were available to compare"
            )
        return out

    @property
    def symmetric_difference(self) -> list[str]:
        return sorted(set(self.in_toc_not_in_body) | set(self.in_body_not_in_toc))

    @property
    def fatal(self) -> bool:
        """A non-empty symmetric difference is disqualifying.

        Empty-set agreement between the table of contents and the body is the
        evidence that the reconstruction is sound. Without it the heading tree
        is not known to describe the document, chunks carry section paths that
        may be wrong, and the index answers confidently from the wrong place.
        That is worse than failing, so it fails.
        """
        return self.validatable and bool(self.symmetric_difference)

    @property
    def degraded(self) -> bool:
        """Non-fatal findings a caller should still be told about."""
        return bool(
            self.detected_more_than_once
            or self.scattered_sections
            or self.unaddressable
            or self.mostly_headless
        )

    @property
    def mostly_headless(self) -> bool:
        return bool(self.chunks and self.headless_chunks / self.chunks >= HEADLESS_DEGRADED_RATE)

    def quality(self) -> str:
        if self.fatal:
            return "failed"
        if self.degraded:
            return "degraded"
        return "ok"

    def failure_message(self) -> str:
        """Operator-readable, and readable by someone with no worker logs."""
        missing = self.in_toc_not_in_body
        extra = self.in_body_not_in_toc
        parts = [
            f"structure validation failed: the {self.structure_source} table of contents "
            f"and the document body disagree on {len(self.symmetric_difference)} section(s)."
        ]
        if missing:
            parts.append(
                f"{len(missing)} section(s) listed in the table of contents were not found "
                f"as headings in the body: {', '.join(missing[:20])}"
                + (" ..." if len(missing) > 20 else "")
            )
        if extra:
            parts.append(
                f"{len(extra)} heading(s) found in the body are absent from the table of "
                f"contents: {', '.join(extra[:20])}" + (" ..." if len(extra) > 20 else "")
            )
        parts.append(
            "The document was not indexed. Chunks would carry section paths that are not "
            "known to match the document."
        )
        return " ".join(parts)

    def to_json(self) -> str:
        payload = asdict(self)
        payload["quality"] = self.quality()
        payload["addressability"] = round(self.addressability, 3)
        payload["notes"] = self.notes()
        return json.dumps(payload, sort_keys=True)


def from_diagnostics(diagnostics: dict[str, Any]) -> StructureReport:
    xv = diagnostics.get("cross_validation") or {}
    return StructureReport(
        structure_source=str(diagnostics.get("structure_source", "unknown")),
        toc_sections=int(xv.get("toc_sections", 0)),
        body_sections=int(xv.get("body_sections", 0)),
        in_toc_not_in_body=list(xv.get("in_toc_not_in_body", [])),
        in_body_not_in_toc=list(xv.get("in_body_not_in_toc", [])),
        detected_more_than_once=list(xv.get("detected_more_than_once", [])),
        candidates_rejected_by_ordering=list(
            diagnostics.get("candidates_rejected_by_ordering", [])
        ),
    )


def scattered_sections(chunks: list[Any]) -> list[str]:
    """Sections whose chunks are not contiguous in document order.

    A section was either subdivided -- which yields adjacent ordinals -- or a
    boundary misfired and scattered one section key across the document. Only
    the second produces gaps.
    """
    by_section: dict[str, list[int]] = {}
    for c in chunks:
        if c.section:
            by_section.setdefault(c.section, []).append(c.ordinal)
    return sorted(
        section
        for section, ordinals in by_section.items()
        if len(ordinals) > 1 and ordinals != list(range(ordinals[0], ordinals[0] + len(ordinals)))
    )
