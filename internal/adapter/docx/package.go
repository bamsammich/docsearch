package docx

import (
	"archive/zip"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Namespaces and relationship types python-docx resolves parts by.
//
//nolint:revive // unsecure-url-scheme: these URIs are names defined by ECMA-376 and Dublin Core, never fetched; an https spelling would match nothing.
const (
	nsW  = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsDC = "http://purl.org/dc/elements/1.1/"

	relOfficeDocument = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"
	relCoreProperties = "http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties"
	relStyles         = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles"
)

// defaultCoreTitle is the title python-docx gives a package that has no
// core properties part.
const defaultCoreTitle = "Word Document"

// document is what Extract needs from a package: its body paragraphs and
// the title in its core properties.
type document struct {
	coreTitle  string
	paragraphs []paragraph
}

// readDocument follows the package relationships to the main document, its
// styles and the core properties.
func readDocument(pkg *zip.Reader) (*document, error) {
	rels, err := readRelationships(pkg, "")
	if err != nil {
		return nil, err
	}
	mainPart, ok, err := rels.related(relOfficeDocument)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("no main document relationship")
	}
	body, err := readXML(pkg, mainPart)
	if err != nil {
		return nil, err
	}
	styles, err := readStyles(pkg, mainPart)
	if err != nil {
		return nil, err
	}
	coreTitle, err := readCoreTitle(pkg, rels)
	if err != nil {
		return nil, err
	}
	doc := &document{coreTitle: coreTitle}
	for _, p := range body.child(nsW, "body").children(nsW, "p") {
		doc.paragraphs = append(doc.paragraphs, paragraph{
			text: paragraphText(p),
			style: styles.paragraphStyleName(
				p.child(nsW, "pPr").child(nsW, "pStyle").attr(nsW, "val"),
			),
		})
	}
	return doc, nil
}

func readCoreTitle(pkg *zip.Reader, rels relationships) (string, error) {
	part, ok, err := rels.related(relCoreProperties)
	if err != nil {
		return "", err
	}
	if !ok {
		return defaultCoreTitle, nil
	}
	core, err := readXML(pkg, part)
	if err != nil {
		return "", err
	}
	return core.child(nsDC, "title").Text, nil
}

// style is one w:style definition.
type style struct {
	id, kind, name string
	isDefault      bool
}

type styles []style

// readStyles reads the styles part related to the main document part. A
// document without one gets python-docx's built-in styles, whose default
// paragraph style is "Normal": neither a title nor a heading, as an empty
// name is neither.
func readStyles(pkg *zip.Reader, mainPart string) (styles, error) {
	rels, err := readRelationships(pkg, mainPart)
	if err != nil {
		return nil, err
	}
	part, ok, err := rels.related(relStyles)
	if err != nil {
		return nil, err
	}
	if !ok {
		return styles{}, nil
	}
	root, err := readXML(pkg, part)
	if err != nil {
		return nil, err
	}
	var out styles
	for _, s := range root.children(nsW, "style") {
		out = append(out, style{
			id:        s.attr(nsW, "styleId"),
			kind:      s.attr(nsW, "type"),
			name:      s.child(nsW, "name").attr(nsW, "val"),
			isDefault: onOff(s.attr(nsW, "default")),
		})
	}
	return out, nil
}

// paragraphStyleName is the name of the style python-docx's Paragraph.style
// returns: the paragraph style with that id, else the document's default
// paragraph style, the last one marked default. "" when there is neither.
func (s styles) paragraphStyleName(id string) string {
	if st, ok := s.byID(id); ok && st.kind == "paragraph" {
		return st.name
	}
	name := ""
	for _, st := range s {
		if st.kind == "paragraph" && st.isDefault {
			name = st.name
		}
	}
	return name
}

// byID is the first style with id, of any type.
func (s styles) byID(id string) (style, bool) {
	if id == "" {
		return style{}, false
	}
	for _, st := range s {
		if st.id == id {
			return st, true
		}
	}
	return style{}, false
}

func onOff(v string) bool {
	return v == "1" || v == "true" || v == "on"
}

type relationship struct {
	target   string
	external bool
}

// relationships are one part's relationships, by type.
type relationships map[string][]relationship

// readRelationships returns the relationships of part ("" for the package
// itself), by type, each target resolved to a part name.
func readRelationships(pkg *zip.Reader, part string) (relationships, error) {
	dir, base := path.Split(part)
	root, err := readXML(pkg, path.Join(dir, "_rels", base+".rels"))
	if errors.Is(err, fs.ErrNotExist) {
		return relationships{}, nil
	}
	if err != nil {
		return nil, err
	}
	rels := relationships{}
	for _, rel := range root.Children {
		kind := rel.attr("", "Type")
		rels[kind] = append(rels[kind], relationship{
			target:   resolveTarget(dir, rel.attr("", "Target")),
			external: rel.attr("", "TargetMode") == "External",
		})
	}
	return rels, nil
}

// related is the part name of the first relationship of kind: false when
// there is none, an error when it points outside the package.
//
// python-docx refuses a package with two relationships of a type it looks
// up. The port reads the first instead, deliberately: the document is still
// readable, and refusing it keeps its text out of the index for nothing.
func (r relationships) related(kind string) (string, bool, error) {
	matching := r[kind]
	if len(matching) == 0 {
		return "", false, nil
	}
	if matching[0].external {
		return "", false, fmt.Errorf("relationship of type %s is external", kind)
	}
	return matching[0].target, true, nil
}

// resolveTarget turns a relationship target into a zip entry name: absolute
// targets are from the package root, the rest from the source part's
// directory.
func resolveTarget(dir, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	return strings.TrimPrefix(path.Join("/"+dir, target), "/")
}

func readXML(pkg *zip.Reader, part string) (*xmlNode, error) {
	f, err := pkg.Open(part)
	if err != nil {
		return nil, fmt.Errorf("open part %s: %w", part, err)
	}
	defer f.Close()
	root, err := decodeXML(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", part, err)
	}
	return root, nil
}
