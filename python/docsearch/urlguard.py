"""The address-validation boundary for fetching.

A security boundary, not a convenience check. A URL reaches the worker through
a job row, and a job row is not proof that anything validated it -- the Go tool
boundary checks what it enqueues, but nothing stops a row arriving another way.
The server can reach what the caller cannot (a metadata service, an admin page,
a printer) and everything it fetches becomes searchable, so a fetch of an
internal address is an exfiltration channel.

Three rules follow:

* What may be fetched is decided here, not by the caller.
* A host is judged by every address it resolves to, not the first. A name
  answering with one public and one private address is refused, or the outcome
  depends on which record arrived first.
* The addresses are returned so the caller connects to what was checked.
  Resolving twice -- once to validate, once to dial -- is a rebinding hole.

Every rejection raises the same error. Distinguishing "private address" from
"does not resolve" turns the fetcher into a network scanner.

The Go half is ``internal/urlguard``. ``testdata/urlguard-addresses.txt`` is
the shared table both must agree on.
"""

from __future__ import annotations

import ipaddress
import socket
from dataclasses import dataclass
from urllib.parse import urlsplit

__all__ = ["BlockedURLError", "Target", "addr_allowed", "check", "host_allowed"]

IPAddress = ipaddress.IPv4Address | ipaddress.IPv6Address


class BlockedURLError(Exception):
    """One error for every rejection, whatever the reason."""

    def __init__(self) -> None:
        super().__init__("url is not permitted")


#: Ranges that must never be fetched, beyond what the stdlib predicates cover.
#:
#: Written out rather than inferred: ``ipaddress.is_private`` and Go's
#: ``netip.Addr.IsPrivate`` disagree about what counts, and this boundary is
#: enforced in two languages that have to agree exactly.
_BLOCKED_NETWORKS = tuple(
    ipaddress.ip_network(cidr)
    for cidr in (
        "100.64.0.0/10",  # CGNAT, and where Tailscale lives
        "192.0.0.0/24",  # IETF protocol assignments
        "192.0.2.0/24",  # TEST-NET-1
        "192.88.99.0/24",  # 6to4 relay anycast
        "198.18.0.0/15",  # benchmarking
        "198.51.100.0/24",  # TEST-NET-2
        "203.0.113.0/24",  # TEST-NET-3
        "240.0.0.0/4",  # reserved, includes broadcast
        "2001:db8::/32",  # documentation
        "2002::/16",  # 6to4, encodes an IPv4 address
        "2001::/32",  # Teredo, tunnels to IPv4
    )
)

#: The only IPv6 space carrying allocated, routable addresses (RFC 4291).
#:
#: IPv6 is an allow-list because the alternative is enumerating everything
#: else, and ``ipaddress.is_reserved`` and Go's ``netip`` predicates disagree
#: about what "reserved" covers -- a disagreement that would silently let one
#: process fetch what the other refuses. NAT64, the discard prefix and every
#: unallocated block fall outside this range and need no rule of their own.
_GLOBAL_UNICAST_V6 = ipaddress.ip_network("2000::/3")

#: Name spaces that only ever refer to a local network. Checked on the name
#: because they may resolve to anything, including a public address served by
#: an attacker's DNS.
_BLOCKED_SUFFIXES = (".local", ".internal", ".localhost", ".home.arpa")


@dataclass(slots=True, frozen=True)
class Target:
    """A URL that passed validation, with the addresses it may be dialled at.

    Connecting to anything else re-opens the rebinding hole this closes.
    """

    url: str
    host: str
    addrs: tuple[IPAddress, ...]


def _unmap(addr: IPAddress) -> IPAddress:
    """An IPv4-mapped IPv6 address is an IPv4 address wearing a hat.

    Judged as what it reaches, or ``::ffff:127.0.0.1`` walks past every IPv4
    rule below.
    """
    if isinstance(addr, ipaddress.IPv6Address) and addr.ipv4_mapped is not None:
        return addr.ipv4_mapped
    return addr


def addr_allowed(addr: IPAddress | str) -> bool:
    """Whether a single address may be fetched."""
    try:
        parsed = ipaddress.ip_address(addr) if isinstance(addr, str) else addr
    except ValueError:
        return False
    a = _unmap(parsed)
    if (
        a.is_unspecified
        or a.is_loopback
        or a.is_private
        or a.is_link_local
        or a.is_multicast
        or a.is_reserved
    ):
        return False
    if a.version == 6 and a not in _GLOBAL_UNICAST_V6:
        return False
    return all(a not in net for net in _BLOCKED_NETWORKS if net.version == a.version)


def host_allowed(host: str) -> bool:
    """Whether a hostname may be looked up at all."""
    h = host.strip().rstrip(".").lower()
    if not h or h == "localhost":
        return False
    return not any(h.endswith(s) for s in _BLOCKED_SUFFIXES)


def _resolve(host: str) -> list[IPAddress]:
    try:
        infos = socket.getaddrinfo(host, None, proto=socket.IPPROTO_TCP)
    except OSError:
        return []
    out: list[IPAddress] = []
    for *_, sockaddr in infos:
        try:
            addr = ipaddress.ip_address(sockaddr[0])
        except ValueError:
            continue
        if addr not in out:
            out.append(addr)
    return out


def check(raw: str, resolve: object = None) -> Target:
    """Validate ``raw`` and return the addresses it may be fetched at.

    A host that is an address literal is judged directly and never resolved. A
    named host must resolve to at least one address, and every address it
    resolves to must be allowed.

    ``resolve`` overrides name resolution for tests; it takes a host and
    returns a list of addresses.
    """
    try:
        parts = urlsplit(raw)
    except ValueError:
        raise BlockedURLError() from None

    if parts.scheme.lower() not in ("http", "https"):
        # file:, gopher:, ftp: and the rest reach places an HTTP fetcher has
        # no business reaching.
        raise BlockedURLError()
    if parts.username or parts.password:
        # Credentials in a URL are never needed for public documentation and
        # are a common way to smuggle a different host past a reader.
        raise BlockedURLError()

    try:
        host = parts.hostname or ""
    except ValueError:
        raise BlockedURLError() from None
    if not host_allowed(host):
        raise BlockedURLError()

    try:
        literal = ipaddress.ip_address(host)
    except ValueError:
        literal = None
    if literal is not None:
        if not addr_allowed(literal):
            raise BlockedURLError()
        return Target(url=raw, host=host, addrs=(_unmap(literal),))

    resolver = _resolve if resolve is None else resolve
    addrs = resolver(host)  # type: ignore[operator]
    if not addrs:
        raise BlockedURLError()
    # Every answer must pass. Accepting the host because one record is public
    # leaves which address gets dialled up to resolver ordering.
    if not all(addr_allowed(a) for a in addrs):
        raise BlockedURLError()
    return Target(url=raw, host=host, addrs=tuple(_unmap(a) for a in addrs))
