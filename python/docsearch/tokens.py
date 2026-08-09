"""Offline token estimation.

Deliberately dependency-free: ingest must never make a network call, which
rules out tiktoken's first-use BPE download. Every threshold in the chunker is
approximate ("~1,200 tokens"), so a calibrated estimate is sufficient.

The estimator counts word and punctuation atoms and scales by a subword factor.
Measured against a Latin-script technical corpus this tracks BPE counts within
~10%.

Error direction matters more than error size. Under-counting is silent and
compounding: a chunk whose tokens are under-counted never trips the
subdivision cap, so it lands oversized and every size-based check downstream
reads a figure in the wrong unit. Over-counting only yields chunks smaller
than they needed to be. Where this file guesses, it guesses high.
"""

import re

_ATOM = re.compile(r"\w+|[^\w\s]")

#: Scripts written without spaces, where a character is roughly one BPE token
#: on its own: Han and its extensions, kana, Hangul syllables, CJK punctuation.
#: Word-atom counting reads an entire run of these as a single atom, which
#: under-counts by roughly 9x on Japanese and 4x on Chinese.
_CJK = re.compile(
    "["
    "\u3000-\u303f"  # CJK punctuation
    "\u3040-\u30ff"  # hiragana and katakana
    "\u3400-\u4dbf"  # Han, extension A
    "\u4e00-\u9fff"  # Han
    "\uf900-\ufaff"  # Han, compatibility ideographs
    "\uac00-\ud7af"  # Hangul syllables
    "]"
)

#: Letters in scripts this estimator has no calibration for. Cyrillic and Greek
#: are known to split more aggressively than Latin, and Arabic, Hebrew,
#: Devanagari and Thai are simply unmeasured here. Rather than invent a factor
#: that cannot be validated offline, their presence is reported so a document
#: relying on one is visible instead of silently mis-sized.
_UNCALIBRATED = re.compile(
    "["
    "\u0370-\u03ff"  # Greek
    "\u0400-\u04ff"  # Cyrillic
    "\u0530-\u058f"  # Armenian
    "\u0590-\u05ff"  # Hebrew
    "\u0600-\u06ff"  # Arabic
    "\u0900-\u097f"  # Devanagari
    "\u0e00-\u0e7f"  # Thai
    "\u10a0-\u10ff"  # Georgian
    "]"
)

_LETTER = re.compile(r"[^\W\d_]")

#: Average BPE sub-token splits per whitespace/punctuation atom for English
#: technical prose. Raising this makes the chunker more conservative.
SUBWORD_FACTOR = 1.3

#: One CJK character is about one token. Expressed relative to SUBWORD_FACTOR
#: so that count_atoms keeps returning a value tokens_from_atoms can scale,
#: preserving the accumulate-then-convert contract the chunker depends on.
_CJK_ATOM_WEIGHT = 1.0 / SUBWORD_FACTOR


def count_atoms(text: str) -> int:
    """Raw word/punctuation atoms in ``text``, before the subword factor.

    Accumulate these -- never summed :func:`estimate_tokens` results -- when
    measuring a sequence of pieces against a budget. ``estimate_tokens``
    truncates to int, so summing it over hundreds of short pieces compounds
    the rounding loss into hundreds of missing tokens and a budget check that
    silently never trips.
    """
    cjk = len(_CJK.findall(text))
    if not cjk:
        return len(_ATOM.findall(text))
    # Counted separately, then removed, so a run of ideographs is not also
    # counted once more as a single word atom.
    rest = len(_ATOM.findall(_CJK.sub(" ", text)))
    return rest + round(cjk * _CJK_ATOM_WEIGHT)


def tokens_from_atoms(atoms: int) -> int:
    """Convert an accumulated atom count to an estimated token count."""
    return int(atoms * SUBWORD_FACTOR)


def estimate_tokens(text: str) -> int:
    """Approximate the BPE token count of ``text``."""
    return tokens_from_atoms(count_atoms(text))


def uncalibrated_letter_share(text: str) -> float:
    """Share of letters written in a script this estimator cannot size.

    Latin and CJK are accounted for. Everything else -- Cyrillic, Greek,
    Arabic, Hebrew, Devanagari, Thai -- falls back to Latin word-atom counting,
    which is known to under-count them by a factor this project has no offline
    way to measure. A document made mostly of such text has chunk sizes, and
    therefore every size threshold applied to it, in an unknown unit.
    """
    letters = len(_LETTER.findall(text))
    if not letters:
        return 0.0
    return len(_UNCALIBRATED.findall(text)) / letters
