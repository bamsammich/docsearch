"""The address-validation boundary, and its agreement with the Go half."""

from __future__ import annotations

import ipaddress
from pathlib import Path

import pytest

from docsearch.urlguard import BlockedURLError, addr_allowed, check, host_allowed

VECTORS = Path(__file__).resolve().parents[1] / "testdata/urlguard-addresses.txt"


def _vectors() -> list[tuple[str, bool, str]]:
    out: list[tuple[str, bool, str]] = []
    for line in VECTORS.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        fields = line.split(maxsplit=2)
        assert len(fields) >= 2, f"malformed vector line: {line!r}"
        out.append((fields[0], fields[1] == "allow", fields[2] if len(fields) > 2 else ""))
    return out


def test_addr_verdicts_match_the_shared_table() -> None:
    """The table is the contract between this guard and ``internal/urlguard``.

    A verdict that differs between them means one process fetches what the
    other refuses, which is the drift the file exists to prevent.
    """
    vectors = _vectors()
    # A truncated or unreadable file would otherwise pass by testing nothing.
    assert len(vectors) >= 40, f"only {len(vectors)} vectors; the table should hold far more"
    for raw, want, why in vectors:
        assert addr_allowed(raw) is want, f"addr_allowed({raw}) should be {want}: {why}"


def _resolver(**mapping: list[str]):
    table = {h.replace("_", "."): [ipaddress.ip_address(a) for a in v] for h, v in mapping.items()}

    def resolve(host: str) -> list[ipaddress.IPv4Address | ipaddress.IPv6Address]:
        return table.get(host, [])

    return resolve


def test_public_host_resolves() -> None:
    target = check("https://docs.example.com/guide", _resolver(docs_example_com=["93.184.216.34"]))
    assert [str(a) for a in target.addrs] == ["93.184.216.34"]


@pytest.mark.parametrize(
    "answers",
    [
        ["93.184.216.34", "169.254.169.254"],
        ["169.254.169.254", "93.184.216.34"],
        ["93.184.216.34", "10.0.0.5"],
        ["93.184.216.34", "::ffff:127.0.0.1"],
    ],
)
def test_a_host_resolving_to_any_blocked_address_is_refused(answers: list[str]) -> None:
    """Accepting a host because one record is public leaves which address gets
    dialled up to resolver ordering."""
    with pytest.raises(BlockedURLError):
        check("https://docs.example.com", _resolver(docs_example_com=answers))


def test_unresolvable_or_empty_answer_is_refused() -> None:
    with pytest.raises(BlockedURLError):
        check("https://nope.example.com", _resolver())
    with pytest.raises(BlockedURLError):
        check("https://docs.example.com", _resolver(docs_example_com=[]))


@pytest.mark.parametrize(
    "raw",
    [
        "http://127.0.0.1/x",
        "http://169.254.169.254/latest/meta-data/",
        "http://[::1]/x",
        "http://[::ffff:127.0.0.1]/x",
        "http://10.0.0.1/x",
        "http://100.64.1.1/x",
    ],
)
def test_blocked_address_literals_are_refused(raw: str) -> None:
    with pytest.raises(BlockedURLError):
        check(raw, _resolver())


def test_public_literal_needs_no_resolution() -> None:
    target = check("http://93.184.216.34/x", _resolver())
    assert [str(a) for a in target.addrs] == ["93.184.216.34"]


@pytest.mark.parametrize(
    "raw",
    [
        "file:///etc/passwd",
        "ftp://docs.example.com/x",
        "gopher://docs.example.com/x",
        "data:text/html,hi",
        "jar:https://docs.example.com/x!/y",
        "//docs.example.com/x",
    ],
)
def test_only_http_schemes_are_fetched(raw: str) -> None:
    with pytest.raises(BlockedURLError):
        check(raw, _resolver(docs_example_com=["93.184.216.34"]))


def test_http_and_https_are_accepted_case_insensitively() -> None:
    for raw in ("http://docs.example.com", "HTTPS://docs.example.com"):
        check(raw, _resolver(docs_example_com=["93.184.216.34"]))


def test_credentials_in_the_url_are_refused() -> None:
    """A common way to make a reader see one host while the request goes to
    another."""
    with pytest.raises(BlockedURLError):
        check(
            "https://docs.example.com@evil.example.com/x",
            _resolver(evil_example_com=["93.184.216.34"]),
        )


@pytest.mark.parametrize(
    "host",
    ["printer.local", "wiki.internal", "localhost", "app.localhost", "nas.home.arpa"],
)
def test_local_name_spaces_are_refused_before_resolving(host: str) -> None:
    """These names only ever mean a local network, and may resolve to anything
    at all on a DNS the operator does not control."""
    assert host_allowed(host) is False
    with pytest.raises(BlockedURLError):
        check(f"https://{host}/x", _resolver())


def test_the_suffix_rule_matches_a_label_boundary() -> None:
    """local.dev is an ordinary public name, not a local one."""
    assert host_allowed("docs.local.dev") is True
    check("https://docs.local.dev/x", _resolver(docs_local_dev=["93.184.216.34"]))


def test_trailing_dot_does_not_evade_the_suffix_rule() -> None:
    assert host_allowed("printer.local.") is False


def test_rejection_does_not_disclose_why() -> None:
    """Otherwise the fetcher reports which internal names exist."""
    messages = set()
    for raw in (
        "https://private.example.com/x",
        "https://absent.example.com/x",
        "http://169.254.169.254/x",
        "https://printer.local/x",
        "file:///etc/passwd",
    ):
        with pytest.raises(BlockedURLError) as exc:
            check(raw, _resolver(private_example_com=["10.0.0.1"]))
        messages.add(str(exc.value))
    assert len(messages) == 1, f"error messages differ and leak the reason: {messages}"
