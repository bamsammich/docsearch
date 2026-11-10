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

const (
	argGroup   = "<group>"
	flagGroup  = "group"
	flagUnlink = "unlink"
)

var errNoGroup = errors.New("name the group: its slug, as `docsearch group list` shows it")

// groupFlag names a group in place of a username on library grant and
// revoke.
func groupFlag() *cli.StringFlag {
	return &cli.StringFlag{Name: flagGroup, Usage: "a `GROUP` in place of a username"}
}

// groupCommand creates groups and runs them, as whoever the token signs in
// as. The server's database decides every rule, so these commands only ask.
func groupCommand() *cli.Command {
	return &cli.Command{
		Name:  "group",
		Usage: "create groups, invite people to them, and share libraries with them",
		Description: "A group is a set of people a library can be shared with at once. " +
			"Whoever creates one maintains it, and invites people by username; each " +
			"accepts or declines. A maintainer who administers a library grants it with " +
			"`docsearch library grant <library> --group <group>`.",
		Commands: []*cli.Command{
			{
				Name: commandList, Usage: "list every group, with your role in each",
				Flags: serverFlags(), Action: listGroups,
			},
			{
				Name: "create", Usage: "create a group, with you as its maintainer",
				ArgsUsage: argGroup,
				Flags: append(serverFlags(), &cli.StringFlag{
					Name: flagName, Usage: "the group's `NAME`, as people read it", Required: true,
				}),
				Action: createGroup,
			},
			{
				Name: "rename", Usage: "change the name of a group you maintain",
				ArgsUsage: argGroup,
				Flags: append(serverFlags(), &cli.StringFlag{
					Name: flagName, Usage: "the group's new `NAME`", Required: true,
				}),
				Action: renameGroup,
			},
			{
				Name: "delete", Usage: "delete a group you maintain; its libraries stay",
				ArgsUsage: argGroup, Flags: serverFlags(), Action: deleteGroup,
			},
			{
				Name:      "members",
				Usage:     "list who is in a group you belong to",
				ArgsUsage: argGroup, Flags: serverFlags(), Action: listGroupMembers,
			},
			{
				Name:      "invite",
				Usage:     "invite someone to a group you maintain, or change a member's role",
				ArgsUsage: argGroup + " " + argUsername,
				Description: "An administrator who must add someone at once, with no " +
					"invitation to accept, passes --force. The group's log and the log of " +
					"each library it holds record it. An administrator joins a group they " +
					"are not in only this way.",
				Flags: append(serverFlags(), &cli.StringFlag{
					Name: flagRole, Value: sharing.RoleMember, Usage: "member or maintainer",
				}, &cli.BoolFlag{
					Name:  flagForce,
					Usage: "administrators only: add them now, with no invitation, and log it",
				}),
				Action: inviteToGroup,
			},
			{
				Name: commandRemove,
				Usage: "take someone out of a group or withdraw their invitation; " +
					"remove yourself to leave",
				ArgsUsage: argGroup + " " + argUsername,
				Flags:     serverFlags(),
				Action:    removeFromGroup,
			},
			{
				Name: "invitations", Usage: "list the groups you are invited to",
				Flags: serverFlags(), Action: listGroupInvitations,
			},
			{
				Name: "accept", Usage: "join a group you are invited to",
				ArgsUsage: argGroup, Flags: serverFlags(), Action: acceptGroup,
			},
			{
				Name: "decline", Usage: "refuse an invitation to a group",
				ArgsUsage: argGroup, Flags: serverFlags(), Action: declineGroup,
			},
			{
				Name: "libraries", Usage: "list the libraries shared with a group you belong to",
				ArgsUsage: argGroup, Flags: serverFlags(), Action: listGroupLibraries,
			},
			{
				Name: "log", Usage: "show the changes to a group you maintain",
				ArgsUsage: argGroup, Flags: serverFlags(), Action: showGroupEvents,
			},
			{
				Name:      "link",
				Usage:     "administrators only: make a group follow a provider group",
				ArgsUsage: argGroup + " [<provider-group>]",
				Description: "Whoever's token carries the provider group is a member, with " +
					"no invitation, and leaves when their token stops carrying it. Members " +
					"invited by hand stay. With no provider group named, it shows the link. " +
					"--unlink stops the group following one, and takes out the members the " +
					"link gave. The server reads provider groups only once " +
					"--oidc-groups-claim is set.",
				Flags: append(serverFlags(), &cli.BoolFlag{
					Name: flagUnlink, Usage: "stop the group following a provider group",
				}),
				Action: linkGroup,
			},
		},
	}
}

// groupArg is the group a command names first.
func groupArg(cmd *cli.Command) (string, error) {
	slug := cmd.Args().First()
	if slug == "" {
		return "", errNoGroup
	}
	return slug, nil
}

// groupAndUser are the two arguments invite and remove take.
func groupAndUser(cmd *cli.Command) (string, string, error) {
	slug, username := cmd.Args().Get(0), cmd.Args().Get(1)
	switch {
	case slug == "":
		return "", "", errNoGroup
	case username == "":
		return "", "", errNoUsername
	}
	return slug, username, nil
}

func listGroups(ctx context.Context, cmd *cli.Command) error {
	groups, canCreate, err := clientFor(cmd).Groups(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "GROUP\tYOUR ROLE\tMEMBERS\tNAME")
	for _, g := range groups {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", g.Slug, dash(g.Role), g.Members, g.Name)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if !canCreate {
		fmt.Println("only docsearch administrators create groups here")
	}
	return nil
}

func createGroup(ctx context.Context, cmd *cli.Command) error {
	slug, err := groupArg(cmd)
	if err != nil {
		return err
	}
	made, err := clientFor(cmd).CreateGroup(ctx, slug, cmd.String(flagName))
	if err != nil {
		return err
	}
	fmt.Printf("created %s (%s); you maintain it\n", made.Slug, made.Name)
	return nil
}

func renameGroup(ctx context.Context, cmd *cli.Command) error {
	slug, err := groupArg(cmd)
	if err != nil {
		return err
	}
	if err := clientFor(cmd).RenameGroup(ctx, slug, cmd.String(flagName)); err != nil {
		return err
	}
	fmt.Printf("%s is now called %s\n", slug, cmd.String(flagName))
	return nil
}

func deleteGroup(ctx context.Context, cmd *cli.Command) error {
	slug, err := groupArg(cmd)
	if err != nil {
		return err
	}
	if err := clientFor(cmd).DeleteGroup(ctx, slug); err != nil {
		return err
	}
	fmt.Printf("deleted %s; its libraries stay with their other members\n", slug)
	return nil
}

func listGroupMembers(ctx context.Context, cmd *cli.Command) error {
	slug, err := groupArg(cmd)
	if err != nil {
		return err
	}
	members, err := clientFor(cmd).GroupMembers(ctx, slug)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "USERNAME\tROLE")
	for _, m := range members {
		role := m.Role
		switch {
		case m.Invited:
			role += " (invited)"
		case m.FromProvider:
			role += " (from the provider)"
		}
		fmt.Fprintf(w, "%s\t%s\n", dash(m.Username), role)
	}
	return w.Flush()
}

func inviteToGroup(ctx context.Context, cmd *cli.Command) error {
	slug, username, err := groupAndUser(cmd)
	if err != nil {
		return err
	}
	role := cmd.String(flagRole)
	if cmd.Bool(flagForce) {
		if err := clientFor(cmd).ForceIntoGroup(ctx, slug, username, role); err != nil {
			return err
		}
		fmt.Printf("added %s to %s as %s by force; the group and its libraries log it\n",
			username, slug, role)
		return nil
	}
	invited, err := clientFor(cmd).InviteToGroup(ctx, slug, username, role)
	if err != nil {
		return err
	}
	if invited {
		fmt.Printf("invited %s to %s as %s; they join once they accept\n", username, slug, role)
		return nil
	}
	fmt.Printf("%s is now %s of %s\n", username, role, slug)
	return nil
}

func removeFromGroup(ctx context.Context, cmd *cli.Command) error {
	slug, username, err := groupAndUser(cmd)
	if err != nil {
		return err
	}
	if err := clientFor(cmd).RemoveFromGroup(ctx, slug, username); err != nil {
		return err
	}
	fmt.Printf("%s is not in %s\n", username, slug)
	return nil
}

func listGroupInvitations(ctx context.Context, cmd *cli.Command) error {
	invitations, err := clientFor(cmd).GroupInvitations(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "GROUP\tROLE\tFROM\tWHEN\tNAME")
	for _, i := range invitations {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", i.Group, i.Role, dash(i.InvitedBy),
			i.At.Local().Format(time.DateTime), i.Name)
	}
	return w.Flush()
}

func acceptGroup(ctx context.Context, cmd *cli.Command) error {
	slug, err := groupArg(cmd)
	if err != nil {
		return err
	}
	role, err := clientFor(cmd).AcceptGroup(ctx, slug)
	if err != nil {
		return err
	}
	fmt.Printf("joined %s as %s\n", slug, role)
	return nil
}

func declineGroup(ctx context.Context, cmd *cli.Command) error {
	slug, err := groupArg(cmd)
	if err != nil {
		return err
	}
	if err := clientFor(cmd).DeclineGroup(ctx, slug); err != nil {
		return err
	}
	fmt.Printf("declined %s\n", slug)
	return nil
}

func listGroupLibraries(ctx context.Context, cmd *cli.Command) error {
	slug, err := groupArg(cmd)
	if err != nil {
		return err
	}
	libs, err := clientFor(cmd).GroupLibraries(ctx, slug)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LIBRARY\tROLE\tNAME")
	for _, l := range libs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", l.Name, l.Role, dash(l.Title))
	}
	return w.Flush()
}

func showGroupEvents(ctx context.Context, cmd *cli.Command) error {
	slug, err := groupArg(cmd)
	if err != nil {
		return err
	}
	events, err := clientFor(cmd).GroupEvents(ctx, slug)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "WHEN\tWHO\tACTION\tWHOSE\tROLE\tDETAIL")
	for _, e := range events {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.At.Local().Format(time.DateTime),
			dash(e.Actor), e.Action, dash(e.Subject), dash(e.Role), dash(e.Detail))
	}
	return w.Flush()
}

func linkGroup(ctx context.Context, cmd *cli.Command) error {
	slug, err := groupArg(cmd)
	if err != nil {
		return err
	}
	provider := cmd.Args().Get(1)
	switch {
	case cmd.Bool(flagUnlink) && provider != "":
		return errors.New("name a provider group or pass --unlink, not both")
	case cmd.Bool(flagUnlink):
		if err := clientFor(cmd).LinkGroup(ctx, slug, ""); err != nil {
			return err
		}
		fmt.Printf("%s follows no provider group; the members it gave are out\n", slug)
		return nil
	case provider != "":
		if err := clientFor(cmd).LinkGroup(ctx, slug, provider); err != nil {
			return err
		}
		fmt.Printf("%s follows %s; each member joins with their next token\n", slug, provider)
		return nil
	}
	return showLink(ctx, cmd, slug)
}

// showLink prints the provider group a group follows.
func showLink(ctx context.Context, cmd *cli.Command, slug string) error {
	linked, _, err := clientFor(cmd).GroupProviderLink(ctx, slug)
	if err != nil {
		return err
	}
	if linked == "" {
		fmt.Printf("%s follows no provider group\n", slug)
		return nil
	}
	fmt.Printf("%s follows %s\n", slug, linked)
	return nil
}

// grantGroup is library grant with --group.
func grantGroup(ctx context.Context, cmd *cli.Command, group string) error {
	slug := cmd.Args().First()
	if slug == "" {
		return errNoLibrary
	}
	role := cmd.String(flagRole)
	if err := clientFor(cmd).GrantGroup(ctx, slug, group, role); err != nil {
		return err
	}
	fmt.Printf("every member of %s is now %s on %s\n", group, role, slug)
	return nil
}

// revokeGroup is library revoke with --group.
func revokeGroup(ctx context.Context, cmd *cli.Command, group string) error {
	slug := cmd.Args().First()
	if slug == "" {
		return errNoLibrary
	}
	if err := clientFor(cmd).RevokeGroup(ctx, slug, group); err != nil {
		return err
	}
	fmt.Printf("%s has no role on %s\n", group, slug)
	return nil
}
