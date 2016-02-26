package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"

	"github.com/codegangsta/cli"
	"github.com/drone/drone-cli/drone/git"
	"github.com/drone/drone-exec/docker"
	"github.com/drone/drone-exec/yaml/secure"
	"github.com/drone/drone-go/drone"
	"github.com/drone/drone/yaml/matrix"
	"github.com/fatih/color"
	"github.com/samalba/dockerclient"
)

var ExecCmd = cli.Command{
	Name:  "exec",
	Usage: "executes a local build",
	Action: func(c *cli.Context) {
		if err := execCmd(c); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	},
	Flags: []cli.Flag{
		cli.StringFlag{
			EnvVar: "DOCKER_HOST",
			Name:   "docker-host",
			Usage:  "docker deamon address",
			Value:  "unix:///var/run/docker.sock",
		},
		cli.BoolFlag{
			EnvVar: "DOCKER_TLS_VERIFY",
			Name:   "docker-tls-verify",
			Usage:  "docker daemon supports tlsverify",
		},
		cli.StringFlag{
			EnvVar: "DOCKER_CERT_PATH",
			Name:   "docker-cert-path",
			Usage:  "docker certificate directory",
			Value:  "",
		},
		cli.StringFlag{
			Name:  "i",
			Value: "",
			Usage: "identify file injected in the container",
		},
		cli.StringFlag{
			Name:  "n",
			Value: "",
			Usage: ".netrc file injected in the container",
		},
		cli.StringSliceFlag{
			Name:  "e",
			Usage: "secret environment variables",
		},
		cli.StringFlag{
			Name:  "E",
			Usage: "secrets from plaintext YAML of .drone.sec (use - for stdin)",
		},
		cli.BoolFlag{
			Name:  "trusted",
			Usage: "enable elevated privilege",
		},
		cli.BoolFlag{
			Name:  "cache",
			Usage: "execute cache steps",
		},
		cli.BoolFlag{
			Name:  "deploy",
			Usage: "execute publish and deployment steps",
		},
		cli.BoolFlag{
			Name:  "notify",
			Usage: "execute notification steps",
		},
		cli.BoolFlag{
			Name:  "pull",
			Usage: "always pull the latest docker image",
		},
		cli.StringFlag{
			Name:  "event",
			Usage: "hook event type",
			Value: "push",
		},
		cli.BoolTFlag{
			Name:  "debug",
			Usage: "execute the build in debug mode",
		},
	},
}

func execCmd(c *cli.Context) error {
	info := git.Info()

	cert, _ := ioutil.ReadFile(filepath.Join(
		c.String("docker-cert-path"),
		"cert.pem",
	))

	key, _ := ioutil.ReadFile(filepath.Join(
		c.String("docker-cert-path"),
		"key.pem",
	))

	ca, _ := ioutil.ReadFile(filepath.Join(
		c.String("docker-cert-path"),
		"ca.pem",
	))
	if len(cert) == 0 || len(key) == 0 || len(ca) == 0 {
		println("")
	}

	yml, err := ioutil.ReadFile(".drone.yml")
	if err != nil {
		return err
	}

	// initially populate globals from the '-e' slice
	globals := c.StringSlice("e")
	if c.IsSet("E") {
		// read the .drone.sec.yml file (plain text)
		plaintext, err := readInput(c.String("E"))
		if err != nil {
			return err
		}

		// parse the plaintext secrets file
		sec := new(secure.Secure)
		err = yaml.Unmarshal(plaintext, sec)
		if err != nil {
			return err
		}

		// prepend values into globals (allow '-e' to override the secrets file)
		for k, v := range sec.Environment.Map() {
			tmp := strings.Join([]string{k, v}, "=")
			globals = append([]string{tmp}, globals...)
		}
	}

	axes, err := matrix.Parse(string(yml))
	if err != nil {
		return err
	}
	if len(axes) == 0 {
		axes = append(axes, matrix.Axis{})
	}

	cli, err := newDockerClient(c.String("docker-host"), cert, key, ca)
	if err != nil {
		return err
	}

	pwd, err := os.Getwd()
	if err != nil {
		return err
	}

	execArgs := []string{"--build", "--debug", "--mount", pwd}
	for _, arg := range []string{"cache", "deploy", "notify", "pull"} {
		if c.Bool(arg) {
			execArgs = append(execArgs, "--"+arg)
		}
	}
	if c.Bool("pull") {
		image := "drone/drone-exec:latest"
		color.Magenta("[DRONE] pulling %s", image)
		err := cli.PullImage(image, nil)
		if err != nil {
			color.Red("[DRONE] failed to pull %s", image)
			os.Exit(1)
		}
	}

	proj := resolvePath(pwd)

	var exits []int

	for i, axis := range axes {
		color.Magenta("[DRONE] starting job #%d", i+1)
		if len(axis) != 0 {
			color.Magenta("[DRONE] export %s", axis)
		}

		payload := drone.Payload{
			Repo: &drone.Repo{
				IsTrusted: c.Bool("trusted"),
				IsPrivate: true,
			},
			Job: &drone.Job{
				Status:      drone.StatusRunning,
				Environment: axis,
			},
			Yaml: string(yml),
			Build: &drone.Build{
				Status:  drone.StatusRunning,
				Branch:  info.Branch,
				Commit:  info.Head.ID,
				Author:  info.Head.AuthorName,
				Email:   info.Head.AuthorEmail,
				Message: info.Head.Message,
				Event:   c.String("event"),
			},
			System: &drone.System{
				Link:    c.GlobalString("server"),
				Globals: globals,
				Plugins: []string{"plugins/*", "*/*"},
			},
		}

		if len(c.String("n")) != 0 {
			key, err = ioutil.ReadFile(c.String("n"))
			if err != nil {
				return err
			}
			words := strings.Fields(string(key))
			payload.Netrc = &drone.Netrc{
				Machine:  words[1],
				Login:    words[3],
				Password: words[5],
			}
		}

		if len(c.String("i")) != 0 {
			key, err = ioutil.ReadFile(c.String("i"))
			if err != nil {
				return err
			}
			payload.Keys = &drone.Key{
				Private: string(key),
			}
		}

		if len(proj) != 0 {
			payload.Repo.Link = fmt.Sprintf("https://%s", proj)
		}
		out, _ := json.Marshal(payload)

		exit, err := run(cli, execArgs, string(out))
		if err != nil {
			return err
		}
		exits = append(exits, exit)

		color.Magenta("[DRONE] finished job #%d", i+1)
		color.Magenta("[DRONE] exit code %d", exit)
	}

	var passed = true
	for i, _ := range axes {
		exit := exits[i]
		if exit == 0 {
			color.Green("[DRONE] job #%d passed", i+1)
		} else {
			color.Red("[DRONE] job #%d failed", i+1)
			passed = false
		}
	}
	if passed {
		color.Green("[DRONE] build passed")
	} else {
		color.Red("[DRONE] build failed")
		os.Exit(1)
	}

	return nil
}

func run(client dockerclient.Client, args []string, input string) (int, error) {

	image := "drone/drone-exec:latest"
	entrypoint := []string{"/bin/drone-exec"}
	args = append(args, "--", input)

	conf := &dockerclient.ContainerConfig{
		Image:      image,
		Entrypoint: entrypoint,
		Cmd:        args,
		HostConfig: dockerclient.HostConfig{
			Binds: []string{"/var/run/docker.sock:/var/run/docker.sock"},
		},
		Volumes: map[string]struct{}{
			"/var/run/docker.sock": struct{}{},
		},
	}

	info, err := docker.Run(client, conf, false)

	client.StopContainer(info.Id, 15)
	client.RemoveContainer(info.Id, true, true)
	return info.State.ExitCode, err
}

func newDockerClient(addr string, cert, key, ca []byte) (dockerclient.Client, error) {
	var tlc *tls.Config

	if len(cert) != 0 {
		pem, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return dockerclient.NewDockerClient(addr, nil)
		}
		tlc = &tls.Config{}
		tlc.Certificates = []tls.Certificate{pem}

		if len(ca) != 0 {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(ca)
			tlc.RootCAs = pool

		} else {
			tlc.InsecureSkipVerify = true
		}
	}

	return dockerclient.NewDockerClient(addr, tlc)
}
