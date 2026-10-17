// Package docsync keeps a docsearch library in step with what a Paperless
// user can see.
//
// It pushes from the network the documents live on, so the server never
// reaches into it: a sync is a client of both, and the only thing listening
// is docsearch. Each Paperless document becomes one upload under the key
// `<prefix>-<id>`, so pushing it again replaces it in place, and the library
// itself is the record of what was pushed. Nothing is kept on disk: a
// document is unchanged when docsearch read it after Paperless last
// modified it, and a document is retired when its key is in the library and
// Paperless no longer shows it.
package docsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/bamsammich/docsearch/internal/paperless"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/service/job"
	"github.com/bamsammich/docsearch/internal/upload"
)

// Paperless is the instance documents come from.
type Paperless interface {
	List(ctx context.Context) ([]paperless.Document, error)
	Download(ctx context.Context, id int, original bool) (io.ReadCloser, error)
}

// Library is the docsearch library documents go to.
type Library interface {
	List(ctx context.Context) ([]document.Document, error)
	Jobs(ctx context.Context, includeCompleted bool, limit int) ([]job.Job, error)
	Remove(ctx context.Context, docID string) error
}

// Uploader puts a file into the library under a key.
type Uploader interface {
	Put(ctx context.Context, filename, key string, body io.Reader) (upload.Received, error)
}

// Kind is what a sync does with one document.
type Kind int

const (
	// Push uploads a document docsearch does not have, or has an older copy of.
	Push Kind = iota + 1
	// Skip leaves a document alone, for the reason given.
	Skip
	// Retire removes a document Paperless no longer shows.
	Retire
)

func (k Kind) String() string {
	switch k {
	case Push:
		return "push"
	case Skip:
		return "skip"
	case Retire:
		return "retire"
	default:
		return "Kind(" + strconv.Itoa(int(k)) + ")"
	}
}

// Action is one thing a sync does.
type Action struct {
	Key      string
	Filename string
	DocID    string
	Reason   string
	Kind     Kind
	ID       int
	Original bool
}

// Options are what an operator decides about a sync.
type Options struct {
	// Supports reports whether docsearch reads a file by its name.
	Supports func(filename string) bool
	// Prefix starts every key this sync pushes, so two Paperless instances
	// can feed one library without retiring each other's documents.
	Prefix string
	// PreferArchive pushes Paperless's archive copy whenever there is one.
	// The default pushes the original, whose structure an archive
	// conversion can lose, and falls back to the archive only for a file
	// docsearch cannot read, such as a photographed page.
	PreferArchive bool
	// AllowEmpty lets a sync retire everything it pushed when Paperless
	// shows nothing. Without it that is refused: an empty list is far more
	// often a revoked share or a wrong token than an emptied library.
	AllowEmpty bool
}

// ErrNothingShown is a Paperless that showed no documents while the library
// holds some this sync pushed.
var ErrNothingShown = errors.New("paperless showed no documents; refusing to retire " +
	"everything this sync pushed (pass --allow-empty if that is what you want)")

// key is the upload key of one Paperless document.
func (o Options) key(id int) string { return o.Prefix + "-" + strconv.Itoa(id) }

// Plan decides what each document needs, from what Paperless shows, what the
// library holds and which uploads are still being read.
func Plan(
	shown []paperless.Document,
	held []document.Document,
	active []job.Job,
	options Options,
) []Action {
	bySource := map[string]document.Document{}
	for _, d := range held {
		bySource[d.Source] = d
	}
	busy := map[string]bool{}
	for _, j := range active {
		busy[j.Source] = true
	}
	keep := map[string]bool{}
	actions := make([]Action, 0, len(shown))
	for _, d := range shown {
		key := options.key(d.ID)
		keep[upload.Target(key)] = true
		actions = append(actions, decide(d, key, bySource, busy, options))
	}
	return append(actions, retirements(held, keep, busy, options)...)
}

func decide(
	d paperless.Document,
	key string,
	bySource map[string]document.Document,
	busy map[string]bool,
	options Options,
) Action {
	action := Action{Kind: Push, Key: key, ID: d.ID}
	target := upload.Target(key)
	if busy[target] {
		action.Kind, action.Reason = Skip, "already being read"
		return action
	}
	if held, ok := bySource[target]; ok && held.IngestedAt.After(d.Modified) {
		action.Kind, action.Reason, action.DocID = Skip, "unchanged", held.DocID
		return action
	}
	action.Filename, action.Original = choose(d, options)
	if action.Filename == "" {
		action.Kind, action.Reason = Skip, "no copy docsearch can read"
	}
	return action
}

// choose is the file to push: the original where docsearch reads it, and
// otherwise the archive copy with its text layer.
func choose(d paperless.Document, options Options) (string, bool) {
	hasArchive := d.ArchivedFileName != "" && options.Supports(d.ArchivedFileName)
	if options.PreferArchive && hasArchive {
		return d.ArchivedFileName, false
	}
	if options.Supports(d.OriginalFileName) {
		return d.OriginalFileName, true
	}
	if hasArchive {
		return d.ArchivedFileName, false
	}
	return "", false
}

// retirements are this sync's documents that Paperless no longer shows. One
// still being read waits for the next sync: removing it would delete the
// upload its job is about to read.
func retirements(
	held []document.Document,
	keep, busy map[string]bool,
	options Options,
) []Action {
	ours := upload.Target(options.Prefix + "-")
	var actions []Action
	for _, d := range held {
		if !strings.HasPrefix(d.Source, ours) || keep[d.Source] || busy[d.Source] {
			continue
		}
		key, _ := upload.KeyOf(d.Source)
		actions = append(actions, Action{Kind: Retire, Key: key, DocID: d.DocID})
	}
	return actions
}

// Syncer runs a sync between one Paperless instance and one library.
type Syncer struct {
	paperless Paperless
	library   Library
	uploader  Uploader
	options   Options
}

// New syncs paperless into library through uploader.
func New(source Paperless, library Library, uploader Uploader, options Options) *Syncer {
	return &Syncer{paperless: source, library: library, uploader: uploader, options: options}
}

// Outcome is an action and what came of it.
type Outcome struct {
	Err error
	Action
}

// Plan reads both sides and decides, changing nothing.
func (s *Syncer) Plan(ctx context.Context) ([]Action, error) {
	shown, err := s.paperless.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list paperless documents: %w", err)
	}
	held, err := s.library.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list library documents: %w", err)
	}
	active, err := s.library.Jobs(ctx, false, 0)
	if err != nil {
		return nil, fmt.Errorf("list ingest jobs: %w", err)
	}
	actions := Plan(shown, held, active, s.options)
	if len(shown) == 0 && len(actions) > 0 && !s.options.AllowEmpty {
		return nil, ErrNothingShown
	}
	return actions, nil
}

// Run plans and then acts. One document that fails does not stop the rest.
func (s *Syncer) Run(ctx context.Context) ([]Outcome, error) {
	actions, err := s.Plan(ctx)
	if err != nil {
		return nil, err
	}
	outcomes := make([]Outcome, 0, len(actions))
	for _, action := range actions {
		outcomes = append(outcomes, Outcome{Action: action, Err: s.act(ctx, action)})
	}
	return outcomes, nil
}

func (s *Syncer) act(ctx context.Context, action Action) error {
	switch action.Kind {
	case Push:
		return s.push(ctx, action)
	case Retire:
		return s.library.Remove(ctx, action.DocID)
	default:
		return nil
	}
}

func (s *Syncer) push(ctx context.Context, action Action) error {
	body, err := s.paperless.Download(ctx, action.ID, action.Original)
	if err != nil {
		return err
	}
	_, err = s.uploader.Put(ctx, action.Filename, action.Key, body)
	return errors.Join(err, body.Close())
}
