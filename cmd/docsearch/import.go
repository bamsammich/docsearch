package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/v1import"
)

var errNoV1File = errors.New("name the v1 index file to import from")

func importCommand() *cli.Command {
	return &cli.Command{
		Name:      "import-v1",
		Usage:     "queue every document a v1 index held, to be read again by v2",
		ArgsUsage: "<v1 index file>",
		Description: "Reads the v1 file read-only and queues each finished document " +
			"from its recorded source. v1's chunks are not copied: v2 reads every " +
			"document itself. A v1 path names the library as v1 saw it; " +
			"--root-map moves it to where v2 sees the same files.",
		Flags: append(serverFlags(),
			&cli.StringSliceFlag{
				Name:  "root-map",
				Usage: "rewrite v1 file paths under `FROM=TO`. Repeat for more than one.",
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "print what would be queued, and queue nothing",
			},
		),
		Action: importV1,
	}
}

func importV1(ctx context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errNoV1File
	}
	var roots []v1import.RootMap
	for _, spelling := range cmd.StringSlice("root-map") {
		root, err := v1import.ParseRootMap(spelling)
		if err != nil {
			return err
		}
		roots = append(roots, root)
	}
	documents, err := v1import.Read(ctx, path)
	if err != nil {
		return err
	}
	entries := v1import.Plan(documents, roots)
	if cmd.Bool("dry-run") {
		for _, entry := range entries {
			fmt.Println(describe(v1import.Outcome{Entry: entry}, "would queue"))
		}
		return nil
	}
	return reportImport(v1import.Run(ctx, clientFor(cmd), entries))
}

// reportImport prints one line per document and fails the command if any source
// was refused, so a script sees an import that needs attention.
func reportImport(outcomes []v1import.Outcome) error {
	failed := 0
	for _, outcome := range outcomes {
		fmt.Println(describe(outcome, "queued"))
		if outcome.Err != nil {
			failed++
		}
	}
	if failed > 0 {
		fmt.Printf("%d of %d documents were not queued\n", failed, len(outcomes))
		return exit(1)
	}
	return nil
}

func describe(outcome v1import.Outcome, done string) string {
	switch {
	case outcome.Skipped != "":
		return fmt.Sprintf("skipped  %s (%s)", outcome.Source, outcome.Skipped)
	case outcome.Err != nil:
		return fmt.Sprintf("failed   %s: %s", outcome.Source, plainly(outcome.Err))
	case len(outcome.Jobs) > 0:
		return fmt.Sprintf("%s job %d: %s", done, outcome.Jobs[0].JobID, outcome.Source)
	default:
		return fmt.Sprintf("%s %s", done, outcome.Source)
	}
}
