package main

import (
	"os"

	"github.com/codegangsta/cli"
)

var (
	// commit sha for the current build.
	version  string
	revision string
)

func main() {
	app := cli.NewApp()
	app.Name = "drone"
	app.Version = version
	app.Usage = "command line utility"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "t, token",
			Value:  "",
			Usage:  "server auth token",
			EnvVar: "DRONE_TOKEN",
		},
		cli.StringFlag{
			Name:   "s, server",
			Value:  "",
			Usage:  "server location",
			EnvVar: "DRONE_SERVER",
		},
	}

	app.Commands = []cli.Command{
		BuildCmd,
		DeployCmd,
		RepoCmd,
		ExecCmd,
		MachineCmd,
		SecureCmd,
		UserCmd,
		SecretCmd,
		SignCmd,
	}

	app.Run(os.Args)
}
