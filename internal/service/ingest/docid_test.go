package ingest_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/service/ingest"
)

// DocIDSuite holds Slugify to what Python's produces, since an identifier
// that changed would file an existing document a second time.
type DocIDSuite struct{ suite.Suite }

func TestDocID(t *testing.T) { suite.Run(t, new(DocIDSuite)) }

func (s *DocIDSuite) TestSlugifyMatchesPython() {
	tests := []struct {
		title string
		want  string
		note  string
	}{
		{title: "Operator Guide", want: "operator-guide"},
		{title: "DM7 series Reference Manual", want: "dm7-series-reference-manual"},
		{title: "A/B & C", want: "a-b-c", note: "every run of punctuation is one hyphen"},
		{
			title: "Café Brûlé",
			want:  "cafe-brule",
			note:  "a decomposed mark is dropped, its letter kept",
		},
		{title: "Ångström", want: "angstrom"},
		{title: "ﬁle", want: "file", note: "NFKD splits the ligature"},
		{
			title: "Ⅻ legions",
			want:  "xii-legions",
			note:  "a numeric letter decomposes to its digits",
		},
		{title: "Æther", want: "ther", note: "a letter with no decomposition is dropped whole"},
		{title: "Straße", want: "strae"},
		{title: "日本語マニュアル", want: "document", note: "a title a slug cannot hold"},
		{title: "  ---  ", want: "document"},
		{title: "", want: "document"},
	}
	for _, tt := range tests {
		s.Run(tt.title, func() {
			s.Equal(tt.want, ingest.Slugify(tt.title), tt.note)
		})
	}
}
