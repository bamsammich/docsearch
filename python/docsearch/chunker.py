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
#:
#: Measured, not assumed. Across two corpora the two families are separable on
#: no structural axis: the reference index has a *higher* stopword rate than
#: the prose chapter (0.33 against 0.30) and *longer* entries (122 tokens
#: against 65), their leaf headings are both a 2-word median with 99% of each
#: leaf's terms echoed in its body, and the same command appears in both --
#: "[GoFastForward] Key" as prose, "[GoFastForward] keyword" as reference.
#: Sibling count is the only feature with any gap, and thresholding on it
#: would fit one corpus. The document's own declaration is the whole signal.
#:
#: So the vocabulary is broadened rather than replaced. Each alternative names
#: a listing of entries, not a topic: a document with a "Glossary" or an "Error
#: Codes" chapter of this size is declaring the same thing "All keywords" does.
#: False positives bury real prose, so the list stays conservative and a term
#: that merely *could* head a topic chapter -- "Reference", "Appendix",
#: "Commands" -- is deliberately absent.
_REFERENCE_PARENT = re.compile(
    r"(?i)\b(keywords?|glossary|error\s+(codes?|messages?)"
    r"|(command|api|syntax|function)\s+(reference|index|listing))\b"
)

#: Members of a reference family are named entries rather than described ones.
#: A leaf heading is a term, a command or a code -- not a sentence. Counted in
#: alphabetic words so that a section number in the heading does not register.
_REFERENCE_LEAF_MAX_WORDS = 4
_REFERENCE_LEAF_RATE = 0.6
_LEAF_WORD = re.compile(r"[^\W\d_]+")


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
    #: Address this chunk was read from; both None for a local file.
    url: str | None = None
    fragment: str | None = None


@dataclass(slots=True)
class _Unit:
    """Consecutive blocks sharing one chunk key."""

    key: tuple[str | None, tuple[str, ...]]
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
    """Consecutive blocks sharing both a section and a heading path.

    Keyed on the pair, not on the section alone. A unit keeps only its first
    block's heading path, so keying on the section discards the path of every
    block after it -- invisible while a section and a heading path are the same
    thing, which is true of every paginated format here: the path is built from
    the section's own ancestry, so it cannot vary within one.

    It is not true of a site. There the section is the page's position in the
    navigation and is deliberately constant across the whole page, so a page
    collapsed to one unit and every chunk of it came back carrying the page
    title alone. Measured on five documentation sites, that cost pytest's
    getting-started page its nine internal headings and left 377 chunks sharing
    57 heading paths -- the shape `unaddressable` is meant to catch, produced by
    the chunker rather than by the document.
    """
    units: list[_Unit] = []
    for b in blocks:
        key = (b.section, tuple(b.heading_path))
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
        # Taken from the block the chunk starts at. A chunk may span several
        # blocks, and where it begins is where a citation should land.
        url=blks[0].url,
        fragment=blks[0].fragment,
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
    # Grouped by heading-path parent rather than by section number. Grouping by
    # number made the whole mechanism unreachable for any document that does
    # not number its sections -- Markdown, HTML, DOCX and every PDF whose
    # structure comes from an outline -- so a glossary in one of those was
    # never classified however plainly the document declared it.
    # Keyed on the shallowest ancestor the document declares as a reference
    # listing, not on the immediate parent. A long entry that subdivision split
    # into "Syntax" and "Options" sits one level deeper than its siblings and
    # would otherwise form its own family of two, below any size threshold --
    # dropping exactly the entries whose length made them worth splitting.
    families: defaultdict[str, list[Chunk]] = defaultdict(list)
    for c in chunks:
        parts = c.heading_path.split(PATH_SEP)
        for depth in range(1, len(parts)):
            if _REFERENCE_PARENT.search(parts[depth - 1]):
                families[PATH_SEP.join(parts[:depth])].append(c)
                break

    for members in families.values():
        if len(members) < REFERENCE_FAMILY_MIN:
            continue
        leaves = [m.heading_path.split(PATH_SEP)[-1] for m in members]
        named = sum(
            1 for leaf in leaves if len(_LEAF_WORD.findall(leaf)) <= _REFERENCE_LEAF_MAX_WORDS
        )
        if named / len(members) < _REFERENCE_LEAF_RATE:
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
