// Package settings changes how docsearch is configured, from the library UI.
//
// A deployment keeps its settings in one place. Started with docsearch.yaml,
// the file holds them, and a change is never stored: it comes back as the
// updated file, for the operator to commit and deploy. Started without one,
// a change is kept in the database and read when the server next starts.
// Either way a setting the environment or the command line sets is refused,
// since what is kept would never take effect over it.
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
}

// Change is one setting's new value, named by its flag. An empty value
// takes the setting back to its default.
type Change struct {
	Flag  string
	Value string
}

// Result is what a change came to.
type Result struct {
	// File is docsearch.yaml with the change made, for a deployment that
	// reads one. Nothing was stored.
	File string
	// Changed are the 1-based lines of File the change touched.
	Changed []int
	// Saved is true when the change was kept in the database, to take
	// effect when the server next starts.
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

// Stored is what the database keeps, by flag, for a deployment with no file.
// It is empty for one that reads docsearch.yaml.
func (s *Service) Stored(ctx context.Context) (map[string]string, error) {
	if s.file != "" {
		return map[string]string{}, nil
	}
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

// Update makes changes: into the file it returns, or into the database.
func (s *Service) Update(ctx context.Context, changes []Change) (Result, error) {
	values := make(map[string]any, len(changes))
	for _, change := range changes {
		e, err := s.check(change)
		if err != nil {
			return Result{}, err
		}
		values[e.Key] = typed(e, change.Value)
	}
	if s.file != "" {
		return s.rewrite(values)
	}
	return s.keep(ctx, changes)
}

// keep stores changes in the database, for a deployment with no file.
func (s *Service) keep(ctx context.Context, changes []Change) (Result, error) {
	for _, change := range changes {
		var value *string
		if change.Value != "" {
			value = &change.Value
		}
		if err := s.store.Set(ctx, s.editable[change.Flag].Key, value); err != nil {
			return Result{}, err
		}
	}
	return Result{Saved: true}, nil
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
	if e.Boolean && change.Value != "" && change.Value != "true" && change.Value != "false" {
		return Editable{}, fmt.Errorf(
			"%w: %s takes true or false, not %q",
			ErrInvalid,
			change.Flag,
			change.Value,
		)
	}
	return e, nil
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
// every key it was not asked to change are kept.
func Rewrite(src []byte, values map[string]any) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("parse the config file: %w", err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("the config file is not a mapping of settings")
	}
	for key, value := range values {
		path := strings.Split(key, ".")
		if value == nil {
			remove(root, path)
			continue
		}
		scalar := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(value)}
		if _, ok := value.(bool); ok {
			scalar.Tag = "!!bool"
		}
		set(root, path, scalar)
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
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

// common marks the lines of b that belong to the longest run a and b share
// in order.
func common(a, b []string) []bool {
	lengths := make([][]int, len(a)+1)
	for i := range lengths {
		lengths[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lengths[i][j] = lengths[i+1][j+1] + 1
			} else {
				lengths[i][j] = max(lengths[i+1][j], lengths[i][j+1])
			}
		}
	}
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
