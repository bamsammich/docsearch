package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/api/connectclient"
	"github.com/bamsammich/docsearch/internal/domain"
)

// Ported from python/docsearch/cli.py.

// progressStride is how far an ingest advances before another line is
// printed. A 1,800-page manual reports every page, and a line each would
// bury the result.
const progressStride = 100

func addCommand(name string) *cli.Command {
	return &cli.Command{
		Name:      name,
		Usage:     "ingest a file, a directory or a documentation site, waiting for it",
		ArgsUsage: argTarget,
		Description: "The server reads the source and writes it to the index while this " +
			"prints what it is doing. `docsearch enqueue` is the other path: it " +
			"returns at once and leaves the work to the worker.",
		Flags:  append(serverFlags(), titleFlag()),
		Action: addTarget,
	}
}

func addTarget(ctx context.Context, cmd *cli.Command) error {
	target := cmd.Args().First()
	if target == "" {
		return errNoTarget
	}
	fmt.Printf("==> %s\n", target)

	result, err := clientFor(cmd).Ingest(
		ctx, target, cmd.String(flagTitle), true, printProgress())
	if err != nil {
		return err
	}
	printResult(result)
	return nil
}

func refreshCommand() *cli.Command {
	return &cli.Command{
		Name:      "refresh",
		Usage:     "re-crawl an ingested site, transferring only what changed",
		ArgsUsage: argDocID,
		Description: "Conditional requests mean a site whose pages are unchanged costs " +
			"one round trip each and no bodies at all. With --from-cache nothing " +
			"is requested, which is what re-indexing after a chunker change wants, " +
			"and it works with the network disconnected.",
		Flags: append(serverFlags(),
			titleFlag(),
			&cli.BoolFlag{
				Name:  "from-cache",
				Usage: "re-chunk from the fetch cache without making a single request",
			},
		),
		Action: refreshDocument,
	}
}

func refreshDocument(ctx context.Context, cmd *cli.Command) error {
	docID := cmd.Args().First()
	if docID == "" {
		return errNoDocID
	}
	client := clientFor(cmd)
	source, err := crawlableSource(ctx, client, docID)
	if err != nil {
		return err
	}

	fromCache := cmd.Bool("from-cache")
	suffix := ""
	if fromCache {
		suffix = "  (from cache)"
	}
	fmt.Printf("==> %s%s\n", source, suffix)

	result, err := client.Ingest(ctx, source, cmd.String(flagTitle), !fromCache, printProgress())
	if err != nil {
		return err
	}
	printResult(result)
	if result.Outcome == "unchanged" {
		fmt.Println("    nothing on the site changed; the index is already current")
	}
	return nil
}

// crawlableSource is the document to re-crawl, refusing one there is nothing
// to re-crawl for.
func crawlableSource(
	ctx context.Context,
	client *connectclient.Client,
	docID string,
) (string, error) {
	docs, err := client.List(ctx)
	if err != nil {
		return "", err
	}
	for _, doc := range docs {
		if doc.DocID != docID {
			continue
		}
		if doc.SourceKind != domain.SourceKindSite {
			return "", fmt.Errorf(
				"%s was ingested from a file, not a site; run `docsearch add` on the "+
					"file instead, since a file has nothing to re-crawl", docID)
		}
		return docID, nil
	}
	return "", fmt.Errorf("no such document: %s", docID)
}

// printProgress reports a phase as it advances, on stderr, so that piping
// the result somewhere does not carry the commentary with it.
func printProgress() func(connectclient.Progress) {
	lastPhase, lastCurrent := "", 0
	return func(p connectclient.Progress) {
		if p.Phase == lastPhase && p.Current-lastCurrent < progressStride && p.Current < p.Total {
			return
		}
		lastPhase, lastCurrent = p.Phase, p.Current
		counted := fmt.Sprint(p.Current)
		if p.Total > 0 {
			counted = fmt.Sprintf("%d/%d", p.Current, p.Total)
		}
		fmt.Fprintf(os.Stderr, "    %-8s %s\n", p.Phase, counted)
	}
}

// printResult is what one ingest did, and what the adapter saw on the way.
func printResult(result *connectclient.Result) {
	fmt.Printf("    %s: %s (%d chunks)\n", result.Outcome, result.DocID, result.ChunkCount)
	if result.Note != "" {
		fmt.Printf("    note: %s\n", result.Note)
	}
	printDiagnostics(result.Diagnostics)
	for _, finding := range result.Findings {
		fmt.Printf("    finding: %s\n", finding)
	}
}

// diagnostics is what an adapter observed, in the shape the report prints.
//
// Every field is optional: which of them an ingest fills depends on what it
// read, and a Markdown file fills none of them.
type diagnostics struct {
	CrossValidation *crossValidation `json:"cross_validation"`
	Site            *siteDiagnostic  `json:"site"`
	Index           *indexDiagnostic `json:"index"`
	StructureSource string           `json:"structure_source"`
}

type crossValidation struct {
	InTOCNotInBody []string `json:"in_toc_not_in_body"`
	InBodyNotInTOC []string `json:"in_body_not_in_toc"`
	TOCSections    int      `json:"toc_sections"`
	BodySections   int      `json:"body_sections"`
}

type siteDiagnostic struct {
	UnreachableReasons []string `json:"unreachable_reasons"`
	PagesFetched       int      `json:"pages_fetched"`
	PagesDeclared      int      `json:"pages_declared"`
	HierarchyInferred  bool     `json:"hierarchy_inferred"`
}

type indexDiagnostic struct {
	Entries                     int `json:"entries"`
	RefsResolvingToKnownSection int `json:"refs_resolving_to_known_sections"`
}

const (
	// namedLimit is how many section names are printed before the rest are
	// counted instead.
	namedLimit = 15
	// reasonLimit is how many unreachable pages are explained. A site that
	// refused a hundred pages refused them for a handful of reasons.
	reasonLimit = 10
)

func printDiagnostics(raw string) {
	if raw == "" {
		return
	}
	var d diagnostics
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return
	}
	if d.StructureSource != "" {
		fmt.Printf("    structure source: %s\n", d.StructureSource)
	}
	printCrossValidation(d.CrossValidation)
	printSite(d.Site)
	if d.Index != nil {
		fmt.Printf("    index: %d refs, %d resolve to known sections\n",
			d.Index.Entries, d.Index.RefsResolvingToKnownSection)
	}
}

// printCrossValidation says whether the table of contents and the body agree
// about what sections the document has.
func printCrossValidation(x *crossValidation) {
	if x == nil {
		return
	}
	fmt.Printf("    cross-validation: %d TOC sections vs %d body sections\n",
		x.TOCSections, x.BodySections)
	if len(x.InTOCNotInBody) > 0 {
		fmt.Printf("      in TOC, absent from body (%d): %s\n",
			len(x.InTOCNotInBody), named(x.InTOCNotInBody))
	}
	if len(x.InBodyNotInTOC) > 0 {
		fmt.Printf("      in body, absent from TOC (%d): %s\n",
			len(x.InBodyNotInTOC), named(x.InBodyNotInTOC))
	}
	if len(x.InTOCNotInBody) > 0 || len(x.InBodyNotInTOC) > 0 {
		return
	}
	// Two empty sets agree, which says nothing at all. Agreement is only
	// worth claiming where there was something on both sides to compare.
	if x.TOCSections > 0 && x.BodySections > 0 {
		fmt.Println("      both structure sources agree exactly")
		return
	}
	fmt.Println("      NOT cross-validated: nothing to compare on both sides")
}

// printSite says how much of a crawled site was reached.
func printSite(site *siteDiagnostic) {
	if site == nil {
		return
	}
	fmt.Printf("    pages: %d fetched of %d known\n", site.PagesFetched, site.PagesDeclared)
	if site.HierarchyInferred {
		fmt.Println("      hierarchy was INFERRED from URL paths; no navigation source " +
			"placed enough of the site to be believed")
	}
	for i, reason := range site.UnreachableReasons {
		if i == reasonLimit {
			break
		}
		fmt.Printf("      unreachable: %s\n", reason)
	}
}

// named lists section names, counting the rest where there are too many to
// read.
func named(values []string) string {
	shown := values
	more := ""
	if len(shown) > namedLimit {
		shown = shown[:namedLimit]
		more = " ..."
	}
	out := ""
	for i, v := range shown {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out + more
}
