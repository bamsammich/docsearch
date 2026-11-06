// Package settings changes how docsearch is configured, from the library UI.
//
// A setting is taken from the strongest place that holds it: the command
// line, the environment, docsearch.yaml, the database, then the default. A
// change made in the UI is kept in the database, and takes effect when the
// server next starts. A setting docsearch.yaml holds is changed in the
// file, so for one of those the change comes back as the updated file, for
// the operator to commit and deploy, and nothing is stored. A setting the
// environment or the command line sets is refused, since neither a kept
// value nor the file would take effect over it.
package settings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bamsammich/docsearch/internal/config"
)

var (
	// ErrNotEditable is a setting the library UI does not change.
	ErrNotEditable = errors.New("that setting is changed where the server starts, not here")
	// ErrLocked is a setting the environment or the command line sets.
	ErrLocked = errors.New("a stronger place sets it")
	// ErrInvalid is a value the setting cannot take.
	ErrInvalid = errors.New("not a value that setting takes")
)

// Editable is a setting the library UI changes.
type Editable struct {
	// Key is the setting's key in docsearch.yaml, and its key in the
	// database.
	Key string
	// Boolean settings take true or false; every other one takes text.
	Boolean bool
	// URL settings take an absolute http or https URL.
	URL bool
}

// Change is one setting's new value, named by its flag. An empty value
// takes the setting back to its default.
type Change struct {
	Flag  string
	Value string
}

// Result is what a change came to.
type Result struct {
	// File is docsearch.yaml with the changes to settings it holds made,
	// and empty when the changes touched none. Those were not stored.
	File string
	// Changed are the 1-based lines of File the change touched.
	Changed []int
	// Saved is true when changes to settings the file does not hold were
	// kept in the database, to take effect when the server next starts.
	Saved bool
}

// Store keeps settings in the database, for a deployment with no file.
// It refuses anyone who is not an administrator.
type Store interface {
	Stored(ctx context.Context) (map[string]string, error)
	// Set keeps value under key, or forgets key when value is nil.
	Set(ctx context.Context, key string, value *string) error
}

// Service changes settings as one user.
type Service struct {
	editable map[string]Editable
	origins  map[string]config.Origin
	store    Store
	read     func(path string) ([]byte, error)
	file     string
}

// New builds the service. file is the docsearch.yaml the server read, and
// empty when it read none; origins are where each running setting came from.
func New(
	editable map[string]Editable,
	origins map[string]config.Origin,
	file string,
	store Store,
	read func(path string) ([]byte, error),
) *Service {
	return &Service{editable: editable, origins: origins, file: file, store: store, read: read}
}

// Stored is what the database keeps, by flag.
func (s *Service) Stored(ctx context.Context) (map[string]string, error) {
	byKey, err := s.store.Stored(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(byKey))
	for flag, e := range s.editable {
		if value, ok := byKey[e.Key]; ok {
			out[flag] = value
		}
	}
	return out, nil
}

// Update makes changes: a setting docsearch.yaml holds into the file it
// returns, and any other into the database.
func (s *Service) Update(ctx context.Context, changes []Change) (Result, error) {
	inFile, kept, err := s.sort(changes)
	if err != nil {
		return Result{}, err
	}
	var result Result
	if len(inFile) > 0 {
		rewritten, err := s.rewrite(inFile)
		if err != nil {
			return Result{}, err
		}
		result = rewritten
	}
	if len(kept) > 0 {
		if err := s.keep(ctx, kept); err != nil {
			return Result{}, err
		}
		result.Saved = true
	}
	return result, nil
}

// sort checks changes and parts them: the values docsearch.yaml holds, by
// key, and the changes to keep in the database.
func (s *Service) sort(changes []Change) (map[string]any, []Change, error) {
	inFile := map[string]any{}
	var kept []Change
	for _, change := range changes {
		e, err := s.check(change)
		if err != nil {
			return nil, nil, err
		}
		if s.origins[change.Flag].Kind == config.FromFile {
			inFile[e.Key] = typed(e, change.Value)
			continue
		}
		kept = append(kept, change)
	}
	return inFile, kept, nil
}

// keep stores changes in the database.
func (s *Service) keep(ctx context.Context, changes []Change) error {
	for _, change := range changes {
		var value *string
		if change.Value != "" {
			value = &change.Value
		}
		if err := s.store.Set(ctx, s.editable[change.Flag].Key, value); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) check(change Change) (Editable, error) {
	e, ok := s.editable[change.Flag]
	if !ok {
		return Editable{}, fmt.Errorf("%s: %w", change.Flag, ErrNotEditable)
	}
	if origin := s.origins[change.Flag]; origin.Kind == config.FromFlag ||
		origin.Kind == config.FromEnv {
		return Editable{}, fmt.Errorf("%w: %s sets it, so change it there", ErrLocked, origin.Where)
	}
	if change.Value == "" {
		return e, nil
	}
	return e, e.takes(change)
}

// takes refuses a value the setting cannot hold, before it is kept or
// written: one that would stop the server from starting.
func (e Editable) takes(change Change) error {
	switch {
	case e.URL && !config.HTTPURL(change.Value):
		return fmt.Errorf("%w: %s takes an absolute http or https URL, not %q",
			ErrInvalid, change.Flag, change.Value)
	case e.Boolean && change.Value != "true" && change.Value != "false":
		return fmt.Errorf(
			"%w: %s takes true or false, not %q",
			ErrInvalid,
			change.Flag,
			change.Value,
		)
	default:
		return nil
	}
}

func (s *Service) rewrite(values map[string]any) (Result, error) {
	before, err := s.read(s.file)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", s.file, err)
	}
	after, err := Rewrite(before, values)
	if err != nil {
		return Result{}, err
	}
	return Result{File: string(after), Changed: changedLines(before, after)}, nil
}

// typed is a change's value as the file should hold it: a boolean for a
// boolean setting, nil to remove the key, and text otherwise.
func typed(e Editable, value string) any {
	switch {
	case value == "":
		return nil
	case e.Boolean:
		return value == "true"
	default:
		return value
	}
}

// Rewrite sets each dotted key in a YAML document to its value, creating
// the mappings it needs and removing a key whose value is nil. Comments and
// every key it was not asked to change are kept. When every change replaces
// a value already on one line, only those values change and every other
// byte stays as written; otherwise the document is written out again.
func Rewrite(src []byte, values map[string]any) ([]byte, error) {
	if out, ok := splice(src, values); ok {
		return out, nil
	}
	doc, err := document(src)
	if err != nil {
		return nil, err
	}
	root := doc.Content[0]
	for key, value := range values {
		path := strings.Split(key, ".")
		if value == nil {
			remove(root, path)
			continue
		}
		set(root, path, scalarOf(value))
	}
	return encode(doc)
}

// document is a config file parsed into nodes, or an empty mapping for an
// empty file.
func document(src []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("parse the config file: %w", err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the config file is not a mapping of settings")
	}
	return &doc, nil
}

// splice replaces each value in place on its own line. It reports false
// when a change needs more: a key to add or remove, or a value that is not
// a scalar on one line.
func splice(src []byte, values map[string]any) ([]byte, bool) {
	doc, err := document(src)
	if err != nil || len(src) == 0 {
		return nil, false
	}
	lines := strings.SplitAfter(string(src), "\n")
	for key, value := range values {
		if !spliceOne(lines, doc.Content[0], key, value) {
			return nil, false
		}
	}
	out := []byte(strings.Join(lines, ""))
	return out, holds(out, values)
}

// spliceOne replaces key's value on its line in lines.
func spliceOne(lines []string, root *yaml.Node, key string, value any) bool {
	if value == nil {
		return false
	}
	node := lookup(root, strings.Split(key, "."))
	if node == nil || node.Kind != yaml.ScalarNode || node.Line > len(lines) {
		return false
	}
	line := lines[node.Line-1]
	from := node.Column - 1
	to, ok := scalarEnd(line, from, node.Style)
	if !ok {
		return false
	}
	text, ok := render(scalarOf(value))
	if !ok {
		return false
	}
	lines[node.Line-1] = line[:from] + text + line[to:]
	return true
}

// lookup is the value node at path under mapping, or nil.
func lookup(mapping *yaml.Node, path []string) *yaml.Node {
	for i := 0; mapping.Kind == yaml.MappingNode && i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != path[0] {
			continue
		}
		if len(path) == 1 {
			return mapping.Content[i+1]
		}
		return lookup(mapping.Content[i+1], path[1:])
	}
	return nil
}

// scalarEnd is the byte offset where a one-line scalar starting at from
// ends, before any comment.
func scalarEnd(line string, from int, style yaml.Style) (int, bool) {
	switch {
	case from < 0 || from > len(line):
		return 0, false
	case style&yaml.DoubleQuotedStyle != 0:
		return doubleQuotedEnd(line, from)
	case style&yaml.SingleQuotedStyle != 0:
		return singleQuotedEnd(line, from)
	case style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return 0, false
	default:
		return plainEnd(line, from), true
	}
}

// doubleQuotedEnd is past the quote that closes a "..." scalar.
func doubleQuotedEnd(line string, from int) (int, bool) {
	for i := from + 1; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

// singleQuotedEnd is past the quote that closes a '...' scalar, where ”
// is a quote inside it.
func singleQuotedEnd(line string, from int) (int, bool) {
	for i := from + 1; i < len(line); i++ {
		if line[i] != '\'' {
			continue
		}
		if i+1 < len(line) && line[i+1] == '\'' {
			i++
			continue
		}
		return i + 1, true
	}
	return 0, false
}

// plainEnd is where a plain scalar ends: before a comment, or the end of
// the line, with trailing space left in place.
func plainEnd(line string, from int) int {
	rest := line[from:]
	for i := 1; i < len(rest); i++ {
		if rest[i] == '#' && (rest[i-1] == ' ' || rest[i-1] == '\t') {
			return from + len(strings.TrimRight(rest[:i], " \t"))
		}
	}
	return from + len(strings.TrimRight(rest, " \t\r\n"))
}

// render is a scalar as YAML writes it on one line.
func render(scalar *yaml.Node) (string, bool) {
	out, err := yaml.Marshal(scalar)
	if err != nil {
		return "", false
	}
	text := strings.TrimSuffix(string(out), "\n")
	return text, !strings.Contains(text, "\n")
}

// holds reports whether src parses and gives each key the value asked for,
// so a splice that went wrong falls back to writing the document out.
func holds(src []byte, values map[string]any) bool {
	doc, err := document(src)
	if err != nil {
		return false
	}
	for key, value := range values {
		node := lookup(doc.Content[0], strings.Split(key, "."))
		if node == nil || node.Value != fmt.Sprint(value) {
			return false
		}
	}
	return true
}

// scalarOf is value as a YAML scalar, tagged as a boolean or as text.
func scalarOf(value any) *yaml.Node {
	scalar := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(value)}
	if _, ok := value.(bool); ok {
		scalar.Tag = "!!bool"
	}
	return scalar
}

// encode writes doc back as YAML, indented two spaces.
func encode(doc *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("write the config file: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("write the config file: %w", err)
	}
	return out.Bytes(), nil
}

// set puts value at path under mapping, making the mappings on the way.
func set(mapping *yaml.Node, path []string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != path[0] {
			continue
		}
		if len(path) == 1 {
			value.LineComment = mapping.Content[i+1].LineComment
			mapping.Content[i+1] = value
			return
		}
		set(mapping.Content[i+1], path[1:], value)
		return
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]}
	if len(path) == 1 {
		mapping.Content = append(mapping.Content, key, value)
		return
	}
	child := &yaml.Node{Kind: yaml.MappingNode}
	mapping.Content = append(mapping.Content, key, child)
	set(child, path[1:], value)
}

// remove deletes the key at path, if it is there.
func remove(mapping *yaml.Node, path []string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != path[0] {
			continue
		}
		if len(path) == 1 {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
		remove(mapping.Content[i+1], path[1:])
		return
	}
}

// changedLines are the 1-based lines of after that before does not hold in
// the same order: the lines a reader should look at.
func changedLines(before, after []byte) []int {
	old := strings.Split(strings.TrimRight(string(before), "\n"), "\n")
	updated := strings.Split(strings.TrimRight(string(after), "\n"), "\n")
	kept := common(old, updated)
	var out []int
	for i := range updated {
		if !kept[i] {
			out = append(out, i+1)
		}
	}
	return out
}

// shared is the length of the longest common run of a[i:] and b[j:], for
// every i and j.
func shared(a, b []string) [][]int {
	lengths := make([][]int, len(a)+1)
	for i := range lengths {
		lengths[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			lengths[i][j] = max(lengths[i+1][j], lengths[i][j+1])
			if a[i] == b[j] {
				lengths[i][j] = lengths[i+1][j+1] + 1
			}
		}
	}
	return lengths
}

// common marks the lines of b that belong to the longest run a and b share
// in order.
func common(a, b []string) []bool {
	lengths := shared(a, b)
	kept := make([]bool, len(b))
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			kept[j] = true
			i++
			j++
		case lengths[i+1][j] >= lengths[i][j+1]:
			i++
		default:
			j++
		}
	}
	return kept
}
