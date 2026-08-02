"""Format-agnostic chunking.

The chunk unit is the deepest *numbered* section a document declares. When a
format supplies no numbering (Markdown, HTML, DOCX, plain text) the unit falls
back to the deepest heading path. The chunker reads only the normalized
intermediate -- it never learns which adapter produced the blocks.

Two rules do the shaping:

* A unit over ``MAX_TOKENS`` subdivides, first at unnumbered subheadings and
  then at block boundaries.
* A unit under ``MIN_TOKENS`` merges forward into its next sibling -- but only
  when the unit is *not* an authoritative numbered section. A numbered section
  is a boundary the document itself declared; small sections stay small.
"""

from __future__ import annotations

import re
from collections import defaultdict
from dataclasses import dataclass, field, replace

from .blocks import Block, Extraction
from .tokens import count_atoms, estimate_tokens, tokens_from_atoms

MAX_TOKENS = 1200
MIN_TOKENS = 100
PATH_SEP = " > "

#: A parent section whose own text is under this and whose next unit is its own
#: child is a chapter stub -- a title plus a sentence of preamble. It folds into
#: that child rather than standing as a chunk. BM25 length normalization scores
#: short documents generously, so an 18-token chapter title would otherwise
#: outrank the subsection that actually answers the query.
STUB_MAX_TOKENS = 30


#: A section family this large that the document itself labels a keyword list
#: is a reference index, not a topic.
REFERENCE_FAMILY_MIN = 30

#: The parent heading must say so. Size alone is not enough: in the grandMA2
#: manual chapter 7 ("Keys & Buttons on the Console") has 84 siblings of which
#: 87% end in "Key", and those are exactly the pages an operator asking about
#: the Flash or B.O. key needs. Only "10.2. All keywords" -- a command-line
#: syntax reference the document names as such -- is the reference index.
#: Classifying on shape alone would bury real prose, which is worse than the
#: problem being solved.
_REFERENCE_PARENT = re.compile(r"(?i)\bkeywords?\b")

#: Most members of a reference family look like entries.
_REFERENCE_LEAF = re.compile(r"(?i)\b(keyword|key)\s*$")
_REFERENCE_LEAF_RATE = 0.6


@dataclass(slots=True)
class Chunk:
    ordinal: int
    heading_path: str
    text: str
    section: str | None = None
    page_start: int | None = None
    page_end: int | None = None
    printed_page_start: int | None = None
    image_count: int = 0
    #: 'prose' | 'keyword-reference'
    kind: str = "prose"


@dataclass(slots=True)
class _Unit:
    """Consecutive blocks sharing one chunk key."""

    key: tuple[str, ...] | str
    section: str | None
    heading_path: list[str]
    blocks: list[Block] = field(default_factory=list)

    @property
    def authoritative(self) -> bool:
        """True when the document declared this boundary via a section number."""
        return self.section is not None

    @property
    def text(self) -> str:
        return "\n\n".join(b.text for b in self.blocks).strip()

    @property
    def tokens(self) -> int:
        return estimate_tokens(self.text)


def _group(blocks: list[Block]) -> list[_Unit]:
    units: list[_Unit] = []
    for b in blocks:
        key: tuple[str, ...] | str = b.section if b.section is not None else tuple(b.heading_path)
        if units and units[-1].key == key:
            units[-1].blocks.append(b)
            continue
        units.append(
            _Unit(key=key, section=b.section, heading_path=list(b.heading_path), blocks=[b])
        )
    return units


def _same_parent(a: _Unit, b: _Unit) -> bool:
    return a.heading_path[:-1] == b.heading_path[:-1]


def _merge_small(units: list[_Unit]) -> list[_Unit]:
    """Merge sub-``MIN_TOKENS`` units forward, never across a declared boundary."""
    out: list[_Unit] = []
    i = 0
    while i < len(units):
        cur = units[i]
        if cur.authoritative or cur.tokens >= MIN_TOKENS:
            out.append(cur)
            i += 1
            continue
        j = i + 1
        while (
            j < len(units)
            and cur.tokens < MIN_TOKENS
            and not units[j].authoritative
            and _same_parent(cur, units[j])
        ):
            cur = _Unit(
                key=cur.key,
                section=None,
                heading_path=cur.heading_path[:-1] or cur.heading_path,
                blocks=cur.blocks + units[j].blocks,
            )
            j += 1
        out.append(cur)
        i = max(j, i + 1)
    return out


def _split_oversized(unit: _Unit) -> list[tuple[list[str], list[Block]]]:
    """Subdivide at unnumbered subheadings, then at block boundaries."""
    parts: list[tuple[list[str], list[Block]]] = []

    # Pass 1: unnumbered subheadings the adapter marked.
    runs: list[tuple[str | None, list[Block]]] = []
    for b in unit.blocks:
        if b.subdivision and not b.figure_only:
            label = b.text.splitlines()[0].strip() if b.text else None
            runs.append((label, [b]))
        elif runs:
            runs[-1][1].append(b)
        else:
            runs.append((None, [b]))

    if len(runs) > 1:
        # Pack consecutive runs up to the cap. Splitting once per subheading
        # would shatter a 1300-token section into eight 160-token fragments;
        # the rule is "subdivide when over MAX_TOKENS", not "subdivide at every
        # subheading of a section that happens to be over it".
        packed: list[tuple[list[str | None], list[Block]]] = []
        cur_labels: list[str | None] = []
        cur_blocks: list[Block] = []
        cur_atoms = 0
        for label, blks in runs:
            run_atoms = sum(count_atoms(b.text) for b in blks)
            if cur_blocks and tokens_from_atoms(cur_atoms + run_atoms) > MAX_TOKENS:
                packed.append((cur_labels, cur_blocks))
                cur_labels, cur_blocks, cur_atoms = [], [], 0
            cur_labels.append(label)
            cur_blocks.extend(blks)
            cur_atoms += run_atoms
        if cur_blocks:
            packed.append((cur_labels, cur_blocks))

        for labels, blks in packed:
            named = [x for x in labels if x]
            # Only claim a subheading in the path when the part is exactly that
            # subheading; a part spanning several must not misattribute itself.
            path = unit.heading_path + (named if len(named) == 1 else [])
            parts.append((path, blks))
    else:
        parts.append((unit.heading_path, list(unit.blocks)))

    # Pass 2: anything still oversized splits greedily at block boundaries, and
    # any single block that is itself oversized splits at paragraph boundaries.
    final: list[tuple[list[str], list[Block]]] = []
    for path, blks in parts:
        if estimate_tokens("\n\n".join(b.text for b in blks)) <= MAX_TOKENS:
            final.append((path, blks))
            continue
        expanded = [sb for b in blks for sb in _split_block(b)]
        cur: list[Block] = []
        cur_atoms = 0
        for b in expanded:
            b_atoms = count_atoms(b.text)
            if cur and tokens_from_atoms(cur_atoms + b_atoms) > MAX_TOKENS:
                final.append((path, cur))
                cur, cur_atoms = [], 0
            cur.append(b)
            cur_atoms += b_atoms
        if cur:
            final.append((path, cur))
    return final


def _split_block(block: Block) -> list[Block]:
    """Split one oversized block at paragraph, then line, boundaries.

    Without this a section holding a single enormous block -- a densely packed
    reference table, a back-of-book index -- has no interior boundary to cut on
    and escapes MAX_TOKENS entirely.
    """
    if estimate_tokens(block.text) <= MAX_TOKENS:
        return [block]
    units = block.text.split("\n\n")
    if len(units) == 1:
        units = block.text.splitlines()
    out: list[Block] = []
    cur: list[str] = []
    cur_atoms = 0
    for unit in units:
        unit_atoms = count_atoms(unit)
        if cur and tokens_from_atoms(cur_atoms + unit_atoms) > MAX_TOKENS:
            out.append(replace(block, text="\n".join(cur)))
            cur, cur_atoms = [], 0
        cur.append(unit)
        cur_atoms += unit_atoms
    if cur:
        out.append(replace(block, text="\n".join(cur)))
    return out


def _emit(ordinal: int, path: list[str], blks: list[Block], section: str | None) -> Chunk:
    pages = [b.locator["page"] for b in blks if "page" in b.locator]
    page_ends = [b.locator.get("page_end", b.locator["page"]) for b in blks if "page" in b.locator]
    printed = [b.printed_page for b in blks if b.printed_page is not None]
    text = "\n\n".join(b.text for b in blks).strip()
    return Chunk(
        ordinal=ordinal,
        heading_path=PATH_SEP.join(p for p in path if p),
        text=text,
        section=section,
        page_start=min(pages) if pages else None,
        page_end=max(page_ends) if page_ends else None,
        printed_page_start=min(printed) if printed else None,
        image_count=sum(b.image_count for b in blks),
    )


def _is_descendant(child: str | None, parent: str | None) -> bool:
    return bool(child and parent and child.startswith(parent + "."))


def _fold_chapter_stubs(units: list[_Unit]) -> list[_Unit]:
    """Fold a parent section's thin preamble into its first child."""
    out: list[_Unit] = []
    i = 0
    while i < len(units):
        cur = units[i]
        nxt = units[i + 1] if i + 1 < len(units) else None
        if (
            nxt is not None
            and cur.authoritative
            and _is_descendant(nxt.section, cur.section)
            and cur.tokens < STUB_MAX_TOKENS
        ):
            units[i + 1] = _Unit(
                key=nxt.key,
                section=nxt.section,
                heading_path=nxt.heading_path,
                blocks=cur.blocks + nxt.blocks,
            )
            i += 1
            continue
        out.append(cur)
        i += 1
    return out


def classify_kinds(chunks: list[Chunk]) -> None:
    """Mark chunks belonging to a self-declared keyword reference index.

    Such a family is term-dense and low-prose, so its entries surface for any
    query sharing a token with a keyword name -- "step by step" pulling up
    StepOut, StepIn and StepFade. That is a chunk-population artifact, not a
    vocabulary gap, and it crowds out real answers.

    They are marked, never dropped: a keyword lookup is a legitimate query and
    these are its correct answers.
    """
    families: defaultdict[str, list[Chunk]] = defaultdict(list)
    for c in chunks:
        if not c.section or "." not in c.section:
            continue
        families[c.section.rsplit(".", 1)[0]].append(c)

    for members in families.values():
        if len(members) < REFERENCE_FAMILY_MIN:
            continue
        parts = members[0].heading_path.split(PATH_SEP)
        parent_heading = parts[-2] if len(parts) >= 2 else ""
        if not _REFERENCE_PARENT.search(parent_heading):
            continue
        leaves = [m.heading_path.split(PATH_SEP)[-1] for m in members]
        matched = sum(1 for leaf in leaves if _REFERENCE_LEAF.search(leaf))
        if matched / len(members) < _REFERENCE_LEAF_RATE:
            continue
        for m in members:
            m.kind = "keyword-reference"


def chunk(extraction: Extraction) -> list[Chunk]:
    units = _fold_chapter_stubs(_merge_small(_group(extraction.blocks)))
    chunks: list[Chunk] = []
    for unit in units:
        if not unit.text:
            continue
        if unit.tokens <= MAX_TOKENS:
            chunks.append(_emit(len(chunks), unit.heading_path, unit.blocks, unit.section))
            continue
        for path, blks in _split_oversized(unit):
            if any(b.text.strip() for b in blks):
                chunks.append(_emit(len(chunks), path, blks, unit.section))
    classify_kinds(chunks)
    return chunks
