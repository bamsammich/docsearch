package fetch_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/site/fetch"
)

// NormalizeSuite checks URL normalization against the Python fetcher's.
type NormalizeSuite struct{ suite.Suite }

func TestNormalize(t *testing.T) { suite.Run(t, new(NormalizeSuite)) }

func (s *NormalizeSuite) TestMatchesPython() {
	// Expected values from python/docsearch/fetch.py's normalize().
	tests := []struct {
		name, in, want string
	}{
		{
			name: "case, default port, dot segments and fragment",
			in:   "HTTP://Example.COM:80/a/./b/../c?q=1#frag",
			want: "http://example.com/a/c?q=1",
		},
		{
			name: "empty path becomes a slash",
			in:   "https://example.com",
			want: "https://example.com/",
		},
		{
			name: "https default port",
			in:   "https://example.com:443/docs/",
			want: "https://example.com/docs/",
		},
		{
			name: "a trailing slash is a different resource",
			in:   "https://example.com/docs",
			want: "https://example.com/docs",
		},
		{
			name: "credentials are dropped",
			in:   "https://user:pw@example.com/x",
			want: "https://example.com/x",
		},
		{
			name: "dot segments cannot climb past the root",
			in:   "https://example.com/a/b/../../../c",
			want: "https://example.com/c",
		},
		{
			name: "a trailing dot leaves a slash",
			in:   "https://example.com/a/b/.",
			want: "https://example.com/a/b/",
		},
		{
			name: "a trailing double dot leaves a slash",
			in:   "https://example.com/a/b/..",
			want: "https://example.com/a/",
		},
		{
			name: "an address literal keeps its brackets, port and query order",
			in:   "https://[2606:4700::1111]:8443/x?b=2&a=1",
			want: "https://[2606:4700::1111]:8443/x?b=2&a=1",
		},
		{
			name: "percent encoding is left as written",
			in:   "https://example.com/%7Euser/a%20b",
			want: "https://example.com/%7Euser/a%20b",
		},
		{
			name: "only the host is lowercased",
			in:   "https://Example.com/Path",
			want: "https://example.com/Path",
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			got, err := fetch.Normalize(tt.in)
			s.Require().NoError(err)
			s.Equal(tt.want, got)
		})
	}
}
