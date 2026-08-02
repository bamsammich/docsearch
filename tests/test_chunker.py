"""Chunker behaviour, exercised through the public normalized intermediate."""

from __future__ import annotations

from docsearch.blocks import Block, Extraction
from docsearch.chunker import MAX_TOKENS, MIN_TOKENS, chunk
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
