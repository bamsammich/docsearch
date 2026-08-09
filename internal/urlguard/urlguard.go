// Package urlguard is the address-validation boundary for fetching.
//
// This is a security boundary, not a convenience check. A URL arrives in a
// tool call, over a network endpoint, and may have been suggested by the
// content of a document or a web page rather than typed by a person. The
// server can reach things the caller cannot -- a metadata service, a database
// admin page, a printer -- and anything it fetches becomes searchable, so a
// fetch of an internal address is an exfiltration channel.
//
// Three rules follow:
//
//   - What may be fetched is decided here, not by the caller. No parameter
//     widens it.
//   - A host is judged by every address it resolves to, not the first. A name
//     answering with one public and one private address must be refused, or
//     the outcome depends on which record arrived first.
//   - The addresses are returned so the caller connects to what was checked.
//     Resolving twice -- once to validate, once to dial -- is a rebinding
//     hole wide enough to drive through.
//
// Every rejection returns the same error. Distinguishing "private address"
// from "does not resolve" turns the fetcher into a network scanner that
// reports which internal names exist.
package urlguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// ErrBlocked is returned for every rejected URL, whatever the reason.
var ErrBlocked = errors.New("url is not permitted")

// Target is a URL that passed validation, with the addresses it may be dialled
// at. Connecting to anything else re-opens the rebinding hole this closes.
type Target struct {
	URL   *url.URL
	Addrs []netip.Addr
}

// Resolver looks up a host. net.Resolver satisfies it; tests supply their own.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// blockedPrefixes are ranges that must never be fetched, beyond what the
// netip predicates already cover.
//
// Written out rather than inferred: the stdlib predicates disagree between
// languages and between versions about what counts as "private", and this
// boundary is enforced in two languages that have to agree exactly.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT, and where Tailscale lives
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, includes broadcast
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4, encodes an IPv4 address
	netip.MustParsePrefix("2001::/32"),       // Teredo, tunnels to IPv4
}

// globalUnicastV6 is the only IPv6 space that carries allocated, routable
// addresses (RFC 4291).
//
// IPv6 is handled as an allow-list because the alternative is enumerating
// everything else, and the two languages' standard libraries disagree about
// what "reserved" covers -- a disagreement that would silently let one process
// fetch what the other refuses. NAT64, the discard prefix and every
// unallocated block fall outside this range and need no rule of their own.
var globalUnicastV6 = netip.MustParsePrefix("2000::/3")

// blockedSuffixes are name spaces that only ever refer to a local network.
// Checked on the name because they may resolve to anything, including a public
// address on an attacker's DNS.
var blockedSuffixes = []string{".local", ".internal", ".localhost", ".home.arpa"}

// AddrAllowed reports whether a single address may be fetched.
//
// Exported so the fetcher can re-check an address it is about to dial without
// repeating the rules, and so the cross-language equivalence tests can drive
// it directly.
func AddrAllowed(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	// An IPv4-mapped IPv6 address is an IPv4 address wearing a hat. Judge it
	// as what it reaches, or ::ffff:127.0.0.1 walks straight past every IPv4
	// rule below.
	a := addr.Unmap()
	switch {
	case a.IsUnspecified(),
		a.IsLoopback(),
		a.IsPrivate(),
		a.IsLinkLocalUnicast(),
		a.IsLinkLocalMulticast(),
		a.IsInterfaceLocalMulticast(),
		a.IsMulticast():
		return false
	}
	if a.Is6() && !globalUnicastV6.Contains(a) {
		return false
	}
	for _, p := range blockedPrefixes {
		// A v4 prefix cannot contain a v6 address and vice versa, so the
		// family check is implicit in Contains.
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// HostAllowed reports whether a hostname is one that may be looked up at all.
func HostAllowed(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" || h == "localhost" {
		return false
	}
	for _, s := range blockedSuffixes {
		if strings.HasSuffix(h, s) {
			return false
		}
	}
	return true
}

// Check validates raw and returns the addresses it may be fetched at.
//
// A URL whose host is an address literal is judged directly and never
// resolved. A named host must resolve to at least one address, and every
// address it resolves to must be allowed.
func Check(ctx context.Context, raw string, res Resolver) (*Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, ErrBlocked
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		// file:, gopher:, ftp: and the rest reach places an HTTP fetcher has
		// no business reaching.
		return nil, ErrBlocked
	}
	if u.User != nil {
		// Credentials in a URL are never needed for public documentation and
		// are a common way to smuggle a different host past a reader.
		return nil, ErrBlocked
	}
	host := u.Hostname()
	if !HostAllowed(host) {
		return nil, ErrBlocked
	}

	if lit, err := netip.ParseAddr(host); err == nil {
		if !AddrAllowed(lit) {
			return nil, ErrBlocked
		}
		return &Target{URL: u, Addrs: []netip.Addr{lit.Unmap()}}, nil
	}

	if res == nil {
		res = net.DefaultResolver
	}
	addrs, err := res.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, ErrBlocked
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		// Every answer must pass. Accepting the host because one record is
		// public leaves which address gets dialled up to resolver ordering.
		if !AddrAllowed(a) {
			return nil, ErrBlocked
		}
		out = append(out, a.Unmap())
	}
	return &Target{URL: u, Addrs: out}, nil
}
