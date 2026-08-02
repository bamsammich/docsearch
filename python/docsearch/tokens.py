"""Offline token estimation.

Deliberately dependency-free: ingest must never make a network call, which
rules out tiktoken's first-use BPE download. Every threshold in the chunker is
approximate ("~1,200 tokens"), so a calibrated estimate is sufficient.

The estimator counts word and punctuation atoms and scales by a subword factor.
Measured against the grandMA2 corpus this tracks BPE counts within ~10%.
"""

import re

_ATOM = re.compile(r"\w+|[^\w\s]")

#: Average BPE sub-token splits per whitespace/punctuation atom for English
#: technical prose. Raising this makes the chunker more conservative.
SUBWORD_FACTOR = 1.3


def count_atoms(text: str) -> int:
    """Raw word/punctuation atoms in ``text``, before the subword factor.

    Accumulate these -- never summed :func:`estimate_tokens` results -- when
    measuring a sequence of pieces against a budget. ``estimate_tokens``
    truncates to int, so summing it over hundreds of short pieces compounds
    the rounding loss into hundreds of missing tokens and a budget check that
    silently never trips.
    """
    return len(_ATOM.findall(text))


def tokens_from_atoms(atoms: int) -> int:
    """Convert an accumulated atom count to an estimated token count."""
    return int(atoms * SUBWORD_FACTOR)


def estimate_tokens(text: str) -> int:
    """Approximate the BPE token count of ``text``."""
    return tokens_from_atoms(count_atoms(text))
