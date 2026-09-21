package main

import (
	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/api/connectclient"
	"github.com/bamsammich/docsearch/internal/config"
)

// DefaultServer is where docsearch-server listens unless told otherwise, and
// the same loopback address the server binds by default.
const DefaultServer = "http://127.0.0.1:8765"

// serverFlags are what every command that talks to the server needs.
//
// The token has no flag, for the reason docsearch-server does not give it
// one: an argument is visible to every process on the host through ps.
func serverFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Value:   DefaultServer,
			Usage:   "`URL` of docsearch-server",
			Sources: cli.EnvVars(config.EnvServer),
		},
	}
}

// clientFor is the client the command was configured to use.
func clientFor(cmd *cli.Command) *connectclient.Client {
	return connectclient.Default(cmd.String("server"), tokenFromEnv())
}

// The argument names and the one flag every ingesting command takes, written
// once so that two commands cannot describe the same argument differently.
const (
	argTarget = "<path or url>"
	argDocID  = "<doc_id>"
	flagTitle = "title"
)

// titleFlag overrides the name a document gives itself.
func titleFlag() *cli.StringFlag {
	return &cli.StringFlag{
		Name:  flagTitle,
		Usage: "override the derived document title",
	}
}
