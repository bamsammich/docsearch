package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/api/connectclient"
)

const flagAs = "as"

var errNoDestination = errors.New("name the library it goes to with --to")

// copyCommand and moveCommand put a document into another library. Both
// queue a job that reads the original again; a move removes the document
// from where it was once its copy is ready.
func copyCommand() *cli.Command {
	return copyingCommand("copy",
		"copy a document into another library; needs reader where it is and editor where it goes",
		(*connectclient.Client).Copy, "copying")
}

func moveCommand() *cli.Command {
	return copyingCommand("move",
		"move a document into another library; needs editor in both libraries",
		(*connectclient.Client).Move, "moving")
}

// copier is Copy or Move.
type copier func(c *connectclient.Client, ctx context.Context, docID, library, as string) (
	connectclient.Copied, error)

func copyingCommand(name, usage string, run copier, doing string) *cli.Command {
	return &cli.Command{
		Name:      name,
		Usage:     usage,
		ArgsUsage: argDocID,
		Flags: append(serverFlags(),
			&cli.StringFlag{Name: flagTo, Usage: "the `LIBRARY` it goes to"},
			&cli.StringFlag{
				Name:  flagAs,
				Usage: "the `DOC_ID` it takes there, where its own is taken",
			},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			docID := cmd.Args().First()
			if docID == "" {
				return errNoDocID
			}
			to := cmd.String(flagTo)
			if to == "" {
				return errNoDestination
			}
			copied, err := run(clientFor(cmd), ctx, docID, to, cmd.String(flagAs))
			if err != nil {
				return err
			}
			fmt.Printf("queued job %d: %s %s to %s\n", copied.JobID, doing, docID, copied.DocID)
			fmt.Printf("follow it with `docsearch jobs --library %s`\n", to)
			return nil
		},
	}
}
