"""Structural quality findings, and the policy that acts on them.

Diagnostics printed to a terminal are lost when the worker runs headless, so
findings are captured as data, persisted on the job and the document, and
surfaced through the status and listing tools.
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from typing import Any

#: Structure sources for which a TOC exists to validate the body against.
_VALIDATABLE = ("outline", "front_toc")


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

    @property
    def validatable(self) -> bool:
        return self.structure_source in _VALIDATABLE

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
        return bool(self.detected_more_than_once or self.scattered_sections)

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
