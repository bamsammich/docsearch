"""Token estimation across scripts.

No tokenizer is available offline by design, so these assert properties rather
than exact BPE counts: that Latin calibration is unchanged, that scripts
written without spaces are not under-counted, and that the accumulate-then-
convert contract the chunker depends on still holds.
"""

from __future__ import annotations

import pytest

from docsearch.tokens import (
    count_atoms,
    estimate_tokens,
    tokens_from_atoms,
    uncalibrated_letter_share,
)

ENGLISH = "The console stores each cue in a sequence and plays it back on an executor. "
JAPANESE = "ミキシングコンソールはチャンネルごとに信号を処理します。"
CHINESE = "调音台按通道处理信号。请操作推子来调整音量。"
KOREAN = "믹싱 콘솔은 채널별로 신호를 처리합니다."
RUSSIAN = "Микшерная консоль обрабатывает сигнал по каждому каналу отдельно. "


def test_latin_calibration_is_unchanged() -> None:
    """Guards the corpus this estimator was measured against.

    Latin technical prose runs about four characters per BPE token. Moving
    this silently re-sizes every chunk in every existing index.
    """
    text = ENGLISH * 20
    assert 3.0 <= len(text) / estimate_tokens(text) <= 4.5


@pytest.mark.parametrize("text", [JAPANESE, CHINESE, KOREAN])
def test_space_free_scripts_are_not_under_counted(text: str) -> None:
    """A character in these scripts is roughly one token.

    Word-atom counting reads a whole run as one atom. The resulting estimate
    is low, which is the dangerous direction: a chunk that under-counts never
    trips the subdivision cap and lands oversized.
    """
    body = text * 20
    chars_per_token = len(body) / estimate_tokens(body)
    assert chars_per_token <= 1.6, f"{chars_per_token:.1f} chars/token is an under-count"


def test_a_cjk_character_is_about_one_token() -> None:
    han = "調整音量信号処理" * 40
    assert 0.8 <= estimate_tokens(han) / len(han) <= 1.4


def test_mixed_script_text_counts_both_halves() -> None:
    mixed = ENGLISH * 5 + JAPANESE * 5
    assert estimate_tokens(mixed) > estimate_tokens(ENGLISH * 5)
    assert estimate_tokens(mixed) > estimate_tokens(JAPANESE * 5)


def test_ideograph_runs_are_not_double_counted() -> None:
    """Removing CJK before word-atom counting must not leave the run behind."""
    han = "信号処理"
    assert count_atoms(han) == count_atoms(han + " ") == round(4 / 1.3)


def test_accumulate_then_convert_matches_direct_estimation() -> None:
    """The chunker sums atoms across blocks and converts once at the end.

    Summing estimate_tokens instead compounds int truncation into a budget
    check that silently never trips, so the two must agree.
    """
    pieces = [ENGLISH, JAPANESE, CHINESE, ENGLISH, KOREAN]
    assert tokens_from_atoms(sum(count_atoms(p) for p in pieces)) == estimate_tokens(
        "".join(pieces)
    )


def test_uncalibrated_scripts_are_reported() -> None:
    assert uncalibrated_letter_share(RUSSIAN * 5) > 0.9
    assert uncalibrated_letter_share(ENGLISH * 5) == 0.0
    assert uncalibrated_letter_share(JAPANESE * 5) == 0.0
    assert uncalibrated_letter_share("") == 0.0


def test_half_russian_text_reports_a_partial_share() -> None:
    share = uncalibrated_letter_share(ENGLISH * 3 + RUSSIAN * 3)
    assert 0.2 < share < 0.8
