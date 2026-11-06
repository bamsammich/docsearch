package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// commandList is the subcommand every group names its listing by.
const commandList = "list"

const (
	argLibrary = "<library>"
	flagName   = "name"
	flagRole   = "role"
)

var (
	errNoLibrary = errors.New(
		"name the library: its slug, as `docsearch library list` shows it",
	)
	errNoInvitation = errors.New(
		"name the library: its slug, as `docsearch library invitations` shows it",
	)
)

// libraryCommand creates libraries and shares them, as whoever the token
// signs in as. The server's database decides every rule, so these commands
// only ask.
func libraryCommand() *cli.Command {
	return &cli.Command{
		Name:  "library",
		Usage: "create shared libraries and choose who may use them",
		Description: "A shared library is a set of documents a team searches together. " +
			"Anyone signed in can create one, becoming its admin, and invite others with a " +
			"role. A reader searches and reads it, an editor also adds and removes " +
			"documents, and an admin also manages who has which role. An invitation " +
			"gives nothing until it is accepted. Agents add to a library by passing its " +
			"name as `library`.",
		Commands: []*cli.Command{
			{
				Name:   commandList,
				Usage:  "list the libraries you read, with your role in each",
				Flags:  serverFlags(),
				Action: listLibraries,
			},
			{
				Name:      "create",
				Usage:     "create a shared library, with you as its admin",
				ArgsUsage: argLibrary,
				Description: "The slug is what agents type: lower-case letters, digits and " +
					"hyphens. --name is what people read.",
				Flags: append(serverFlags(), &cli.StringFlag{
					Name:     flagName,
					Usage:    "the library's `NAME`, as people read it",
					Required: true,
				}),
				Action: createLibrary,
			},
			{
				Name:      "members",
				Usage:     "list everyone with a role on a library",
				ArgsUsage: argLibrary,
				Flags:     serverFlags(),
				Action:    listMembers,
			},
			{
				Name: "grant",
				Usage: "invite someone to a library you administer with a role, or change " +
					"the role of a member",
				ArgsUsage: argLibrary + " " + argUsername,
				Description: "Someone can be invited once they have signed in at least " +
					"once, which is when their username is recorded. The library reaches " +
					"them once they accept with `docsearch library accept`.",
				Flags: append(serverFlags(), &cli.StringFlag{
					Name: flagRole, Value: sharing.Reader, Usage: "reader, editor or admin",
				}),
				Action: grantRole,
			},
			{
				Name:      "revoke",
				Usage:     "take someone's role away or withdraw their invitation; revoke your own to leave",
				ArgsUsage: argLibrary + " " + argUsername,
				Flags:     serverFlags(),
				Action:    revokeRole,
			},
			{
				Name:   "invitations",
				Usage:  "list the libraries you are invited to",
				Flags:  serverFlags(),
				Action: listInvitations,
			},
			{
				Name:      "accept",
				Usage:     "join a library you are invited to",
				ArgsUsage: argLibrary,
				Flags:     serverFlags(),
				Action:    acceptInvitation,
			},
			{
				Name:      "decline",
				Usage:     "refuse an invitation to a library",
				ArgsUsage: argLibrary,
				Flags:     serverFlags(),
				Action:    declineInvitation,
			},
			{
				Name:      "log",
				Usage:     "show who changed which role on a library you administer",
				ArgsUsage: argLibrary,
				Flags:     serverFlags(),
				Action:    showEvents,
			},
		},
	}
}

func listLibraries(ctx context.Context, cmd *cli.Command) error {
	libs, err := clientFor(cmd).Libraries(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LIBRARY\tROLE\tNAME")
	for _, lib := range libs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", lib.Name, lib.Role, dash(lib.Title))
	}
	return w.Flush()
}

func createLibrary(ctx context.Context, cmd *cli.Command) error {
	slug := cmd.Args().First()
	if slug == "" {
		return errNoLibrary
	}
	made, err := clientFor(cmd).CreateLibrary(ctx, slug, cmd.String(flagName))
	if err != nil {
		return err
	}
	fmt.Printf("created %s (%s); you are its admin\n", made.Name, made.Title)
	return nil
}

func listMembers(ctx context.Context, cmd *cli.Command) error {
	slug := cmd.Args().First()
	if slug == "" {
		return errNoLibrary
	}
	members, err := clientFor(cmd).Members(ctx, slug)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "USERNAME\tROLE")
	for _, m := range members {
		role := m.Role
		if m.Invited {
			role += " (invited)"
		}
		fmt.Fprintf(w, "%s\t%s\n", dash(m.Username), role)
	}
	return w.Flush()
}

// libraryAndUser are the two arguments grant and revoke take.
func libraryAndUser(cmd *cli.Command) (string, string, error) {
	slug, username := cmd.Args().Get(0), cmd.Args().Get(1)
	switch {
	case slug == "":
		return "", "", errNoLibrary
	case username == "":
		return "", "", errNoUsername
	}
	return slug, username, nil
}

func grantRole(ctx context.Context, cmd *cli.Command) error {
	slug, username, err := libraryAndUser(cmd)
	if err != nil {
		return err
	}
	role := cmd.String(flagRole)
	invited, err := clientFor(cmd).Grant(ctx, slug, username, role)
	if err != nil {
		return err
	}
	if invited {
		fmt.Printf("invited %s to %s as %s; they get access once they accept\n",
			username, slug, role)
		return nil
	}
	fmt.Printf("%s is now %s on %s\n", username, role, slug)
	return nil
}

func listInvitations(ctx context.Context, cmd *cli.Command) error {
	invitations, err := clientFor(cmd).Invitations(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LIBRARY\tROLE\tFROM\tWHEN\tNAME")
	for _, i := range invitations {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", i.Library, i.Role, dash(i.InvitedBy),
			i.At.Local().Format(time.DateTime), dash(i.Title))
	}
	return w.Flush()
}

func acceptInvitation(ctx context.Context, cmd *cli.Command) error {
	slug := cmd.Args().First()
	if slug == "" {
		return errNoInvitation
	}
	joined, err := clientFor(cmd).Accept(ctx, slug)
	if err != nil {
		return err
	}
	fmt.Printf("joined %s as %s\n", joined.Name, joined.Role)
	return nil
}

func declineInvitation(ctx context.Context, cmd *cli.Command) error {
	slug := cmd.Args().First()
	if slug == "" {
		return errNoInvitation
	}
	if err := clientFor(cmd).Decline(ctx, slug); err != nil {
		return err
	}
	fmt.Printf("declined %s\n", slug)
	return nil
}

func revokeRole(ctx context.Context, cmd *cli.Command) error {
	slug, username, err := libraryAndUser(cmd)
	if err != nil {
		return err
	}
	if err := clientFor(cmd).Revoke(ctx, slug, username); err != nil {
		return err
	}
	fmt.Printf("%s has no role on %s\n", username, slug)
	return nil
}

func showEvents(ctx context.Context, cmd *cli.Command) error {
	slug := cmd.Args().First()
	if slug == "" {
		return errNoLibrary
	}
	events, err := clientFor(cmd).Events(ctx, slug)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "WHEN\tWHO\tACTION\tWHOSE\tROLE")
	for _, e := range events {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.At.Local().Format(time.DateTime),
			dash(e.Actor), e.Action, dash(e.Subject), dash(e.Role))
	}
	return w.Flush()
}
