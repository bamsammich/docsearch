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


def estimate_tokens(text: str) -> int:
    """Approximate the BPE token count of ``text``."""
    return int(len(_ATOM.findall(text)) * SUBWORD_FACTOR)
