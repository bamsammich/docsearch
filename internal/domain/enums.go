package domain

import "fmt"

// Quality grades a document's derived structure.
type Quality uint8

// Numbering starts at 1 so a zero value reads as unset rather than as a
// passing grade, and so the proto enum that mirrors these can reserve 0 for
// UNSPECIFIED and share the numbering.
const (
	QualityOK Quality = iota + 1
	QualityDegraded
	QualityFailed
)

// ChunkKind says what a chunk holds, which decides how it is scored.
type ChunkKind uint8

const (
	KindProse ChunkKind = iota + 1
	KindKeywordReference
)

// SourceKind says what a document was read from. It is stored on the
// document so a reader knows what an identity means: a path on disk, or a
// URL that was crawled.
type SourceKind uint8

const (
	SourceKindFile SourceKind = iota + 1
	SourceKindSite
)

// StructureSource is what a document's heading tree was derived from. The set
// is closed: format adapters and the site navigation are its only producers,
// and every value one of them can report is named here.
type StructureSource uint8

const (
	// SourceOutline is a PDF's embedded outline, which declares which
	// sections exist, how they nest and where each begins. Nothing is
	// inferred, so nothing is required to corroborate it.
	SourceOutline StructureSource = iota + 1
	// SourceFrontTOC, SourceSidebarDOM and SourceIndexPage are equally
	// author-declared but recovered by parsing a printed page or a rendered
	// navigation. A parse can misread, so each is checked against what the
	// document turned out to contain, and a disagreement fails.
	SourceFrontTOC
	SourceSidebarDOM
	SourceIndexPage
	// SourceFontHeuristic is inferred from type sizes with nothing to check
	// it against. SourceURLPath sits beside it: nesting a site by the shape
	// of its addresses is a guess about a publisher's routing, and the pages
	// it places were by definition absent from every source that declared
	// anything.
	SourceFontHeuristic
	SourceURLPath
	// The remaining sources are a format's own heading markup, taken at face
	// value because the markup is the declaration.
	SourceATXHeadings
	SourceHTMLNesting
	SourceHeadingStyles
	SourceBlankLines
	// SourceUnknown is a report whose source was never recorded.
	SourceUnknown
)

// The text each value persists as. Written out rather than derived from the
// constant names: these strings reach documents.warnings and the extraction
// JSON the Python pipeline is held to, so a generator's spelling rule has no
// business between a constant and the byte that lands in the database.
var (
	qualityText = newEnumText("quality", map[Quality]string{
		QualityOK:       "ok",
		QualityDegraded: "degraded",
		QualityFailed:   "failed",
	})
	chunkKindText = newEnumText("chunk kind", map[ChunkKind]string{
		KindProse:            "prose",
		KindKeywordReference: "keyword-reference",
	})
	sourceKindText = newEnumText("source kind", map[SourceKind]string{
		SourceKindFile: "file",
		SourceKindSite: FormatSite,
	})
	structureSourceText = newEnumText("structure source", map[StructureSource]string{
		SourceOutline:       "outline",
		SourceFrontTOC:      "front_toc",
		SourceSidebarDOM:    "sidebar_dom",
		SourceIndexPage:     "index_page",
		SourceFontHeuristic: "font_heuristic",
		SourceURLPath:       "url_path",
		SourceATXHeadings:   "atx_headings",
		SourceHTMLNesting:   "h1_h6_nesting",
		SourceHeadingStyles: "heading_styles",
		SourceBlankLines:    "none (blank-line paragraphs)",
		SourceUnknown:       "unknown",
	})
)

func (q Quality) String() string                { return qualityText.name(q) }
func (q Quality) MarshalText() ([]byte, error)  { return qualityText.marshal(q) }
func (q *Quality) UnmarshalText(b []byte) error { return qualityText.unmarshal(b, q) }

func (k ChunkKind) String() string                { return chunkKindText.name(k) }
func (k ChunkKind) MarshalText() ([]byte, error)  { return chunkKindText.marshal(k) }
func (k *ChunkKind) UnmarshalText(b []byte) error { return chunkKindText.unmarshal(b, k) }

func (k SourceKind) String() string                { return sourceKindText.name(k) }
func (k SourceKind) MarshalText() ([]byte, error)  { return sourceKindText.marshal(k) }
func (k *SourceKind) UnmarshalText(b []byte) error { return sourceKindText.unmarshal(b, k) }

func (s StructureSource) String() string { return structureSourceText.name(s) }

func (s StructureSource) MarshalText() ([]byte, error) { return structureSourceText.marshal(s) }

func (s *StructureSource) UnmarshalText(b []byte) error {
	return structureSourceText.unmarshal(b, s)
}

// ParseStructureSource reads a source back from the text it persists as.
func ParseStructureSource(text string) (StructureSource, error) {
	return structureSourceText.parse(text)
}

// enumText is one enum's values and the text each persists as. Three enums
// marshal and parse the same way and differ only in their type, which is what
// a type parameter is for.
type enumText[T ~uint8] struct {
	names  map[T]string
	values map[string]T
	kind   string
}

func newEnumText[T ~uint8](kind string, names map[T]string) enumText[T] {
	values := make(map[string]T, len(names))
	for value, name := range names {
		values[name] = value
	}
	return enumText[T]{names: names, values: values, kind: kind}
}

// name is the persisted text, or a form naming the type and the number for a
// value that has none. Nothing persists the second form: marshalling refuses
// an unnamed value, so it reaches a log or a test failure and no further.
func (e enumText[T]) name(value T) string {
	if name, ok := e.names[value]; ok {
		return name
	}
	return fmt.Sprintf("%s(%d)", e.kind, uint8(value))
}

func (e enumText[T]) marshal(value T) ([]byte, error) {
	name, ok := e.names[value]
	if !ok {
		return nil, fmt.Errorf("unknown %s %d", e.kind, uint8(value))
	}
	return []byte(name), nil
}

func (e enumText[T]) unmarshal(text []byte, into *T) error {
	value, err := e.parse(string(text))
	if err != nil {
		return err
	}
	*into = value
	return nil
}

// parse reads a value back from its text.
//
//nolint:ireturn // T is constrained to ~uint8, so this returns a concrete enum rather than an interface.
func (e enumText[T]) parse(text string) (T, error) {
	value, ok := e.values[text]
	if !ok {
		var zero T
		return zero, fmt.Errorf("unknown %s %q", e.kind, text)
	}
	return value, nil
}
