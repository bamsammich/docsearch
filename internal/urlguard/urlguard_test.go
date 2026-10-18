package urlguard_test

import (
	"bufio"
	"errors"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/urlguard"
	"github.com/bamsammich/docsearch/internal/urlguard/mocks"
)

const (
	vectorFile     = "../../testdata/urlguard-addresses.txt"
	hostVectorFile = "../../testdata/urlguard-hosts.txt"
	public         = "93.184.216.34"
)

// GuardSuite covers which URLs the guard lets a fetcher reach. The resolver
// is a mock, so a case that must be refused before any lookup fails if one
// is made.
type GuardSuite struct {
	suite.Suite
	resolver *mocks.MockResolver
}

func TestGuard(t *testing.T) { suite.Run(t, new(GuardSuite)) }

func (s *GuardSuite) SetupTest() {
	s.resolver = mocks.NewMockResolver(s.T())
}

// resolves answers host with addrs.
func (s *GuardSuite) resolves(host string, addrs ...string) {
	parsed := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		parsed = append(parsed, netip.MustParseAddr(a))
	}
	s.resolver.EXPECT().LookupNetIP(mock.Anything, mock.Anything, host).Return(parsed, nil)
}

func (s *GuardSuite) check(raw string) (*urlguard.Target, error) {
	return urlguard.Check(s.T().Context(), raw, s.resolver)
}

// vectors reads a shared table: one subject and its verdict per line, then
// a reason.
func (s *GuardSuite) vectors(path string) [][]string {
	f, err := os.Open(path)
	s.Require().NoError(err)
	defer func() { s.NoError(f.Close()) }()
	var rows [][]string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		s.Require().GreaterOrEqual(len(fields), 2, "malformed vector line: %q", line)
		rows = append(rows, fields)
	}
	s.Require().NoError(scan.Err())
	return rows
}

// The shared table is the contract between this package and the Python guard.
// A verdict that differs between them means one process fetches what the other
// refuses, which is the drift the file exists to prevent.
func (s *GuardSuite) TestAddrVerdictsMatchTheSharedTable() {
	rows := s.vectors(vectorFile)
	for _, fields := range rows {
		addr, err := netip.ParseAddr(fields[0])
		s.Require().NoError(err, fields[0])
		s.Equal(fields[1] == "allow", urlguard.AddrAllowed(addr),
			"%s (%s)", fields[0], strings.Join(fields[2:], " "))
	}
	// A truncated or unreadable file would otherwise pass by testing nothing.
	s.GreaterOrEqual(len(rows), 40, "the shared table should hold far more")
}

// The host half of the shared contract. Address parity had a file and host
// parity did not, which is exactly where the two implementations drifted: a
// trailing-dot difference, and a Unicode-separator bypass that let
// "wiki．internal" past the suffix rule on the Python side.
func (s *GuardSuite) TestHostVerdictsMatchTheSharedTable() {
	rows := s.vectors(hostVectorFile)
	for _, fields := range rows {
		s.Equal(fields[1] == "allow", urlguard.HostAllowed(fields[0]),
			"%q (%s)", fields[0], strings.Join(fields[2:], " "))
	}
	s.GreaterOrEqual(len(rows), 25, "the shared table should hold more")
}

func (s *GuardSuite) TestPublicHostResolves() {
	s.resolves("docs.example.com", public)
	got, err := s.check("https://docs.example.com/guide")
	s.Require().NoError(err)
	s.Equal([]netip.Addr{netip.MustParseAddr(public)}, got.Addrs)
}

// Accepting a host because one record is public leaves which address gets
// dialled up to resolver ordering.
func (s *GuardSuite) TestAHostResolvingToAnyBlockedAddressIsRefused() {
	for _, mixed := range [][]string{
		{public, "169.254.169.254"},
		{"169.254.169.254", public},
		{public, "10.0.0.5"},
		{public, "::ffff:127.0.0.1"},
	} {
		s.SetupTest()
		s.resolves("docs.example.com", mixed...)
		_, err := s.check("https://docs.example.com")
		s.ErrorIs(err, urlguard.ErrBlocked, mixed)
	}
}

func (s *GuardSuite) TestUnresolvableHostIsRefused() {
	s.resolver.EXPECT().LookupNetIP(mock.Anything, mock.Anything, "nope.example.com").
		Return(nil, errors.New("no such host"))
	_, err := s.check("https://nope.example.com")
	s.ErrorIs(err, urlguard.ErrBlocked)
}

func (s *GuardSuite) TestEmptyAnswerIsRefused() {
	s.resolves("docs.example.com")
	_, err := s.check("https://docs.example.com")
	s.ErrorIs(err, urlguard.ErrBlocked)
}

// A literal is judged directly, so no lookup can move it.
func (s *GuardSuite) TestAddressLiteralsAreJudgedWithoutResolving() {
	_, err := s.check("http://" + public + "/x")
	s.Require().NoError(err, "a public literal")
	for _, raw := range []string{
		"http://127.0.0.1/x",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/x",
		"http://[::ffff:127.0.0.1]/x",
		"http://10.0.0.1/x",
		"http://100.64.1.1/x",
	} {
		_, err := s.check(raw)
		s.ErrorIs(err, urlguard.ErrBlocked, raw)
	}
}

func (s *GuardSuite) TestOnlyHTTPSchemesAreFetched() {
	for _, raw := range []string{
		"file:///etc/passwd",
		"ftp://docs.example.com/x",
		"gopher://docs.example.com/x",
		"data:text/html,hi",
		"jar:https://docs.example.com/x!/y",
		"//docs.example.com/x",
	} {
		_, err := s.check(raw)
		s.Require().ErrorIs(err, urlguard.ErrBlocked, raw)
	}
	for _, raw := range []string{"http://docs.example.com", "HTTPS://docs.example.com"} {
		s.SetupTest()
		s.resolves("docs.example.com", public)
		_, err := s.check(raw)
		s.NoError(err, raw)
	}
}

// Credentials in a URL are a common way to make a reader see one host while
// the request goes to another.
func (s *GuardSuite) TestCredentialsInTheURLAreRefused() {
	_, err := s.check("https://docs.example.com@evil.example.com/x")
	s.ErrorIs(err, urlguard.ErrBlocked)
}

// These names only ever mean a local network, and may resolve to anything at
// all on a DNS the operator does not control, so they are refused before any
// lookup is made.
func (s *GuardSuite) TestLocalNameSpacesAreRefusedBeforeResolving() {
	for _, host := range []string{
		"printer.local", "wiki.internal", "localhost", "app.localhost", "nas.home.arpa",
	} {
		_, err := s.check("https://" + host + "/x")
		s.Require().ErrorIs(err, urlguard.ErrBlocked, host)
	}
	// The suffix must match a label boundary, not a substring: local.dev is
	// an ordinary public name.
	s.resolves("docs.local.dev", public)
	_, err := s.check("https://docs.local.dev/x")
	s.NoError(err)
}

func (s *GuardSuite) TestTrailingDotDoesNotEvadeTheSuffixRule() {
	_, err := s.check("https://printer.local./x")
	s.ErrorIs(err, urlguard.ErrBlocked)
}

// Every rejection must look the same, or the fetcher reports which internal
// names exist.
func (s *GuardSuite) TestRejectionDoesNotDiscloseWhy() {
	s.resolves("private.example.com", "10.0.0.1")
	s.resolver.EXPECT().LookupNetIP(mock.Anything, mock.Anything, "absent.example.com").
		Return(nil, errors.New("no such host"))
	var messages []string
	for _, raw := range []string{
		"https://private.example.com/x", // resolves, but privately
		"https://absent.example.com/x",  // does not resolve
		"http://169.254.169.254/x",      // literal, blocked
		"https://printer.local/x",       // refused on the name
		"file:///etc/passwd",            // refused on the scheme
	} {
		_, err := s.check(raw)
		s.Require().Error(err, raw)
		messages = append(messages, err.Error())
	}
	for _, m := range messages[1:] {
		s.Equal(messages[0], m, "the messages differ and leak the reason")
	}
}
