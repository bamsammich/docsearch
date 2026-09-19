package docx_test

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/adapter/docx"
)

// DocxSuite checks package shapes the goldens, all written by python-docx,
// never have.
type DocxSuite struct{ suite.Suite }

func TestDocx(t *testing.T) { suite.Run(t, new(DocxSuite)) }

func (s *DocxSuite) TestTitlesAPackageWithoutCorePropertiesAsPythonDocxDoes() {
	// python-docx supplies a core properties part titled "Word Document".
	got, err := docx.Extract(s.minimalDocx(1))
	s.Require().NoError(err)
	s.Equal("Word Document", got.Title)
	s.Require().Len(got.Blocks, 1)
	s.Equal("Only paragraph", got.Blocks[0].Text)
}

func (s *DocxSuite) TestReadsAPackageWithTwoMainDocumentRelationships() {
	// python-docx raises ValueError here; the port reads the first on purpose.
	got, err := docx.Extract(s.minimalDocx(2))
	s.Require().NoError(err)
	s.Require().Len(got.Blocks, 1)
	s.Equal("Only paragraph", got.Blocks[0].Text)
}

func (s *DocxSuite) TestRejectsAFileThatIsNotAPackage() {
	path := filepath.Join(s.T().TempDir(), "plain.docx")
	s.Require().NoError(os.WriteFile(path, []byte("not a zip"), 0o600))
	_, err := docx.Extract(path)
	s.Error(err)
}

// minimalDocx writes a package holding one paragraph, with no styles or
// core properties part and mainRels relationships to its main document.
func (s *DocxSuite) minimalDocx(mainRels int) string {
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`
	for i := range mainRels {
		rels += fmt.Sprintf(`<Relationship Id="rId%d" Type="%s" Target="word/document.xml"/>`,
			i, "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument")
	}
	rels += `</Relationships>`
	parts := map[string]string{
		"_rels/.rels": rels,
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
			`<w:body><w:p><w:r><w:t>Only paragraph</w:t></w:r></w:p></w:body></w:document>`,
	}

	path := filepath.Join(s.T().TempDir(), "minimal.docx")
	f, err := os.Create(path)
	s.Require().NoError(err)
	z := zip.NewWriter(f)
	for name, body := range parts {
		w, err := z.Create(name)
		s.Require().NoError(err)
		_, err = w.Write([]byte(body))
		s.Require().NoError(err)
	}
	s.Require().NoError(z.Close())
	s.Require().NoError(f.Close())
	return path
}
