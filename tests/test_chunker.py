"""Chunker behaviour, exercised through the public normalized intermediate."""

from __future__ import annotations

from docsearch.blocks import Block, Extraction
from docsearch.chunker import (
    MAX_TOKENS,
    MIN_TOKENS,
    PATH_SEP,
    Chunk,
    chunk,
    classify_kinds,
)
from docsearch.tokens import estimate_tokens


def _extraction(blocks: list[Block]) -> Extraction:
    return Extraction(title="t", format="test", blocks=blocks)


def _para(n: int) -> str:
    return "word " * n


def test_numbered_sections_are_never_merged_however_small() -> None:
    """A declared section boundary survives even when both sides are tiny."""
    blocks = [
        Block(heading_path=["1. A", "1.1. First"], locator={"page": 1}, text="tiny", section="1.1"),
        Block(
            heading_path=["1. A", "1.2. Second"], locator={"page": 1}, text="also", section="1.2"
        ),
    ]
    chunks = chunk(_extraction(blocks))
    assert [c.section for c in chunks] == ["1.1", "1.2"]
    assert all(estimate_tokens(c.text) < MIN_TOKENS for c in chunks)


def test_unnumbered_small_blocks_merge_forward_under_shared_parent() -> None:
    blocks = [
        Block(heading_path=["Top", "One"], locator={"offset": 0}, text="tiny"),
        Block(heading_path=["Top", "Two"], locator={"offset": 1}, text="small"),
    ]
    chunks = chunk(_extraction(blocks))
    assert len(chunks) == 1
    assert "tiny" in chunks[0].text and "small" in chunks[0].text


def test_unnumbered_small_blocks_do_not_merge_across_parents() -> None:
    blocks = [
        Block(heading_path=["Alpha", "One"], locator={"offset": 0}, text="tiny"),
        Block(heading_path=["Beta", "Two"], locator={"offset": 1}, text="small"),
    ]
    chunks = chunk(_extraction(blocks))
    assert len(chunks) == 2


def test_oversized_section_subdivides_under_the_cap() -> None:
    blocks = [
        Block(
            heading_path=["1. A"],
            locator={"page": 1},
            text=_para(900),
            section="1",
            subdivision=(i > 0),
        )
        for i in range(4)
    ]
    chunks = chunk(_extraction(blocks))
    assert len(chunks) > 1
    assert all(estimate_tokens(c.text) <= MAX_TOKENS for c in chunks)
    assert all(c.section == "1" for c in chunks)


def test_single_oversized_block_is_split_at_paragraph_boundaries() -> None:
    """A section with no interior heading must still respect the cap."""
    body = "\n\n".join(_para(300) for _ in range(12))
    blocks = [Block(heading_path=["1. A"], locator={"page": 1}, text=body, section="1")]
    chunks = chunk(_extraction(blocks))
    assert len(chunks) > 1
    assert all(estimate_tokens(c.text) <= MAX_TOKENS for c in chunks)


def test_subdivision_does_not_fire_below_the_cap() -> None:
    """Under the cap a section stays whole even with subheadings inside it."""
    blocks = [
        Block(heading_path=["1. A"], locator={"page": 1}, text=_para(20), section="1"),
        Block(
            heading_path=["1. A"],
            locator={"page": 1},
            text=_para(20),
            section="1",
            subdivision=True,
        ),
    ]
    chunks = chunk(_extraction(blocks))
    assert len(chunks) == 1


def test_ordinals_are_dense_and_ascending() -> None:
    blocks = [
        Block(heading_path=[f"{i}. S"], locator={"page": i}, text=_para(60), section=str(i))
        for i in range(1, 8)
    ]
    chunks = chunk(_extraction(blocks))
    assert [c.ordinal for c in chunks] == list(range(len(chunks)))


def test_heading_path_is_full_ancestry_joined() -> None:
    blocks = [
        Block(
            heading_path=["5. System", "5.2. Units", "5.2.1. RPU"],
            locator={"page": 3},
            text=_para(50),
            section="5.2.1",
        )
    ]
    (c,) = chunk(_extraction(blocks))
    assert c.heading_path == "5. System > 5.2. Units > 5.2.1. RPU"


def test_locator_and_image_count_survive_into_the_chunk() -> None:
    blocks = [
        Block(
            heading_path=["1. A"],
            locator={"page": 10, "page_end": 11},
            text=_para(50),
            section="1",
            printed_page=9,
            image_count=3,
        ),
        Block(
            heading_path=["1. A"],
            locator={"page": 11, "page_end": 12},
            text=_para(50),
            section="1",
            printed_page=10,
            image_count=2,
        ),
    ]
    (c,) = chunk(_extraction(blocks))
    assert (c.page_start, c.page_end) == (10, 12)
    assert c.printed_page_start == 9
    assert c.image_count == 5


def test_many_short_lines_still_split_at_the_cap() -> None:
    """Budgeting must accumulate atoms, not summed token estimates.

    estimate_tokens truncates to int. Summing it over hundreds of short lines
    compounds the rounding loss -- a 1530-token block measured line-by-line
    sums to 1200 and the cap check never trips, so the block never splits.
    """
    body = "\n".join("Decimal 255 equals hex FF and octal 377" for _ in range(800))
    blocks = [Block(heading_path=["1. Table"], locator={"page": 1}, text=body, section="1")]
    chunks = chunk(_extraction(blocks))
    assert estimate_tokens(body) > MAX_TOKENS
    assert len(chunks) > 1
    assert all(estimate_tokens(c.text) <= MAX_TOKENS for c in chunks)


def test_chapter_stub_folds_into_its_first_child() -> None:
    """A thin chapter preamble must not compete with the subsection below it."""
    blocks = [
        Block(
            heading_path=["4. Devices"], locator={"page": 1}, text="A short lead-in.", section="4"
        ),
        Block(
            heading_path=["4. Devices", "4.1. Console"],
            locator={"page": 1},
            text=_para(120),
            section="4.1",
        ),
    ]
    chunks = chunk(_extraction(blocks))
    assert len(chunks) == 1
    assert chunks[0].section == "4.1"
    assert "A short lead-in." in chunks[0].text


def test_substantial_parent_section_keeps_its_own_chunk() -> None:
    blocks = [
        Block(heading_path=["4. Devices"], locator={"page": 1}, text=_para(200), section="4"),
        Block(
            heading_path=["4. Devices", "4.1. Console"],
            locator={"page": 1},
            text=_para(120),
            section="4.1",
        ),
    ]
    chunks = chunk(_extraction(blocks))
    assert [c.section for c in chunks] == ["4", "4.1"]


def test_stub_does_not_fold_into_a_non_descendant() -> None:
    """A short section followed by a sibling is not a stub -- it stays put."""
    blocks = [
        Block(heading_path=["4. Devices"], locator={"page": 1}, text="Short.", section="4"),
        Block(heading_path=["5. Network"], locator={"page": 2}, text=_para(120), section="5"),
    ]
    chunks = chunk(_extraction(blocks))
    assert [c.section for c in chunks] == ["4", "5"]


def _reference_family(parent: str, n: int, leaf: str = "Entry") -> list[Chunk]:
    return [
        Chunk(
            ordinal=i,
            heading_path=PATH_SEP.join(["Manual", parent, f"{leaf} {i}"]),
            text=f"Definition of term {i}. " * 8,
        )
        for i in range(n)
    ]


def test_a_glossary_in_an_unnumbered_document_is_classified() -> None:
    """The mechanism was unreachable for any document without section numbers.

    Families were keyed on the section number, so Markdown, HTML, DOCX and any
    PDF structured from an outline could never be classified however plainly
    the document declared a reference listing.
    """
    chunks = _reference_family("Glossary", 40)
    for c in chunks:
        assert c.section is None
    classify_kinds(chunks)
    assert all(c.kind == "keyword-reference" for c in chunks)


def test_subdivided_entries_stay_with_their_family() -> None:
    """A long entry split into parts sits a level deeper than its siblings.

    Keyed on the immediate parent it would form its own family of two, below
    any size threshold -- dropping exactly the entries long enough to split.
    """
    chunks = _reference_family("All keywords", 40)
    chunks += [
        Chunk(
            ordinal=100 + i,
            heading_path=PATH_SEP.join(["Manual", "All keywords", "Clone keyword", part]),
            text="Detail. " * 20,
        )
        for i, part in enumerate(["Syntax", "Options", "Examples"])
    ]
    classify_kinds(chunks)
    assert all(c.kind == "keyword-reference" for c in chunks)


def test_a_topic_chapter_of_uniform_siblings_is_left_alone() -> None:
    """Measured: the two are separable on no structural axis.

    A chapter of 84 sibling pages about physical keys looks exactly like a
    reference index by size, leaf length and term density. Only the parent
    heading distinguishes a listing from a topic, so only it decides.
    """
    chunks = _reference_family("Keys & Buttons on the Console", 84, leaf="Key")
    classify_kinds(chunks)
    assert all(c.kind == "prose" for c in chunks)


def test_a_small_declared_family_is_left_alone() -> None:
    chunks = _reference_family("Glossary", 12)
    classify_kinds(chunks)
    assert all(c.kind == "prose" for c in chunks)


def test_entries_described_in_sentences_are_not_a_reference_listing() -> None:
    """Leaf headings in a listing are named entries, not descriptions."""
    chunks = [
        Chunk(
            ordinal=i,
            heading_path=PATH_SEP.join(
                ["Manual", "Glossary", f"How to configure the {i} subsystem correctly"]
            ),
            text="Prose. " * 20,
        )
        for i in range(40)
    ]
    classify_kinds(chunks)
    assert all(c.kind == "prose" for c in chunks)


def test_one_section_keeps_its_internal_heading_paths() -> None:
    """A section held constant across differing headings must not collapse.

    Grouping on the section alone kept only the first block's heading path and
    discarded the rest. That is invisible for a paginated format, where the
    path is built from the section's own ancestry and cannot vary within one --
    and wrong for a site, where the section is the page's position in the
    navigation and is constant across every heading on the page.
    """
    blocks = [
        Block(heading_path=["Page"], locator={"offset": 0}, text=_para(40), section="3.2"),
        Block(
            heading_path=["Page", "Install"], locator={"offset": 1}, text=_para(40), section="3.2"
        ),
        Block(
            heading_path=["Page", "Configure"], locator={"offset": 2}, text=_para(40), section="3.2"
        ),
    ]
    chunks = chunk(_extraction(blocks))
    assert [c.heading_path for c in chunks] == [
        "Page",
        "Page > Install",
        "Page > Configure",
    ]
    assert all(c.section == "3.2" for c in chunks), "the declared section is unchanged"


def test_a_paginated_section_is_grouped_exactly_as_before() -> None:
    """The guard on the change above: same section, same path, still one unit."""
    blocks = [
        Block(
            heading_path=["5. Sys", "5.2. Units"],
            locator={"page": 3},
            text=_para(40),
            section="5.2",
        )
        for _ in range(4)
    ]
    chunks = chunk(_extraction(blocks))
    assert len(chunks) == 1
    assert chunks[0].heading_path == "5. Sys > 5.2. Units"
