package main

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/service/document"
)

// titleWidth is where a title is cut in the listing, so that one document
// stays on one line.
const titleWidth = 40

func listCommand() *cli.Command {
	return &cli.Command{
		Name:   "list",
		Usage:  "list ingested documents",
		Flags:  serverFlags(),
		Action: listDocuments,
	}
}

func listDocuments(ctx context.Context, cmd *cli.Command) error {
	docs, err := clientFor(cmd).List(ctx)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		fmt.Println("no documents")
		return nil
	}
	fmt.Printf("%-28s %-7s %-10s %-9s %6s %7s  TITLE\n",
		"DOC_ID", "FMT", "STATUS", "QUALITY", "PAGES", "CHUNKS")
	for _, doc := range docs {
		fmt.Printf("%-28s %-7s %-10s %-9s %6s %7s  %s\n",
			doc.DocID, doc.Format, doc.Status, quality(doc),
			count(doc.PageCount), count(doc.ChunkCount), cut(doc.Title, titleWidth))
	}
	return nil
}

func verifyCommand() *cli.Command {
	return &cli.Command{
		Name:      "verify",
		Usage:     "print structural checks for an ingested document",
		ArgsUsage: argDocID,
		Flags:     serverFlags(),
		Action:    verifyDocument,
	}
}

func verifyDocument(ctx context.Context, cmd *cli.Command) error {
	docID := cmd.Args().First()
	if docID == "" {
		return errNoDocID
	}
	report, err := clientFor(cmd).Verify(ctx, docID)
	if err != nil {
		return err
	}
	fmt.Println(report.Display())
	// A document with problems exits non-zero so that a script checking a
	// library does not have to read the report to know one failed.
	if len(report.Problems) > 0 {
		return exit(1)
	}
	return nil
}

func inspectCommand() *cli.Command {
	return &cli.Command{
		Name:      "inspect",
		Usage:     "report what structure a target offers, without ingesting it",
		ArgsUsage: argTarget,
		Description: "A URL is crawled live, since every question worth asking about a " +
			"site is a question about what the server actually returns, and nothing " +
			"is written to the index.",
		Flags:  serverFlags(),
		Action: inspectTarget,
	}
}

func inspectTarget(ctx context.Context, cmd *cli.Command) error {
	target := cmd.Args().First()
	if target == "" {
		return errNoTarget
	}
	report, err := clientFor(cmd).Inspect(ctx, target)
	if err != nil {
		return err
	}
	fmt.Println(report.Report())
	if report.Blocked() {
		return exit(1)
	}
	return nil
}

func removeCommand() *cli.Command {
	return &cli.Command{
		Name:      "remove",
		Usage:     "delete a document and all of its rows",
		ArgsUsage: argDocID,
		Flags:     serverFlags(),
		Action:    removeDocument,
	}
}

func removeDocument(ctx context.Context, cmd *cli.Command) error {
	docID := cmd.Args().First()
	if docID == "" {
		return errNoDocID
	}
	if err := clientFor(cmd).Remove(ctx, docID); err != nil {
		return err
	}
	fmt.Printf("removed %s\n", docID)
	return nil
}

// quality is the grade a document carries, or a dash where it has none.
func quality(doc document.Document) string {
	if doc.Quality == 0 {
		return "-"
	}
	return doc.Quality.String()
}

// count reads as a dash where a document has nothing to count, which is
// every unpaginated format.
func count(n *int) string {
	if n == nil || *n == 0 {
		return "-"
	}
	return fmt.Sprint(*n)
}

// cut shortens text to width characters, counting characters rather than
// bytes so that a title is not cut through one.
func cut(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width])
}

// tokenFromEnv is the bearer token, which only the environment carries.
func tokenFromEnv() string { return os.Getenv(config.EnvToken) }
