package client

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/stdcopy"
)

func (cli *Client) Composer(ctx context.Context, dir, version, action string) error {
	image := fmt.Sprintf("docker.io/library/%s:%s", "composer", version)

	// pull container
	fmt.Println("Pulling composer image")
	_, err := cli.docker.ImagePull(ctx, image, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("unable to pull the docker image, %w", err)
	}

	// get the version from the flag, default to 1
	var cmd []string
	switch action {
	case "install":
		cmd = []string{"composer", "install", "--ignore-platform-reqs", "--prefer-dist"}
	default:
		cmd = []string{"composer", "update", "--ignore-platform-reqs", "--prefer-dist"}
	}

	fmt.Println("  ==> creating temporary container")

	// create the temp container
	resp, err := cli.docker.ContainerCreate(ctx,
		&container.Config{
			Image: image,
			Cmd:   cmd,
			Tty:   false,
		},
		&container.HostConfig{
			Mounts: []mount.Mount{{
				Type:   "bind",
				Source: dir,
				Target: "/app",
			},
			}},
		nil,
		"")
	if err != nil {
		return fmt.Errorf("unable to create the composer container\n%w", err)
	}

	fmt.Println("  ==> running composer, please stand by")

	stream, err := cli.docker.ContainerAttach(ctx, resp.ID, types.ContainerAttachOptions{
		Stream: true,
		Stdout: true,
		Stderr: true,
		Logs:   true,
	})
	if err != nil {
		return fmt.Errorf("unable to attach to container, %w", err)
	}
	defer stream.Close()

	// run the container
	if err := cli.docker.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("unable to start the container, %w", err)
	}

	if _, err := stdcopy.StdCopy(os.Stdout, os.Stderr, stream.Reader); err != nil {
		return fmt.Errorf("unable to copy the output of the container logs, %w", err)
	}

	// if _, err := cli.docker.ContainerWait(ctx, resp.ID); err != nil {
	// 	return fmt.Errorf("error while waiting for the container, %w", err)
	// }

	fmt.Println("Composer", action, "ran successfully")

	fmt.Println("  ==> removing temporary container")

	// if err := cli.docker.ContainerRemove(ctx, resp.ID, types.ContainerRemoveOptions{Force: true}); err != nil {
	// 	return fmt.Errorf("unable to remove the temporary container %q, %w", resp.ID, err)
	// }

	fmt.Println("Cleanup completed")

	return nil
}
