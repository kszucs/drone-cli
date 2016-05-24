package docker

import (
	"errors"
	"io"
	"os"

	log "github.com/Sirupsen/logrus"
	"github.com/samalba/dockerclient"
)

var (
	ErrTimeout = errors.New("Timeout")
	ErrLogging = errors.New("Logs not available")
)

var (
	// options to fetch the stdout and stderr logs
	logOpts = &dockerclient.LogOptions{
		Stdout: true,
		Stderr: true,
	}
)

func Run(client dockerclient.Client, conf *dockerclient.ContainerConfig, auth *dockerclient.AuthConfig, pull bool, outw, errw io.Writer) (*dockerclient.ContainerInfo, error) {
	if outw == nil {
		outw = os.Stdout
	}
	if errw == nil {
		errw = os.Stdout
	}

	// fetches the container information.
	info, err := Start(client, conf, auth, pull)
	if err != nil {
		return nil, err
	}

	// ensures the container is always stopped
	// and ready to be removed.
	defer func() {
		client.StopContainer(info.Id, 5)
		client.KillContainer(info.Id, "9")
	}()

	// channel listening for errors while the
	// container is running async.
	errc := make(chan error, 1)
	infoc := make(chan *dockerclient.ContainerInfo, 1)
	go func() {
		// options to fetch the stdout and stderr logs
		// by tailing the output.
		logOptsTail := &dockerclient.LogOptions{
			Follow: true,
			Stdout: true,
			Stderr: true,
		}

		// It's possible that the docker logs endpoint returns before the container
		// is done, we'll naively resume up to 5 times if when the logs unblocks
		// the container is still reported to be running.
		for attempts := 0; attempts < 5; attempts++ {
			if attempts > 0 {
				// When resuming the stream, only grab the last line when starting
				// the tailing.
				logOptsTail.Tail = 1
			}

			// blocks and waits for the container to finish
			// by streaming the logs (to /dev/null). Ideally
			// we could use the `wait` function instead
			rc, err := client.ContainerLogs(info.Id, logOptsTail)
			if err != nil {
				log.Errorf("Error tailing %s. %s\n", conf.Image, err)
				errc <- err
				return
			}
			defer rc.Close()

			_, err = StdCopy(outw, errw, rc)
			if err != nil {
				log.Errorf("Error streaming docker logs for %s. %s\n", conf.Image, err)
				errc <- err
				return
			}

			// fetches the container information
			info, err := client.InspectContainer(info.Id)
			if err != nil {
				log.Errorf("Error getting exit code for %s. %s\n", conf.Image, err)
				errc <- err
				return
			}

			if !info.State.Running {
				// The container is no longer running, there should be no more logs to tail.
				infoc <- info
				return
			}

			log.Debugf("Attempting to resume log tailing after %d attempts.\n", attempts)
		}

		errc <- errors.New("Maximum number of attempts made while tailing logs.")
	}()

	select {
	case info := <-infoc:
		return info, nil
	case err := <-errc:
		return info, err
	}
}

func Start(client dockerclient.Client, conf *dockerclient.ContainerConfig, auth *dockerclient.AuthConfig, pull bool) (*dockerclient.ContainerInfo, error) {

	// force-pull the image if specified.
	if pull {
		log.Printf("Pulling image %s", conf.Image)
		client.PullImage(conf.Image, auth)
	}

	// attempts to create the contianer
	id, err := client.CreateContainer(conf, "", auth)
	if err != nil {
		log.Printf("Pulling image %s", conf.Image)

		// and pull the image and re-create if that fails
		err = client.PullImage(conf.Image, auth)
		if err != nil {
			log.Errorf("Error pulling %s. %s\n", conf.Image, err)
			return nil, err
		}
		id, err = client.CreateContainer(conf, "", auth)
		if err != nil {
			log.Errorf("Error creating %s. %s\n", conf.Image, err)
			client.RemoveContainer(id, true, true)
			return nil, err
		}
	}

	// fetches the container information
	info, err := client.InspectContainer(id)
	if err != nil {
		log.Errorf("Error inspecting %s. %s\n", conf.Image, err)
		client.RemoveContainer(id, true, true)
		return nil, err
	}

	// starts the container
	err = client.StartContainer(id, &conf.HostConfig)
	if err != nil {
		log.Errorf("Error starting %s. %s\n", conf.Image, err)
	}
	return info, err
}
