"""Failure taxonomy.

The worker retries transient failures and refuses to retry deterministic ones.
That decision cannot be made from an error message, so it is carried by type:
anything deriving from :class:`PermanentIngestError` will fail identically on
attempt two and is failed immediately rather than burning the retry budget.
"""

from __future__ import annotations


class DocsearchError(Exception):
    """Base for every error this package raises deliberately."""


class PermanentIngestError(DocsearchError):
    """A failure that will recur identically on retry. Never retried."""


class UnsupportedFormatError(PermanentIngestError, ValueError):
    """No adapter is registered for the file's suffix."""


class StructureValidationError(PermanentIngestError):
    """The derived heading structure failed validation.

    Raised when the reconstructed structure and the body disagree. The
    document is not indexed: a heading tree that does not match the body
    produces chunks attributed to the wrong sections, and a search answering
    from the wrong section is worse than one answering nothing.
    """


class IngestCancelled(DocsearchError):
    """Cancellation was requested and observed at a checkpoint."""
