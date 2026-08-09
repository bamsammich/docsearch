package urlguard

import (
	"bufio"
	"context"
	"errors"
	"net/netip"
	"os"
	"strings"
	"testing"
)

const vectorFile = "../../testdata/urlguard-addresses.txt"

// The shared table is the contract between this package and the Python guard.
// A verdict that differs between them means one process fetches what the other
// refuses, which is the drift the file exists to prevent.
func TestAddrVerdictsMatchTheSharedTable(t *testing.T) {
	f, err := os.Open(vectorFile)
	if err != nil {
		t.Fatalf("read shared vectors: %v", err)
	}
	defer func() { _ = f.Close() }()

	seen := 0
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("malformed vector line: %q", line)
		}
		raw, want := fields[0], fields[1]
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			t.Errorf("%s: not parseable as an address: %v", raw, err)
			continue
		}
		got := AddrAllowed(addr)
		if got != (want == "allow") {
			t.Errorf("AddrAllowed(%s) = %v, want %s (%s)",
				raw, got, want, strings.Join(fields[2:], " "))
		}
		seen++
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	// A truncated or unreadable file would otherwise pass by testing nothing.
	if seen < 40 {
		t.Fatalf("only %d vectors exercised; the shared table should hold far more", seen)
	}
}

type fixedResolver map[string][]netip.Addr

func (r fixedResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addrs, ok := r[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	return addrs, nil
}

func mustAddrs(t *testing.T, ss ...string) []netip.Addr {
	t.Helper()
	out := make([]netip.Addr, 0, len(ss))
	for _, s := range ss {
		a, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestPublicHostResolves(t *testing.T) {
	res := fixedResolver{"docs.example.com": mustAddrs(t, "93.184.216.34")}
	got, err := Check(context.Background(), "https://docs.example.com/guide", res)
	if err != nil {
		t.Fatalf("Check() error = %v, want nil", err)
	}
	if len(got.Addrs) != 1 || got.Addrs[0].String() != "93.184.216.34" {
		t.Errorf("Addrs = %v, want the resolved public address", got.Addrs)
	}
}

// Accepting a host because one record is public leaves which address gets
// dialled up to resolver ordering.
func TestAHostResolvingToAnyBlockedAddressIsRefused(t *testing.T) {
	for _, mixed := range [][]string{
		{"93.184.216.34", "169.254.169.254"},
		{"169.254.169.254", "93.184.216.34"},
		{"93.184.216.34", "10.0.0.5"},
		{"93.184.216.34", "::ffff:127.0.0.1"},
	} {
		res := fixedResolver{"docs.example.com": mustAddrs(t, mixed...)}
		if _, err := Check(context.Background(), "https://docs.example.com", res); !errors.Is(err, ErrBlocked) {
			t.Errorf("answers %v: error = %v, want ErrBlocked", mixed, err)
		}
	}
}

func TestUnresolvableHostIsRefused(t *testing.T) {
	res := fixedResolver{}
	if _, err := Check(context.Background(), "https://nope.example.com", res); !errors.Is(err, ErrBlocked) {
		t.Errorf("error = %v, want ErrBlocked", err)
	}
}

func TestEmptyAnswerIsRefused(t *testing.T) {
	res := fixedResolver{"docs.example.com": {}}
	if _, err := Check(context.Background(), "https://docs.example.com", res); !errors.Is(err, ErrBlocked) {
		t.Errorf("error = %v, want ErrBlocked", err)
	}
}

// A literal is judged directly, so no lookup can move it.
func TestAddressLiteralsAreJudgedWithoutResolving(t *testing.T) {
	res := fixedResolver{}
	if _, err := Check(context.Background(), "http://93.184.216.34/x", res); err != nil {
		t.Errorf("public literal: error = %v, want nil", err)
	}
	for _, raw := range []string{
		"http://127.0.0.1/x",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/x",
		"http://[::ffff:127.0.0.1]/x",
		"http://10.0.0.1/x",
		"http://100.64.1.1/x",
	} {
		if _, err := Check(context.Background(), raw, res); !errors.Is(err, ErrBlocked) {
			t.Errorf("Check(%q) error = %v, want ErrBlocked", raw, err)
		}
	}
}

func TestOnlyHTTPSchemesAreFetched(t *testing.T) {
	res := fixedResolver{"docs.example.com": mustAddrs(t, "93.184.216.34")}
	for _, raw := range []string{
		"file:///etc/passwd",
		"ftp://docs.example.com/x",
		"gopher://docs.example.com/x",
		"data:text/html,hi",
		"jar:https://docs.example.com/x!/y",
		"//docs.example.com/x",
	} {
		if _, err := Check(context.Background(), raw, res); !errors.Is(err, ErrBlocked) {
			t.Errorf("Check(%q) error = %v, want ErrBlocked", raw, err)
		}
	}
	for _, raw := range []string{"http://docs.example.com", "HTTPS://docs.example.com"} {
		if _, err := Check(context.Background(), raw, res); err != nil {
			t.Errorf("Check(%q) error = %v, want nil", raw, err)
		}
	}
}

// Credentials in a URL are a common way to make a reader see one host while
// the request goes to another.
func TestCredentialsInTheURLAreRefused(t *testing.T) {
	res := fixedResolver{"evil.example.com": mustAddrs(t, "93.184.216.34")}
	if _, err := Check(context.Background(), "https://docs.example.com@evil.example.com/x", res); !errors.Is(err, ErrBlocked) {
		t.Errorf("error = %v, want ErrBlocked", err)
	}
}

// These names only ever mean a local network, and may resolve to anything at
// all on a DNS the operator does not control.
func TestLocalNameSpacesAreRefusedBeforeResolving(t *testing.T) {
	res := fixedResolver{
		"printer.local":  mustAddrs(t, "93.184.216.34"),
		"wiki.internal":  mustAddrs(t, "93.184.216.34"),
		"localhost":      mustAddrs(t, "93.184.216.34"),
		"app.localhost":  mustAddrs(t, "93.184.216.34"),
		"nas.home.arpa":  mustAddrs(t, "93.184.216.34"),
		"docs.local.dev": mustAddrs(t, "93.184.216.34"),
	}
	for _, host := range []string{
		"printer.local", "wiki.internal", "localhost", "app.localhost", "nas.home.arpa",
	} {
		if _, err := Check(context.Background(), "https://"+host+"/x", res); !errors.Is(err, ErrBlocked) {
			t.Errorf("host %q: error = %v, want ErrBlocked", host, err)
		}
	}
	// The suffix must match a label boundary, not a substring: local.dev is
	// an ordinary public name.
	if _, err := Check(context.Background(), "https://docs.local.dev/x", res); err != nil {
		t.Errorf("docs.local.dev: error = %v, want nil", err)
	}
}

func TestTrailingDotDoesNotEvadeTheSuffixRule(t *testing.T) {
	res := fixedResolver{"printer.local.": mustAddrs(t, "93.184.216.34")}
	if _, err := Check(context.Background(), "https://printer.local./x", res); !errors.Is(err, ErrBlocked) {
		t.Errorf("error = %v, want ErrBlocked for a rooted local name", err)
	}
}

// Every rejection must look the same, or the fetcher reports which internal
// names exist.
func TestRejectionDoesNotDiscloseWhy(t *testing.T) {
	res := fixedResolver{
		"private.example.com": mustAddrs(t, "10.0.0.1"),
	}
	var msgs []string
	for _, raw := range []string{
		"https://private.example.com/x", // resolves, but privately
		"https://absent.example.com/x",  // does not resolve
		"http://169.254.169.254/x",      // literal, blocked
		"https://printer.local/x",       // refused on the name
		"file:///etc/passwd",            // refused on the scheme
	} {
		_, err := Check(context.Background(), raw, res)
		if err == nil {
			t.Fatalf("Check(%q) unexpectedly succeeded", raw)
		}
		msgs = append(msgs, err.Error())
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i] != msgs[0] {
			t.Errorf("error messages differ and leak the reason:\n %q\n %q", msgs[0], msgs[i])
		}
	}
}
